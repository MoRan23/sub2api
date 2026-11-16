//go:build unit

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestSubjectDashboardRejectsUnreadyHistoryWithoutRawFallback(t *testing.T) {
	setGroupUsageRollupTestTimezone(t)
	for _, state := range []struct{ name, date, tz string }{
		{"uninitialized", "1970-01-01", "Asia/Shanghai"},
		{"wrong timezone", service.GroupUsageDate(time.Now()), "UTC"},
		{"future", service.GroupUsageDate(time.Now().AddDate(0, 0, 1)), "Asia/Shanghai"},
	} {
		t.Run(state.name, func(t *testing.T) {
			db, mock := newSQLMock(t)
			mock.ExpectBegin()
			mock.ExpectQuery(`SELECT closed_before::text,retained_from,timezone_name FROM usage_subject_rollup_state`).WillReturnRows(sqlmock.NewRows([]string{"closed", "retained", "tz"}).AddRow(state.date, time.Unix(0, 0), state.tz))
			mock.ExpectRollback()
			_, err := newUsageLogRepositoryWithSQL(nil, db).GetUserDashboardStats(context.Background(), 1)
			require.ErrorIs(t, err, service.ErrUsageStatsInitializing)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestSubjectRollupTailUsesCalendarDaysAcrossDST(t *testing.T) {
	useGroupUsageRepositoryTestTimezone(t, "America/New_York")
	for _, tc := range []struct{ today, yesterday, tooOld string }{
		{"2026-03-09", "2026-03-08", "2026-03-07"},
		{"2026-11-02", "2026-11-01", "2026-10-31"},
	} {
		today, err := service.ParseGroupUsageDate(tc.today)
		require.NoError(t, err)
		_, err = subjectRollupTailStart(tc.yesterday, "America/New_York", today.UTC())
		require.NoError(t, err)
		_, err = subjectRollupTailStart(tc.tooOld, "America/New_York", today.UTC())
		require.ErrorIs(t, err, service.ErrUsageStatsInitializing)
	}
}

func TestSubjectDashboardMissingStateIsInitializing(t *testing.T) {
	db, mock := newSQLMock(t)
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT closed_before::text,retained_from,timezone_name FROM usage_subject_rollup_state`).WillReturnRows(sqlmock.NewRows([]string{"closed", "retained", "tz"}))
	mock.ExpectRollback()
	_, err := newUsageLogRepositoryWithSQL(nil, db).GetAPIKeyDashboardStats(context.Background(), 1)
	require.ErrorIs(t, err, service.ErrUsageStatsInitializing)
	require.NoError(t, mock.ExpectationsWereMet())
}
