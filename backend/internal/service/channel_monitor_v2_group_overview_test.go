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
	attributions              map[int64]ChannelMonitorV2OverviewAttribution
	asOf                      time.Time
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
func (r *overviewRepositoryStub) GetGroupAttributionOverview(_ context.Context, ids []int64, asOf time.Time) (map[int64]ChannelMonitorV2OverviewAttribution, error) {
	r.historyCalls++
	r.historyIDs = append([]int64(nil), ids...)
	r.asOf = asOf
	if r.attributions != nil {
		return r.attributions, r.historyErr
	}
	out := make(map[int64]ChannelMonitorV2OverviewAttribution, len(ids))
	for _, id := range ids {
		item := NewChannelMonitorV2OverviewAttribution(asOf)
		if recent := r.history[id]; len(recent) > 0 {
			item.Recent, item.Latest = recent, &recent[0]
		}
		out[id] = item
	}
	return out, r.historyErr
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
	require.Len(t, out.Items, 2)
	require.Equal(t, 1, r.matrixCalls)
	require.Equal(t, 1, r.historyCalls)
	require.Equal(t, ChannelMonitorV2GroupBy("group"), r.groupBy)
	require.True(t, r.filter.RestrictGroups)
	require.ElementsMatch(t, []int64{2, 3}, r.filter.AllowedGroupIDs)
	require.ElementsMatch(t, []int64{2, 3}, r.historyIDs)
	require.Empty(t, r.filter.GroupIDs)
	require.Empty(t, r.filter.Models)
	require.Empty(t, r.filter.Platforms)
	byID := map[int64]ChannelMonitorV2GroupOverviewItem{}
	for _, item := range out.Items {
		byID[item.GroupID] = item
		require.True(t, item.MonitoringEnabled)
		require.Len(t, item.Buckets, 18)
	}
	require.Equal(t, 0.0, byID[2].RateMultiplier)
	require.Equal(t, PlatformComposite, byID[2].Platform)
	require.Equal(t, "healthy", byID[2].State)
	require.Equal(t, .97, *byID[2].Metrics.AvailabilityRate)
	require.Equal(t, int64(500), *byID[2].Metrics.TTFTP50Ms)
	require.Equal(t, "passed", byID[2].Attribution.Latest.Status)
	require.Equal(t, "no_data", byID[3].State)
	require.NotContains(t, byID, int64(4))
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

func TestChannelMonitorV2OverviewOnlyReadsMonitoredGroups(t *testing.T) {
	for _, tc := range []struct {
		name       string
		configured []int64
		disabled   bool
		wantIDs    []int64
	}{
		{name: "nil group selection means all", wantIDs: []int64{1, 3}},
		{name: "empty group selection means all", configured: []int64{}, wantIDs: []int64{1, 3}},
		{name: "configured intersection", configured: []int64{1, 2}, wantIDs: []int64{1}},
		{name: "no configured overlap", configured: []int64{99}},
		{name: "all platforms disabled", disabled: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := overviewTestRepository()
			r.cfg.GroupIDs = tc.configured
			r.cfg.Platforms[0].Enabled = !tc.disabled
			svc := NewChannelMonitorV2Service(r)
			f, err := svc.ParseFilter("90m", nil, nil, nil)
			require.NoError(t, err)
			out, err := svc.GetGroupOverview(context.Background(), f, []Group{
				{ID: 1, Platform: PlatformOpenAI, Status: StatusActive},
				{ID: 2, Platform: PlatformAnthropic, Status: StatusActive},
				{ID: 3, Platform: PlatformComposite, Status: StatusActive},
			}, nil)
			require.NoError(t, err)
			require.Len(t, out.Items, len(tc.wantIDs))
			if len(tc.wantIDs) == 0 {
				require.Zero(t, r.matrixCalls)
				require.Zero(t, r.historyCalls)
				return
			}
			require.ElementsMatch(t, tc.wantIDs, r.filter.AllowedGroupIDs)
			require.ElementsMatch(t, tc.wantIDs, r.historyIDs)
			for _, item := range out.Items {
				require.True(t, item.MonitoringEnabled)
				require.Equal(t, "no_data", item.State)
			}
		})
	}
}

func TestChannelMonitorV2OverviewHistoricalCoverageAndRanges(t *testing.T) {
	for rangeName, buckets := range map[string]int{"90m": 18, "24h": 24, "7d": 14, "30d": 30} {
		t.Run(rangeName, func(t *testing.T) {
			r := overviewTestRepository()
			svc := NewChannelMonitorV2Service(r)
			f, _ := svc.ParseFilter(rangeName, nil, nil, nil)
			r.matrix.Coverage.AvailabilityReady = false
			r.matrix.Coverage.AvailabilityVersion = ChannelMonitorV2AvailabilitySchemaVersion
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
	_, err := svc.GetGroupOverview(context.Background(), f, []Group{{ID: 1, Status: StatusActive, Platform: PlatformOpenAI}}, nil)
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

func TestChannelMonitorV2OverviewPreservesLowSampleBucketRates(t *testing.T) {
	r := overviewTestRepository()
	r.cfg.HealthThresholds.MinimumSample = 50
	svc := NewChannelMonitorV2Service(r)
	f, err := svc.ParseFilter("90m", nil, nil, nil)
	require.NoError(t, err)
	id, rate := int64(91), 1.0
	row := ChannelMonitorV2MatrixRow{GroupID: &id}
	// The whole window has enough samples, while individual five-minute
	// intervals do not. Neither classification may discard their measured rate.
	for i := 0; i < 17; i++ {
		row.Buckets = append(row.Buckets, ChannelMonitorV2TrendPoint{
			BucketStart: f.Start.Add(time.Duration(i) * f.Bucket),
			Metrics:     ChannelMonitorV2Metric{AvailabilitySampleCount: 10, AvailabilityRate: &rate},
		})
	}
	row.Metrics = ChannelMonitorV2Metric{AvailabilitySampleCount: 170, AvailabilityRate: &rate}
	r.matrix.Items = []ChannelMonitorV2MatrixRow{row}
	out, err := svc.GetGroupOverview(context.Background(), f, []Group{{ID: id, Platform: PlatformOpenAI, Status: StatusActive}}, nil)
	require.NoError(t, err)
	require.Equal(t, "healthy", out.Items[0].State)
	require.Len(t, out.Items[0].Buckets, 18)
	for _, bucket := range out.Items[0].Buckets[:17] {
		require.Equal(t, "insufficient", bucket.State)
		require.NotNil(t, bucket.AvailabilityRate)
		require.Equal(t, 1.0, *bucket.AvailabilityRate)
	}
	require.Equal(t, "no_data", out.Items[0].Buckets[17].State)
	require.Nil(t, out.Items[0].Buckets[17].AvailabilityRate)
}

func TestChannelMonitorV2OverviewAttributionUsesCurrentClockForAllRanges(t *testing.T) {
	now := time.Date(2026, 11, 14, 12, 34, 56, 123000000, time.UTC)
	for _, rangeName := range []string{"90m", "24h", "7d", "30d"} {
		t.Run(rangeName, func(t *testing.T) {
			r := overviewTestRepository()
			svc := NewChannelMonitorV2Service(r)
			svc.now = func() time.Time { return now }
			f, err := svc.ParseFilter(rangeName, nil, nil, nil)
			require.NoError(t, err)
			require.NotEqual(t, now, f.End)
			clockCalls := 0
			svc.now = func() time.Time { clockCalls++; return now }
			out, err := svc.GetGroupOverview(context.Background(), f, []Group{{ID: 1, Status: StatusActive, Platform: PlatformOpenAI}}, nil)
			require.NoError(t, err)
			require.Equal(t, 1, clockCalls)
			require.Equal(t, now, r.asOf)
			attr := out.Items[0].Attribution
			require.Equal(t, now, attr.AsOf)
			require.False(t, attr.Enabled)
			require.Equal(t, "no_data", attr.SummaryState)
			require.Empty(t, attr.Recent)
			require.NotNil(t, attr.Recent)
			require.Len(t, attr.History, 20)
			require.Equal(t, now.Add(-200*time.Minute), attr.History[0].BucketStart)
			require.Equal(t, now, attr.History[19].BucketEnd)
			for i, bucket := range attr.History {
				require.Equal(t, "no_data", bucket.State)
				require.Equal(t, 10*time.Minute, bucket.BucketEnd.Sub(bucket.BucketStart))
				if i > 0 {
					require.Equal(t, attr.History[i-1].BucketEnd, bucket.BucketStart)
				}
			}
		})
	}
}

func TestChannelMonitorV2OverviewAttributionProjectsAuthoritativeSummary(t *testing.T) {
	r := overviewTestRepository()
	now := time.Date(2026, 11, 14, 12, 34, 56, 0, time.UTC)
	attr := NewChannelMonitorV2OverviewAttribution(now)
	attr.Enabled = false
	attr.ProbeModel = "currently configured probe"
	attr.SummaryState, attr.History[19].State = "passed", "passed"
	attr.Recent = []ChannelMonitorV2AttributionEntry{{Status: "failed", ProbeModel: "historical probe", FinishedAt: now}}
	attr.Latest = &attr.Recent[0]
	r.attributions = map[int64]ChannelMonitorV2OverviewAttribution{1: attr}
	svc := NewChannelMonitorV2Service(r)
	svc.now = func() time.Time { return now }
	f, err := svc.ParseFilter("30d", nil, nil, nil)
	require.NoError(t, err)
	out, err := svc.GetGroupOverview(context.Background(), f, []Group{{ID: 1, Status: StatusActive, Platform: PlatformOpenAI}}, nil)
	require.NoError(t, err)
	require.Equal(t, attr, out.Items[0].Attribution)
	encoded, err := json.Marshal(out.Items[0].Attribution)
	require.NoError(t, err)
	for _, field := range []string{`"enabled":false`, `"probe_model":"currently configured probe"`, `"summary_state":"passed"`, `"as_of":`, `"history":`, `"bucket_end":`, `"latest":`, `"recent":`} {
		require.Contains(t, string(encoded), field)
	}
}
