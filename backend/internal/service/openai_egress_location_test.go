package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type egressLocationTestProber func(context.Context, string) (*ProxyExitInfo, int64, error)

func (f egressLocationTestProber) ProbeProxy(ctx context.Context, url string) (*ProxyExitInfo, int64, error) {
	return f(ctx, url)
}

func egressLocationTestInfo(ip string, now time.Time) *ProxyExitInfo {
	return &ProxyExitInfo{IP: ip, Country: "Germany", CountryCode: "DE", Region: "Berlin", City: "Berlin",
		Timezone: "Europe/Berlin", GeoStatus: "success", GeoCheckedAt: now}
}

func TestOpenAIEgressLocationJSONOmitsUncollectedTime(t *testing.T) {
	snapshot := DefaultOpenAIEgressLocationSnapshot(OpenAIEgressRoute{})
	encoded, err := json.Marshal(snapshot)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "checked_at")
	snapshot.CheckedAt = time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	encoded, err = json.Marshal(snapshot)
	require.NoError(t, err)
	require.Contains(t, string(encoded), `"checked_at":"2026-09-18T12:00:00Z"`)
}

func egressLocationClock(t *testing.T) (*atomic.Int64, func() time.Time) {
	t.Helper()
	clock := &atomic.Int64{}
	clock.Store(time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC).UnixNano())
	return clock, func() time.Time { return time.Unix(0, clock.Load()).UTC() }
}

func TestOpenAIEgressLocationResolveIsAsyncAndDeduplicates(t *testing.T) {
	started, release := make(chan string, 1), make(chan struct{})
	var calls atomic.Int64
	s := NewOpenAIEgressLocationService(egressLocationTestProber(func(ctx context.Context, url string) (*ProxyExitInfo, int64, error) {
		calls.Add(1)
		started <- url
		select {
		case <-ctx.Done():
			return nil, 0, ctx.Err()
		case <-release:
			return egressLocationTestInfo("203.0.113.9", time.Now()), 10, nil
		}
	}))
	t.Cleanup(s.Stop)
	route := OpenAIEgressRoute{ProxyID: 12, ProxyURL: "http://name:secret@proxy.example:8080"}
	first := s.Resolve(route)
	require.Equal(t, "fallback", first.Status)
	require.Equal(t, "Seattle", first.City)
	select {
	case url := <-started:
		require.Equal(t, route.ProxyURL, url)
	case <-time.After(time.Second):
		t.Fatal("background probe did not start")
	}
	for range 100 {
		require.Equal(t, "Seattle", s.Resolve(route).City)
	}
	require.EqualValues(t, 1, calls.Load())
	close(release)
	require.Eventually(t, func() bool { return s.Resolve(route).City == "Berlin" }, time.Second, time.Millisecond)
	actual := s.Resolve(route)
	require.Equal(t, "fresh", actual.Status)
	require.Equal(t, "proxy_exit", actual.Source)
	require.Equal(t, "Seattle", first.City, "previous request snapshots must not be mutated")
	encoded, err := json.Marshal(actual)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "secret")
	require.NotContains(t, string(encoded), "proxy.example")
	require.NotContains(t, string(encoded), actual.RouteKey)
}

func TestOpenAIEgressLocationRouteIsolation(t *testing.T) {
	a := NewOpenAIEgressLocationService(nil)
	b := NewOpenAIEgressLocationService(nil)
	t.Cleanup(a.Stop)
	t.Cleanup(b.Stop)
	base := OpenAIEgressRoute{ProxyID: 1, ProxyURL: "http://user:one@proxy.example:8080"}
	a.ObserveResult(base, egressLocationTestInfo("203.0.113.1", time.Now()), nil)
	for _, route := range []OpenAIEgressRoute{
		{ProxyID: 1, ProxyURL: "http://user:two@proxy.example:8080"},
		{ProxyID: 2, ProxyURL: base.ProxyURL},
		{ProxyID: 1, ProxyURL: "https://user:one@proxy.example:8080"},
		{},
	} {
		got := a.Resolve(route)
		require.Equal(t, "Seattle", got.City)
		require.NotEqual(t, a.Resolve(base).RouteKey, got.RouteKey)
	}
	a.ObserveResult(OpenAIEgressRoute{}, egressLocationTestInfo("203.0.113.2", time.Now()), nil)
	require.Equal(t, "direct_exit", a.Resolve(OpenAIEgressRoute{}).Source)
	require.Equal(t, "Berlin", a.Resolve(OpenAIEgressRoute{}).City)
	require.Equal(t, "Seattle", b.Resolve(OpenAIEgressRoute{}).City)
	require.NotEqual(t, a.Resolve(OpenAIEgressRoute{}).RouteKey, b.Resolve(OpenAIEgressRoute{}).RouteKey)
	var absent *OpenAIEgressLocationService
	require.Equal(t, DefaultOpenAIEgressLocationSnapshot(base), absent.Resolve(base))
}

func TestOpenAIEgressLocationFreshStaleExpiryAndFailureRetry(t *testing.T) {
	clock, now := egressLocationClock(t)
	var calls atomic.Int64
	s := newOpenAIEgressLocationService(egressLocationTestProber(func(context.Context, string) (*ProxyExitInfo, int64, error) {
		calls.Add(1)
		return nil, 0, errors.New("credential must not appear: password")
	}), now, 30, time.Minute)
	t.Cleanup(s.Stop)
	route := OpenAIEgressRoute{}
	s.ObserveResult(route, egressLocationTestInfo("203.0.113.1", now()), nil)
	originalTime := s.Resolve(route).CheckedAt
	clock.Add(int64(time.Hour - time.Second))
	require.Equal(t, "fresh", s.Resolve(route).Status)
	require.Zero(t, calls.Load())
	clock.Add(int64(time.Second))
	require.Equal(t, "stale", s.Resolve(route).Status)
	require.Eventually(t, func() bool { return s.Resolve(route).Reason == "probe_failed" }, time.Second, time.Millisecond)
	for range 20 {
		require.Equal(t, "Berlin", s.Resolve(route).City)
	}
	require.EqualValues(t, 1, calls.Load())
	require.Equal(t, originalTime, s.Resolve(route).CheckedAt)
	clock.Add(int64(5*time.Minute - time.Second))
	s.Resolve(route)
	require.EqualValues(t, 1, calls.Load())
	clock.Add(int64(time.Second))
	s.Resolve(route)
	require.Eventually(t, func() bool { return calls.Load() == 2 }, time.Second, time.Millisecond)
	clock.Add(int64(24 * time.Hour))
	got := s.Resolve(route)
	require.Equal(t, "fallback", got.Status)
	require.Equal(t, "Seattle", got.City)
	require.NotContains(t, got.Reason, "password")
}

func TestOpenAIEgressLocationNewIPInvalidatesOldGeo(t *testing.T) {
	clock, now := egressLocationClock(t)
	s := newOpenAIEgressLocationService(nil, now, 30, time.Minute)
	t.Cleanup(s.Stop)
	route := OpenAIEgressRoute{ProxyID: 3, ProxyURL: "socks5://proxy.example:8080"}
	s.ObserveResult(route, egressLocationTestInfo("203.0.113.1", now()), nil)
	clock.Add(int64(time.Hour))
	s.ObserveResult(route, &ProxyExitInfo{IP: "203.0.113.1", GeoStatus: "failed", GeoCheckedAt: now()}, nil)
	require.Equal(t, "Berlin", s.Resolve(route).City, "same IP may use the last good result")
	clock.Add(int64(time.Minute))
	s.ObserveResult(route, &ProxyExitInfo{IP: "203.0.113.2", GeoStatus: "failed", GeoCheckedAt: now()}, nil)
	got := s.Resolve(route)
	require.Equal(t, "fallback", got.Status)
	require.Equal(t, "Seattle", got.City)
	require.Equal(t, "US", got.CountryCode)
	require.Equal(t, "203.0.113.2", got.IPAddress)
	require.Equal(t, "ip_changed_geo_unavailable", got.Reason)
}

func TestOpenAIEgressLocationRejectsPartialGeo(t *testing.T) {
	for _, mutate := range []struct {
		name string
		fn   func(*ProxyExitInfo)
	}{
		{"missing timezone", func(i *ProxyExitInfo) { i.Timezone = "" }},
		{"invalid timezone", func(i *ProxyExitInfo) { i.Timezone = "Mars/Olympus" }},
		{"local timezone", func(i *ProxyExitInfo) { i.Timezone = "Local" }},
		{"missing region", func(i *ProxyExitInfo) { i.Region = "" }},
		{"missing city", func(i *ProxyExitInfo) { i.City = "" }},
		{"country code", func(i *ProxyExitInfo) { i.CountryCode = "USA" }},
		{"invalid ip", func(i *ProxyExitInfo) { i.IP = "not-an-ip" }},
		{"failed geo with fields", func(i *ProxyExitInfo) { i.GeoStatus = "failed" }},
	} {
		t.Run(mutate.name, func(t *testing.T) {
			s := NewOpenAIEgressLocationService(nil)
			t.Cleanup(s.Stop)
			info := egressLocationTestInfo("203.0.113.9", time.Now())
			mutate.fn(info)
			s.ObserveResult(OpenAIEgressRoute{}, info, nil)
			got := s.Resolve(OpenAIEgressRoute{})
			require.Equal(t, "fallback", got.Status)
			require.Equal(t, "America/Los_Angeles", got.Timezone)
			require.Equal(t, "United States", got.Country)
			require.Equal(t, "Washington", got.Region)
			require.Equal(t, "Seattle", got.City)
		})
	}
}

func TestOpenAIEgressLocationManualProbeSupersedesBackground(t *testing.T) {
	started, release, returned := make(chan struct{}), make(chan struct{}), make(chan struct{})
	s := NewOpenAIEgressLocationService(egressLocationTestProber(func(ctx context.Context, _ string) (*ProxyExitInfo, int64, error) {
		close(started)
		select {
		case <-ctx.Done():
			return nil, 0, ctx.Err()
		case <-release:
			close(returned)
			return egressLocationTestInfo("203.0.113.1", time.Now()), 0, nil
		}
	}))
	t.Cleanup(s.Stop)
	s.Resolve(OpenAIEgressRoute{})
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("probe did not start")
	}
	s.ObserveResult(OpenAIEgressRoute{}, egressLocationTestInfo("203.0.113.2", time.Now()), nil)
	require.Equal(t, "203.0.113.2", s.Resolve(OpenAIEgressRoute{}).IPAddress)
	close(release)
	select {
	case <-returned:
	case <-time.After(time.Second):
		t.Fatal("old probe did not return")
	}
	// Join workers without clearing their result to assert that the old completion
	// cannot replace the newer manual result, even though it returned later.
	s.cancel()
	s.wg.Wait()
	require.Equal(t, "203.0.113.2", s.Resolve(OpenAIEgressRoute{}).IPAddress)
}

func TestOpenAIEgressLocationQueueBoundAndStopCancellation(t *testing.T) {
	started := make(chan struct{}, 2)
	var cancelled atomic.Int64
	s := NewOpenAIEgressLocationService(egressLocationTestProber(func(ctx context.Context, _ string) (*ProxyExitInfo, int64, error) {
		started <- struct{}{}
		<-ctx.Done()
		cancelled.Add(1)
		return nil, 0, ctx.Err()
	}))
	t.Cleanup(s.Stop)
	for i := range 2 {
		s.Resolve(OpenAIEgressRoute{ProxyID: int64(i + 1), ProxyURL: fmt.Sprintf("http://proxy-%d.example:80", i)})
	}
	for range 2 {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("workers did not start")
		}
	}
	for i := range openAIEgressLocationQueueSize {
		s.Resolve(OpenAIEgressRoute{ProxyID: int64(i + 10), ProxyURL: fmt.Sprintf("http://queued-%d.example:80", i)})
	}
	got := s.Resolve(OpenAIEgressRoute{ProxyID: 1000, ProxyURL: "http://overflow.example:80"})
	require.Equal(t, "queue_full", got.Reason)
	require.Equal(t, openAIEgressLocationQueueSize, len(s.queue))
	stopped := make(chan struct{})
	go func() { s.Stop(); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("Stop did not cancel probes")
	}
	require.EqualValues(t, 2, cancelled.Load())
	require.Empty(t, s.queue)
}

func TestOpenAIEgressLocationGlobalBudgetAndCacheBound(t *testing.T) {
	var calls atomic.Int64
	s := newOpenAIEgressLocationService(egressLocationTestProber(func(context.Context, string) (*ProxyExitInfo, int64, error) {
		calls.Add(1)
		return egressLocationTestInfo("203.0.113.9", time.Now()), 0, nil
	}), time.Now, 2, time.Minute)
	t.Cleanup(s.Stop)
	for i := range 20 {
		s.Resolve(OpenAIEgressRoute{ProxyID: int64(i + 1), ProxyURL: fmt.Sprintf("http://limited-%d.example:80", i)})
	}
	require.Eventually(t, func() bool { return calls.Load() == 2 }, time.Second, time.Millisecond)
	// Waiting on a minute budget must still be immediately cancellable.
	s.Stop()
	require.EqualValues(t, 2, calls.Load())
	cache := NewOpenAIEgressLocationService(nil)
	t.Cleanup(cache.Stop)
	for i := range openAIEgressLocationCapacity + 40 {
		cache.Resolve(OpenAIEgressRoute{ProxyID: int64(i + 1), ProxyURL: fmt.Sprintf("http://cached-%d.example:80", i)})
	}
	cache.mu.Lock()
	require.Len(t, cache.entries, openAIEgressLocationCapacity)
	for key := range cache.entries {
		require.Len(t, key, 64)
		require.False(t, strings.Contains(key, "http"))
	}
	cache.mu.Unlock()
}

func TestOpenAIEgressLocationOrdersProbeResultsByObservationTime(t *testing.T) {
	clock, now := egressLocationClock(t)
	s := newOpenAIEgressLocationService(nil, now, 30, time.Minute)
	t.Cleanup(s.Stop)
	route := OpenAIEgressRoute{}
	old := now()
	s.ObserveResult(route, egressLocationTestInfo("203.0.113.1", old), nil)
	clock.Add(int64(10 * time.Second))
	// A slow failed result was observed earlier than the successful manual result
	// that will arrive next. Completion time must not make the failure newer.
	s.ObserveResult(route, &ProxyExitInfo{IP: "203.0.113.1", GeoStatus: "failed", GeoCheckedAt: old.Add(time.Second)}, nil)
	s.ObserveResult(route, egressLocationTestInfo("203.0.113.2", old.Add(5*time.Second)), nil)
	require.Equal(t, "203.0.113.2", s.Resolve(route).IPAddress)
	// A result actually older than the committed observation cannot restore it.
	s.ObserveResult(route, egressLocationTestInfo("203.0.113.1", old.Add(2*time.Second)), nil)
	require.Equal(t, "203.0.113.2", s.Resolve(route).IPAddress)
}

func TestOpenAIEgressLocationConcurrentResolveObserveAndStop(t *testing.T) {
	s := NewOpenAIEgressLocationService(nil)
	t.Cleanup(s.Stop)
	var wg sync.WaitGroup
	for i := range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			route := OpenAIEgressRoute{ProxyID: int64(i), ProxyURL: fmt.Sprintf("http://race-%d.example:80", i)}
			for range 50 {
				s.Resolve(route)
				s.ObserveResult(route, egressLocationTestInfo("203.0.113.1", time.Now()), nil)
			}
		}()
	}
	s.Stop()
	wg.Wait()
	require.Equal(t, "service_stopped", s.Resolve(OpenAIEgressRoute{}).Reason)
}
