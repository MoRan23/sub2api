//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestChannelMonitorV2RetentionBacklogAndPlanIntegration(t *testing.T) {
	preservePrecisionUpgradeWatermark(t)
	f := newMonitorOverviewFixture(t, "openai")
	repo := &channelMonitorV2Repository{db: integrationDB}
	now := time.Now().UTC().Truncate(time.Minute)
	day := now.Add(-10 * 24 * time.Hour)
	setPrecisionUpgradeWatermark(t, service.ChannelMonitorV2AggregationVersion, day, day, now)
	_, err := integrationDB.Exec(`INSERT INTO channel_monitor_v2_user_metrics_1m (bucket_start,platform,group_id,model,user_id)
SELECT $1,'openai',$2,'retention-'||n::text,$3 FROM generate_series(1,5000) n`, day, f.group, f.user)
	require.NoError(t, err)
	_, err = integrationDB.Exec(`ANALYZE channel_monitor_v2_user_metrics_1m`)
	require.NoError(t, err)
	var rawPlan []byte
	require.NoError(t, integrationDB.QueryRow(`EXPLAIN (FORMAT JSON) DELETE FROM channel_monitor_v2_user_metrics_1m WHERE ctid = ANY(ARRAY(
SELECT ctid FROM channel_monitor_v2_user_metrics_1m WHERE bucket_start < $1
AND NOT (bucket_start >= $2 AND bucket_start < $3) ORDER BY bucket_start LIMIT 1000))`, now.Add(-3*24*time.Hour), day, day).Scan(&rawPlan))
	var plan any
	require.NoError(t, json.Unmarshal(rawPlan, &plan))
	require.Contains(t, string(rawPlan), `"Node Type": "Tid Scan"`, "the outer delete should address the selected tuples rather than scan the whole table")
	t.Logf("bounded_delete_plan=%s", rawPlan)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	require.NoError(t, repo.pruneChannelMonitorV2Retention(ctx, now))
	var remaining int
	require.NoError(t, integrationDB.QueryRow(`SELECT COUNT(*) FROM channel_monitor_v2_user_metrics_1m WHERE group_id=$1`, f.group).Scan(&remaining))
	require.Zero(t, remaining, "one cleanup budget must drain multiple batches, not cap throughput at 1000 rows per refresh")
}

func TestChannelMonitorV2BoundedHistoricalRetentionIntegration(t *testing.T) {
	preservePrecisionUpgradeWatermark(t)
	f := newMonitorOverviewFixture(t, "openai")
	repo := &channelMonitorV2Repository{db: integrationDB}
	now := time.Now().UTC().Truncate(time.Minute)
	day := now.Truncate(24 * time.Hour).Add(-10 * 24 * time.Hour)
	end := day.Add(48 * time.Hour)
	setPrecisionUpgradeWatermark(t, service.ChannelMonitorV2AggregationVersion, day, end, now)
	for _, offset := range []time.Duration{time.Hour, 13 * time.Hour, 19 * time.Hour, 25 * time.Hour, 37 * time.Hour, 43 * time.Hour} {
		f.at = day.Add(offset)
		insertLatencyPrecisionUsage(t, f, "bounded-history", 7432, 9000)
	}
	// Six-hour chunks cross two midnights. Old minute facts are needed only
	// until that day's rollup is complete, not until the entire replay ends.
	for cursor := end; cursor.After(day); {
		start := cursor.Add(-6 * time.Hour)
		require.NoError(t, repo.RecomputeRange(context.Background(), start, cursor))
		var stale int
		boundary := start.Truncate(24 * time.Hour)
		if !start.Equal(boundary) {
			boundary = boundary.Add(24 * time.Hour)
		}
		for _, table := range []string{"channel_monitor_v2_user_metrics_1m", "channel_monitor_v2_latency_histograms_1m"} {
			require.NoError(t, integrationDB.QueryRow(`SELECT COUNT(*) FROM `+table+` WHERE group_id=$1 AND (bucket_start < $2 OR bucket_start >= $3)`, f.group, start, boundary).Scan(&stale))
			require.Zero(t, stale, table)
		}
		if !start.After(day.Add(24 * time.Hour)) {
			assertPrecisionUpgradeHistogram(t, f.group, day.Add(24*time.Hour), end, 86400, map[int64]int64{7432: 3})
		}
		cursor = start
	}
	assertPrecisionUpgradeHistogram(t, f.group, day, end, 86400, map[int64]int64{7432: 6})
	assertPrecisionUpgradeHistogram(t, f.group, day, end, 43200, map[int64]int64{7432: 6})
	var expired int
	require.NoError(t, integrationDB.QueryRow(`SELECT COUNT(*) FROM channel_monitor_v2_latency_histograms_rollup WHERE group_id=$1 AND bucket_seconds=300`, f.group).Scan(&expired))
	require.Zero(t, expired, "already-expired five-minute rollups must not be written")
}

func TestChannelMonitorV2CleanupFailureKeepsProgressIntegration(t *testing.T) {
	preservePrecisionUpgradeWatermark(t)
	f := newMonitorOverviewFixture(t, "openai")
	repo := &channelMonitorV2Repository{db: integrationDB}
	now := time.Now().UTC().Truncate(time.Minute)
	start := now.Add(-5 * time.Minute)
	setPrecisionUpgradeWatermark(t, service.ChannelMonitorV2AggregationVersion, now.Add(-10*24*time.Hour), start, start)
	f.at = start
	insertLatencyPrecisionUsage(t, f, "cleanup-failure", 7432, 9000)
	_, err := integrationDB.Exec(`INSERT INTO channel_monitor_v2_user_metrics_1m (bucket_start,platform,group_id,model,user_id) VALUES($1,'openai',$2,'cleanup-failure',$3)`, now.Add(-8*24*time.Hour), f.group, f.user)
	require.NoError(t, err)
	name := fmt.Sprintf("monitor_cleanup_failure_%d", f.group)
	_, err = integrationDB.Exec(fmt.Sprintf(`CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected cleanup failure'; END $$;
CREATE TRIGGER %s BEFORE DELETE ON channel_monitor_v2_user_metrics_1m FOR EACH ROW WHEN (OLD.group_id=%d AND OLD.bucket_start < TIMESTAMPTZ '%s') EXECUTE FUNCTION %s()`, name, name, f.group, start.Format(time.RFC3339), name))
	require.NoError(t, err)
	t.Cleanup(func() {
		_, err := integrationDB.Exec(fmt.Sprintf(`DROP TRIGGER %s ON channel_monitor_v2_user_metrics_1m; DROP FUNCTION %s()`, name, name))
		require.NoError(t, err)
	})
	require.NoError(t, repo.RecomputeRange(context.Background(), start, now), "cleanup failure must not retry committed work")
	wm, err := repo.GetAggregationWatermark(context.Background())
	require.NoError(t, err)
	require.Equal(t, now, wm.DataThrough)
	assertPrecisionUpgradeHistogram(t, f.group, start, now, 0, map[int64]int64{7432: 1})
}

func TestChannelMonitorV2SharedUsageAggregationIntegration(t *testing.T) {
	f := newMonitorOverviewFixture(t, "openai")
	start := time.Now().UTC().Truncate(24 * time.Hour).Add(-24 * time.Hour)
	end := start.Add(time.Hour)
	_, err := integrationDB.Exec(`INSERT INTO usage_logs
(user_id,api_key_id,account_id,group_id,request_id,model,input_tokens,output_tokens,first_token_ms,duration_ms,actual_cost,created_at)
SELECT $1::bigint,$2::bigint,$3::bigint,$4::bigint,'local:shared-scan-'||$4::bigint::text||'-'||n::text,'shared-scan-'||(n%3)::text,
100,20,1+n%10000,1000+n%20000,0,$5::timestamptz+(n%60)*INTERVAL '1 minute'
FROM generate_series(1,20000) n`, f.user, f.key, f.account, f.group, start)
	require.NoError(t, err)
	baseline := []string{
		fmt.Sprintf(channelMonitorV2UsageMetricsSQL, channelMonitorV2PlatformSQL, channelMonitorV2ModelSQL),
		fmt.Sprintf(channelMonitorV2UserMetricsSQL, channelMonitorV2PlatformSQL, channelMonitorV2ModelSQL),
		fmt.Sprintf(channelMonitorV2HistogramSQL, channelMonitorV2PlatformSQL, channelMonitorV2ModelSQL, channelMonitorV2MetricHistogramBoundSQL("latency.metric", "latency.value_ms")),
	}
	// Compare every persisted field, excluding each statement's wall clock.
	// Keep the comparison in PostgreSQL instead of transferring full histograms.
	func() {
		tx, err := integrationDB.Begin()
		require.NoError(t, err)
		defer tx.Rollback()
		tables := []string{"channel_monitor_v2_metrics_1m", "channel_monitor_v2_user_metrics_1m", "channel_monitor_v2_latency_histograms_1m"}
		clear := func() {
			for _, table := range tables {
				_, err := tx.Exec(`DELETE FROM `+table+` WHERE bucket_start >= $1 AND bucket_start < $2`, start, end)
				require.NoError(t, err)
			}
		}
		clear()
		for _, query := range baseline {
			_, err := tx.Exec(query, start, end)
			require.NoError(t, err)
		}
		for i, table := range tables {
			_, err := tx.Exec(fmt.Sprintf(`CREATE TEMP TABLE old_facts_%d ON COMMIT DROP AS SELECT to_jsonb(t)-'computed_at' AS row_data FROM %s t WHERE group_id=$1`, i, table), f.group)
			require.NoError(t, err)
		}
		clear()
		_, err = tx.Exec(channelMonitorV2UsageAggregationSQL, start, end)
		require.NoError(t, err)
		for i, table := range tables {
			var differences int
			query := fmt.Sprintf(`WITH current_rows AS (SELECT to_jsonb(t)-'computed_at' AS row_data FROM %s t WHERE group_id=$1)
SELECT COUNT(*) FROM ((SELECT row_data FROM old_facts_%d EXCEPT ALL SELECT row_data FROM current_rows)
UNION ALL (SELECT row_data FROM current_rows EXCEPT ALL SELECT row_data FROM old_facts_%d)) diff`, table, i, i)
			require.NoError(t, tx.QueryRow(query, f.group).Scan(&differences))
			require.Zero(t, differences, table)
		}
	}()
	measure := func(queries []string) (time.Duration, []int64) {
		tx, err := integrationDB.Begin()
		require.NoError(t, err)
		defer tx.Rollback()
		for _, table := range []string{"channel_monitor_v2_metrics_1m", "channel_monitor_v2_user_metrics_1m", "channel_monitor_v2_latency_histograms_1m"} {
			_, err := tx.Exec(`DELETE FROM `+table+` WHERE bucket_start >= $1 AND bucket_start < $2`, start, end)
			require.NoError(t, err)
		}
		began := time.Now()
		for _, query := range queries {
			_, err = tx.Exec(query, start, end)
			require.NoError(t, err)
		}
		elapsed := time.Since(began)
		var metrics, users, histogram int64
		require.NoError(t, tx.QueryRow(`SELECT SUM(success_requests) FROM channel_monitor_v2_metrics_1m WHERE group_id=$1`, f.group).Scan(&metrics))
		require.NoError(t, tx.QueryRow(`SELECT SUM(success_requests) FROM channel_monitor_v2_user_metrics_1m WHERE group_id=$1`, f.group).Scan(&users))
		require.NoError(t, tx.QueryRow(`SELECT SUM(sample_count) FROM channel_monitor_v2_latency_histograms_1m WHERE group_id=$1`, f.group).Scan(&histogram))
		return elapsed, []int64{metrics, users, histogram}
	}
	for round := 1; round <= 3; round++ {
		oldDuration, oldCounts := measure(baseline)
		newDuration, newCounts := measure([]string{channelMonitorV2UsageAggregationSQL})
		require.Equal(t, []int64{20000, 20000, 80000}, oldCounts)
		require.Equal(t, oldCounts, newCounts)
		t.Logf("round=%d three_scans=%s shared_scan=%s samples=%v", round, oldDuration, newDuration, newCounts)
	}
}
