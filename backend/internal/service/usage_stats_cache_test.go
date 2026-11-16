//go:build unit

package service

import (
	"context"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/stretchr/testify/require"
)

type dashboardCacheRepository struct {
	UsageLogRepository
	calls   atomic.Int32
	started chan struct{}
	release chan struct{}
	err     error
}

func (r *dashboardCacheRepository) GetUserDashboardStats(ctx context.Context, id int64) (*usagestats.UserDashboardStats, error) {
	r.calls.Add(1)
	if r.err != nil {
		return nil, r.err
	}
	if r.started != nil {
		select {
		case r.started <- struct{}{}:
		default:
		}
	}
	if r.release != nil {
		select {
		case <-r.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return &usagestats.UserDashboardStats{TotalRequests: id, ByPlatform: []usagestats.PlatformDashboardStats{{Platform: "openai", TotalRequests: 7}}}, nil
}

func TestUsageStatsCacheKeepsOwnedValuesAndBounds(t *testing.T) {
	var c usageStatsCache
	v := &usagestats.UserDashboardStats{ByPlatform: []usagestats.PlatformDashboardStats{{Platform: "openai"}}}
	c.put("one", v)
	v.ByPlatform[0].Platform = "changed"
	require.Equal(t, "openai", c.get("one").ByPlatform[0].Platform)
	for i := 0; i < usageStatsMaxEntries+10; i++ {
		c.put(strconv.Itoa(i), v)
	}
	require.Len(t, c.entries, usageStatsMaxEntries)
	require.LessOrEqual(t, c.bytes, usageStatsMaxBytes)
	require.Nil(t, c.get("one"))
	el := c.entries["10"]
	el.Value.(*usageStatsCacheEntry).expires = time.Now().Add(-time.Second)
	require.Nil(t, c.get("10"))
}

func TestUsageStatsCacheCancellationDoesNotPoisonSharedLoad(t *testing.T) {
	repo := &dashboardCacheRepository{started: make(chan struct{}, 1), release: make(chan struct{})}
	svc := &UsageService{usageRepo: repo}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := svc.GetUserDashboardStats(ctx, 42); done <- err }()
	<-repo.started
	cancel()
	require.ErrorIs(t, <-done, context.Canceled)
	close(repo.release)
	result, err := svc.GetUserDashboardStats(context.Background(), 42)
	require.NoError(t, err)
	result.ByPlatform[0].Platform = "modified"
	cached, err := svc.GetUserDashboardStats(context.Background(), 42)
	require.NoError(t, err)
	require.Equal(t, "openai", cached.ByPlatform[0].Platform)
	require.Equal(t, int32(1), repo.calls.Load())
}

func TestUsageStatsCacheDoesNotKeepErrorsOrOversizeValues(t *testing.T) {
	repo := &dashboardCacheRepository{err: ErrUsageStatsInitializing}
	svc := &UsageService{usageRepo: repo}
	for range 2 {
		_, err := svc.GetUserDashboardStats(context.Background(), 42)
		require.ErrorIs(t, err, ErrUsageStatsInitializing)
	}
	require.Equal(t, int32(2), repo.calls.Load())
	var cache usageStatsCache
	cache.put("oversize", &usagestats.UserDashboardStats{ByPlatform: []usagestats.PlatformDashboardStats{{Platform: strings.Repeat("x", usageStatsMaxBytes)}}})
	require.Nil(t, cache.get("oversize"))
	require.Zero(t, cache.bytes)
}

func BenchmarkUsageStatsCacheHit(b *testing.B) {
	svc := &UsageService{usageRepo: &dashboardCacheRepository{}}
	_, _ = svc.GetUserDashboardStats(context.Background(), 42)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = svc.GetUserDashboardStats(context.Background(), 42)
	}
}
