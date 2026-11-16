package service

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type blockingOverviewRepo struct {
	*overviewRepositoryStub
	started chan struct{}
	release chan struct{}
	calls   atomic.Int32
}

func (r *blockingOverviewRepo) GetGroupOverviewMetrics(ctx context.Context, _ ChannelMonitorV2Filter, _ ChannelMonitorV2Config) (*ChannelMonitorV2Matrix, error) {
	if r.calls.Add(1) == 1 {
		close(r.started)
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-r.release:
		return r.matrix, nil
	}
}

func TestChannelMonitorV2OverviewCacheCancelledWaiterDoesNotCancelSharedLoad(t *testing.T) {
	r := &blockingOverviewRepo{overviewRepositoryStub: overviewTestRepository(), started: make(chan struct{}), release: make(chan struct{})}
	svc := NewChannelMonitorV2Service(r)
	filter, err := svc.ParseFilter("90m", nil, nil, nil)
	require.NoError(t, err)
	groups := []Group{{ID: 1, Status: StatusActive, Platform: PlatformOpenAI}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := svc.GetGroupOverview(ctx, filter, groups, nil); done <- err }()
	<-r.started
	cancel()
	require.ErrorIs(t, <-done, context.Canceled)
	close(r.release)
	_, err = svc.GetGroupOverview(context.Background(), filter, groups, nil)
	require.NoError(t, err)
	require.Equal(t, int32(1), r.calls.Load())
}

func TestChannelMonitorV2OverviewCacheScopeAndResponseOwnership(t *testing.T) {
	r := overviewTestRepository()
	id := int64(1)
	rate := .99
	ttft := int64(1234)
	r.matrix.Items = []ChannelMonitorV2MatrixRow{{GroupID: &id, Metrics: ChannelMonitorV2Metric{AvailabilityRate: &rate, AvailabilitySampleCount: 50, TTFT: ChannelMonitorV2Latency{P50Ms: &ttft}}}}
	r.history = map[int64][]ChannelMonitorV2AttributionEntry{1: {{Status: "passed"}}}
	svc := NewChannelMonitorV2Service(r)
	now := time.Now().UTC()
	svc.now = func() time.Time { return now }
	filter, err := svc.ParseFilter("90m", nil, nil, nil)
	require.NoError(t, err)
	groups := []Group{{ID: 1, Name: "first", Status: StatusActive, Platform: PlatformOpenAI}}
	first, err := svc.GetGroupOverviewWithOptions(context.Background(), filter, groups, map[int64]float64{1: 2}, false, false)
	require.NoError(t, err)
	*first.Items[0].Metrics.TTFTP50Ms = 9
	first.Items[0].Attribution.Recent[0].Status = "changed"
	first.Items[0].Attribution.History[0].State = "changed"
	groups[0].Name = "second"
	second, err := svc.GetGroupOverviewWithOptions(context.Background(), filter, groups, map[int64]float64{1: 3}, false, false)
	require.NoError(t, err)
	require.Equal(t, 1, r.matrixCalls)
	require.Equal(t, "second", second.Items[0].GroupName)
	require.Equal(t, 3.0, second.Items[0].RateMultiplier)
	require.Equal(t, int64(1234), *second.Items[0].Metrics.TTFTP50Ms)
	require.Equal(t, "passed", second.Items[0].Attribution.Recent[0].Status)
	require.NotEqual(t, "changed", second.Items[0].Attribution.History[0].State)
	_, err = svc.GetGroupOverviewWithOptions(context.Background(), filter, nil, nil, false, false)
	require.NoError(t, err)
	require.Equal(t, 1, r.matrixCalls, "revoked authorization must not reuse a cached viewer response")
	_, err = svc.GetGroupOverviewWithOptions(context.Background(), filter, groups, nil, false, true)
	require.NoError(t, err)
	require.Equal(t, 2, r.matrixCalls, "legacy representation uses a separate entry")
	now = now.Add(11 * time.Second)
	_, err = svc.GetGroupOverviewWithOptions(context.Background(), filter, groups, nil, false, false)
	require.NoError(t, err)
	require.Equal(t, 3, r.matrixCalls)
}

func TestChannelMonitorV2OverviewCacheBounded(t *testing.T) {
	var cache channelOverviewCache
	now := time.Now()
	data := &channelOverviewData{matrix: &ChannelMonitorV2Matrix{}}
	for i := 0; i < 257; i++ {
		cache.put(fmt.Sprint(i), data, now)
	}
	require.Len(t, cache.entries, 256)
	require.Nil(t, cache.get("0", now))
	require.NotNil(t, cache.get("256", now))
	require.Nil(t, cache.get("256", now.Add(10*time.Second)))
	oversized := &channelOverviewData{matrix: &ChannelMonitorV2Matrix{Items: []ChannelMonitorV2MatrixRow{{Buckets: make([]ChannelMonitorV2TrendPoint, 32768)}}}}
	cache.put("oversized", oversized, now)
	require.Nil(t, cache.get("oversized", now))
	require.LessOrEqual(t, cache.bytes, 32<<20)
}
