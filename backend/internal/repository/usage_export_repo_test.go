package repository

import (
	"context"
	"database/sql"
	"reflect"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

type usageExportScannerFunc func(...any) error

func (f usageExportScannerFunc) Scan(v ...any) error { return f(v...) }
func TestUsageExportCursorUsesValueBeforeDisplayTrimming(t *testing.T) {
	for _, requested := range []string{"\tgpt\t", "  gpt\t  ", "　gpt　", ""} {
		scanner := usageExportScannerFunc(func(args ...any) error {
			for _, arg := range args {
				v := reflect.ValueOf(arg).Elem()
				v.Set(reflect.Zero(v.Type()))
			}
			*args[5].(*string) = "fallback"
			*args[6].(*sql.NullString) = sql.NullString{String: requested, Valid: true}
			return nil
		})
		var cursor string
		_, err := scanUsageLog(scanner, &cursor)
		require.NoError(t, err)
		require.Equal(t, usageExportModelCursor(requested, "fallback"), cursor)
	}
}

func TestUsageExportClaimSerializesCapacityAndQuota(t *testing.T) {
	db, m, e := sqlmock.New()
	require.NoError(t, e)
	defer db.Close()
	r := &usageExportRepository{db: db}
	m.ExpectBegin()
	m.ExpectExec("pg_advisory_xact_lock").WillReturnResult(sqlmock.NewResult(0, 1))
	m.ExpectExec("UPDATE usage_export_jobs SET status='failed'").WillReturnResult(sqlmock.NewResult(0, 0))
	m.ExpectQuery("SELECT .*COUNT").WillReturnRows(sqlmock.NewRows([]string{"running", "allocated"}).AddRow(2, 0))
	m.ExpectCommit()
	j, e := r.Claim(context.Background(), "claim", 256<<20)
	require.Error(t, e)
	require.Nil(t, j)
	require.NoError(t, m.ExpectationsWereMet())
}
func TestUsageExportCreationPerUserLimit(t *testing.T) {
	db, m, e := sqlmock.New()
	require.NoError(t, e)
	defer db.Close()
	r := &usageExportRepository{db: db}
	m.ExpectBegin()
	m.ExpectExec("pg_advisory_xact_lock").WillReturnResult(sqlmock.NewResult(0, 1))
	m.ExpectQuery("SELECT .*COUNT").WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"waiting", "active"}).AddRow(0, 1))
	m.ExpectRollback()
	require.Error(t, r.Create(context.Background(), &service.UsageExportJob{Owner: service.UsageExportOwner{UserID: 7}}))
	require.NoError(t, m.ExpectationsWereMet())
}
func TestUsageExportHeartbeatCannotReviveExpiredLease(t *testing.T) {
	db, m, e := sqlmock.New()
	require.NoError(t, e)
	defer db.Close()
	r := &usageExportRepository{db: db}
	m.ExpectExec("UPDATE usage_export_jobs.*status='running' AND lease_until>NOW").WithArgs("job", "old", int64(12)).WillReturnResult(sqlmock.NewResult(0, 0))
	ok, e := r.Heartbeat(context.Background(), "job", "old", 12)
	require.NoError(t, e)
	require.False(t, ok)
	require.NoError(t, m.ExpectationsWereMet())
}
func TestUsageExportSourceKeysetAndIDBoundary(t *testing.T) {
	db, m, e := sqlmock.New()
	require.NoError(t, e)
	defer db.Close()
	r := newUsageLogRepositoryWithSQL(nil, db)
	m.ExpectQuery(`SELECT .* FROM usage_logs WHERE id <= \$1 AND \(COALESCE\(NULLIF\(TRIM\(requested_model\), ''\), model\),id\) > \(\$2,\$3\) ORDER BY .* ASC,id ASC LIMIT \$4`).WithArgs(int64(99), "gpt", int64(4), 1000).WillReturnRows(sqlmock.NewRows([]string{"id"}))
	rows, next, e := r.ReadUsageExport(context.Background(), service.UsageExportSpec{SortBy: "model", SortOrder: "asc"}, 99, &service.UsageExportCursor{ID: 4, Model: "gpt"}, 100000, true)
	require.NoError(t, e)
	require.Empty(t, rows)
	require.Nil(t, next)
	require.NoError(t, m.ExpectationsWereMet())
}
