package service

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type adminProxyGeoCache struct {
	mu        sync.Mutex
	values    map[int64]*ProxyLatencyInfo
	beforeGet func(context.Context)
}

func (c *adminProxyGeoCache) GetProxyLatencies(ctx context.Context, ids []int64) (map[int64]*ProxyLatencyInfo, error) {
	if c.beforeGet != nil {
		c.beforeGet(ctx)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	result := make(map[int64]*ProxyLatencyInfo)
	for _, id := range ids {
		if value := c.values[id]; value != nil {
			copy := *value
			result[id] = &copy
		}
	}
	return result, nil
}

func (c *adminProxyGeoCache) SetProxyLatency(_ context.Context, id int64, value *ProxyLatencyInfo) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.values == nil {
		c.values = make(map[int64]*ProxyLatencyInfo)
	}
	copy := *value
	c.values[id] = &copy
	return nil
}

type adminProxyGeoRepo struct {
	ProxyRepository
	value *Proxy
}

func (r *adminProxyGeoRepo) GetByID(context.Context, int64) (*Proxy, error) {
	copy := *r.value
	return &copy, nil
}

type adminProxyGeoProber func(context.Context, string) (*ProxyExitInfo, int64, error)

func (p adminProxyGeoProber) ProbeProxy(ctx context.Context, proxy string) (*ProxyExitInfo, int64, error) {
	return p(ctx, proxy)
}

func adminProxyGeoFixture() *Proxy {
	return &Proxy{ID: 7, Protocol: "http", Host: "proxy.example", Port: 8080}
}
func adminProxyGeoExit(ip string, checked time.Time) *ProxyExitInfo {
	return &ProxyExitInfo{IP: ip, Country: "United States", CountryCode: "US", Region: "California", City: "Los Angeles", Timezone: "America/Los_Angeles", GeoStatus: "success", GeoCheckedAt: checked}
}

func TestAdminProxyGeoMergePreservesOnlyUsableSameExit(t *testing.T) {
	now := time.Now()
	proxy := adminProxyGeoFixture()
	for _, tc := range []struct {
		name     string
		alter    func(*ProxyLatencyInfo)
		ip       string
		preserve bool
	}{
		{name: "same IP geo failure", ip: "203.0.113.1", preserve: true},
		{name: "connectivity failure", preserve: true},
		{name: "new IP", ip: "203.0.113.2"},
		{name: "different route", ip: "203.0.113.1", alter: func(v *ProxyLatencyInfo) { v.RouteKey = "old-route" }},
		{name: "legacy no route", ip: "203.0.113.1", alter: func(v *ProxyLatencyInfo) { v.RouteKey = "" }},
		{name: "legacy no timezone", ip: "203.0.113.1", alter: func(v *ProxyLatencyInfo) { v.Timezone = "" }},
		{name: "missing country", ip: "203.0.113.1", alter: func(v *ProxyLatencyInfo) { v.Country = "" }},
		{name: "old success remains usable", ip: "203.0.113.1", alter: func(v *ProxyLatencyInfo) { old := now.AddDate(-2, 0, 0); v.GeoCheckedAt = &old }, preserve: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			old := proxyLatencyFromExit(proxy, adminProxyGeoExit("203.0.113.1", now.Add(-time.Hour)), 10, nil, now.Add(-time.Hour))
			if tc.alter != nil {
				tc.alter(old)
			}
			cache := &adminProxyGeoCache{values: map[int64]*ProxyLatencyInfo{proxy.ID: old}}
			svc := &adminServiceImpl{proxyLatencyCache: cache}
			var exit *ProxyExitInfo
			var probeErr error
			if tc.ip == "" {
				probeErr = errors.New("probe failed")
			} else {
				exit = &ProxyExitInfo{IP: tc.ip, GeoStatus: "failed", GeoReason: "timeout", GeoCheckedAt: now}
			}
			current := proxyLatencyFromExit(proxy, exit, 20, probeErr, now)
			got := svc.saveProxyLatency(context.Background(), proxy.ID, current)
			require.Equal(t, probeErr == nil, got.Success)
			require.Equal(t, "failed", got.GeoStatus, "last good location must not hide latest failure")
			require.Equal(t, now.UnixMilli(), got.GeoResultUnixMs)
			if tc.preserve {
				require.Equal(t, "203.0.113.1", got.IPAddress)
				require.Equal(t, "Los Angeles", got.City)
				require.Equal(t, "America/Los_Angeles", got.Timezone)
				require.Equal(t, old.GeoCheckedAt, got.GeoCheckedAt)
			} else {
				require.Empty(t, got.Country)
				require.Empty(t, got.City)
				require.Empty(t, got.Timezone)
				if tc.ip != "" {
					require.Equal(t, tc.ip, got.IPAddress)
				}
			}
		})
	}
}

func TestAdminProxyGeoOldResultCannotReplaceWholeSnapshot(t *testing.T) {
	now := time.Now()
	proxy := adminProxyGeoFixture()
	latest := proxyLatencyFromExit(proxy, adminProxyGeoExit("203.0.113.2", now.Add(-time.Second)), 9, nil, now.Add(-2*time.Second))
	score := 82
	latest.QualityStatus, latest.QualityGrade, latest.QualityScore = "warn", "B", &score
	cache := &adminProxyGeoCache{values: map[int64]*ProxyLatencyInfo{proxy.ID: latest}}
	svc := &adminServiceImpl{proxyLatencyCache: cache}
	// The old request's geo completed later. Ordering must still use probe start.
	old := proxyLatencyFromExit(proxy, adminProxyGeoExit("203.0.113.1", now), 2000, nil, now.Add(-3*time.Second))
	old.Success, old.QualityGrade = false, "F"
	got := svc.saveProxyLatency(context.Background(), proxy.ID, old)
	require.Equal(t, latest, got)
	require.Equal(t, latest, cache.values[proxy.ID])
	// The same ID with changed route credentials still cannot be rolled back.
	old.RouteKey = "old-route"
	require.Equal(t, latest, svc.saveProxyLatency(context.Background(), proxy.ID, old))
}

func TestAdminProxyGeoListDropsLegacyAndInvalidLocation(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		name   string
		alter  func(*ProxyLatencyInfo)
		reason string
	}{
		{"missing route", func(v *ProxyLatencyInfo) { v.RouteKey = "" }, "not_checked"},
		{"missing timezone", func(v *ProxyLatencyInfo) { v.Timezone = "" }, "incomplete_location"},
		{"missing time", func(v *ProxyLatencyInfo) { v.GeoCheckedAt = nil }, "not_checked"},
		{"future time", func(v *ProxyLatencyInfo) { future := now.Add(time.Hour); v.GeoCheckedAt = &future }, "invalid_checked_at"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			proxy := adminProxyGeoFixture()
			info := proxyLatencyFromExit(proxy, adminProxyGeoExit("203.0.113.1", now.Add(-time.Hour)), 12, nil, now.Add(-time.Hour))
			tc.alter(info)
			svc := &adminServiceImpl{proxyLatencyCache: &adminProxyGeoCache{values: map[int64]*ProxyLatencyInfo{proxy.ID: info}}}
			rows := []ProxyWithAccountCount{{Proxy: *proxy}}
			svc.attachProxyLatency(context.Background(), rows)
			require.Equal(t, "success", rows[0].LatencyStatus)
			require.Empty(t, rows[0].Country)
			require.Empty(t, rows[0].Timezone)
			require.Equal(t, "failed", rows[0].GeoStatus)
			require.Equal(t, tc.reason, rows[0].GeoReason)
		})
	}
}

func TestAdminProxyGeoQualityFailureKeepsLocationAndFailureState(t *testing.T) {
	now := time.Now()
	proxy := adminProxyGeoFixture()
	old := proxyLatencyFromExit(proxy, adminProxyGeoExit("203.0.113.1", now.Add(-time.Hour)), 10, nil, now.Add(-time.Hour))
	cache := &adminProxyGeoCache{values: map[int64]*ProxyLatencyInfo{proxy.ID: old}}
	svc := &adminServiceImpl{proxyRepo: &adminProxyGeoRepo{value: proxy}, proxyLatencyCache: cache, proxyProber: adminProxyGeoProber(func(context.Context, string) (*ProxyExitInfo, int64, error) { return nil, 0, errors.New("unavailable") })}
	result, err := svc.CheckProxyQuality(context.Background(), proxy.ID)
	require.NoError(t, err)
	require.Equal(t, "failed", result.GeoStatus)
	require.Equal(t, "probe_failed", result.GeoReason)
	require.Equal(t, old.Timezone, result.Timezone)
	require.Equal(t, old.GeoCheckedAt, result.GeoCheckedAt)
	require.False(t, cache.values[proxy.ID].Success)
	require.GreaterOrEqual(t, cache.values[proxy.ID].GeoResultUnixMs, now.UnixMilli())
}

func TestAdminProxyGeoLateQualityDoesNotOverwriteManual(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	proxyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(started); <-release; w.WriteHeader(http.StatusOK) }))
	defer proxyServer.Close()
	defer unblock()
	parsed, err := url.Parse(proxyServer.URL)
	require.NoError(t, err)
	host, port, err := net.SplitHostPort(parsed.Host)
	require.NoError(t, err)
	portNum, err := strconv.Atoi(port)
	require.NoError(t, err)
	proxy := &Proxy{ID: 7, Protocol: "http", Host: host, Port: portNum}
	priorTargets := proxyQualityTargets
	proxyQualityTargets = []proxyQualityTarget{{Target: "fixture", URL: "http://quality.example/check", Method: http.MethodGet, AllowedStatuses: map[int]struct{}{http.StatusOK: {}}}}
	defer func() { proxyQualityTargets = priorTargets }()
	cache := &adminProxyGeoCache{}
	var calls atomic.Int32
	svc := &adminServiceImpl{proxyRepo: &adminProxyGeoRepo{value: proxy}, proxyLatencyCache: cache, proxyProber: adminProxyGeoProber(func(context.Context, string) (*ProxyExitInfo, int64, error) {
		ip := "203.0.113.1"
		if calls.Add(1) > 1 {
			ip = "203.0.113.2"
		}
		return adminProxyGeoExit(ip, time.Now()), 8, nil
	})}
	done := make(chan error, 1)
	go func() { _, err := svc.CheckProxyQuality(context.Background(), proxy.ID); done <- err }()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("quality request did not reach local fixture")
	}
	// Cross the millisecond ordering boundary before starting the newer probe.
	time.Sleep(2 * time.Millisecond)
	manual, err := svc.TestProxy(context.Background(), proxy.ID)
	require.NoError(t, err)
	require.Equal(t, "203.0.113.2", manual.IPAddress)
	unblock()
	require.NoError(t, <-done)
	require.Equal(t, "203.0.113.2", cache.values[proxy.ID].IPAddress)
	require.True(t, cache.values[proxy.ID].Success)
	require.Empty(t, cache.values[proxy.ID].QualityGrade, "older quality run must not overwrite newer snapshot")
}

func TestAdminProxyGeoCacheWaitCanBeCanceled(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	cache := &adminProxyGeoCache{beforeGet: func(context.Context) { once.Do(func() { close(started); <-release }) }}
	svc := &adminServiceImpl{proxyLatencyCache: cache}
	proxy := adminProxyGeoFixture()
	done := make(chan struct{})
	go func() {
		defer close(done)
		svc.saveProxyLatency(context.Background(), proxy.ID, proxyLatencyFromExit(proxy, nil, 0, errors.New("failed"), time.Now()))
	}()
	<-started
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	returned := make(chan struct{})
	go func() { defer close(returned); svc.saveProxyLatency(ctx, proxy.ID, &ProxyLatencyInfo{}) }()
	select {
	case <-returned:
	case <-time.After(time.Second):
		t.Error("canceled save waited for another cache operation")
	}
	close(release)
	<-done
}

func TestAdminProxyGeoQualityPartialResultDoesNotEraseCompleteLocation(t *testing.T) {
	now := time.Now()
	proxy := adminProxyGeoFixture()
	old := proxyLatencyFromExit(proxy, adminProxyGeoExit("203.0.113.1", now.Add(-time.Hour)), 10, nil, now.Add(-time.Hour))
	cache := &adminProxyGeoCache{values: map[int64]*ProxyLatencyInfo{proxy.ID: old}}
	svc := &adminServiceImpl{proxyLatencyCache: cache}
	result := &ProxyQualityCheckResult{ProxyID: proxy.ID, Score: 100, Grade: "A", CheckedAt: now.Unix(), Items: []ProxyQualityCheckItem{{Target: "base_connectivity", Status: "pass"}}}
	// Compatibility probers may still return the legacy partial payload.
	svc.saveProxyQualitySnapshot(context.Background(), proxy, result, &ProxyExitInfo{IP: "203.0.113.1", CountryCode: "US"}, now)
	require.Equal(t, "Los Angeles", result.City)
	require.Equal(t, "America/Los_Angeles", result.Timezone)
	require.Equal(t, "failed", result.GeoStatus)
	require.Equal(t, "incomplete_location", result.GeoReason)
	require.Equal(t, "A", cache.values[proxy.ID].QualityGrade)
}

func TestAdminProxyGeoOldFailureDoesNotSupersedeResolverObservation(t *testing.T) {
	now := time.Now()
	resolver := newOpenAIEgressLocationService(nil, func() time.Time { return now }, 30, time.Minute)
	defer resolver.Stop()
	proxy := adminProxyGeoFixture()
	svc := &adminServiceImpl{egressLocationService: resolver}
	checked := now.Add(-time.Minute)
	svc.observeProxyExit(proxy, adminProxyGeoExit("203.0.113.1", checked), nil, checked)
	before := resolver.Resolve(proxyEgressRoute(proxy))
	svc.observeProxyExit(proxy, nil, errors.New("old failed probe"), checked.Add(-time.Minute))
	after := resolver.Resolve(proxyEgressRoute(proxy))
	require.Equal(t, before, after)
	resolver.mu.Lock()
	entry := resolver.entries[before.RouteKey]
	require.Equal(t, checked, entry.checkedAt)
	require.Empty(t, entry.reason)
	resolver.mu.Unlock()
}

type adminProxyGeoWinningCache struct {
	adminProxyGeoCache
	winner *ProxyLatencyInfo
}

func (c *adminProxyGeoWinningCache) SetProxyLatency(ctx context.Context, id int64, _ *ProxyLatencyInfo) error {
	// Simulate another instance committing a newer test between our read/write.
	// The persistent repository silently rejects this caller's older candidate.
	return c.adminProxyGeoCache.SetProxyLatency(ctx, id, c.winner)
}

func TestAdminProxyGeoManualPublishesCommittedWinner(t *testing.T) {
	proxy := adminProxyGeoFixture()
	cache := &adminProxyGeoWinningCache{}
	resolver := NewOpenAIEgressLocationService(nil)
	defer resolver.Stop()
	svc := &adminServiceImpl{proxyRepo: &adminProxyGeoRepo{value: proxy}, proxyLatencyCache: cache, egressLocationService: resolver}
	svc.proxyProber = adminProxyGeoProber(func(context.Context, string) (*ProxyExitInfo, int64, error) {
		time.Sleep(2 * time.Millisecond)
		winnerStarted := time.Now()
		cache.winner = proxyLatencyFromExit(proxy, adminProxyGeoExit("203.0.113.2", winnerStarted), 10, nil, winnerStarted)
		time.Sleep(2 * time.Millisecond)
		// The old attempt's geolocation completes later than the newer attempt.
		return adminProxyGeoExit("203.0.113.1", time.Now()), 100, nil
	})
	result, err := svc.TestProxy(context.Background(), proxy.ID)
	require.NoError(t, err)
	require.Equal(t, "203.0.113.2", result.IPAddress)
	location := resolver.Resolve(proxyEgressRoute(proxy))
	require.Equal(t, "203.0.113.2", location.IPAddress)
	require.Equal(t, *cache.winner.GeoCheckedAt, location.CheckedAt)
}

func TestMergeProxyLatencySnapshotNewRouteDoesNotReuseOldClock(t *testing.T) {
	now := time.Now()
	proxy := adminProxyGeoFixture()
	old := proxyLatencyFromExit(proxy, adminProxyGeoExit("203.0.113.1", now), 10, nil, now)
	changed := *proxy
	changed.Host = "replacement.proxy.example"
	current := proxyLatencyFromExit(&changed, adminProxyGeoExit("203.0.113.2", now.Add(-time.Second)), 20, nil, now.Add(-time.Second))
	merged := MergeProxyLatencySnapshot(current, old, now)
	require.Equal(t, current, merged)
}
