//go:build integration

package repository

import (
	"context"
	"database/sql"
	"net/url"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/Wei-Shaw/sub2api/migrations"
	"github.com/stretchr/testify/require"
)

func newSubjectRollupTestDB(t *testing.T) *sql.DB {
	t.Helper()
	ctx := context.Background()
	schema := createGroupUsageRollupTriggerTestSchema(t, ctx, false)
	u, err := url.Parse(integrationDSN)
	require.NoError(t, err)
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	db, err := sql.Open("postgres", u.String())
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.ExecContext(ctx, `ALTER TABLE groups ADD COLUMN platform TEXT;
 CREATE TABLE accounts(id BIGINT PRIMARY KEY,platform TEXT);
 CREATE TABLE api_keys(id BIGINT PRIMARY KEY,user_id BIGINT,status TEXT,deleted_at TIMESTAMPTZ);
 ALTER TABLE usage_logs ADD COLUMN api_key_id BIGINT, ADD COLUMN account_id BIGINT,
 ADD COLUMN input_tokens BIGINT NOT NULL DEFAULT 0, ADD COLUMN output_tokens BIGINT NOT NULL DEFAULT 0,
 ADD COLUMN cache_creation_tokens BIGINT NOT NULL DEFAULT 0, ADD COLUMN cache_read_tokens BIGINT NOT NULL DEFAULT 0,
 ADD COLUMN total_cost NUMERIC(20,10) NOT NULL DEFAULT 0, ADD COLUMN duration_ms BIGINT,
 ADD COLUMN grok_safety_rejection JSONB;
 CREATE INDEX usage_created_at ON usage_logs(created_at);`)
	require.NoError(t, err)
	for _, name := range []string{"290_usage_rollup_fenced_builds.sql", "291_usage_subject_daily_rollups.sql"} {
		data, err := migrations.FS.ReadFile(name)
		require.NoError(t, err)
		for range 2 {
			_, err = db.ExecContext(ctx, string(data))
			require.NoError(t, err)
		}
	}
	return db
}

func TestSubjectDailyRollupsPreserveMetricsAndLateChanges(t *testing.T) {
	db := newSubjectRollupTestDB(t)
	ctx := context.Background()
	today := service.GroupUsageTodayStart(time.Now())
	_, err := db.ExecContext(ctx, `INSERT INTO users(id) VALUES(1); INSERT INTO groups(id,platform) VALUES(10,'composite');
 INSERT INTO accounts VALUES(20,'openai'); INSERT INTO api_keys VALUES(30,1,'active',NULL);`)
	require.NoError(t, err)
	for i, values := range []struct {
		offset   int
		cost     float64
		duration any
		rejected any
	}{
		{-3, 1.25, 100, nil}, {-2, 0, nil, nil}, {-1, 2.5, 300, `{"code":"safety_review_rejected"}`}, {0, 3.75, 500, nil},
	} {
		_, err = db.ExecContext(ctx, `INSERT INTO usage_logs(id,user_id,api_key_id,group_id,account_id,actual_cost,total_cost,input_tokens,output_tokens,cache_creation_tokens,cache_read_tokens,duration_ms,grok_safety_rejection,created_at)
	  VALUES($1,1,30,10,20,$2::numeric,$2::numeric*2,10,20,30,40,$3,$4,$5)`, i+1, values.cost, values.duration, values.rejected, today.AddDate(0, 0, values.offset).Add(time.Minute))
		require.NoError(t, err)
	}
	repo := newUsageLogRepositoryWithSQL(nil, db)
	_, err = repo.GetUserDashboardStats(ctx, 1)
	require.ErrorIs(t, err, service.ErrUsageStatsInitializing)
	require.NoError(t, newDashboardAggregationRepositoryWithSQL(db).SyncGroupUsageRollups(ctx, today))
	got, err := repo.GetUserDashboardStats(ctx, 1)
	require.NoError(t, err)
	require.EqualValues(t, 4, got.TotalRequests)
	require.EqualValues(t, 400, got.TotalTokens)
	require.InDelta(t, 7.5, got.TotalActualCost, 1e-9)
	require.InDelta(t, 300, got.AverageDurationMs, 1e-9)
	require.EqualValues(t, 1, got.TodayRequests)
	require.EqualValues(t, 1, got.TotalAPIKeys)
	require.Len(t, got.ByPlatform, 1)
	require.Equal(t, "openai", got.ByPlatform[0].Platform)
	require.EqualValues(t, 2, got.ByPlatform[0].TotalRequests)
	require.EqualValues(t, 300, got.ByPlatform[0].TotalTokens)
	key, err := repo.GetAPIKeyDashboardStats(ctx, 30)
	require.NoError(t, err)
	require.Equal(t, got.TotalActualCost, key.TotalActualCost)
	// A current metadata edit changes platform projection without rebuilding history.
	_, err = db.ExecContext(ctx, `UPDATE accounts SET platform='grok' WHERE id=20`)
	require.NoError(t, err)
	got, err = repo.GetUserDashboardStats(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, "grok", got.ByPlatform[0].Platform)
	// A historical deletion invalidates both summaries. Rebuilding removes it exactly once.
	_, err = db.ExecContext(ctx, `DELETE FROM usage_logs WHERE id=1`)
	require.NoError(t, err)
	_, err = repo.GetUserDashboardStats(ctx, 1)
	require.ErrorIs(t, err, service.ErrUsageStatsInitializing)
	require.NoError(t, newDashboardAggregationRepositoryWithSQL(db).SyncGroupUsageRollups(ctx, today))
	got, err = repo.GetUserDashboardStats(ctx, 1)
	require.NoError(t, err)
	require.EqualValues(t, 3, got.TotalRequests)
	require.InDelta(t, 6.25, got.TotalActualCost, 1e-9)
}

func TestSubjectRollupLateInsertFencesPreparedBuild(t *testing.T) {
	db := newSubjectRollupTestDB(t)
	ctx := context.Background()
	today := service.GroupUsageTodayStart(time.Now())
	_, err := db.ExecContext(ctx, `INSERT INTO users VALUES(1); INSERT INTO groups(id) VALUES(10);`)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `UPDATE usage_subject_rollup_state SET closed_before=$1::date,building_before=$2::date,timezone_name=$3,build_token='00000000-0000-0000-0000-000000000001'`, service.GroupUsageDate(today.AddDate(0, 0, -1)), service.GroupUsageDate(today), service.GroupUsageTimezoneName())
	require.NoError(t, err)
	var before, after int64
	require.NoError(t, db.QueryRowContext(ctx, `SELECT revision FROM usage_subject_rollup_state`).Scan(&before))
	_, err = db.ExecContext(ctx, `INSERT INTO usage_logs(id,user_id,group_id,actual_cost,created_at) VALUES(1,1,10,1,$1)`, today.Add(-time.Hour))
	require.NoError(t, err)
	require.NoError(t, db.QueryRowContext(ctx, `SELECT revision FROM usage_subject_rollup_state`).Scan(&after))
	require.Greater(t, after, before)
}

func TestSubjectRollupRetentionFencesUnchangedWatermark(t *testing.T) {
	db := newSubjectRollupTestDB(t)
	ctx := context.Background()
	affected := service.GroupUsageYesterdayStart(time.Now())
	date := service.GroupUsageDate(affected)
	_, err := db.ExecContext(ctx, `UPDATE usage_group_rollup_state SET closed_before=$1::date WHERE id=1`, date)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `UPDATE usage_subject_rollup_state SET closed_before=$1::date,building_before=$1::date+1,build_token='00000000-0000-0000-0000-000000000001' WHERE id=1`, date)
	require.NoError(t, err)
	var before, after int64
	require.NoError(t, db.QueryRowContext(ctx, `SELECT revision FROM usage_subject_rollup_state`).Scan(&before))
	tx, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	require.NoError(t, invalidateGroupUsageRollupsAt(ctx, tx, affected))
	require.NoError(t, tx.Commit())
	var closed string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT revision,closed_before::text FROM usage_subject_rollup_state`).Scan(&after, &closed))
	require.Equal(t, date, closed)
	require.Greater(t, after, before)
}

func TestUsageRollupHistoryScanDoesNotBlockCurrentWrites(t *testing.T) {
	db := newSubjectRollupTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	today := service.GroupUsageTodayStart(time.Now())
	_, err := db.ExecContext(ctx, `INSERT INTO users VALUES(1); INSERT INTO groups(id) VALUES(10);
 CREATE FUNCTION pause_rollup_build() RETURNS TRIGGER LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_advisory_xact_lock(290,123); RETURN NEW; END $$;
 CREATE TRIGGER pause_build BEFORE INSERT ON usage_group_rollup_stage FOR EACH ROW EXECUTE FUNCTION pause_rollup_build();`)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO usage_logs(id,user_id,group_id,actual_cost,created_at) VALUES(1,1,10,1,$1)`, today.Add(-time.Hour))
	require.NoError(t, err)
	blocker, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer blocker.Rollback()
	_, err = blocker.ExecContext(ctx, `SELECT pg_advisory_xact_lock(290,123)`)
	require.NoError(t, err)
	done := make(chan error, 1)
	go func() { done <- syncUsageDailyRollups(ctx, db, "group", today) }()
	require.Eventually(t, func() bool {
		var waiting bool
		return db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_locks WHERE locktype='advisory' AND classid=290 AND objid=123 AND NOT granted)`).Scan(&waiting) == nil && waiting
	}, 5*time.Second, 10*time.Millisecond)
	writeCtx, stopWrite := context.WithTimeout(ctx, time.Second)
	started := time.Now()
	_, err = db.ExecContext(writeCtx, `INSERT INTO usage_logs(id,user_id,group_id,actual_cost,created_at) VALUES(2,1,10,5,$1)`, today.Add(time.Minute))
	stopWrite()
	require.NoError(t, err, "current write must not wait for the blocked history scan")
	t.Logf("current insert while historical stage build is blocked: %s", time.Since(started))
	// This commit happens after the build snapshot. Publication must reject it
	// and rebuild, preserving the newly inserted historical cost.
	_, err = db.ExecContext(ctx, `INSERT INTO usage_logs(id,user_id,group_id,actual_cost,created_at) VALUES(3,1,10,2,$1)`, today.Add(-time.Hour))
	require.NoError(t, err)
	require.NoError(t, blocker.Commit())
	require.NoError(t, <-done)
	var cost float64
	require.NoError(t, db.QueryRowContext(ctx, `SELECT SUM(actual_cost) FROM usage_group_daily_rollups WHERE group_id=10`).Scan(&cost))
	require.Equal(t, float64(3), cost)
}
