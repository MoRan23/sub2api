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
	mock.ExpectQuery(regexp.QuoteMeta(channelMonitorV2GroupAttributionSQL)).WithArgs(pq.Array([]int64{1, 2})).WillReturnRows(rows)
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
