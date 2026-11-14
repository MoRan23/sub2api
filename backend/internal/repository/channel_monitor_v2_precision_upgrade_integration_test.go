//go:build integration

package repository

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// The repository TestMain owns the disposable PostgreSQL used by these tests.
// Preserve its singleton watermark because the upgrade deliberately rewinds it.
func preservePrecisionUpgradeWatermark(t *testing.T) {
	t.Helper()
	var saved string
	require.NoError(t, integrationDB.QueryRow(`SELECT row_to_json(w) FROM channel_monitor_v2_watermarks w WHERE id=1`).Scan(&saved))
	t.Cleanup(func() {
		tx, err := integrationDB.Begin()
		require.NoError(t, err)
		defer tx.Rollback()
		_, err = tx.Exec(`DELETE FROM channel_monitor_v2_watermarks WHERE id=1`)
		require.NoError(t, err)
		_, err = tx.Exec(`INSERT INTO channel_monitor_v2_watermarks SELECT * FROM json_populate_record(NULL::channel_monitor_v2_watermarks,$1::json)`, saved)
		require.NoError(t, err)
		require.NoError(t, tx.Commit())
	})
}

func setPrecisionUpgradeWatermark(t *testing.T, version int, source, cursor, through time.Time) {
	t.Helper()
	_, err := integrationDB.Exec(`UPDATE channel_monitor_v2_watermarks SET aggregation_version=$1,
	 usage_coverage_start=$2,error_coverage_start=$2,availability_coverage_start=$2,
	 availability_source_start=$2,backfill_cursor=$3,data_through=$4,last_successful_at=NOW() WHERE id=1`, version, source, cursor, through)
	require.NoError(t, err)
}

// Raw-source retention is global, so these cases temporarily isolate both
// source tables and restore other fixtures before leaving the disposable DB.
func isolatePrecisionUpgradeSources(t *testing.T) {
	t.Helper()
	conn, err := integrationDB.Conn(context.Background())
	require.NoError(t, err)
	// Billing entries cascade on usage deletion; save them before clearing either
	// source and restore them after their referenced usage rows.
	tables := []string{"usage_logs", "ops_error_logs", "billing_usage_entries"}
	for _, table := range tables {
		_, err := conn.ExecContext(context.Background(), `CREATE TEMP TABLE precision_saved_`+table+` AS TABLE `+table)
		require.NoError(t, err)
	}
	t.Cleanup(func() {
		defer conn.Close()
		for _, table := range []string{"usage_logs", "ops_error_logs"} {
			_, err := conn.ExecContext(context.Background(), `DELETE FROM `+table)
			require.NoError(t, err)
		}
		for _, table := range tables {
			_, err := conn.ExecContext(context.Background(), `INSERT INTO `+table+` SELECT * FROM precision_saved_`+table)
			require.NoError(t, err)
			_, err = conn.ExecContext(context.Background(), `DROP TABLE precision_saved_`+table)
			require.NoError(t, err)
		}
	})
	for _, table := range []string{"usage_logs", "ops_error_logs"} {
		_, err := conn.ExecContext(context.Background(), `DELETE FROM `+table)
		require.NoError(t, err)
	}
}

func makePrecisionUpgradeLegacy(t *testing.T, groupID int64) {
	t.Helper()
	for _, table := range []string{"channel_monitor_v2_latency_histograms_1m", "channel_monitor_v2_latency_histograms_rollup"} {
		_, err := integrationDB.Exec(`UPDATE `+table+` SET upper_bound_ms=8000 WHERE group_id=$1 AND metric='ttft'`, groupID)
		require.NoError(t, err)
	}
	for _, table := range []string{"channel_monitor_v2_metrics_1m", "channel_monitor_v2_user_metrics_1m", "channel_monitor_v2_metrics_rollup", "channel_monitor_v2_user_metrics_rollup"} {
		_, err := integrationDB.Exec(`UPDATE `+table+` SET aggregation_version=2 WHERE group_id=$1`, groupID)
		require.NoError(t, err)
	}
}

type precisionUpgradeRecompute struct {
	start, end time.Time
	err        error
}

type precisionUpgradeRepository struct {
	*channelMonitorV2Repository
	calls chan precisionUpgradeRecompute
}

func (r *precisionUpgradeRepository) GetConfig(context.Context) (*service.ChannelMonitorV2Config, error) {
	thresholds := service.DefaultChannelMonitorV2HealthThresholds()
	thresholds.MinimumSample = 1
	return &service.ChannelMonitorV2Config{Enabled: true, RefreshIntervalSeconds: 3600,
		Platforms: []service.ChannelMonitorV2PlatformConfig{{Platform: "openai", Enabled: true}}, HealthThresholds: thresholds}, nil
}

func (r *precisionUpgradeRepository) RecomputeRange(ctx context.Context, start, end time.Time) error {
	err := r.channelMonitorV2Repository.RecomputeRange(ctx, start, end)
	if r.calls != nil {
		r.calls <- precisionUpgradeRecompute{start, end, err}
	}
	return err
}

type precisionUpgradeRuntime struct{}

func (precisionUpgradeRuntime) GetChannelMonitorRuntime(context.Context) service.ChannelMonitorRuntime {
	return service.ChannelMonitorRuntime{Enabled: true, Mode: service.ChannelMonitorModeV2}
}

func runPrecisionUpgradeTick(t *testing.T, repo *channelMonitorV2Repository, count int) []precisionUpgradeRecompute {
	t.Helper()
	observed := &precisionUpgradeRepository{channelMonitorV2Repository: repo, calls: make(chan precisionUpgradeRecompute, 4)}
	// There is only one test worker. SQL still runs against the real repository;
	// no shared leader lock or Redis is needed to exercise the public lifecycle.
	worker := service.NewChannelMonitorV2Aggregator(observed, nil, precisionUpgradeRuntime{})
	worker.Start()
	defer worker.Stop()
	var calls []precisionUpgradeRecompute
	for range count {
		select {
		case call := <-observed.calls:
			calls = append(calls, call)
		case <-time.After(15 * time.Second):
			t.Fatal("timed out waiting for the local precision upgrade tick")
		}
	}
	return calls
}

func assertPrecisionUpgradeOverview(t *testing.T, repo *channelMonitorV2Repository, f monitorOverviewFixture, start, end time.Time, bucket time.Duration, wantP50 int64, wantAvailability float64) {
	t.Helper()
	observed := &precisionUpgradeRepository{channelMonitorV2Repository: repo}
	got, err := service.NewChannelMonitorV2Service(observed).GetGroupOverview(context.Background(),
		service.ChannelMonitorV2Filter{Start: start, End: end, Bucket: bucket, Range: "90m"},
		[]service.Group{{ID: f.group, Name: "precision upgrade", Platform: "openai", Status: service.StatusActive}}, nil)
	require.NoError(t, err)
	require.True(t, got.Coverage.AvailabilityReady, "version 2 availability stays usable throughout the precision replay")
	require.True(t, got.Coverage.CoverageComplete)
	require.Len(t, got.Items, 1)
	require.NotEqual(t, "rebuilding", got.Items[0].State)
	require.NotNil(t, got.Items[0].Metrics.TTFTP50Ms)
	require.Equal(t, wantP50, *got.Items[0].Metrics.TTFTP50Ms)
	require.NotNil(t, got.Items[0].Metrics.AvailabilityRate)
	require.InDelta(t, wantAvailability, *got.Items[0].Metrics.AvailabilityRate, 1e-9)
}

func assertPrecisionUpgradeHistogram(t *testing.T, groupID int64, start, end time.Time, seconds int, want map[int64]int64) {
	t.Helper()
	table, bucketFilter := "channel_monitor_v2_latency_histograms_1m", ""
	if seconds != 0 {
		table = "channel_monitor_v2_latency_histograms_rollup"
		bucketFilter = fmt.Sprintf(" AND bucket_seconds=%d", seconds)
	}
	rows, err := integrationDB.Query(`SELECT upper_bound_ms,SUM(sample_count) FROM `+table+`
	 WHERE group_id=$1 AND user_id=0 AND metric='ttft' AND bucket_start >= $2 AND bucket_start < $3`+bucketFilter+` GROUP BY upper_bound_ms`, groupID, start, end)
	require.NoError(t, err)
	defer rows.Close()
	got := map[int64]int64{}
	for rows.Next() {
		var bound, count int64
		require.NoError(t, rows.Scan(&bound, &count))
		got[bound] = count
	}
	require.NoError(t, rows.Err())
	require.Equal(t, want, got, "bucket_seconds=%d", seconds)
}

func addPrecisionUpgradeSQLFailure(t *testing.T, groupID int64, before time.Time) func() {
	t.Helper()
	name := fmt.Sprintf("precision_upgrade_failure_%d", groupID)
	// NOT VALID permits the retained old rows, but rejects the historical insert
	// after the transaction has already deleted and rebuilt its metric facts.
	_, err := integrationDB.Exec(fmt.Sprintf(`ALTER TABLE channel_monitor_v2_latency_histograms_1m ADD CONSTRAINT %s CHECK (group_id <> %d OR bucket_start >= TIMESTAMPTZ '%s') NOT VALID`, name, groupID, before.UTC().Format(time.RFC3339)))
	require.NoError(t, err)
	drop := func() {
		_, err := integrationDB.Exec(`ALTER TABLE channel_monitor_v2_latency_histograms_1m DROP CONSTRAINT IF EXISTS ` + name)
		require.NoError(t, err)
	}
	t.Cleanup(drop)
	return drop
}

func TestChannelMonitorV2PrecisionUpgradeSeedResumeIntegration(t *testing.T) {
	preservePrecisionUpgradeWatermark(t)
	ctx := context.Background()
	repo := &channelMonitorV2Repository{db: integrationDB}
	f := newMonitorOverviewFixture(t, "openai")
	now := time.Now().UTC().Truncate(time.Minute)
	source := now.Add(-6 * time.Hour)
	setPrecisionUpgradeWatermark(t, 2, source, source, now)
	var windows []time.Time
	for _, age := range []time.Duration{30 * time.Minute, 165 * time.Minute, 5 * time.Hour} {
		f.at = now.Add(-age).Truncate(5 * time.Minute)
		windows = append(windows, f.at)
		for range 3 {
			insertLatencyPrecisionUsage(t, f, "precision-upgrade", 7432, 9000)
		}
		f.failure(t, f.key, "precision-upgrade", uuid.NewString(), "", "provider", "upstream", "upstream_error", "upstream failure", 502, 20*time.Second)
	}
	require.NoError(t, repo.RecomputeRange(ctx, source, now))
	makePrecisionUpgradeLegacy(t, f.group)
	setPrecisionUpgradeWatermark(t, 2, source, source, now)
	for _, at := range windows {
		assertPrecisionUpgradeOverview(t, repo, f, at, at.Add(5*time.Minute), 5*time.Minute, 8000, .75)
	}

	// A failed first seed must leave version 2 and the complete old coverage.
	drop := addPrecisionUpgradeSQLFailure(t, f.group, now.Add(time.Minute))
	failedSeed := runPrecisionUpgradeTick(t, repo, 1)[0]
	require.ErrorContains(t, failedSeed.err, "precision_upgrade_failure")
	wm, err := repo.GetAggregationWatermark(ctx)
	require.NoError(t, err)
	require.Equal(t, 2, wm.AggregationVersion)
	require.True(t, wm.BackfillCursor.Equal(source))
	require.True(t, wm.AvailabilityCoverageStart.Equal(source))
	assertPrecisionUpgradeOverview(t, repo, f, windows[0], windows[0].Add(5*time.Minute), 5*time.Minute, 8000, .75)
	drop()

	seed := runPrecisionUpgradeTick(t, repo, 1)[0]
	require.NoError(t, seed.err)
	require.Equal(t, 2*time.Hour, seed.end.Sub(seed.start), "an existing version 2 installation reseeds the default window automatically")
	wm, err = repo.GetAggregationWatermark(ctx)
	require.NoError(t, err)
	require.Equal(t, service.ChannelMonitorV2AggregationVersion, wm.AggregationVersion)
	require.True(t, wm.BackfillCursor.Equal(seed.start), "precision cursor restarts even though availability already covers older history")
	require.True(t, wm.AvailabilityCoverageStart.Equal(source))
	assertPrecisionUpgradeOverview(t, repo, f, windows[0], windows[0].Add(5*time.Minute), 5*time.Minute, 7432, .75)
	assertPrecisionUpgradeHistogram(t, f.group, windows[0], windows[0].Add(5*time.Minute), 0, map[int64]int64{7432: 3})
	assertPrecisionUpgradeHistogram(t, f.group, windows[0], windows[0].Add(5*time.Minute), 300, map[int64]int64{7432: 3})
	assertPrecisionUpgradeOverview(t, repo, f, windows[1], windows[1].Add(5*time.Minute), 5*time.Minute, 8000, .75)

	// A new process resumes with its 10m overlap, then the durable history cursor.
	drop = addPrecisionUpgradeSQLFailure(t, f.group, seed.start)
	failed := runPrecisionUpgradeTick(t, repo, 2)
	require.NoError(t, failed[0].err)
	require.Equal(t, 10*time.Minute, failed[0].end.Sub(failed[0].start))
	require.ErrorContains(t, failed[1].err, "precision_upgrade_failure")
	require.True(t, failed[1].end.Equal(seed.start))
	wm, err = repo.GetAggregationWatermark(ctx)
	require.NoError(t, err)
	require.True(t, wm.BackfillCursor.Equal(seed.start), "failed SQL rolls back facts and cursor together")
	require.True(t, wm.AvailabilityCoverageStart.Equal(source))
	assertPrecisionUpgradeOverview(t, repo, f, windows[1], windows[1].Add(5*time.Minute), 5*time.Minute, 8000, .75)
	drop()

	resumed := runPrecisionUpgradeTick(t, repo, 2)
	for _, call := range resumed {
		require.NoError(t, call.err)
	}
	require.Equal(t, 10*time.Minute, resumed[0].end.Sub(resumed[0].start), "restart must not repeat the 2h seed")
	require.True(t, resumed[1].end.Equal(seed.start))
	require.Equal(t, time.Hour, resumed[1].end.Sub(resumed[1].start))
	for range 2 {
		require.NoError(t, repo.RecomputeRange(ctx, resumed[1].start, resumed[1].end))
	}
	wm, err = repo.GetAggregationWatermark(ctx)
	require.NoError(t, err)
	require.True(t, wm.BackfillCursor.Equal(resumed[1].start))
	require.True(t, wm.AvailabilityCoverageStart.Equal(source))
	require.True(t, wm.UsageCoverageStart.Equal(source))
	require.True(t, wm.ErrorCoverageStart.Equal(source))
	assertPrecisionUpgradeOverview(t, repo, f, windows[1], windows[1].Add(5*time.Minute), 5*time.Minute, 7432, .75)
	assertPrecisionUpgradeHistogram(t, f.group, windows[1], windows[1].Add(5*time.Minute), 300, map[int64]int64{7432: 3})
	assertPrecisionUpgradeOverview(t, repo, f, windows[2], windows[2].Add(5*time.Minute), 5*time.Minute, 8000, .75)
}

func TestChannelMonitorV2PrecisionUpgradeHistoricalRollupsIntegration(t *testing.T) {
	preservePrecisionUpgradeWatermark(t)
	isolatePrecisionUpgradeSources(t)
	ctx := context.Background()
	repo := &channelMonitorV2Repository{db: integrationDB}
	f := newMonitorOverviewFixture(t, "openai")
	now := time.Now().UTC().Truncate(time.Minute)
	day := now.Truncate(24 * time.Hour).Add(-8 * 24 * time.Hour)
	end := day.Add(24 * time.Hour)
	setPrecisionUpgradeWatermark(t, service.ChannelMonitorV2AggregationVersion, day, day, now)
	// A successful-only installation has no ops error rows. Its retained usage
	// still rebuilds historical TTFT instead of being mistaken for empty history.
	for _, hour := range []int{0, 13, 19} {
		f.at = day.Add(time.Duration(hour) * time.Hour)
		insertLatencyPrecisionUsage(t, f, "precision-upgrade-history", 7432, 9000)
	}
	// Exact whole-day ranges must build daily rollups; no extra minute is needed.
	require.NoError(t, repo.RecomputeRange(ctx, day, end))
	makePrecisionUpgradeLegacy(t, f.group)
	setPrecisionUpgradeWatermark(t, 2, day, day, now)
	for _, table := range []string{"channel_monitor_v2_metrics_1m", "channel_monitor_v2_user_metrics_1m", "channel_monitor_v2_latency_histograms_1m", "channel_monitor_v2_error_metrics_1m"} {
		_, err := integrationDB.Exec(`DELETE FROM `+table+` WHERE group_id=$1`, f.group)
		require.NoError(t, err)
	}
	assertPrecisionUpgradeHistogram(t, f.group, day, end, 86400, map[int64]int64{8000: 3})
	assertPrecisionUpgradeOverview(t, repo, f, day, end, 24*time.Hour, 8000, 1)

	seedStart := now.Add(-2 * time.Hour)
	require.NoError(t, repo.RecomputeRange(ctx, seedStart, now))
	assertPrecisionUpgradeHistogram(t, f.group, day, end, 86400, map[int64]int64{8000: 3})
	// Advance through the empty gap, then rebuild this old day in small chunks.
	require.NoError(t, repo.RecomputeRange(ctx, end, seedStart))
	for _, chunk := range []struct {
		startHour, endHour int
		fineSamples        int64
	}{{18, 24, 1}, {12, 18, 2}, {6, 12, 2}} {
		require.NoError(t, repo.RecomputeRange(ctx, day.Add(time.Duration(chunk.startHour)*time.Hour), day.Add(time.Duration(chunk.endHour)*time.Hour)))
		assertPrecisionUpgradeHistogram(t, f.group, day, end, 86400, map[int64]int64{8000: 3})
		assertPrecisionUpgradeOverview(t, repo, f, day, end, 24*time.Hour, 8000, 1)
		wm, err := repo.GetAggregationWatermark(ctx)
		require.NoError(t, err)
		require.True(t, wm.AvailabilityCoverageStart.Equal(day))
		require.True(t, wm.BackfillCursor.Equal(day.Add(time.Duration(chunk.startHour)*time.Hour)))
		// Old minute facts must survive until the entire historical pass finishes.
		assertPrecisionUpgradeHistogram(t, f.group, day.Add(12*time.Hour), end, 0,
			map[int64]int64{7432: chunk.fineSamples})
	}
	// The final chunk completes the day; keep the samples from earlier chunks.
	require.NoError(t, repo.RecomputeRange(ctx, day, day.Add(6*time.Hour)))
	assertPrecisionUpgradeHistogram(t, f.group, day, end, 43200, map[int64]int64{7432: 3})
	assertPrecisionUpgradeHistogram(t, f.group, day, end, 86400, map[int64]int64{7432: 3})
	assertPrecisionUpgradeOverview(t, repo, f, day, end, 24*time.Hour, 7432, 1)
	for range 2 {
		require.NoError(t, (&channelMonitorV2Repository{db: integrationDB}).RecomputeRange(ctx, day, end))
		assertPrecisionUpgradeHistogram(t, f.group, day, end, 86400, map[int64]int64{7432: 3})
	}
	wm, err := repo.GetAggregationWatermark(ctx)
	require.NoError(t, err)
	require.True(t, wm.BackfillCursor.Equal(day))
	require.True(t, wm.AvailabilityCoverageStart.Equal(day))
}

func TestChannelMonitorV2PrecisionUpgradePartialSourceDayIntegration(t *testing.T) {
	preservePrecisionUpgradeWatermark(t)
	isolatePrecisionUpgradeSources(t)
	ctx := context.Background()
	repo := &channelMonitorV2Repository{db: integrationDB}
	f := newMonitorOverviewFixture(t, "openai")
	now := time.Now().UTC().Truncate(time.Minute)
	day := now.Truncate(24 * time.Hour).Add(-8 * 24 * time.Hour)
	end := day.Add(24 * time.Hour)
	setPrecisionUpgradeWatermark(t, service.ChannelMonitorV2AggregationVersion, day, day, now)
	for _, at := range []time.Time{day.Add(10 * time.Minute), day.Add(30 * time.Minute), day.Add(13 * time.Hour)} {
		f.at = at
		insertLatencyPrecisionUsage(t, f, "precision-partial-source", 7432, 9000)
	}
	require.NoError(t, repo.RecomputeRange(ctx, day, end))
	makePrecisionUpgradeLegacy(t, f.group)
	// The raw source now starts partway through the old daily bucket. Replaying
	// all available source must retain that old complete daily histogram.
	source := day.Add(30 * time.Minute)
	f.at = source
	f.failure(t, f.key, "precision-partial-source", uuid.NewString(), "", "provider", "upstream", "upstream_error", "recovered", 200, 0)
	_, err := integrationDB.Exec(`DELETE FROM usage_logs WHERE group_id=$1 AND created_at < $2`, f.group, source)
	require.NoError(t, err)
	setPrecisionUpgradeWatermark(t, 2, day, day, now)
	wm, err := repo.GetAggregationWatermark(ctx)
	require.NoError(t, err)
	require.True(t, wm.AvailabilitySourceStart.Equal(source), "the version upgrade detects the retained raw source floor")
	require.NoError(t, repo.RecomputeRange(ctx, now.Add(-2*time.Hour), now))
	require.NoError(t, repo.RecomputeRange(ctx, source, now.Add(-2*time.Hour)))
	assertPrecisionUpgradeHistogram(t, f.group, day, end, 86400, map[int64]int64{8000: 3})
	assertPrecisionUpgradeHistogram(t, f.group, day.Add(12*time.Hour), end, 43200, map[int64]int64{7432: 1})
	var success, version int64
	require.NoError(t, integrationDB.QueryRow(`SELECT SUM(success_requests),MIN(aggregation_version) FROM channel_monitor_v2_metrics_rollup WHERE group_id=$1 AND bucket_seconds=86400 AND bucket_start=$2`, f.group, day).Scan(&success, &version))
	require.EqualValues(t, 3, success)
	require.EqualValues(t, 2, version, "an incomplete source cannot replace the existing complete version 2 day")
}

func TestChannelMonitorV2PrecisionUpgradeEmptySourcesIntegration(t *testing.T) {
	preservePrecisionUpgradeWatermark(t)
	isolatePrecisionUpgradeSources(t)
	ctx := context.Background()
	repo := &channelMonitorV2Repository{db: integrationDB}
	f := newMonitorOverviewFixture(t, "openai")
	now := time.Now().UTC().Truncate(time.Minute)
	day := now.Truncate(24 * time.Hour).Add(-8 * 24 * time.Hour)
	end := day.Add(24 * time.Hour)
	setPrecisionUpgradeWatermark(t, service.ChannelMonitorV2AggregationVersion, day, day, now)
	f.at = day
	insertLatencyPrecisionUsage(t, f, "precision-empty-source", 7432, 9000)
	require.NoError(t, repo.RecomputeRange(ctx, day, end))
	makePrecisionUpgradeLegacy(t, f.group)
	setPrecisionUpgradeWatermark(t, 2, day, day, now)
	_, err := integrationDB.Exec(`DELETE FROM usage_logs`)
	require.NoError(t, err)
	before, err := repo.GetAggregationWatermark(ctx)
	require.NoError(t, err)
	require.False(t, before.AvailabilitySourceStart.Before(now))

	seed := runPrecisionUpgradeTick(t, repo, 1)[0]
	require.NoError(t, seed.err)
	wm, err := repo.GetAggregationWatermark(ctx)
	require.NoError(t, err)
	require.Equal(t, service.ChannelMonitorV2AggregationVersion, wm.AggregationVersion)
	require.False(t, wm.BackfillCursor.Before(now), "empty raw sources finish the precision upgrade without endlessly reseeding")
	require.True(t, wm.DataThrough.Equal(before.DataThrough))
	require.True(t, wm.LastSuccessfulAt.Equal(before.LastSuccessfulAt))
	require.True(t, wm.AvailabilityCoverageStart.Equal(day))
	assertPrecisionUpgradeHistogram(t, f.group, day, end, 86400, map[int64]int64{8000: 1})
	assertPrecisionUpgradeOverview(t, repo, f, day, end, 24*time.Hour, 8000, 1)
}
