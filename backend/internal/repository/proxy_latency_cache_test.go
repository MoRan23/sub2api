package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func newProxyLatencyCacheTest(t *testing.T) (*proxyLatencyCache, *miniredis.Miniredis) {
	t.Helper()
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	return &proxyLatencyCache{rdb: client}, server
}

// Decode the wire metadata to also exercise rolling-upgrade compatibility with
// cache records written before the observation-order field existed.
func proxyLatencyTestInfo(t *testing.T, route string, sequence int64, city string) *service.ProxyLatencyInfo {
	t.Helper()
	var info service.ProxyLatencyInfo
	payload, err := json.Marshal(map[string]any{
		"success": true, "route_key": route, "geo_result_unix_ms": sequence,
		"city": city, "geo_status": "success", "updated_at": time.Now().UTC(),
	})
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(payload, &info))
	return &info
}

func TestProxyLatencyCacheRejectsOlderObservationAtomically(t *testing.T) {
	cache, server := newProxyLatencyCacheTest(t)
	ctx := context.Background()
	newer := proxyLatencyTestInfo(t, "route-a", 2000, "Berlin")
	newer.QualityStatus = "pass"
	require.NoError(t, cache.SetProxyLatency(ctx, 7, newer))
	older := proxyLatencyTestInfo(t, "route-a", 1000, "Paris")
	older.QualityStatus = "fail"
	require.NoError(t, cache.SetProxyLatency(ctx, 7, older))
	values, err := cache.GetProxyLatencies(ctx, []int64{7})
	require.NoError(t, err)
	require.Equal(t, "Berlin", values[7].City)
	require.Equal(t, "pass", values[7].QualityStatus, "reject the entire stale snapshot")
	require.Zero(t, server.TTL(proxyLatencyKey(7)), "preserve the existing non-expiring cache contract")
	newest := proxyLatencyTestInfo(t, "route-a", 3000, "Tokyo")
	require.NoError(t, cache.SetProxyLatency(ctx, 7, newest))
	values, err = cache.GetProxyLatencies(ctx, []int64{7})
	require.NoError(t, err)
	require.Equal(t, "Tokyo", values[7].City)
}

func TestProxyLatencyCacheConcurrentOutOfOrderResults(t *testing.T) {
	cache, _ := newProxyLatencyCacheTest(t)
	var wg sync.WaitGroup
	errors := make(chan error, 48)
	for i := range 48 {
		info := proxyLatencyTestInfo(t, "same-route", int64(1000+i), fmt.Sprintf("city-%d", i))
		wg.Add(1)
		go func() {
			defer wg.Done()
			errors <- cache.SetProxyLatency(context.Background(), 9, info)
		}()
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		require.NoError(t, err)
	}
	values, err := cache.GetProxyLatencies(context.Background(), []int64{9})
	require.NoError(t, err)
	require.Equal(t, "city-47", values[9].City)
}

func TestProxyLatencyCacheLegacyEqualAndChangedRouteCompatibility(t *testing.T) {
	cache, _ := newProxyLatencyCacheTest(t)
	ctx := context.Background()
	for _, step := range []struct {
		route string
		seq   int64
		city  string
		want  string
	}{
		{"", 0, "legacy-first", "legacy-first"},
		{"", 0, "legacy-second", "legacy-second"},
		{"route-a", 2000, "new-format", "new-format"},
		{"route-a", 0, "missing-observation-time", "new-format"},
		{"route-a", 2000, "quality-update", "quality-update"},
		{"route-b", 1000, "new-route", "new-route"},
	} {
		require.NoError(t, cache.SetProxyLatency(ctx, 1, proxyLatencyTestInfo(t, step.route, step.seq, step.city)))
		values, err := cache.GetProxyLatencies(ctx, []int64{1})
		require.NoError(t, err)
		require.Equal(t, step.want, values[1].City)
	}
}

func TestProxyLatencyCacheMalformedRecordAndNil(t *testing.T) {
	cache, server := newProxyLatencyCacheTest(t)
	ctx := context.Background()
	require.NoError(t, server.Set(proxyLatencyKey(3), "not-json"))
	require.NoError(t, cache.SetProxyLatency(ctx, 3, nil))
	values, err := cache.GetProxyLatencies(ctx, []int64{3, 4})
	require.NoError(t, err)
	require.Empty(t, values)
	require.NoError(t, cache.SetProxyLatency(ctx, 3, proxyLatencyTestInfo(t, "route", 100, "Berlin")))
	values, err = cache.GetProxyLatencies(ctx, []int64{3})
	require.NoError(t, err)
	require.Equal(t, "Berlin", values[3].City)
}

func TestProxyLatencyCacheDatabaseFailureDoesNotReturnRedisSnapshot(t *testing.T) {
	cache, server := newProxyLatencyCacheTest(t)
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	cache.db = db
	legacy, err := json.Marshal(proxyLatencyTestInfo(t, "old-route", 100, "Berlin"))
	require.NoError(t, err)
	require.NoError(t, server.Set(proxyLatencyKey(3), string(legacy)))
	mock.ExpectQuery("SELECT p.id").WillReturnError(errors.New("database unavailable"))
	values, err := cache.GetProxyLatencies(context.Background(), []int64{3})
	require.EqualError(t, err, "database unavailable")
	require.Empty(t, values)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestProxyProbeLeaseExpiryAndOwnerFencing(t *testing.T) {
	first, server := newProxyLatencyCacheTest(t)
	second := &proxyLatencyCache{rdb: first.rdb}
	ctx := context.Background()
	releaseFirst, acquired, err := first.AcquireProxyProbe(ctx, 7, "route-a", time.Minute)
	require.NoError(t, err)
	require.True(t, acquired)
	_, acquired, err = second.AcquireProxyProbe(ctx, 7, "route-b", time.Minute)
	require.NoError(t, err)
	require.False(t, acquired, "a changed route still shares the proxy's probe slot")
	server.FastForward(time.Minute)
	releaseSecond, acquired, err := second.AcquireProxyProbe(ctx, 7, "route-b", time.Minute)
	require.NoError(t, err)
	require.True(t, acquired)
	releaseFirst()
	_, acquired, err = first.AcquireProxyProbe(ctx, 7, "route-b", time.Minute)
	require.NoError(t, err)
	require.False(t, acquired, "expired owner cannot release its successor's lock")
	releaseSecond()
	releaseLast, acquired, err := first.AcquireProxyProbe(ctx, 7, "route-b", time.Minute)
	require.NoError(t, err)
	require.True(t, acquired)
	releaseLast()
}

func TestProxyProbeLeaseReleaseAfterCancellationAndUnavailableStore(t *testing.T) {
	cache, _ := newProxyLatencyCacheTest(t)
	ctx, cancel := context.WithCancel(context.Background())
	release, acquired, err := cache.AcquireProxyProbe(ctx, 8, "route", time.Minute)
	require.NoError(t, err)
	require.True(t, acquired)
	cancel()
	release()
	lastRelease, acquired, err := cache.AcquireProxyProbe(context.Background(), 8, "route", time.Minute)
	require.NoError(t, err)
	require.True(t, acquired)
	lastRelease()
	missing := &proxyLatencyCache{}
	_, acquired, err = missing.AcquireProxyProbe(context.Background(), 8, "route", time.Minute)
	require.Error(t, err)
	require.False(t, acquired)
}
