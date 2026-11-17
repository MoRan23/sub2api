//go:build unit

package repository

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func expectRollupBuilderBusy(mock sqlmock.Sqlmock) {
	for _, id := range []int64{1, 2} {
		mock.ExpectQuery(`SELECT pg_try_advisory_lock`).WithArgs(id).WillReturnRows(sqlmock.NewRows([]string{"locked"}).AddRow(false))
	}
}

func expectRollupBuilderStart(mock sqlmock.Sqlmock, kind string, id int64) {
	mock.ExpectQuery(`SELECT pg_try_advisory_lock`).WithArgs(id).WillReturnRows(sqlmock.NewRows([]string{"locked"}).AddRow(true))
	mock.ExpectExec(`DELETE FROM usage_` + kind + `_rollup_stage`).WillReturnResult(sqlmock.NewResult(0, 0))
}

func expectRollupBuilderUnlock(mock sqlmock.Sqlmock, id int64) {
	mock.ExpectExec(`SELECT pg_advisory_unlock`).WithArgs(id).WillReturnResult(sqlmock.NewResult(0, 1))
}

func TestDashboardAggregationRepositorySyncGroupUsageRollupsNoopsAtCurrentDate(t *testing.T) {
	setGroupUsageRollupTestTimezone(t)
	db, mock := newSQLMock(t)
	today := service.GroupUsageTodayStart(time.Date(2026, 8, 14, 8, 0, 0, 0, time.UTC))
	for i, kind := range []string{"group", "subject"} {
		expectRollupBuilderStart(mock, kind, int64(i+1))
		mock.ExpectBegin()
		mock.ExpectQuery(`SELECT closed_before::text,timezone_name,retained_from,expired_before`).WillReturnRows(sqlmock.NewRows([]string{"closed", "tz", "retained", "expired"}).AddRow("2026-08-14", "Asia/Shanghai", today, time.Unix(0, 0).UTC()))
		mock.ExpectQuery(`SELECT MIN`).WillReturnRows(sqlmock.NewRows([]string{"min"}).AddRow(today))
		mock.ExpectExec(`UPDATE usage_` + kind + `_rollup_state`).WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectCommit()
		mock.ExpectExec(`DELETE FROM usage_` + kind + `_daily_rollups`).WillReturnResult(sqlmock.NewResult(0, 0))
		expectRollupBuilderUnlock(mock, int64(i+1))
	}
	require.NoError(t, newDashboardAggregationRepositoryWithSQL(db).SyncGroupUsageRollups(context.Background(), today))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUsageRollupBuildRunsAfterPrepareCommit(t *testing.T) {
	setGroupUsageRollupTestTimezone(t)
	db, mock := newSQLMock(t)
	today := service.GroupUsageTodayStart(time.Date(2026, 8, 14, 8, 0, 0, 0, time.UTC))
	expectRollupBuilderStart(mock, "group", 1)
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT closed_before::text,timezone_name,retained_from,expired_before`).WillReturnRows(sqlmock.NewRows([]string{"closed", "tz", "retained", "expired"}).AddRow("2026-08-13", "Asia/Shanghai", today.AddDate(0, 0, -1), time.Unix(0, 0).UTC()))
	mock.ExpectQuery(`SELECT MIN`).WillReturnRows(sqlmock.NewRows([]string{"min"}).AddRow(today.AddDate(0, 0, -1)))
	mock.ExpectQuery(`UPDATE usage_group_rollup_state.*RETURNING revision`).WillReturnRows(sqlmock.NewRows([]string{"revision"}).AddRow(42))
	mock.ExpectCommit()
	buildErr := errors.New("build interrupted")
	mock.ExpectExec(`INSERT INTO usage_group_rollup_stage`).WillReturnError(buildErr)
	expectRollupBuilderUnlock(mock, 1)
	require.ErrorIs(t, syncUsageDailyRollups(context.Background(), db, "group", today), buildErr)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUsageRollupRejectsStalePublication(t *testing.T) {
	setGroupUsageRollupTestTimezone(t)
	db, mock := newSQLMock(t)
	today := service.GroupUsageTodayStart(time.Date(2026, 8, 14, 8, 0, 0, 0, time.UTC))
	expectRollupBuilderStart(mock, "group", 1)
	for range 3 {
		mock.ExpectBegin()
		mock.ExpectQuery(`SELECT closed_before::text,timezone_name,retained_from,expired_before`).WillReturnRows(sqlmock.NewRows([]string{"closed", "tz", "retained", "expired"}).AddRow("2026-08-13", "Asia/Shanghai", today.AddDate(0, 0, -1), time.Unix(0, 0).UTC()))
		mock.ExpectQuery(`SELECT MIN`).WillReturnRows(sqlmock.NewRows([]string{"min"}).AddRow(today.AddDate(0, 0, -1)))
		mock.ExpectQuery(`UPDATE usage_group_rollup_state.*RETURNING revision`).WillReturnRows(sqlmock.NewRows([]string{"revision"}).AddRow(42))
		mock.ExpectCommit()
		mock.ExpectExec(`INSERT INTO usage_group_rollup_stage`).WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectBegin()
		mock.ExpectQuery(`SELECT revision,build_token::text`).WillReturnRows(sqlmock.NewRows([]string{"revision", "token", "closed", "tz"}).AddRow(43, nil, "2026-08-13", "Asia/Shanghai"))
		mock.ExpectRollback()
		mock.ExpectExec(`DELETE FROM usage_group_rollup_stage WHERE`).WillReturnResult(sqlmock.NewResult(0, 1))
	}
	expectRollupBuilderUnlock(mock, 1)
	require.ErrorContains(t, syncUsageDailyRollups(context.Background(), db, "group", today), "changed during build")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDashboardAggregationRepositoryRecomputeRangeBuildFailureRollsBackWithoutLockingRollups(t *testing.T) {
	db, mock := newSQLMock(t)
	mock.ExpectExec(`SELECT invalidate_group_usage_rollups_at`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectBegin()
	mock.ExpectExec(`DELETE FROM usage_dashboard_hourly`).WillReturnError(sql.ErrConnDone)
	mock.ExpectRollback()
	start := time.Now().Add(-time.Hour)
	require.ErrorIs(t, newDashboardAggregationRepositoryWithSQL(db).RecomputeRange(context.Background(), start, time.Now()), sql.ErrConnDone)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDashboardAggregationRepositoryRecomputeRangeInvalidatesBeforeTransactionWithoutSynchronousBuild(t *testing.T) {
	db, mock := newSQLMock(t)
	mock.ExpectExec(`SELECT invalidate_group_usage_rollups_at`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectBegin()
	for _, query := range []string{`DELETE FROM usage_dashboard_hourly WHERE`, `DELETE FROM usage_dashboard_hourly_users WHERE`, `DELETE FROM usage_dashboard_daily WHERE`, `DELETE FROM usage_dashboard_daily_users WHERE`, `INSERT INTO usage_dashboard_hourly_users`, `INSERT INTO usage_dashboard_daily_users`, `INSERT INTO usage_dashboard_hourly`, `INSERT INTO usage_dashboard_daily`} {
		mock.ExpectExec(query).WillReturnResult(sqlmock.NewResult(0, 1))
	}
	mock.ExpectCommit()
	start := time.Now().Add(-time.Hour)
	require.NoError(t, newDashboardAggregationRepositoryWithSQL(db).RecomputeRange(context.Background(), start, time.Now()))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDashboardAggregationRepositoryCleanupUsageLogsNonPartitionedExpiresOnceAndSyncs(t *testing.T) {
	setGroupUsageRollupTestTimezone(t)
	db, mock := newSQLMock(t)
	repo := newDashboardAggregationRepositoryWithSQL(db)
	cutoff := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	fixedNow := time.Date(2026, 8, 14, 8, 0, 0, 0, time.UTC)
	repo.clock = func() time.Time { return fixedNow }

	mock.ExpectExec(`SELECT expire_usage_rollups_before`).WithArgs(cutoff, "Asia/Shanghai").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT EXISTS`).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectExec(`(?s)SELECT tableoid, ctid.*ORDER BY created_at ASC, id ASC.*DELETE FROM usage_logs`).
		WithArgs(cutoff, usageLogsCleanupBatchSize).
		WillReturnResult(sqlmock.NewResult(0, 2))
	expectRollupBuilderBusy(mock)

	require.NoError(t, repo.CleanupUsageLogs(context.Background(), cutoff))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDashboardAggregationRepositoryCleanupUsageLogsPartitionedSortsAndUsesExactCutoff(t *testing.T) {
	setGroupUsageRollupTestTimezone(t)
	db, mock := newSQLMock(t)
	repo := newDashboardAggregationRepositoryWithSQL(db)
	cutoff := time.Date(2026, 7, 18, 0, 0, 0, 0, time.UTC)
	fixedNow := time.Date(2026, 8, 14, 8, 0, 0, 0, time.UTC)
	repo.clock = func() time.Time { return fixedNow }

	mock.ExpectExec(`SELECT expire_usage_rollups_before`).WithArgs(cutoff, "Asia/Shanghai").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT EXISTS`).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectQuery(`SELECT c.relname`).
		WillReturnRows(sqlmock.NewRows([]string{"relname"}).
			AddRow("usage_logs_202606").
			AddRow("usage_logs_invalid").
			AddRow("usage_logs_202604").
			AddRow("usage_logs_202607"))

	for _, partition := range []string{"usage_logs_202604", "usage_logs_202606"} {
		mock.ExpectBegin()
		mock.ExpectExec(`SET LOCAL lock_timeout`).WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectQuery(`(?s)SELECT expired_before.*FOR KEY SHARE`).WillReturnRows(sqlmock.NewRows([]string{"expired"}).AddRow(cutoff))
		mock.ExpectQuery(`SELECT MIN\(created_at\) FROM "` + partition + `" WHERE created_at >=`).WithArgs(cutoff).WillReturnRows(sqlmock.NewRows([]string{"min"}).AddRow(nil))
		mock.ExpectExec(`DROP TABLE IF EXISTS "` + partition + `"`).
			WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectCommit()
	}
	// The boundary partition is pruned by exact timestamp. tableoid is required
	// because ctid alone can also identify a retained row in another partition.
	mock.ExpectExec(`(?s)SELECT tableoid, ctid.*WHERE created_at < \$1.*WHERE \(tableoid, ctid\) IN`).
		WithArgs(cutoff, usageLogsCleanupBatchSize).
		WillReturnResult(sqlmock.NewResult(0, 1))
	expectRollupBuilderBusy(mock)

	require.NoError(t, repo.CleanupUsageLogs(context.Background(), cutoff))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDashboardAggregationRepositoryCleanupUsageLogsNonPartitionedFailureStopsWithoutSync(t *testing.T) {
	setGroupUsageRollupTestTimezone(t)
	db, mock := newSQLMock(t)
	repo := newDashboardAggregationRepositoryWithSQL(db)
	cutoff := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	mock.ExpectExec(`SELECT expire_usage_rollups_before`).WithArgs(cutoff, "Asia/Shanghai").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT EXISTS`).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectExec(`(?s)SELECT tableoid, ctid.*ORDER BY created_at ASC, id ASC.*DELETE FROM usage_logs`).
		WithArgs(cutoff, usageLogsCleanupBatchSize).
		WillReturnError(sql.ErrConnDone)

	err := repo.CleanupUsageLogs(context.Background(), cutoff)
	require.ErrorIs(t, err, sql.ErrConnDone)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDashboardAggregationRepositoryCleanupUsageLogsPartitionFailureRollsBackAndStops(t *testing.T) {
	setGroupUsageRollupTestTimezone(t)
	db, mock := newSQLMock(t)
	repo := newDashboardAggregationRepositoryWithSQL(db)
	cutoff := time.Date(2026, 7, 18, 0, 0, 0, 0, time.UTC)
	dropErr := errors.New("drop partition failed")

	mock.ExpectExec(`SELECT expire_usage_rollups_before`).WithArgs(cutoff, "Asia/Shanghai").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT EXISTS`).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectQuery(`SELECT c.relname`).
		WillReturnRows(sqlmock.NewRows([]string{"relname"}).
			AddRow("usage_logs_202606").
			AddRow("usage_logs_202604"))
	mock.ExpectBegin()
	mock.ExpectExec(`SET LOCAL lock_timeout`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`(?s)SELECT expired_before.*FOR KEY SHARE`).WillReturnRows(sqlmock.NewRows([]string{"expired"}).AddRow(cutoff))
	mock.ExpectQuery(`SELECT MIN\(created_at\) FROM "usage_logs_202604"`).WithArgs(cutoff).WillReturnRows(sqlmock.NewRows([]string{"min"}).AddRow(nil))
	mock.ExpectExec(`DROP TABLE IF EXISTS "usage_logs_202604"`).
		WillReturnError(dropErr)
	mock.ExpectRollback()

	err := repo.CleanupUsageLogs(context.Background(), cutoff)
	require.ErrorIs(t, err, dropErr)
	require.NoError(t, mock.ExpectationsWereMet())
}

func setGroupUsageRollupTestTimezone(t *testing.T) {
	t.Helper()
	useGroupUsageRepositoryTestTimezone(t, "Asia/Shanghai")
}
