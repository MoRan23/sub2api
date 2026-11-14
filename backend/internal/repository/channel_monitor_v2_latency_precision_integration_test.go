//go:build integration

package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// These tests use only TestMain's disposable PostgreSQL. Explicit historical
// timestamps exercise all four rollup sizes without waiting for a boundary.
func newLatencyPrecisionFixture(t *testing.T) (monitorOverviewFixture, *channelMonitorV2Repository, time.Time) {
	t.Helper()
	f := newMonitorOverviewFixture(t, "openai")
	start := time.Now().UTC().Truncate(24 * time.Hour).Add(-24 * time.Hour)
	f.at = start.Add(time.Hour)
	var previous sql.NullTime
	require.NoError(t, integrationDB.QueryRow(`SELECT availability_source_start FROM channel_monitor_v2_watermarks WHERE id=1`).Scan(&previous))
	_, err := integrationDB.Exec(`UPDATE channel_monitor_v2_watermarks SET availability_source_start=$1 WHERE id=1`, start)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, err := integrationDB.Exec(`UPDATE channel_monitor_v2_watermarks SET availability_source_start=$1 WHERE id=1`, previous)
		require.NoError(t, err)
	})
	return f, &channelMonitorV2Repository{db: integrationDB}, start
}

func insertLatencyPrecisionUsage(t *testing.T, f monitorOverviewFixture, model string, ttft, duration int) {
	t.Helper()
	_, err := integrationDB.Exec(`INSERT INTO usage_logs
	 (user_id,api_key_id,account_id,group_id,request_id,model,input_tokens,output_tokens,
	 first_token_ms,duration_ms,actual_cost,created_at)
	 VALUES($1,$2,$3,$4,$5,$6,100,20,$7,$8,0,$9)`,
		f.user, f.key, f.account, f.group, "local:latency-"+uuid.NewString(), model, ttft, duration, f.at.Add(10*time.Second))
	require.NoError(t, err)
}

func TestChannelMonitorV2LatencyPrecisionBoundariesAndRollupsIntegration(t *testing.T) {
	f, repo, start := newLatencyPrecisionFixture(t)
	cases := []struct {
		value, ttft, duration int64
	}{
		{0, 0, 50}, {1, 50, 50}, {50, 50, 50}, {51, 100, 100},
		{250, 250, 250}, {251, 300, 500}, {1000, 1000, 1000}, {1001, 1100, 2000},
		{3210, 3300, 5000}, {5420, 5500, 8000}, {14210, 14300, 15000},
		{30000, 30000, 30000}, {30001, 30250, 60000}, {30250, 30250, 60000}, {30251, 30500, 60000},
		{60000, 60000, 60000}, {60001, 61000, 120000}, {61000, 61000, 120000}, {61001, 62000, 120000},
		{120000, 120000, 120000}, {120001, 125000, 300000}, {125000, 125000, 300000}, {125001, 130000, 300000},
		{600000, 600000, 600000}, {600001, 2147483647, 2147483647}, {2147483647, 2147483647, 2147483647},
	}
	type expectedBucket struct {
		model, metric string
		bound         int64
	}
	want := make(map[expectedBucket]int64, len(cases)*2)
	for _, tc := range cases {
		model := fmt.Sprintf("precision-%d", tc.value)
		insertLatencyPrecisionUsage(t, f, model, int(tc.value), int(tc.value))
		want[expectedBucket{model, "ttft", tc.ttft}] = 1
		want[expectedBucket{model, "duration", tc.duration}] = 1
	}
	for run := 0; run < 2; run++ {
		// Include one minute of the following day: a range wholly inside one
		// daily bucket deliberately leaves that coarse rollup untouched.
		require.NoError(t, repo.RecomputeRange(context.Background(), start, start.Add(24*time.Hour+time.Minute)))
		for _, seconds := range []int{0, 300, 3600, 43200, 86400} {
			table := "channel_monitor_v2_latency_histograms_1m"
			where := ""
			if seconds != 0 {
				table = "channel_monitor_v2_latency_histograms_rollup"
				where = fmt.Sprintf(" AND bucket_seconds=%d", seconds)
			}
			rows, err := integrationDB.Query(`SELECT model,metric,upper_bound_ms,SUM(sample_count) FROM `+table+` WHERE group_id=$1 AND user_id=0`+where+` GROUP BY model,metric,upper_bound_ms`, f.group)
			require.NoError(t, err)
			got := map[expectedBucket]int64{}
			for rows.Next() {
				var bucket expectedBucket
				var count int64
				require.NoError(t, rows.Scan(&bucket.model, &bucket.metric, &bucket.bound, &count))
				got[bucket] = count
			}
			require.NoError(t, rows.Err())
			require.NoError(t, rows.Close())
			require.Equal(t, want, got, "run=%d bucket_seconds=%d: single-sample buckets retain their upper bounds; rewrites do not duplicate samples", run, seconds)
		}
	}
	cfg := service.ChannelMonitorV2Config{Platforms: []service.ChannelMonitorV2PlatformConfig{{Platform: "openai", Enabled: true}}}
	for _, tc := range []struct{ value, want int64 }{{0, 0}, {3210, 3300}, {5420, 5500}, {14210, 14300}} {
		filter := service.ChannelMonitorV2Filter{Start: f.at, End: f.at.Add(5 * time.Minute), Bucket: 5 * time.Minute, Models: []string{fmt.Sprintf("precision-%d", tc.value)}, AllowedGroupIDs: []int64{f.group}, RestrictGroups: true}
		matrix, err := repo.GetMatrix(context.Background(), filter, cfg, service.ChannelMonitorV2GroupByGroup, false)
		require.NoError(t, err)
		require.Len(t, matrix.Items, 1)
		require.EqualValues(t, 1, matrix.Items[0].Metrics.TTFT.SampleCount)
		require.NotNil(t, matrix.Items[0].Metrics.TTFT.P50Ms)
		require.Equal(t, tc.want, *matrix.Items[0].Metrics.TTFT.P50Ms, "a sparse single-sample percentile uses the real bucket upper bound")
	}
}

func TestChannelMonitorV2LatencyPrecisionMergesUsersAndPlatformsIntegration(t *testing.T) {
	f, repo, start := newLatencyPrecisionFixture(t)
	_, err := integrationDB.Exec(`UPDATE groups SET platform='composite' WHERE id=$1`, f.group)
	require.NoError(t, err)
	insertLatencyPrecisionUsage(t, f, "sparse-fast", 3210, 3210)
	client := testEntClient(t)
	second := f
	second.user = mustCreateUser(t, client, &service.User{Email: uuid.NewString() + "@latency.test"}).ID
	second.account = mustCreateAccount(t, client, &service.Account{Name: "latency-anthropic-" + uuid.NewString(), Platform: "anthropic"}).ID
	second.key = mustCreateApiKey(t, client, &service.APIKey{UserID: second.user, GroupID: &f.group}).ID
	for i := 0; i < 3; i++ {
		insertLatencyPrecisionUsage(t, second, "sparse-slow", 14210, 14210)
	}
	other := newMonitorOverviewFixture(t, "openai")
	other.at = f.at
	for i := 0; i < 2; i++ {
		insertLatencyPrecisionUsage(t, other, "separate-group", 5420, 5420)
	}
	require.NoError(t, repo.RecomputeRange(context.Background(), start, start.Add(24*time.Hour)))
	cfg := service.ChannelMonitorV2Config{Platforms: []service.ChannelMonitorV2PlatformConfig{{Platform: "openai", Enabled: true}, {Platform: "anthropic", Enabled: true}}}
	filter := service.ChannelMonitorV2Filter{Start: f.at, End: f.at.Add(5 * time.Minute), Bucket: 5 * time.Minute, AllowedGroupIDs: []int64{f.group, other.group}, RestrictGroups: true}
	matrix, err := repo.GetMatrix(context.Background(), filter, cfg, service.ChannelMonitorV2GroupByGroup, false)
	require.NoError(t, err)
	require.Len(t, matrix.Items, 2)
	for _, row := range matrix.Items {
		require.NotNil(t, row.GroupID)
		require.NotNil(t, row.Metrics.TTFT.P50Ms)
		if *row.GroupID == f.group {
			require.EqualValues(t, 4, row.Metrics.TTFT.SampleCount)
			require.EqualValues(t, 14300, *row.Metrics.TTFT.P50Ms, "combine sample counts across platforms, not per-platform medians")
			require.EqualValues(t, 15000, *row.Metrics.Duration.P50Ms, "duration retains legacy precision")
		} else {
			require.Equal(t, other.group, *row.GroupID)
			require.EqualValues(t, 2, row.Metrics.TTFT.SampleCount)
			require.EqualValues(t, 5500, *row.Metrics.TTFT.P50Ms)
		}
	}
	for _, audience := range []struct{ id, count int64 }{{0, 4}, {f.user, 1}, {second.user, 3}} {
		var count int64
		require.NoError(t, integrationDB.QueryRow(`SELECT SUM(sample_count) FROM channel_monitor_v2_latency_histograms_rollup WHERE group_id=$1 AND user_id=$2 AND metric='ttft' AND bucket_seconds=300`, f.group, audience.id).Scan(&count))
		require.Equal(t, audience.count, count, "global and per-user audiences stay separate")
	}
}

func TestChannelMonitorV2LatencyPrecisionLegacyAndFineBucketsIntegration(t *testing.T) {
	f, repo, start := newLatencyPrecisionFixture(t)
	for i := 0; i < 3; i++ {
		insertLatencyPrecisionUsage(t, f, "mixed-resolution", 3210, 3210)
	}
	require.NoError(t, repo.RecomputeRange(context.Background(), start, start.Add(24*time.Hour)))
	// Simulate a retained pre-upgrade minute and its rollups. Its true values
	// cannot be recovered from the old upper bound, so it must remain 5000.
	for _, table := range []string{"channel_monitor_v2_latency_histograms_1m", "channel_monitor_v2_latency_histograms_rollup"} {
		_, err := integrationDB.Exec(`UPDATE `+table+` SET upper_bound_ms=5000 WHERE group_id=$1 AND metric='ttft'`, f.group)
		require.NoError(t, err)
	}
	f.at = f.at.Add(5 * time.Minute)
	for i := 0; i < 2; i++ {
		insertLatencyPrecisionUsage(t, f, "mixed-resolution", 3210, 3210)
	}
	for run := 0; run < 2; run++ {
		require.NoError(t, repo.RecomputeRange(context.Background(), f.at, f.at.Add(time.Minute)))
	}
	rows, err := integrationDB.Query(`SELECT upper_bound_ms,SUM(sample_count) FROM channel_monitor_v2_latency_histograms_rollup WHERE group_id=$1 AND user_id=0 AND metric='ttft' AND bucket_seconds=300 GROUP BY upper_bound_ms`, f.group)
	require.NoError(t, err)
	hist := map[int64]int64{}
	for rows.Next() {
		var bound, count int64
		require.NoError(t, rows.Scan(&bound, &count))
		hist[bound] = count
	}
	require.NoError(t, rows.Err())
	require.NoError(t, rows.Close())
	require.Equal(t, map[int64]int64{3300: 2, 5000: 3}, hist)
	cfg := service.ChannelMonitorV2Config{Platforms: []service.ChannelMonitorV2PlatformConfig{{Platform: "openai", Enabled: true}}}
	filter := service.ChannelMonitorV2Filter{Start: f.at.Add(-5 * time.Minute), End: f.at.Add(5 * time.Minute), Bucket: 5 * time.Minute, AllowedGroupIDs: []int64{f.group}, RestrictGroups: true}
	matrix, err := repo.GetMatrix(context.Background(), filter, cfg, service.ChannelMonitorV2GroupByGroup, false)
	require.NoError(t, err)
	require.Len(t, matrix.Items, 1)
	require.EqualValues(t, 5, matrix.Items[0].Metrics.TTFT.SampleCount)
	require.EqualValues(t, 5000, *matrix.Items[0].Metrics.TTFT.P50Ms, "legacy counts keep their conservative upper bound; sparse bins are not interpolated")
	var p50 float64
	require.NoError(t, integrationDB.QueryRow(`SELECT percentile_cont(0.5) WITHIN GROUP (ORDER BY first_token_ms) FROM usage_logs WHERE group_id=$1`, f.group).Scan(&p50))
	require.GreaterOrEqual(t, float64(*matrix.Items[0].Metrics.TTFT.P50Ms), p50)
}

func TestChannelMonitorV2LatencyPrecisionAggregationSampleIntegration(t *testing.T) {
	ctx := context.Background()
	var version string
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT version()`).Scan(&version))
	t.Logf("database=%s", version)
	conn, err := integrationDB.Conn(ctx)
	require.NoError(t, err)
	defer conn.Close()
	_, err = conn.ExecContext(ctx, `CREATE TEMP TABLE latency_precision_sample ON COMMIT PRESERVE ROWS AS
	 SELECT TIMESTAMPTZ '2026-11-01 00:00:00+00' + (n % 60) * INTERVAL '1 minute' AS created_at,
	        1 + n % 7 AS user_id, 1 + n % 3 AS model_id,
	        1 + (n * 7919) % 30000 AS first_token_ms,
	        1 + (n * 3571) % 60000 AS duration_ms
	 FROM generate_series(1,20000) n`)
	require.NoError(t, err)
	defer conn.ExecContext(ctx, `DROP TABLE IF EXISTS latency_precision_sample`)
	_, err = conn.ExecContext(ctx, `ANALYZE latency_precision_sample`)
	require.NoError(t, err)
	oldBound := channelMonitorV2HistogramBoundSQL("latency.value_ms")
	newBound := channelMonitorV2MetricHistogramBoundSQL("latency.metric", "latency.value_ms")
	type planSummary struct {
		Plan struct {
			ActualRows float64 `json:"Actual Rows"`
		} `json:"Plan"`
		ExecutionTime float64 `json:"Execution Time"`
	}
	for run := 1; run <= 3; run++ {
		for _, variant := range []struct{ name, bound string }{{"legacy", oldBound}, {"fine TTFT", newBound}} {
			query := `SELECT created_at,model_id,audience.user_id,latency.metric,` + variant.bound + ` AS upper_bound_ms,COUNT(*) AS sample_count
			 FROM latency_precision_sample
			 CROSS JOIN LATERAL (VALUES (0), (user_id)) audience(user_id)
			 CROSS JOIN LATERAL (VALUES ('ttft',first_token_ms),('duration',duration_ms)) latency(metric,value_ms)
			 GROUP BY 1,2,3,4,5`
			var encoded []byte
			require.NoError(t, conn.QueryRowContext(ctx, "EXPLAIN (ANALYZE, FORMAT JSON) "+query).Scan(&encoded))
			var plans []planSummary
			require.NoError(t, json.Unmarshal(encoded, &plans))
			require.Len(t, plans, 1)
			var samples int64
			require.NoError(t, conn.QueryRowContext(ctx, `SELECT SUM(sample_count) FROM (`+query+`) histogram`).Scan(&samples))
			require.EqualValues(t, 80000, samples, "both audiences and both metrics retain every source sample")
			t.Logf("run=%d variant=%s source_rows=20000 occupied_histogram_rows=%.0f execution_ms=%.3f samples=%d", run, variant.name, plans[0].Plan.ActualRows, plans[0].ExecutionTime, samples)
		}
	}
}
