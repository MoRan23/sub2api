package repository

import (
	"context"
	"errors"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

func TestChannelMonitorV2GroupAttributionBatchAndSafeReasons(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	now := time.Now()
	rows := sqlmock.NewRows([]string{"group_id", "finished_at", "status", "probe_model"}).
		AddRow(1, now, "passed", "model with space").AddRow(1, now.Add(-time.Minute), "mismatch", "gpt-6").
		AddRow(2, now, "abnormal", "gpt-6").AddRow(2, now.Add(-time.Minute), "cancelled", "gpt-6").AddRow(2, now.Add(-2*time.Minute), "skipped", "gpt-6")
	mock.ExpectQuery(regexp.QuoteMeta(channelMonitorV2GroupAttributionSQL)).WithArgs(pq.Array([]int64{1, 2}), sqlmock.AnyArg()).WillReturnRows(rows)
	r := &channelMonitorV2Repository{db: db}
	result, err := r.GetGroupAttributionHistory(context.Background(), []int64{1, 2})
	require.NoError(t, err)
	require.Len(t, result[1], 2)
	require.Len(t, result[2], 3)
	require.Equal(t, "model with space", result[1][0].ProbeModel)
	require.Equal(t, "reference_match", result[1][0].ReasonCode)
	require.Equal(t, "reference_mismatch", result[1][1].ReasonCode)
	require.Equal(t, "abnormal", result[2][0].Status)
	require.Equal(t, "analysis_abnormal", result[2][0].ReasonCode)
	require.Equal(t, "cancelled", result[2][1].ReasonCode)
	require.Equal(t, "skipped", result[2][2].ReasonCode)
	require.NoError(t, mock.ExpectationsWereMet())
	// The SQL projects a scalar model, never the complete private snapshot/result.
	require.Contains(t, channelMonitorV2GroupAttributionSQL, "LIMIT 20")
	require.Contains(t, channelMonitorV2GroupAttributionSQL, "rank <= 20")
	require.NotContains(t, channelMonitorV2GroupAttributionSQL, "j.reason")
	require.NotContains(t, channelMonitorV2GroupAttributionSQL, "j.result")
}

func TestChannelMonitorV2GroupAttributionEmptyScope(t *testing.T) {
	r := &channelMonitorV2Repository{}
	got, err := r.GetGroupAttributionHistory(context.Background(), nil)
	require.NoError(t, err)
	require.Empty(t, got)
}

func TestChannelMonitorV2GroupAttributionReadErrors(t *testing.T) {
	for _, kind := range []string{"query", "scan", "iterate"} {
		t.Run(kind, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			defer db.Close()
			expect := mock.ExpectQuery(regexp.QuoteMeta(channelMonitorV2GroupAttributionSQL))
			switch kind {
			case "query":
				expect.WillReturnError(context.DeadlineExceeded)
			case "scan":
				expect.WillReturnRows(sqlmock.NewRows([]string{"group_id", "finished_at", "status", "probe_model"}).AddRow(1, "invalid-time", "passed", "model"))
			case "iterate":
				expect.WillReturnRows(sqlmock.NewRows([]string{"group_id", "finished_at", "status", "probe_model"}).AddRow(1, time.Now(), "passed", "model").RowError(0, errors.New("row failed")))
			}
			r := &channelMonitorV2Repository{db: db}
			_, err = r.GetGroupAttributionHistory(context.Background(), []int64{1})
			require.Error(t, err)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestChannelMonitorV2GroupAttributionOverviewBatchProjection(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	asOf := time.Date(2026, 11, 14, 12, 34, 56, 0, time.UTC)
	ids := []int64{1, 2, 3}
	rows := sqlmock.NewRows([]string{"group_id", "enabled", "sample", "state"})
	for _, id := range ids {
		for sample := 1; sample <= 20; sample++ {
			state := "no_data"
			if id == 1 {
				state = "passed"
				if sample > 10 {
					state = "failed"
				}
			}
			rows.AddRow(id, id == 1, sample, state)
		}
	}
	mock.ExpectQuery(regexp.QuoteMeta(channelMonitorV2GroupAttributionOverviewSQL)).WithArgs(pq.Array(ids), asOf).WillReturnRows(rows).RowsWillBeClosed()
	mock.ExpectQuery(regexp.QuoteMeta(channelMonitorV2GroupAttributionSQL)).WithArgs(pq.Array(ids), asOf).
		WillReturnRows(sqlmock.NewRows([]string{"group_id", "finished_at", "status", "probe_model"}).AddRow(1, asOf, "cancelled", "model"))
	r := &channelMonitorV2Repository{db: db}
	got, err := r.GetGroupAttributionOverview(context.Background(), ids, asOf)
	require.NoError(t, err)
	require.Len(t, got, 3)
	for _, id := range ids {
		attr := got[id]
		require.Equal(t, asOf, attr.AsOf)
		require.Len(t, attr.History, 20)
		require.Equal(t, asOf.Add(-200*time.Minute), attr.History[0].BucketStart)
		require.Equal(t, asOf, attr.History[19].BucketEnd)
		require.Equal(t, attr.History[19].State, attr.SummaryState)
	}
	require.True(t, got[1].Enabled)
	require.Equal(t, "passed", got[1].History[9].State)
	require.Equal(t, "failed", got[1].SummaryState)
	require.Equal(t, "cancelled", got[1].Latest.Status, "legacy display rows never override the summary")
	require.False(t, got[2].Enabled)
	require.Equal(t, "no_data", got[2].SummaryState)
	require.NotNil(t, got[2].Recent)
	require.Nil(t, got[2].Latest)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestChannelMonitorV2GroupAttributionOverviewEmptyScope(t *testing.T) {
	r := &channelMonitorV2Repository{}
	got, err := r.GetGroupAttributionOverview(context.Background(), nil, time.Now())
	require.NoError(t, err)
	require.Empty(t, got)
}

func TestChannelMonitorV2GroupAttributionOverviewReadErrors(t *testing.T) {
	for _, kind := range []string{"query", "scan", "iterate", "sample", "group", "legacy"} {
		t.Run(kind, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			defer db.Close()
			expect := mock.ExpectQuery(regexp.QuoteMeta(channelMonitorV2GroupAttributionOverviewSQL))
			rows := sqlmock.NewRows([]string{"group_id", "enabled", "sample", "state"})
			switch kind {
			case "query":
				expect.WillReturnError(context.DeadlineExceeded)
			case "scan":
				expect.WillReturnRows(rows.AddRow(1, "invalid-bool", 1, "passed"))
			case "iterate":
				expect.WillReturnRows(rows.AddRow(1, true, 1, "passed").RowError(0, errors.New("row failed")))
			case "sample":
				expect.WillReturnRows(rows.AddRow(1, true, 21, "passed"))
			case "group":
				expect.WillReturnRows(rows.AddRow(99, true, 1, "passed"))
			case "legacy":
				expect.WillReturnRows(rows)
				mock.ExpectQuery(regexp.QuoteMeta(channelMonitorV2GroupAttributionSQL)).WillReturnError(context.DeadlineExceeded)
			}
			r := &channelMonitorV2Repository{db: db}
			_, err = r.GetGroupAttributionOverview(context.Background(), []int64{1}, time.Now())
			require.Error(t, err)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestChannelMonitorV2GroupAttributionQueriesLimitMembershipAndPrivateData(t *testing.T) {
	for _, query := range []string{channelMonitorV2GroupAttributionSQL, channelMonitorV2GroupAttributionOverviewSQL} {
		require.Contains(t, query, "a.deleted_at IS NULL AND a.schedulable = TRUE")
		for _, forbidden := range []string{"a.credentials", "j.result", "j.reason", "rate_limit_reset_at", "a.status"} {
			require.NotContains(t, query, forbidden)
		}
	}
	require.NotContains(t, channelMonitorV2GroupAttributionOverviewSQL, "LIMIT 20")
	require.NotContains(t, channelMonitorV2GroupAttributionOverviewSQL, "j.snapshot")
	require.Contains(t, channelMonitorV2GroupAttributionOverviewSQL, "SELECT DISTINCT account_id, type FROM members")
}
