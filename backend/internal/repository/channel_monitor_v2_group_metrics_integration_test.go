//go:build integration

package repository

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func assertGroupOverviewMetricsMatch(t *testing.T, repo *channelMonitorV2Repository, filter service.ChannelMonitorV2Filter, cfg service.ChannelMonitorV2Config) {
	t.Helper()
	detail, err := repo.GetMatrix(context.Background(), filter, cfg, service.ChannelMonitorV2GroupByGroup, false)
	require.NoError(t, err)
	overview, err := repo.GetGroupOverviewMetrics(context.Background(), filter, cfg)
	require.NoError(t, err)
	byID := map[int64]service.ChannelMonitorV2MatrixRow{}
	for _, row := range overview.Items {
		byID[*row.GroupID] = row
	}
	assertMetric := func(want, got service.ChannelMonitorV2Metric, latency bool) {
		t.Helper()
		require.Equal(t, want.AvailabilityRate, got.AvailabilityRate)
		require.Equal(t, want.AvailabilitySampleCount, got.AvailabilitySampleCount)
		require.Equal(t, want.AvailabilityErrorRequests, got.AvailabilityErrorRequests)
		require.Equal(t, want.CacheRate, got.CacheRate)
		require.Equal(t, want.CacheRateDenominator, got.CacheRateDenominator)
		require.Equal(t, want.SuccessRequests, got.SuccessRequests)
		require.Equal(t, want.ErrorRequests, got.ErrorRequests)
		require.Equal(t, want.RequestCount, got.RequestCount)
		require.Equal(t, want.TokenCount, got.TokenCount)
		require.Equal(t, want.TPM, got.TPM)
		require.Equal(t, want.TTFT.SampleCount, got.TTFT.SampleCount)
		require.Equal(t, want.TTFT.AvgMs, got.TTFT.AvgMs)
		if latency {
			require.Equal(t, want.TTFT.P50Ms, got.TTFT.P50Ms)
			require.Equal(t, want.TTFT.P90Ms, got.TTFT.P90Ms)
		} else {
			require.Nil(t, got.TTFT.P50Ms)
			require.Nil(t, got.TTFT.P90Ms)
		}
		require.Nil(t, got.Duration.P50Ms)
	}
	for _, want := range detail.Items {
		got := byID[*want.GroupID]
		assertMetric(want.Metrics, got.Metrics, true)
		require.Len(t, got.Buckets, len(want.Buckets))
		for i := range want.Buckets {
			require.True(t, want.Buckets[i].BucketStart.Equal(got.Buckets[i].BucketStart))
			assertMetric(want.Buckets[i].Metrics, got.Buckets[i].Metrics, false)
		}
		delete(byID, *want.GroupID)
	}
	require.Empty(t, byID, "no extra groups may escape the authorized scope")
}

func TestChannelMonitorV2GroupMetricsStatisticsEdgesIntegration(t *testing.T) {
	f, repo, start := newLatencyPrecisionFixture(t)
	ctx := context.Background()
	empty := newMonitorOverviewFixture(t, "openai")
	single := newMonitorOverviewFixture(t, "openai")
	single.at = f.at
	insertLatencyPrecisionUsage(t, single, "zero-latency", 0, 100)
	for i := 0; i < 9; i++ {
		insertLatencyPrecisionUsage(t, f, "many-fast", 1001, 2000)
	}
	f.at = f.at.Add(5 * time.Minute)
	insertLatencyPrecisionUsage(t, f, "one-slow", 9001, 10000)
	require.NoError(t, repo.RecomputeRange(ctx, start, start.Add(24*time.Hour)))
	cfg := service.ChannelMonitorV2Config{Platforms: []service.ChannelMonitorV2PlatformConfig{{Platform: "openai", Enabled: true}}}
	filter := service.ChannelMonitorV2Filter{Start: start, End: start.Add(24 * time.Hour), Bucket: 5 * time.Minute,
		RestrictGroups: true, AllowedGroupIDs: []int64{f.group, single.group, empty.group}}
	assertGroupOverviewMetricsMatch(t, repo, filter, cfg)
	result, err := repo.GetGroupOverviewMetrics(ctx, filter, cfg)
	require.NoError(t, err)
	require.Len(t, result.Items, 3)
	for _, row := range result.Items {
		switch *row.GroupID {
		case f.group:
			require.EqualValues(t, 10, row.Metrics.TTFT.SampleCount)
			require.Equal(t, 1801.0, *row.Metrics.TTFT.AvgMs, "weight original samples, not per-bucket averages")
			require.EqualValues(t, 1001, *row.Metrics.TTFT.P90Ms, "the ninth of ten samples exactly meets P90")
		case single.group:
			require.EqualValues(t, 1, row.Metrics.TTFT.SampleCount)
			require.Equal(t, 0.0, *row.Metrics.TTFT.AvgMs)
			require.EqualValues(t, 0, *row.Metrics.TTFT.P50Ms)
			require.EqualValues(t, 0, *row.Metrics.TTFT.P90Ms)
		case empty.group:
			require.Zero(t, row.Metrics.TPM)
			require.Zero(t, row.Metrics.RequestCount)
			require.Nil(t, row.Metrics.TTFT.AvgMs)
			require.Nil(t, row.Metrics.TTFT.P50Ms)
			require.Nil(t, row.Metrics.TTFT.P90Ms)
		}
	}
	// Keep legacy coarse bounds beside a newer fine bucket. The SQL must merge
	// distributions without duplicating the private-user histogram audience.
	_, err = integrationDB.Exec(`UPDATE channel_monitor_v2_latency_histograms_rollup SET upper_bound_ms=2000
	 WHERE group_id=$1 AND metric='ttft' AND upper_bound_ms=1001`, f.group)
	require.NoError(t, err)
	f.at = f.at.Add(5 * time.Minute)
	insertLatencyPrecisionUsage(t, f, "new-fine", 11001, 12000)
	require.NoError(t, repo.RecomputeRange(ctx, f.at, f.at.Add(time.Minute)))
	assertGroupOverviewMetricsMatch(t, repo, filter, cfg)
	result, err = repo.GetGroupOverviewMetrics(ctx, filter, cfg)
	require.NoError(t, err)
	for _, row := range result.Items {
		if *row.GroupID == f.group {
			require.EqualValues(t, 11, row.Metrics.TTFT.SampleCount)
			require.EqualValues(t, 2000, *row.Metrics.TTFT.P50Ms)
			require.EqualValues(t, 9001, *row.Metrics.TTFT.P90Ms, "P90 crosses into the next bound for eleven samples")
			require.InDelta(t, float64(1001*9+9001+11001)/11, *row.Metrics.TTFT.AvgMs, 1e-9)
		}
	}
}

func TestChannelMonitorV2GroupMetricsPartialCoverageIntegration(t *testing.T) {
	f, repo, start := newLatencyPrecisionFixture(t)
	ctx := context.Background()
	for i, latency := range []int{50001, 3001, 7001, 60001} {
		f.at = start.Add(time.Hour + time.Duration(i)*5*time.Minute)
		f.usage(t, f.key, "partial-free", fmt.Sprintf("local:partial-%d-%d", f.group, i), latency)
	}
	require.NoError(t, repo.RecomputeRange(ctx, start, start.Add(24*time.Hour)))
	var watermark string
	require.NoError(t, integrationDB.QueryRow(`SELECT row_to_json(w)::text FROM channel_monitor_v2_watermarks w WHERE id=1`).Scan(&watermark))
	t.Cleanup(func() {
		_, err := integrationDB.Exec(`UPDATE channel_monitor_v2_watermarks w SET
		 usage_coverage_start=p.usage_coverage_start,error_coverage_start=p.error_coverage_start,data_through=p.data_through
		 FROM json_populate_record(NULL::channel_monitor_v2_watermarks,$1::json) p WHERE w.id=1`, watermark)
		require.NoError(t, err)
	})
	_, err := integrationDB.Exec(`UPDATE channel_monitor_v2_watermarks SET
	 usage_coverage_start=$1,error_coverage_start=$2,data_through=$3 WHERE id=1`,
		start.Add(time.Hour), start.Add(65*time.Minute), start.Add(75*time.Minute))
	require.NoError(t, err)
	cfg := service.ChannelMonitorV2Config{Platforms: []service.ChannelMonitorV2PlatformConfig{{Platform: "openai", Enabled: true}}}
	filter := service.ChannelMonitorV2Filter{Start: start, End: start.Add(48 * time.Hour), Bucket: 5 * time.Minute,
		RestrictGroups: true, AllowedGroupIDs: []int64{f.group}}
	assertGroupOverviewMetricsMatch(t, repo, filter, cfg)
	result, err := repo.GetGroupOverviewMetrics(ctx, filter, cfg)
	require.NoError(t, err)
	require.Len(t, result.Items, 1)
	metric := result.Items[0].Metrics
	require.EqualValues(t, 2, metric.RequestCount, "source coverage excludes both the early and uncomputed trailing facts")
	require.EqualValues(t, 640, metric.TokenCount, "all input/output/cache token classes count, including free requests")
	require.Equal(t, 64.0, metric.TPM, "divide by the ten covered minutes, not the requested two days")
	require.Equal(t, 5001.0, *metric.TTFT.AvgMs)
	require.EqualValues(t, 7001, *metric.TTFT.P90Ms)
}

func TestChannelMonitorV2GroupMetricsMatchesDetailedIntegration(t *testing.T) {
	f, repo, start := newLatencyPrecisionFixture(t)
	ctx := context.Background()
	_, err := integrationDB.Exec(`UPDATE groups SET platform='composite' WHERE id=$1`, f.group)
	require.NoError(t, err)
	other := newMonitorOverviewFixture(t, "openai")
	other.at = f.at
	for _, value := range []int{0, 7432, 7432, 9999, 14211} {
		insertLatencyPrecisionUsage(t, f, "model-a", value, 25000)
		insertLatencyPrecisionUsage(t, other, "model-hidden", 1111, 25000)
	}
	anthropic := f
	anthropic.account = mustCreateAccount(t, testEntClient(t), &service.Account{Name: "overview-anthropic", Platform: "anthropic"}).ID
	insertLatencyPrecisionUsage(t, anthropic, "model-claude", 5432, 25000)
	f.at = f.at.Add(5 * time.Minute)
	insertLatencyPrecisionUsage(t, f, "model-b", 3333, 600001)
	f.failure(t, f.key, "model-b", "failed", "", "provider", "upstream", "upstream_error", "timeout", 504, time.Second)
	_, err = integrationDB.Exec(`INSERT INTO ops_error_logs(user_id,api_key_id,group_id,request_id,model,platform,error_owner,error_phase,error_type,error_message,status_code,created_at) VALUES($1,$2,$3,'unrouted','model-c','composite','gateway','scheduling','scheduling_error','No available accounts',503,$4)`, f.user, f.key, f.group, f.at.Add(2*time.Second))
	require.NoError(t, err)
	require.NoError(t, repo.RecomputeRange(ctx, start, start.Add(24*time.Hour)))
	cfg := service.ChannelMonitorV2Config{Platforms: []service.ChannelMonitorV2PlatformConfig{{Platform: "openai", Enabled: true, Models: []string{"model-a"}}, {Platform: "anthropic", Enabled: true}}, IgnoredErrorCategories: []string{"timeout"}}
	for _, bucket := range []time.Duration{time.Minute, 5 * time.Minute, time.Hour, 12 * time.Hour, 24 * time.Hour} {
		t.Run(bucket.String(), func(t *testing.T) {
			filter := service.ChannelMonitorV2Filter{Start: start, End: start.Add(24 * time.Hour), Bucket: bucket, RestrictGroups: true, AllowedGroupIDs: []int64{f.group}}
			assertGroupOverviewMetricsMatch(t, repo, filter, cfg)
		})
	}
	filter := service.ChannelMonitorV2Filter{Start: start, End: start.Add(24 * time.Hour), Bucket: 5 * time.Minute, RestrictGroups: true, AllowedGroupIDs: []int64{f.group, other.group}}
	assertGroupOverviewMetricsMatch(t, repo, filter, cfg)
	// Old facts must keep availability unknown even when newer rows are present.
	_, err = integrationDB.Exec(`UPDATE channel_monitor_v2_metrics_rollup SET aggregation_version=1,availability_error_requests=NULL WHERE group_id=$1 AND model='model-b'`, f.group)
	require.NoError(t, err)
	assertGroupOverviewMetricsMatch(t, repo, filter, cfg)
	filter.AllowedGroupIDs = nil
	empty, err := repo.GetGroupOverviewMetrics(ctx, filter, cfg)
	require.NoError(t, err)
	require.Empty(t, empty.Items)
	filter.AllowedGroupIDs = []int64{f.group}
	cfg.GroupIDs = []int64{other.group}
	empty, err = repo.GetGroupOverviewMetrics(ctx, filter, cfg)
	require.NoError(t, err)
	require.Empty(t, empty.Items)
}

func TestChannelMonitorV2GroupMetricsReadBenchmarkIntegration(t *testing.T) {
	f, repo, start := newLatencyPrecisionFixture(t)
	ctx := context.Background()
	var watermark string
	require.NoError(t, integrationDB.QueryRow(`SELECT row_to_json(w)::text FROM channel_monitor_v2_watermarks w WHERE id=1`).Scan(&watermark))
	t.Cleanup(func() {
		_, err := integrationDB.Exec(`UPDATE channel_monitor_v2_watermarks w SET
		 usage_coverage_start=p.usage_coverage_start,error_coverage_start=p.error_coverage_start,
		 availability_coverage_start=p.availability_coverage_start,backfill_cursor=p.backfill_cursor,
		 data_through=p.data_through,last_successful_at=p.last_successful_at,aggregation_version=p.aggregation_version
		 FROM json_populate_record(NULL::channel_monitor_v2_watermarks,$1::json) p WHERE w.id=1`, watermark)
		require.NoError(t, err)
		for _, table := range []string{"channel_monitor_v2_metrics_rollup", "channel_monitor_v2_latency_histograms_rollup"} {
			_, err = integrationDB.Exec(fmt.Sprintf("DELETE FROM %s WHERE group_id=$1", table), f.group)
			require.NoError(t, err)
		}
	})
	// A fixed 30-day histogram fixture: 3 models, 2000 distinct TTFT bounds
	// and a duration distribution per day, with a second private-user audience.
	_, err := integrationDB.Exec(`INSERT INTO channel_monitor_v2_metrics_rollup(bucket_start,bucket_seconds,platform,group_id,model,success_requests,input_tokens,cache_read_tokens,ttft_count,aggregation_version,availability_error_requests)
	 SELECT $1::timestamptz-d*INTERVAL '1 day',86400,'openai',$2,'model-'||m,2000,100000,200000,2000,3,0 FROM generate_series(0,29)d CROSS JOIN generate_series(1,3)m`, start, f.group)
	require.NoError(t, err)
	_, err = integrationDB.Exec(`INSERT INTO channel_monitor_v2_latency_histograms_rollup(bucket_start,bucket_seconds,platform,group_id,model,user_id,metric,upper_bound_ms,sample_count)
	 SELECT $1::timestamptz-d*INTERVAL '1 day',86400,'openai',$2,'model-'||m,u,metric,1000+b,1
	 FROM generate_series(0,29)d CROSS JOIN generate_series(1,3)m CROSS JOIN generate_series(1,2000)b CROSS JOIN (VALUES(0::bigint),($3::bigint))audience(u) CROSS JOIN (VALUES('ttft'),('duration'))metrics(metric)`, start, f.group, f.user)
	require.NoError(t, err)
	_, err = integrationDB.Exec(`UPDATE channel_monitor_v2_watermarks SET usage_coverage_start=$1,error_coverage_start=$1,availability_coverage_start=$1,backfill_cursor=$1,data_through=$2,last_successful_at=NOW(),aggregation_version=3 WHERE id=1`, start.Add(-29*24*time.Hour), start.Add(24*time.Hour))
	require.NoError(t, err)
	cfg := service.ChannelMonitorV2Config{Platforms: []service.ChannelMonitorV2PlatformConfig{{Platform: "openai", Enabled: true}}}
	filter := service.ChannelMonitorV2Filter{Start: start.Add(-29 * 24 * time.Hour), End: start.Add(24 * time.Hour), Bucket: 24 * time.Hour, RestrictGroups: true, AllowedGroupIDs: []int64{f.group}}
	assertGroupOverviewMetricsMatch(t, repo, filter, cfg)
	for round := 1; round <= 3; round++ {
		for _, method := range []string{"matrix", "overview"} {
			result := testing.Benchmark(func(b *testing.B) {
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					var err error
					if method == "matrix" {
						_, err = repo.GetMatrix(ctx, filter, cfg, service.ChannelMonitorV2GroupByGroup, false)
					} else {
						_, err = repo.GetGroupOverviewMetrics(ctx, filter, cfg)
					}
					if err != nil {
						b.Fatal(err)
					}
				}
			})
			t.Logf("round=%d method=%s %s %s", round, method, result.String(), result.MemString())
		}
	}
}
