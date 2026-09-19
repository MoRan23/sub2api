//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func proxyGeoLifecycleFixture(t *testing.T) (*service.Proxy, *redis.Client) {
	t.Helper()
	proxy := mustCreateProxy(t, testEntClient(t), &service.Proxy{
		Name: t.Name(), Protocol: "http", Host: "127.0.0.1", Port: 18080,
	})
	t.Cleanup(func() {
		_, err := integrationDB.ExecContext(context.Background(), "DELETE FROM proxies WHERE id=$1", proxy.ID)
		require.NoError(t, err)
	})
	return proxy, testRedis(t)
}

func proxyGeoLifecycleInfo(proxy *service.Proxy, at time.Time, city string) *service.ProxyLatencyInfo {
	checked := at.UTC().Truncate(time.Millisecond)
	latency := int64(9)
	return &service.ProxyLatencyInfo{
		Success: true, LatencyMs: &latency, Message: "synthetic success",
		IPAddress: "203.0.113.17", Country: "Germany", CountryCode: "DE",
		Region: "Berlin", City: city, Timezone: "Europe/Berlin",
		GeoStatus: "success", GeoCheckedAt: &checked, UpdatedAt: checked,
		RouteKey: service.ProxyGeoRouteKey(proxy), GeoResultUnixMs: checked.UnixMilli(),
	}
}

func TestProxyGeoPersistentRealStoreRestoresAfterRedisLoss(t *testing.T) {
	ctx := context.Background()
	proxy, rdb := proxyGeoLifecycleFixture(t)
	first := NewProxyLatencyCache(rdb, integrationDB)
	// A success remains useful after much longer than the old 24-hour ceiling.
	good := proxyGeoLifecycleInfo(proxy, time.Now().AddDate(-1, 0, 0), "Berlin")
	require.NoError(t, first.SetProxyLatency(ctx, proxy.ID, good))
	var persisted int
	require.NoError(t, integrationDB.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM proxy_geo_snapshots WHERE proxy_id=$1", proxy.ID).Scan(&persisted))
	require.Equal(t, 1, persisted)
	require.NoError(t, rdb.Del(ctx, proxyLatencyKey(proxy.ID)).Err())
	restarted := NewProxyLatencyCache(rdb, integrationDB)
	got, err := restarted.GetProxyLatencies(ctx, []int64{proxy.ID})
	require.NoError(t, err)
	require.NotNil(t, got[proxy.ID])
	require.Equal(t, good.Timezone, got[proxy.ID].Timezone)
	require.Equal(t, good.City, got[proxy.ID].City)
	require.Equal(t, good.GeoCheckedAt, got[proxy.ID].GeoCheckedAt)
	require.Equal(t, good.GeoResultUnixMs, got[proxy.ID].GeoResultUnixMs)
	// Redis is only a legacy migration source and a coordination store. Reads
	// remain correct while the old cache entry is absent.
	exists, err := rdb.Exists(ctx, proxyLatencyKey(proxy.ID)).Result()
	require.NoError(t, err)
	require.Zero(t, exists)
}

func TestProxyGeoPersistentRealStoreMigratesLegacyRedis(t *testing.T) {
	ctx := context.Background()
	proxy, rdb := proxyGeoLifecycleFixture(t)
	legacy := proxyGeoLifecycleInfo(proxy, time.Now().AddDate(0, -6, 0), "Legacy Berlin")
	payload, err := json.Marshal(legacy)
	require.NoError(t, err)
	require.NoError(t, rdb.Set(ctx, proxyLatencyKey(proxy.ID), payload, 0).Err())
	cache := NewProxyLatencyCache(rdb, integrationDB)
	got, err := cache.GetProxyLatencies(ctx, []int64{proxy.ID})
	require.NoError(t, err)
	require.Equal(t, legacy.City, got[proxy.ID].City)
	var city string
	require.NoError(t, integrationDB.QueryRowContext(ctx,
		"SELECT payload->>'city' FROM proxy_geo_snapshots WHERE proxy_id=$1", proxy.ID).Scan(&city))
	require.Equal(t, legacy.City, city)
	require.NoError(t, rdb.Del(ctx, proxyLatencyKey(proxy.ID)).Err())
	got, err = NewProxyLatencyCache(rdb, integrationDB).GetProxyLatencies(ctx, []int64{proxy.ID})
	require.NoError(t, err)
	require.Equal(t, legacy.City, got[proxy.ID].City)
}

func TestProxyGeoPersistentRealStoreConcurrentOrderingAndFailedRefresh(t *testing.T) {
	ctx := context.Background()
	proxy, rdb := proxyGeoLifecycleFixture(t)
	first, second := NewProxyLatencyCache(rdb, integrationDB), NewProxyLatencyCache(rdb, integrationDB)
	base := time.Now().Add(-time.Hour).UTC().Truncate(time.Millisecond)
	var wg sync.WaitGroup
	errCh := make(chan error, 24)
	for i := range 24 {
		info := proxyGeoLifecycleInfo(proxy, base.Add(time.Duration(i)*time.Second), fmt.Sprintf("Berlin %d", i))
		cache := first
		if i%2 == 0 {
			cache = second
		}
		wg.Add(1)
		go func() { defer wg.Done(); errCh <- cache.SetProxyLatency(ctx, proxy.ID, info) }()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		require.NoError(t, err)
	}
	latest, err := second.GetProxyLatencies(ctx, []int64{proxy.ID})
	require.NoError(t, err)
	require.Equal(t, "Berlin 23", latest[proxy.ID].City)
	require.Equal(t, base.Add(23*time.Second).UnixMilli(), latest[proxy.ID].GeoResultUnixMs)
	failedAt := base.Add(time.Minute)
	require.NoError(t, first.SetProxyLatency(ctx, proxy.ID, &service.ProxyLatencyInfo{
		RouteKey: service.ProxyGeoRouteKey(proxy), GeoResultUnixMs: failedAt.UnixMilli(), UpdatedAt: failedAt,
		GeoStatus: "failed", GeoReason: "probe_failed", Success: false,
	}))
	// An older complete result must not overwrite the newer failure metadata or
	// the retained successful location, even when written by another instance.
	require.NoError(t, second.SetProxyLatency(ctx, proxy.ID, proxyGeoLifecycleInfo(proxy, base, "stale city")))
	require.NoError(t, rdb.Del(ctx, proxyLatencyKey(proxy.ID)).Err())
	latest, err = NewProxyLatencyCache(rdb, integrationDB).GetProxyLatencies(ctx, []int64{proxy.ID})
	require.NoError(t, err)
	require.Equal(t, "Berlin 23", latest[proxy.ID].City)
	require.Equal(t, "Europe/Berlin", latest[proxy.ID].Timezone)
	require.False(t, latest[proxy.ID].Success)
	require.Equal(t, "probe_failed", latest[proxy.ID].GeoReason)
	require.Equal(t, failedAt.UnixMilli(), latest[proxy.ID].GeoResultUnixMs)
}

func TestProxyGeoPersistentRealStoreRejectsChangedRouteAndDeletedProxy(t *testing.T) {
	ctx := context.Background()
	proxy, rdb := proxyGeoLifecycleFixture(t)
	cache := NewProxyLatencyCache(rdb, integrationDB)
	oldRoute := proxyGeoLifecycleInfo(proxy, time.Now().Add(-time.Hour), "old-route-city")
	require.NoError(t, cache.SetProxyLatency(ctx, proxy.ID, oldRoute))
	_, err := integrationDB.ExecContext(ctx, "UPDATE proxies SET host='127.0.0.2', password='changed-secret' WHERE id=$1", proxy.ID)
	require.NoError(t, err)
	got, err := cache.GetProxyLatencies(ctx, []int64{proxy.ID})
	require.NoError(t, err)
	require.NotContains(t, got, proxy.ID, "the old route's location cannot be displayed for changed connection identity")
	oldRoute.GeoResultUnixMs = time.Now().UnixMilli()
	require.NoError(t, cache.SetProxyLatency(ctx, proxy.ID, oldRoute))
	proxy.Host, proxy.Password = "127.0.0.2", "changed-secret"
	current := proxyGeoLifecycleInfo(proxy, time.Now().Add(-time.Minute), "new-route-city")
	require.NoError(t, cache.SetProxyLatency(ctx, proxy.ID, current))
	require.NoError(t, cache.SetProxyLatency(ctx, proxy.ID, oldRoute))
	got, err = cache.GetProxyLatencies(ctx, []int64{proxy.ID})
	require.NoError(t, err)
	require.Equal(t, current.City, got[proxy.ID].City)
	require.Equal(t, current.RouteKey, got[proxy.ID].RouteKey)
	_, err = integrationDB.ExecContext(ctx, "UPDATE proxies SET deleted_at=NOW() WHERE id=$1", proxy.ID)
	require.NoError(t, err)
	require.NoError(t, cache.SetProxyLatency(ctx, proxy.ID, current))
	got, err = cache.GetProxyLatencies(ctx, []int64{proxy.ID})
	require.NoError(t, err)
	require.NotContains(t, got, proxy.ID)
}

func TestProxyGeoPersistentRealStoreDoesNotMigrateWrongLegacyRoute(t *testing.T) {
	ctx := context.Background()
	proxy, rdb := proxyGeoLifecycleFixture(t)
	legacy := proxyGeoLifecycleInfo(proxy, time.Now().Add(-time.Hour), "wrong-route-city")
	legacy.RouteKey = "different-route"
	payload, err := json.Marshal(legacy)
	require.NoError(t, err)
	require.NoError(t, rdb.Set(ctx, proxyLatencyKey(proxy.ID), payload, 0).Err())
	got, err := NewProxyLatencyCache(rdb, integrationDB).GetProxyLatencies(ctx, []int64{proxy.ID})
	require.NoError(t, err)
	require.NotContains(t, got, proxy.ID)
	var count int
	require.NoError(t, integrationDB.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM proxy_geo_snapshots WHERE proxy_id=$1", proxy.ID).Scan(&count))
	require.Zero(t, count)
}

type proxyGeoLifecycleProber func(context.Context, string) (*service.ProxyExitInfo, int64, error)

func (p proxyGeoLifecycleProber) ProbeProxy(ctx context.Context, url string) (*service.ProxyExitInfo, int64, error) {
	return p(ctx, url)
}

func TestProxyGeoPersistentRealStoreResolverBackfillOnlyMissing(t *testing.T) {
	ctx := context.Background()
	known, rdb := proxyGeoLifecycleFixture(t)
	missing := mustCreateProxy(t, testEntClient(t), &service.Proxy{
		Name: t.Name() + "/missing", Protocol: "http", Host: "127.0.0.2", Port: 18081,
	})
	t.Cleanup(func() {
		_, err := integrationDB.ExecContext(ctx, "DELETE FROM proxies WHERE id=$1", missing.ID)
		require.NoError(t, err)
	})
	cache := NewProxyLatencyCache(rdb, integrationDB)
	require.NoError(t, cache.SetProxyLatency(ctx, known.ID, proxyGeoLifecycleInfo(known, time.Now().AddDate(-1, 0, 0), "Saved Berlin")))
	var knownCalls, missingCalls atomic.Int64
	prober := proxyGeoLifecycleProber(func(_ context.Context, url string) (*service.ProxyExitInfo, int64, error) {
		if url == known.URL() {
			knownCalls.Add(1)
		} else if url == missing.URL() {
			missingCalls.Add(1)
		} else {
			return nil, 0, fmt.Errorf("unexpected synthetic route")
		}
		return &service.ProxyExitInfo{IP: "203.0.113.18", Country: "Japan", CountryCode: "JP", Region: "Tokyo", City: "Tokyo",
			Timezone: "Asia/Tokyo", GeoStatus: "success", GeoCheckedAt: time.Now()}, 8, nil
	})
	knownRoute := service.OpenAIEgressRoute{ProxyID: known.ID, ProxyURL: known.URL()}
	missingRoute := service.OpenAIEgressRoute{ProxyID: missing.ID, ProxyURL: missing.URL()}
	first := service.NewOpenAIEgressLocationService(prober)
	first.SetProxyLatencyCache(cache)
	t.Cleanup(first.Stop)
	require.Eventually(t, func() bool {
		return first.Resolve(knownRoute).City == "Saved Berlin" && first.Resolve(missingRoute).City == "Tokyo"
	}, 5*time.Second, 10*time.Millisecond)
	require.Zero(t, knownCalls.Load(), "a persisted successful route is hydrated without probing")
	require.EqualValues(t, 1, missingCalls.Load())
	first.Stop()
	require.NoError(t, rdb.Del(ctx, proxyLatencyKey(known.ID), proxyLatencyKey(missing.ID)).Err())
	restarted := service.NewOpenAIEgressLocationService(prober)
	restarted.SetProxyLatencyCache(NewProxyLatencyCache(rdb, integrationDB))
	t.Cleanup(restarted.Stop)
	require.Eventually(t, func() bool {
		return restarted.Resolve(knownRoute).City == "Saved Berlin" && restarted.Resolve(missingRoute).City == "Tokyo"
	}, 5*time.Second, 10*time.Millisecond)
	for range 20 {
		require.Equal(t, "Saved Berlin", restarted.Resolve(knownRoute).City)
		require.Equal(t, "Tokyo", restarted.Resolve(missingRoute).City)
	}
	require.Zero(t, knownCalls.Load())
	require.EqualValues(t, 1, missingCalls.Load(), "a new process/container must not resample successful stored geography")
}

func TestProxyGeoPersistentRealRedisProbeLeaseOwnerFence(t *testing.T) {
	ctx := context.Background()
	proxy, rdb := proxyGeoLifecycleFixture(t)
	first, ok := NewProxyLatencyCache(rdb, integrationDB).(service.ProxyProbeLeaseCache)
	require.True(t, ok)
	second, ok := NewProxyLatencyCache(rdb, integrationDB).(service.ProxyProbeLeaseCache)
	require.True(t, ok)
	releaseOld, acquired, err := first.AcquireProxyProbe(ctx, proxy.ID, service.ProxyGeoRouteKey(proxy), 250*time.Millisecond)
	require.NoError(t, err)
	require.True(t, acquired)
	t.Cleanup(releaseOld)
	_, acquired, err = second.AcquireProxyProbe(ctx, proxy.ID, "different-route", time.Second)
	require.NoError(t, err)
	require.False(t, acquired, "one proxy keeps a single active probe even across route changes")
	var releaseNew func()
	require.Eventually(t, func() bool {
		var leaseErr error
		releaseNew, acquired, leaseErr = second.AcquireProxyProbe(ctx, proxy.ID, "different-route", time.Second)
		return leaseErr == nil && acquired
	}, 3*time.Second, 25*time.Millisecond)
	t.Cleanup(releaseNew)
	releaseOld()
	_, acquired, err = first.AcquireProxyProbe(ctx, proxy.ID, "third-route", time.Second)
	require.NoError(t, err)
	require.False(t, acquired, "an expired owner's release cannot delete the replacement lease")
	releaseNew()
	releaseFinal, acquired, err := first.AcquireProxyProbe(ctx, proxy.ID, "third-route", time.Second)
	require.NoError(t, err)
	require.True(t, acquired)
	releaseFinal()
}
