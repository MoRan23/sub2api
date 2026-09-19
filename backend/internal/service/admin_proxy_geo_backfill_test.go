package service

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/stretchr/testify/require"
)

type proxyGeoBackfillRepo struct {
	ProxyRepository
	proxies []Proxy
	pages   atomic.Int32
}

func (r *proxyGeoBackfillRepo) Create(_ context.Context, p *Proxy) error {
	p.ID = 42
	return nil
}

func (r *proxyGeoBackfillRepo) ListWithFilters(_ context.Context, params pagination.PaginationParams, _, status, _ string) ([]Proxy, *pagination.PaginationResult, error) {
	r.pages.Add(1)
	if status != "" {
		return nil, nil, errors.New("backfill must include disabled proxies")
	}
	start := (params.Page - 1) * params.PageSize
	if start >= len(r.proxies) {
		return nil, nil, nil
	}
	end := min(start+params.PageSize, len(r.proxies))
	return append([]Proxy(nil), r.proxies[start:end]...), nil, nil
}

func waitProxyGeoIdle(t *testing.T, resolver *OpenAIEgressLocationService, proxy *Proxy) {
	t.Helper()
	require.Eventually(t, func() bool {
		resolver.mu.Lock()
		defer resolver.mu.Unlock()
		entry := resolver.entries[proxyGeoRouteKey(proxy)]
		return entry != nil && !entry.pending
	}, time.Second*3, time.Millisecond*5)
}

func TestAdminProxyGeoBackfillRestoresPermanentSuccessAndFillsMissingOnce(t *testing.T) {
	now := time.Now()
	first := *adminProxyGeoFixture()
	first.Status = StatusActive
	second := first
	second.ID++
	second.Status = StatusDisabled
	old := proxyLatencyFromExit(&first, adminProxyGeoExit("203.0.113.1", now.AddDate(-2, 0, 0)), 12, nil, now.AddDate(-2, 0, 0))
	cache := &adminProxyGeoCache{values: map[int64]*ProxyLatencyInfo{first.ID: old}}
	var calls atomic.Int32
	resolver := NewOpenAIEgressLocationService(adminProxyGeoProber(func(context.Context, string) (*ProxyExitInfo, int64, error) {
		calls.Add(1)
		return adminProxyGeoExit("203.0.113.2", time.Now()), 20, nil
	}))
	resolver.SetProxyLatencyCache(cache)
	defer resolver.Stop()
	svc := &adminServiceImpl{proxyRepo: &proxyGeoBackfillRepo{proxies: []Proxy{first, second}}, proxyLatencyCache: cache, egressLocationService: resolver}
	stop := svc.startProxyGeoBackfill()
	defer stop()
	// Startup restoration is synchronous; the first business request keeps its
	// stored timezone, while missing proxies are completed asynchronously.
	got := resolver.Resolve(proxyEgressRoute(&first))
	require.Equal(t, "America/Los_Angeles", got.Timezone)
	require.Equal(t, old.IPAddress, got.IPAddress)
	require.Equal(t, *old.GeoCheckedAt, got.CheckedAt)
	waitProxyGeoIdle(t, resolver, &second)
	require.EqualValues(t, 1, calls.Load())
	for range 3 {
		svc.backfillProxyGeo(context.Background())
	}
	require.EqualValues(t, 1, calls.Load())
	rows := []ProxyWithAccountCount{{Proxy: first}, {Proxy: second}}
	svc.attachProxyLatency(context.Background(), rows)
	require.Equal(t, "Los Angeles", rows[0].City)
	require.Equal(t, "America/Los_Angeles", rows[0].Timezone)
	require.Equal(t, "203.0.113.2", rows[1].IPAddress)
	require.Equal(t, StatusDisabled, rows[1].Status, "metadata backfill must not enable the proxy")
}

func TestAdminProxyGeoCreateQueuesBoundedWorkWithoutWaiting(t *testing.T) {
	started := make(chan struct{}, 1)
	resolver := NewOpenAIEgressLocationService(adminProxyGeoProber(func(ctx context.Context, _ string) (*ProxyExitInfo, int64, error) {
		started <- struct{}{}
		<-ctx.Done()
		return nil, 0, ctx.Err()
	}))
	resolver.SetProxyLatencyCache(&adminProxyGeoCache{})
	defer resolver.Stop()
	svc := &adminServiceImpl{proxyRepo: &proxyGeoBackfillRepo{}, egressLocationService: resolver}
	proxy, err := svc.CreateProxy(context.Background(), &CreateProxyInput{Name: "new", Protocol: "http", Host: "proxy.example", Port: 8080})
	require.NoError(t, err)
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("new proxy was not scheduled")
	}
	for range 20 {
		svc.scheduleProxyGeo(proxy)
	}
	resolver.mu.Lock()
	require.True(t, resolver.entries[proxyGeoRouteKey(proxy)].pending)
	require.Empty(t, resolver.queue, "in-flight probes must not be queued twice")
	resolver.mu.Unlock()
	// Cancellation reaches the blocked probe, and Stop waits for it to finish.
	resolver.Stop()
}

type failingProxyGeoCache struct{ ProxyLatencyCache }

func (failingProxyGeoCache) GetProxyLatencies(context.Context, []int64) (map[int64]*ProxyLatencyInfo, error) {
	return nil, errors.New("storage unavailable")
}

func TestAdminProxyGeoBackfillStorageFailureDoesNotProbe(t *testing.T) {
	proxy := *adminProxyGeoFixture()
	var calls atomic.Int32
	resolver := NewOpenAIEgressLocationService(adminProxyGeoProber(func(context.Context, string) (*ProxyExitInfo, int64, error) {
		calls.Add(1)
		return nil, 0, errors.New("must not run")
	}))
	defer resolver.Stop()
	svc := &adminServiceImpl{proxyRepo: &proxyGeoBackfillRepo{proxies: []Proxy{proxy}}, proxyLatencyCache: failingProxyGeoCache{}, egressLocationService: resolver}
	svc.backfillProxyGeo(context.Background())
	require.Zero(t, calls.Load())
	resolver.mu.Lock()
	require.Empty(t, resolver.entries)
	resolver.mu.Unlock()
}

func TestAdminProxyGeoBackfillPagesAndStops(t *testing.T) {
	proxies := make([]Proxy, proxyGeoBackfillPageSize+1)
	cache := &adminProxyGeoCache{values: make(map[int64]*ProxyLatencyInfo)}
	for i := range proxies {
		proxies[i] = *adminProxyGeoFixture()
		proxies[i].ID = int64(i + 1)
		cache.values[proxies[i].ID] = proxyLatencyFromExit(&proxies[i], adminProxyGeoExit("203.0.113.1", time.Now().Add(-time.Hour)), 10, nil, time.Now().Add(-time.Hour))
	}
	repo := &proxyGeoBackfillRepo{proxies: proxies}
	resolver := NewOpenAIEgressLocationService(nil)
	defer resolver.Stop()
	svc := &adminServiceImpl{proxyRepo: repo, proxyLatencyCache: cache, egressLocationService: resolver}
	stop := svc.startProxyGeoBackfill()
	require.EqualValues(t, 2, repo.pages.Load())
	stop()
	stop()
}

func TestAdminProxyGeoBackfillRepairsInvalidCompleteSnapshot(t *testing.T) {
	for _, tc := range []struct {
		name  string
		alter func(*ProxyLatencyInfo)
	}{
		{"invalid IP", func(info *ProxyLatencyInfo) { info.IPAddress = "not-an-ip" }},
		{"invalid timezone", func(info *ProxyLatencyInfo) { info.Timezone = "Invalid/Timezone" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			proxy := *adminProxyGeoFixture()
			old := time.Now().Add(-time.Hour)
			info := proxyLatencyFromExit(&proxy, adminProxyGeoExit("203.0.113.1", old), 10, nil, old)
			info.UpdatedAt = old
			tc.alter(info)
			cache := &adminProxyGeoCache{values: map[int64]*ProxyLatencyInfo{proxy.ID: info}}
			var calls atomic.Int32
			resolver := NewOpenAIEgressLocationService(adminProxyGeoProber(func(context.Context, string) (*ProxyExitInfo, int64, error) {
				calls.Add(1)
				return adminProxyGeoExit("203.0.113.2", time.Now()), 10, nil
			}))
			resolver.SetProxyLatencyCache(cache)
			defer resolver.Stop()
			svc := &adminServiceImpl{proxyRepo: &proxyGeoBackfillRepo{proxies: []Proxy{proxy}}, proxyLatencyCache: cache, egressLocationService: resolver}
			svc.backfillProxyGeo(context.Background())
			waitProxyGeoIdle(t, resolver, &proxy)
			require.EqualValues(t, 1, calls.Load())
			require.Equal(t, "203.0.113.2", resolver.Resolve(proxyEgressRoute(&proxy)).IPAddress)
		})
	}
}
