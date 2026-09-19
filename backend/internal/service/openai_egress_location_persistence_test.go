package service

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type durableEgressTestCache struct {
	mu        sync.Mutex
	values    map[int64]*ProxyLatencyInfo
	getErr    error
	setErr    error
	rejectOld bool
	leaseHeld bool
	reads     int
	writes    int
}

func (c *durableEgressTestCache) GetProxyLatencies(_ context.Context, ids []int64) (map[int64]*ProxyLatencyInfo, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.reads++
	result := map[int64]*ProxyLatencyInfo{}
	for _, id := range ids {
		if value := c.values[id]; value != nil {
			copy := *value
			result[id] = &copy
		}
	}
	return result, c.getErr
}

func (c *durableEgressTestCache) SetProxyLatency(_ context.Context, id int64, value *ProxyLatencyInfo) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.writes++
	if c.setErr != nil {
		return c.setErr
	}
	if c.values == nil {
		c.values = map[int64]*ProxyLatencyInfo{}
	}
	if current := c.values[id]; c.rejectOld && current != nil && current.RouteKey == value.RouteKey && current.GeoResultUnixMs > value.GeoResultUnixMs {
		return nil
	}
	copy := *value
	c.values[id] = &copy
	return nil
}

func (c *durableEgressTestCache) AcquireProxyProbe(context.Context, int64, string, time.Duration) (func(), bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.leaseHeld {
		return nil, false, nil
	}
	c.leaseHeld = true
	return func() { c.mu.Lock(); c.leaseHeld = false; c.mu.Unlock() }, true, nil
}

func durableEgressFixture(now time.Time) (OpenAIEgressRoute, *ProxyLatencyInfo) {
	proxy := &Proxy{ID: 12, Protocol: "http", Host: "proxy.example", Port: 8080, Username: "user", Password: "secret"}
	return proxyEgressRoute(proxy), proxyLatencyFromExit(proxy, egressLocationTestInfo("203.0.113.9", now), 10, nil, now)
}

func TestOpenAIEgressLocationManagedSuccessDoesNotExpire(t *testing.T) {
	clock, now := egressLocationClock(t)
	var calls atomic.Int64
	s := newOpenAIEgressLocationService(egressLocationTestProber(func(context.Context, string) (*ProxyExitInfo, int64, error) {
		calls.Add(1)
		return nil, 0, errors.New("unexpected refresh")
	}), now, 30, time.Minute)
	t.Cleanup(s.Stop)
	route, _ := durableEgressFixture(now())
	s.ObserveResult(route, egressLocationTestInfo("203.0.113.9", now()), nil)
	before := s.Resolve(route)
	clock.Add(int64(365 * 24 * time.Hour))
	require.Equal(t, before, s.Resolve(route))
	require.Zero(t, calls.Load())
	s.ObserveResult(route, egressLocationTestInfo("203.0.113.10", now()), nil)
	require.Equal(t, "203.0.113.10", s.Resolve(route).IPAddress)
}

func TestOpenAIEgressLocationRestoresDurableSnapshotWithoutProbe(t *testing.T) {
	for _, latestStatus := range []string{"success", "failed"} {
		t.Run(latestStatus, func(t *testing.T) {
			_, now := egressLocationClock(t)
			route, value := durableEgressFixture(now().Add(-365 * 24 * time.Hour))
			value.GeoStatus = latestStatus
			value.UpdatedAt = now().Add(-time.Minute)
			value.GeoResultUnixMs = value.UpdatedAt.UnixMilli()
			cache := &durableEgressTestCache{values: map[int64]*ProxyLatencyInfo{route.ProxyID: value}}
			var calls atomic.Int64
			s := newOpenAIEgressLocationService(egressLocationTestProber(func(context.Context, string) (*ProxyExitInfo, int64, error) {
				calls.Add(1)
				return nil, 0, errors.New("must use durable result")
			}), now, 30, time.Minute)
			t.Cleanup(s.Stop)
			s.SetProxyLatencyCache(cache)
			require.Eventually(t, func() bool { return s.Resolve(route).City == "Berlin" }, time.Second, time.Millisecond)
			require.Equal(t, *value.GeoCheckedAt, s.Resolve(route).CheckedAt)
			require.Zero(t, calls.Load())
		})
	}
}

func TestOpenAIEgressLocationObserveProxySnapshotIsImmediateAndIsolated(t *testing.T) {
	_, now := egressLocationClock(t)
	route, value := durableEgressFixture(now().Add(-365 * 24 * time.Hour))
	s := newOpenAIEgressLocationService(nil, now, 30, time.Minute)
	t.Cleanup(s.Stop)
	require.True(t, s.ObserveProxySnapshot(route, value))
	require.Equal(t, "Berlin", s.Resolve(route).City)
	changed := route
	changed.ProxyURL = "http://user:replacement@proxy.example:8080"
	require.False(t, s.ObserveProxySnapshot(changed, value))
	require.Equal(t, "fallback", s.Resolve(changed).Status)
	require.False(t, s.ObserveProxySnapshot(OpenAIEgressRoute{}, value))
	future := *value
	checked := now().Add(time.Hour)
	future.GeoCheckedAt = &checked
	require.False(t, s.ObserveProxySnapshot(route, &future))
}

func TestOpenAIEgressLocationStorageFailureDoesNotProbe(t *testing.T) {
	_, now := egressLocationClock(t)
	route, _ := durableEgressFixture(now())
	var calls atomic.Int64
	s := newOpenAIEgressLocationService(egressLocationTestProber(func(context.Context, string) (*ProxyExitInfo, int64, error) {
		calls.Add(1)
		return nil, 0, errors.New("unexpected probe")
	}), now, 30, time.Minute)
	t.Cleanup(s.Stop)
	s.SetProxyLatencyCache(&durableEgressTestCache{getErr: errors.New("store unavailable")})
	require.Eventually(t, func() bool { return s.Resolve(route).Reason == "snapshot_store_unavailable" }, time.Second, time.Millisecond)
	require.Zero(t, calls.Load())
}

func TestOpenAIEgressLocationRetriesPersistenceWithoutRepeatingProbe(t *testing.T) {
	clock, now := egressLocationClock(t)
	route, _ := durableEgressFixture(now())
	cache := &durableEgressTestCache{setErr: errors.New("write unavailable")}
	var calls atomic.Int64
	s := newOpenAIEgressLocationService(egressLocationTestProber(func(context.Context, string) (*ProxyExitInfo, int64, error) {
		calls.Add(1)
		return egressLocationTestInfo("203.0.113.9", now()), 10, nil
	}), now, 30, time.Minute)
	t.Cleanup(s.Stop)
	s.SetProxyLatencyCache(cache)
	require.Eventually(t, func() bool {
		s.Resolve(route)
		s.mu.Lock()
		defer s.mu.Unlock()
		entry := s.entries[openAIEgressRouteKey(route, s.instance)]
		return entry != nil && !entry.pending && entry.reason == "snapshot_store_unavailable"
	}, time.Second, time.Millisecond)
	cache.mu.Lock()
	cache.setErr = nil
	cache.mu.Unlock()
	clock.Add(int64(openAIEgressLocationRetry))
	require.Eventually(t, func() bool {
		s.Resolve(route)
		cache.mu.Lock()
		defer cache.mu.Unlock()
		return cache.values[route.ProxyID] != nil
	}, time.Second, time.Millisecond)
	require.EqualValues(t, 1, calls.Load())
	cache.mu.Lock()
	require.Equal(t, "Europe/Berlin", cache.values[route.ProxyID].Timezone)
	cache.mu.Unlock()
}

func TestOpenAIEgressLocationRestoresFailureRetryAcrossRestart(t *testing.T) {
	clock, now := egressLocationClock(t)
	route, info := durableEgressFixture(now())
	info.Country, info.Region, info.City, info.Timezone = "", "", "", ""
	info.GeoStatus, info.UpdatedAt = "failed", now().Add(-time.Minute)
	cache := &durableEgressTestCache{values: map[int64]*ProxyLatencyInfo{route.ProxyID: info}}
	var calls atomic.Int64
	s := newOpenAIEgressLocationService(egressLocationTestProber(func(context.Context, string) (*ProxyExitInfo, int64, error) {
		calls.Add(1)
		return egressLocationTestInfo("203.0.113.9", now()), 10, nil
	}), now, 30, time.Minute)
	t.Cleanup(s.Stop)
	s.SetProxyLatencyCache(cache)
	require.Eventually(t, func() bool { return s.Resolve(route).Reason == "probe_retry_pending" }, time.Second, time.Millisecond)
	require.Zero(t, calls.Load())
	clock.Add(int64(4 * time.Minute))
	require.Eventually(t, func() bool { return s.Resolve(route).City == "Berlin" }, time.Second, time.Millisecond)
	require.EqualValues(t, 1, calls.Load())
}

func TestOpenAIEgressLocationMultipleInstancesShareProbeLease(t *testing.T) {
	cache := &durableEgressTestCache{}
	started, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int64
	prober := egressLocationTestProber(func(ctx context.Context, _ string) (*ProxyExitInfo, int64, error) {
		if calls.Add(1) == 1 {
			close(started)
		}
		select {
		case <-release:
			return egressLocationTestInfo("203.0.113.9", time.Now()), 10, nil
		case <-ctx.Done():
			return nil, 0, ctx.Err()
		}
	})
	a, b := NewOpenAIEgressLocationService(prober), NewOpenAIEgressLocationService(prober)
	t.Cleanup(a.Stop)
	t.Cleanup(b.Stop)
	a.SetProxyLatencyCache(cache)
	b.SetProxyLatencyCache(cache)
	route, _ := durableEgressFixture(time.Now())
	a.Resolve(route)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("probe did not start")
	}
	require.Eventually(t, func() bool { return b.Resolve(route).Reason == "probe_in_progress" }, time.Second, time.Millisecond)
	close(release)
	require.Eventually(t, func() bool { return a.Resolve(route).City == "Berlin" }, time.Second, time.Millisecond)
	require.EqualValues(t, 1, calls.Load())
}

func TestOpenAIEgressLocationLateProbePublishesCommittedWinner(t *testing.T) {
	clock, now := egressLocationClock(t)
	cache := &durableEgressTestCache{rejectOld: true}
	started, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int64
	s := newOpenAIEgressLocationService(egressLocationTestProber(func(ctx context.Context, _ string) (*ProxyExitInfo, int64, error) {
		calls.Add(1)
		close(started)
		select {
		case <-release:
			// The earlier physical request reaches enrichment after the newer
			// manual request has already committed on a different instance.
			return egressLocationTestInfo("203.0.113.1", now()), 10, nil
		case <-ctx.Done():
			return nil, 0, ctx.Err()
		}
	}), now, 30, time.Minute)
	t.Cleanup(s.Stop)
	s.SetProxyLatencyCache(cache)
	route, _ := durableEgressFixture(now())
	s.Resolve(route)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("probe did not start")
	}
	clock.Add(int64(time.Second))
	_, winner := durableEgressFixture(now())
	winner.IPAddress = "203.0.113.2"
	require.NoError(t, cache.SetProxyLatency(context.Background(), route.ProxyID, winner))
	clock.Add(int64(10 * time.Second))
	close(release)
	require.Eventually(t, func() bool { return s.Resolve(route).IPAddress == winner.IPAddress }, time.Second, time.Millisecond)
	require.Equal(t, *winner.GeoCheckedAt, s.Resolve(route).CheckedAt)
	// A subsequent committed observation must also outrank the rejected probe,
	// even when its check predates that old probe's late enrichment timestamp.
	_, next := durableEgressFixture(now().Add(-5 * time.Second))
	next.IPAddress = "203.0.113.3"
	require.True(t, s.ObserveProxySnapshot(route, next))
	require.Equal(t, next.IPAddress, s.Resolve(route).IPAddress)
	require.EqualValues(t, 1, calls.Load())
}

func TestOpenAIEgressLocationManualAttemptOrderIgnoresLateEnrichment(t *testing.T) {
	clock, now := egressLocationClock(t)
	startedAt := now()
	route, _ := durableEgressFixture(startedAt)
	clock.Add(int64(20 * time.Second))
	s := newOpenAIEgressLocationService(nil, now, 30, time.Minute)
	t.Cleanup(s.Stop)
	s.SetProxyLatencyCache(&durableEgressTestCache{})
	lateOld := egressLocationTestInfo("203.0.113.1", startedAt.Add(10*time.Second))
	s.ObserveResultAt(route, lateOld, nil, startedAt)
	_, winner := durableEgressFixture(startedAt.Add(time.Second))
	winner.IPAddress = "203.0.113.2"
	require.True(t, s.ObserveProxySnapshot(route, winner))
	require.Equal(t, winner.IPAddress, s.Resolve(route).IPAddress)
	s.ObserveResultAt(route, lateOld, nil, startedAt)
	require.Equal(t, winner.IPAddress, s.Resolve(route).IPAddress)
}

func TestOpenAIEgressLocationRemoteNewIPFailureClearsOldLocationAndRetries(t *testing.T) {
	clock, now := egressLocationClock(t)
	route, original := durableEgressFixture(now())
	cache := &durableEgressTestCache{values: map[int64]*ProxyLatencyInfo{route.ProxyID: original}}
	started, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int64
	s := newOpenAIEgressLocationService(egressLocationTestProber(func(ctx context.Context, _ string) (*ProxyExitInfo, int64, error) {
		calls.Add(1)
		close(started)
		select {
		case <-release:
			return egressLocationTestInfo("203.0.113.10", now()), 10, nil
		case <-ctx.Done():
			return nil, 0, ctx.Err()
		}
	}), now, 30, time.Minute)
	t.Cleanup(s.Stop)
	s.SetProxyLatencyCache(cache)
	require.True(t, s.ObserveProxySnapshot(route, original))
	clock.Add(int64(time.Second))
	failed := &ProxyLatencyInfo{RouteKey: original.RouteKey, IPAddress: "203.0.113.10", GeoStatus: "failed",
		GeoResultUnixMs: now().UnixMilli(), UpdatedAt: now()}
	require.NoError(t, cache.SetProxyLatency(context.Background(), route.ProxyID, failed))
	require.False(t, s.ObserveProxySnapshot(route, failed))
	actual := s.Resolve(route)
	require.Equal(t, "fallback", actual.Status)
	require.Equal(t, failed.IPAddress, actual.IPAddress)
	require.NotEqual(t, original.City, actual.City)
	clock.Add(int64(openAIEgressLocationRetry - time.Second))
	require.False(t, s.ObserveProxySnapshot(route, failed))
	s.Resolve(route)
	require.Zero(t, calls.Load())
	clock.Add(int64(time.Second))
	s.Resolve(route)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("retry did not start after durable cooldown")
	}
	// The maintenance scanner may see the same failure while retrying. That
	// unchanged observation must not cancel the active attempt or start another.
	repeated := *failed
	// Storage decoding can use a different location object for the same instant.
	repeated.UpdatedAt = failed.UpdatedAt.In(time.FixedZone("persisted offset", 3600))
	require.False(t, s.ObserveProxySnapshot(route, &repeated))
	s.mu.Lock()
	require.True(t, s.entries[original.RouteKey].pending)
	s.mu.Unlock()
	close(release)
	require.Eventually(t, func() bool { return s.Resolve(route).City == "Berlin" }, time.Second, time.Millisecond)
	require.Equal(t, failed.IPAddress, s.Resolve(route).IPAddress)
	require.EqualValues(t, 1, calls.Load())
}

func TestOpenAIEgressLocationRemoteSameIPFailureKeepsPermanentSuccess(t *testing.T) {
	clock, now := egressLocationClock(t)
	route, original := durableEgressFixture(now())
	var calls atomic.Int64
	s := newOpenAIEgressLocationService(egressLocationTestProber(func(context.Context, string) (*ProxyExitInfo, int64, error) {
		calls.Add(1)
		return nil, 0, errors.New("unexpected retry")
	}), now, 30, time.Minute)
	t.Cleanup(s.Stop)
	require.True(t, s.ObserveProxySnapshot(route, original))
	clock.Add(int64(time.Second))
	failed := &ProxyLatencyInfo{RouteKey: original.RouteKey, IPAddress: original.IPAddress, GeoStatus: "failed",
		GeoResultUnixMs: now().UnixMilli(), UpdatedAt: now()}
	require.True(t, s.ObserveProxySnapshot(route, failed))
	clock.Add(int64(365 * 24 * time.Hour))
	require.Equal(t, original.City, s.Resolve(route).City)
	require.Equal(t, *original.GeoCheckedAt, s.Resolve(route).CheckedAt)
	require.Zero(t, calls.Load())
}

func TestOpenAIEgressLocationInvalidStoredTimezoneIsRetried(t *testing.T) {
	clock, now := egressLocationClock(t)
	route, invalid := durableEgressFixture(now())
	invalid.Timezone, invalid.UpdatedAt = "Mars/Olympus", now()
	cache := &durableEgressTestCache{values: map[int64]*ProxyLatencyInfo{route.ProxyID: invalid}}
	var calls atomic.Int64
	s := newOpenAIEgressLocationService(egressLocationTestProber(func(context.Context, string) (*ProxyExitInfo, int64, error) {
		calls.Add(1)
		return egressLocationTestInfo(invalid.IPAddress, now()), 10, nil
	}), now, 30, time.Minute)
	t.Cleanup(s.Stop)
	s.SetProxyLatencyCache(cache)
	require.False(t, s.ObserveProxySnapshot(route, invalid))
	require.Equal(t, "fallback", s.Resolve(route).Status)
	clock.Add(int64(openAIEgressLocationRetry))
	require.Eventually(t, func() bool { return s.Resolve(route).Timezone == "Europe/Berlin" }, time.Second, time.Millisecond)
	require.EqualValues(t, 1, calls.Load())
}

func TestOpenAIEgressLocationSameMillisecondCommittedWinnerReplacesSnapshot(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*ProxyLatencyInfo)
	}{
		{name: "IP", change: func(info *ProxyLatencyInfo) { info.IPAddress = "203.0.113.10" }},
		{name: "timezone", change: func(info *ProxyLatencyInfo) { info.Timezone = "Europe/Paris" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, now := egressLocationClock(t)
			route, original := durableEgressFixture(now())
			s := newOpenAIEgressLocationService(nil, now, 30, time.Minute)
			t.Cleanup(s.Stop)
			require.True(t, s.ObserveProxySnapshot(route, original))
			winner := *original
			tc.change(&winner)
			require.Equal(t, original.GeoResultUnixMs, winner.GeoResultUnixMs)
			require.True(t, s.ObserveProxySnapshot(route, &winner))
			actual := s.Resolve(route)
			require.Equal(t, winner.IPAddress, actual.IPAddress)
			require.Equal(t, winner.Timezone, actual.Timezone)
		})
	}
}
