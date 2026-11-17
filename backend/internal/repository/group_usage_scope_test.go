package repository

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

func TestGroupUsageSummaryFiltersHistoryTailAndOutput(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	repo := &usageLogRepository{sql: db}
	empty, err := repo.GetGroupUsageSummaryByIDs(context.Background(), time.Now(), nil)
	require.NoError(t, err)
	require.Empty(t, empty)
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT COUNT").WillReturnRows(sqlmock.NewRows([]string{"count", "closed", "retained", "timezone", "expired"}).AddRow(0, nil, nil, nil, nil))
	mock.ExpectQuery(`(?s)WITH historical.*rollup.group_id = ANY\(\$8::bigint\[\]\).*ul.group_id = ANY\(\$8::bigint\[\]\).*WHERE g.id = ANY\(\$8::bigint\[\]\)`).
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), false, sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), pq.Array([]int64{2, 4})).
		WillReturnRows(sqlmock.NewRows([]string{"group_id", "total", "today", "yesterday"}).AddRow(2, 12.0, 2.0, 3.0).AddRow(4, 0.0, 0.0, 0.0))
	mock.ExpectCommit()
	got, err := repo.GetGroupUsageSummaryByIDs(context.Background(), time.Now(), []int64{2, 4})
	require.NoError(t, err)
	require.Len(t, got, 2)
	require.Equal(t, int64(2), got[0].GroupID)
	require.NoError(t, mock.ExpectationsWereMet())
}
