package service

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type overviewRepositoryStub struct {
	ChannelMonitorV2Repository
	cfg                       ChannelMonitorV2Config
	matrix                    *ChannelMonitorV2Matrix
	history                   map[int64][]ChannelMonitorV2AttributionEntry
	filter                    ChannelMonitorV2Filter
	groupBy                   ChannelMonitorV2GroupBy
	historyIDs                []int64
	matrixCalls, historyCalls int
	historyErr                error
}

func (r *overviewRepositoryStub) GetConfig(context.Context) (*ChannelMonitorV2Config, error) {
	return &r.cfg, nil
}
func (r *overviewRepositoryStub) GetMatrix(_ context.Context, f ChannelMonitorV2Filter, _ ChannelMonitorV2Config, by ChannelMonitorV2GroupBy, admin bool) (*ChannelMonitorV2Matrix, error) {
	if admin {
		panic("public overview must not load admin metadata")
	}
	r.filter, r.groupBy = f, by
	r.matrixCalls++
	return r.matrix, nil
}
func (r *overviewRepositoryStub) GetGroupAttributionHistory(_ context.Context, ids []int64) (map[int64][]ChannelMonitorV2AttributionEntry, error) {
	r.historyCalls++
	r.historyIDs = append([]int64(nil), ids...)
	return r.history, r.historyErr
}

func overviewTestRepository() *overviewRepositoryStub {
	return &overviewRepositoryStub{cfg: ChannelMonitorV2Config{Enabled: true, RefreshIntervalSeconds: 60, Platforms: []ChannelMonitorV2PlatformConfig{{Platform: PlatformOpenAI, Enabled: true}}, HealthThresholds: ChannelMonitorV2HealthThresholds{MinimumSample: 10, WarningErrorRate: .05, CriticalErrorRate: .2}}, matrix: &ChannelMonitorV2Matrix{Coverage: ChannelMonitorV2Coverage{AvailabilityReady: true}}}
}

func TestChannelMonitorV2OverviewScopesAndProjectsOnce(t *testing.T) {
	r := overviewTestRepository()
	r.cfg.GroupIDs = []int64{2, 3}
	id, unallowed := int64(2), int64(99)
	rate, ttft := .97, int64(500)
	r.matrix.Items = []ChannelMonitorV2MatrixRow{{GroupID: &id, Metrics: ChannelMonitorV2Metric{AvailabilityRate: &rate, AvailabilitySampleCount: 100, CacheRate: .4, CacheRateDenominator: 500, TTFT: ChannelMonitorV2Latency{P50Ms: &ttft}}}, {GroupID: &unallowed, GroupName: "private"}}
	r.history = map[int64][]ChannelMonitorV2AttributionEntry{2: {{Status: "passed", ReasonCode: "reference_match", ProbeModel: "gpt-6", FinishedAt: time.Now()}}}
	svc := NewChannelMonitorV2Service(r)
	f, _ := svc.ParseFilter("90m", []string{"grok"}, []string{"private"}, []int64{99})
	out, err := svc.GetGroupOverview(context.Background(), f, []Group{{ID: 2, Name: "shared", Platform: PlatformComposite, Status: StatusActive, RateMultiplier: 1}, {ID: 3, Name: "idle", Platform: PlatformOpenAI, Status: StatusActive}, {ID: 4, Name: "outside monitor", Platform: PlatformOpenAI, Status: StatusActive}, {ID: 9, Status: StatusDisabled}, {ID: 2, Status: StatusActive}}, map[int64]float64{2: 0})
	require.NoError(t, err)
	require.Len(t, out.Items, 3)
	require.Equal(t, 1, r.matrixCalls)
	require.Equal(t, 1, r.historyCalls)
	require.Equal(t, ChannelMonitorV2GroupBy("group"), r.groupBy)
	require.True(t, r.filter.RestrictGroups)
	require.ElementsMatch(t, []int64{2, 3, 4}, r.filter.AllowedGroupIDs)
	require.ElementsMatch(t, []int64{2, 3, 4}, r.historyIDs)
	require.Empty(t, r.filter.GroupIDs)
	require.Empty(t, r.filter.Models)
	require.Empty(t, r.filter.Platforms)
	byID := map[int64]ChannelMonitorV2GroupOverviewItem{}
	for _, item := range out.Items {
		byID[item.GroupID] = item
		require.Len(t, item.Buckets, 18)
	}
	require.Equal(t, 0.0, byID[2].RateMultiplier)
	require.Equal(t, PlatformComposite, byID[2].Platform)
	require.Equal(t, "healthy", byID[2].State)
	require.Equal(t, .97, *byID[2].Metrics.AvailabilityRate)
	require.Equal(t, int64(500), *byID[2].Metrics.TTFTP50Ms)
	require.Equal(t, "passed", byID[2].Attribution.Latest.Status)
	require.Equal(t, "no_data", byID[3].State)
	require.Equal(t, "unmonitored", byID[4].State)
	require.Nil(t, byID[4].Metrics.AvailabilityRate)
	encoded, _ := json.Marshal(out)
	for _, field := range []string{"account_id", "account_name", "request_count", "api_key", "snapshot", "private"} {
		require.NotContains(t, string(encoded), field)
	}
}

func TestChannelMonitorV2OverviewEmptyPermissionDoesNotReadStats(t *testing.T) {
	r := overviewTestRepository()
	svc := NewChannelMonitorV2Service(r)
	f, _ := svc.ParseFilter("90m", nil, nil, nil)
	out, err := svc.GetGroupOverview(context.Background(), f, nil, nil)
	require.NoError(t, err)
	require.Empty(t, out.Items)
	require.Zero(t, r.matrixCalls)
	require.Zero(t, r.historyCalls)
}

func TestChannelMonitorV2OverviewHistoricalCoverageAndRanges(t *testing.T) {
	for rangeName, buckets := range map[string]int{"90m": 18, "24h": 24, "7d": 14, "30d": 30} {
		t.Run(rangeName, func(t *testing.T) {
			r := overviewTestRepository()
			svc := NewChannelMonitorV2Service(r)
			f, _ := svc.ParseFilter(rangeName, nil, nil, nil)
			r.matrix.Coverage.AvailabilityReady = false
			r.matrix.Coverage.AvailabilityVersion = ChannelMonitorV2AggregationVersion
			r.matrix.Coverage.AvailabilityStart = f.Start.Add(f.Bucket)
			out, err := svc.GetGroupOverview(context.Background(), f, []Group{{ID: 1, Status: StatusActive, Platform: PlatformOpenAI}}, nil)
			require.NoError(t, err)
			require.Equal(t, "rebuilding", out.Items[0].State)
			require.Len(t, out.Items[0].Buckets, buckets)
			require.Equal(t, "rebuilding", out.Items[0].Buckets[0].State)
			require.Equal(t, "no_data", out.Items[0].Buckets[1].State)
		})
	}
}

func TestChannelMonitorV2OverviewHistoryFailureIsNotHidden(t *testing.T) {
	r := overviewTestRepository()
	r.historyErr = errors.New("database down")
	svc := NewChannelMonitorV2Service(r)
	f, _ := svc.ParseFilter("90m", nil, nil, nil)
	_, err := svc.GetGroupOverview(context.Background(), f, []Group{{ID: 1, Status: StatusActive}}, nil)
	require.ErrorIs(t, err, r.historyErr)
}

func TestChannelMonitorV2OverviewStateOnlyUsesAvailability(t *testing.T) {
	thresholds := overviewTestRepository().cfg.HealthThresholds
	for _, tc := range []struct {
		rate    float64
		samples int64
		want    string
	}{{1, 0, "no_data"}, {1, 9, "insufficient"}, {.99, 10, "healthy"}, {.95, 100, "warning"}, {.9, 100, "warning"}, {.8, 100, "critical"}, {.5, 100, "critical"}} {
		m := ChannelMonitorV2Metric{AvailabilityRate: &tc.rate, AvailabilitySampleCount: tc.samples, AvailabilityErrorRequests: int64(math.Round((1 - tc.rate) * float64(tc.samples))), CacheRate: 0, ErrorRate: 1}
		require.Equal(t, tc.want, channelMonitorV2OverviewState(m, true, true, thresholds))
	}
}
