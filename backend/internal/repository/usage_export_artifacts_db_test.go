package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func usageExportTestDB(t *testing.T) (*usageExportRepository, *sql.DB) {
	t.Helper()
	dsn := os.Getenv("USAGE_EXPORT_TEST_DSN")
	if dsn == "" {
		t.Skip("USAGE_EXPORT_TEST_DSN is not set")
	}
	admin, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	schema := fmt.Sprintf("usage_export_test_%d", time.Now().UnixNano())
	_, err = admin.Exec("CREATE SCHEMA " + schema)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = admin.Exec("DROP SCHEMA " + schema + " CASCADE"); _ = admin.Close() })
	u, err := url.Parse(dsn)
	require.NoError(t, err)
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	db, err := sql.Open("postgres", u.String())
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.Exec("CREATE TABLE users(id BIGINT PRIMARY KEY); INSERT INTO users VALUES(1),(2),(3)")
	require.NoError(t, err)
	migration, err := os.ReadFile("../../migrations/303_usage_export_jobs.sql")
	require.NoError(t, err)
	for range 2 {
		_, err = db.Exec(string(migration))
		require.NoError(t, err)
	}
	return &usageExportRepository{db: db}, db
}
func createClaimedExport(t *testing.T, r *usageExportRepository, id, claim string) *service.UsageExportJob {
	t.Helper()
	ctx := context.Background()
	require.NoError(t, r.Create(ctx, &service.UsageExportJob{ID: id, Owner: service.UsageExportOwner{UserID: 1, Kind: "jwt", Revision: "0"}, MaxID: 10, TotalRows: 10}))
	j, err := r.Claim(ctx, claim, 256<<20)
	require.NoError(t, err)
	return j
}
func completeClaimedExport(t *testing.T, r *usageExportRepository, j *service.UsageExportJob) {
	t.Helper()
	j.Status = "completed"
	j.FileKey = j.ID + "-" + j.ClaimToken + ".xlsx"
	j.SizeBytes = 100
	ok, err := r.Finish(context.Background(), j)
	require.NoError(t, err)
	require.True(t, ok)
}
func artifactReserved(t *testing.T, db *sql.DB) int64 {
	t.Helper()
	var total int64
	require.NoError(t, db.QueryRow("SELECT COALESCE(SUM(reserved_bytes),0) FROM usage_export_artifacts").Scan(&total))
	return total
}
func TestUsageExportArtifactQuotaSurvivesLostWorker(t *testing.T) {
	r, db := usageExportTestDB(t)
	ctx := context.Background()
	first := createClaimedExport(t, r, "lost", "first")
	_, err := db.Exec("UPDATE usage_export_jobs SET lease_until=NOW()-INTERVAL '1 second' WHERE id=$1", first.ID)
	require.NoError(t, err)
	_, err = db.Exec("UPDATE usage_export_artifacts SET lease_until=NOW()-INTERVAL '1 second' WHERE job_id=$1", first.ID)
	require.NoError(t, err)
	second, err := r.Claim(ctx, "second", 256<<20)
	require.NoError(t, err)
	require.EqualValues(t, 512<<20, artifactReserved(t, db))
	pending, err := r.CleanupArtifacts(ctx)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	require.Equal(t, "first", pending[0].ClaimToken)
	diskErr := errors.New("sharing violation")
	require.ErrorIs(t, r.ReleaseArtifact(ctx, pending[0], func(service.UsageExportArtifact) error { return diskErr }), diskErr)
	require.EqualValues(t, 512<<20, artifactReserved(t, db))
	require.NoError(t, r.ReleaseArtifact(ctx, pending[0], func(service.UsageExportArtifact) error { return nil }))
	require.EqualValues(t, 256<<20, artifactReserved(t, db))
	completeClaimedExport(t, r, second)
	require.EqualValues(t, 100, artifactReserved(t, db))
	// A duplicate stale callback cannot unlink the successfully published file.
	removed := false
	require.NoError(t, r.ReleaseArtifact(ctx, service.UsageExportArtifact{JobID: second.ID, ClaimToken: second.ClaimToken, FileKey: second.FileKey}, func(service.UsageExportArtifact) error { removed = true; return nil }))
	require.False(t, removed)
}
func TestUsageExportDeletionRetainsQuotaUntilUnlink(t *testing.T) {
	r, db := usageExportTestDB(t)
	ctx := context.Background()
	j := createClaimedExport(t, r, "delete", "claim")
	completeClaimedExport(t, r, j)
	require.NoError(t, r.SetTicket(ctx, j.ID, "ticket"))
	diskErr := errors.New("disk unavailable")
	require.ErrorIs(t, r.Delete(ctx, j.ID, func(service.UsageExportArtifact) error { return diskErr }), diskErr)
	got, err := r.Get(ctx, j.ID)
	require.NoError(t, err)
	require.Equal(t, "deleting", got.Status)
	require.EqualValues(t, 100, artifactReserved(t, db))
	_, err = r.GetByTicket(ctx, "ticket", func(*service.UsageExportJob) error { t.Fatal("deleting artifact opened"); return nil })
	require.ErrorIs(t, err, sql.ErrNoRows)
	require.NoError(t, r.Delete(ctx, j.ID, func(service.UsageExportArtifact) error { return nil }))
	require.Zero(t, artifactReserved(t, db))
	_, err = r.Get(ctx, j.ID)
	require.ErrorIs(t, err, sql.ErrNoRows)
}
func TestUsageExportTicketOpenSerializesDeletion(t *testing.T) {
	r, _ := usageExportTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	j := createClaimedExport(t, r, "download", "claim")
	completeClaimedExport(t, r, j)
	require.NoError(t, r.SetTicket(ctx, j.ID, "ticket"))
	entered, release := make(chan struct{}), make(chan struct{})
	downloaded := make(chan error, 1)
	deleted := make(chan error, 1)
	go func() {
		_, err := r.GetByTicket(ctx, "ticket", func(*service.UsageExportJob) error {
			close(entered)
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
		downloaded <- err
	}()
	<-entered
	go func() { deleted <- r.Delete(ctx, j.ID, func(service.UsageExportArtifact) error { return nil }) }()
	select {
	case err := <-deleted:
		t.Fatalf("delete overtook file open: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	require.NoError(t, <-downloaded)
	require.NoError(t, <-deleted)
	require.NoError(t, r.EndDownload(ctx, j.ID, j.ClaimToken, "ticket"))
	require.NoError(t, r.Delete(ctx, j.ID, func(service.UsageExportArtifact) error { return nil }))
}
func TestUsageExportTicketFailedOpenCanRetry(t *testing.T) {
	r, _ := usageExportTestDB(t)
	ctx := context.Background()
	j := createClaimedExport(t, r, "retry-open", "claim")
	completeClaimedExport(t, r, j)
	require.NoError(t, r.SetTicket(ctx, j.ID, "ticket"))
	denied := errors.New("access revoked")
	_, err := r.GetByTicket(ctx, "ticket", func(*service.UsageExportJob) error { return denied })
	require.ErrorIs(t, err, denied)
	_, err = r.GetByTicket(ctx, "ticket", func(*service.UsageExportJob) error { return nil })
	require.NoError(t, err)
}
func TestUsageExportDownloadLeaseRetainsQuota(t *testing.T) {
	r, db := usageExportTestDB(t)
	ctx := context.Background()
	j := createClaimedExport(t, r, "download-lease", "claim")
	completeClaimedExport(t, r, j)
	require.NoError(t, r.SetTicket(ctx, j.ID, "ticket"))
	_, err := r.GetByTicket(ctx, "ticket", func(*service.UsageExportJob) error { return nil })
	require.NoError(t, err)
	ok, err := r.RenewDownload(ctx, j.ID, j.ClaimToken, "ticket")
	require.NoError(t, err)
	require.True(t, ok)
	removed := false
	require.NoError(t, r.Delete(ctx, j.ID, func(service.UsageExportArtifact) error { removed = true; return nil }))
	require.False(t, removed)
	require.EqualValues(t, 100, artifactReserved(t, db))
	deleting, err := r.Get(ctx, j.ID)
	require.NoError(t, err)
	require.Equal(t, "deleting", deleting.Status)
	pending, err := r.CleanupArtifacts(ctx)
	require.NoError(t, err)
	require.Empty(t, pending)
	require.NoError(t, r.EndDownload(ctx, j.ID, j.ClaimToken, "stale-token"))
	ok, err = r.RenewDownload(ctx, j.ID, j.ClaimToken, "ticket")
	require.NoError(t, err)
	require.True(t, ok)
	require.NoError(t, r.EndDownload(ctx, j.ID, j.ClaimToken, "ticket"))
	ok, err = r.RenewDownload(ctx, j.ID, j.ClaimToken, "ticket")
	require.NoError(t, err)
	require.False(t, ok)
	pending, err = r.CleanupArtifacts(ctx)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	require.NoError(t, r.Delete(ctx, j.ID, func(service.UsageExportArtifact) error { return nil }))
	require.Zero(t, artifactReserved(t, db))
}
func TestUsageExportOrphanArtifactsKeepReservation(t *testing.T) {
	r, db := usageExportTestDB(t)
	ctx := context.Background()
	j := createClaimedExport(t, r, "orphan", "claim")
	_, err := db.Exec("DELETE FROM users WHERE id=1")
	require.NoError(t, err)
	require.EqualValues(t, 256<<20, artifactReserved(t, db))
	pending, err := r.CleanupArtifacts(ctx)
	require.NoError(t, err)
	require.Empty(t, pending)
	removed := false
	require.NoError(t, r.ReleaseArtifact(ctx, service.UsageExportArtifact{JobID: j.ID, ClaimToken: j.ClaimToken}, func(service.UsageExportArtifact) error { removed = true; return nil }))
	require.False(t, removed)
	// A hard-deleted owner's live worker still occupies one cluster slot.
	require.NoError(t, r.Create(ctx, &service.UsageExportJob{ID: "second", Owner: service.UsageExportOwner{UserID: 2}}))
	_, err = r.Claim(ctx, "second", 256<<20)
	require.NoError(t, err)
	require.NoError(t, r.Create(ctx, &service.UsageExportJob{ID: "third", Owner: service.UsageExportOwner{UserID: 3}}))
	_, err = r.Claim(ctx, "third", 256<<20)
	require.ErrorIs(t, err, sql.ErrNoRows)
	_, err = db.Exec("UPDATE usage_export_artifacts SET lease_until=NOW()-INTERVAL '1 second' WHERE job_id=$1", j.ID)
	require.NoError(t, err)
	pending, err = r.CleanupArtifacts(ctx)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	require.Equal(t, j.ID, pending[0].JobID)
	require.NoError(t, r.ReleaseArtifact(ctx, pending[0], func(service.UsageExportArtifact) error { return nil }))
	require.EqualValues(t, 256<<20, artifactReserved(t, db))
}
func TestUsageExportUnlinkedQuotaBlocksClaim(t *testing.T) {
	r, db := usageExportTestDB(t)
	ctx := context.Background()
	require.NoError(t, r.Create(ctx, &service.UsageExportJob{ID: "queued", Owner: service.UsageExportOwner{UserID: 1}}))
	_, err := db.Exec(`INSERT INTO usage_export_artifacts(job_id,claim_token,file_key,reserved_bytes) SELECT 'orphan-'||i,'lost','orphan-'||i||'.xlsx',268435456 FROM generate_series(1,8) i`)
	require.NoError(t, err)
	_, err = r.Claim(ctx, "claim", 256<<20)
	require.ErrorIs(t, err, sql.ErrNoRows)
	pending, err := r.CleanupArtifacts(ctx)
	require.NoError(t, err)
	require.Len(t, pending, 8)
	require.NoError(t, r.ReleaseArtifact(ctx, pending[0], func(service.UsageExportArtifact) error { return nil }))
	_, err = r.Claim(ctx, "claim", 256<<20)
	require.NoError(t, err)
	require.EqualValues(t, 2<<30, artifactReserved(t, db))
}
func TestUsageExportListFiltersBeforeLimit(t *testing.T) {
	r, db := usageExportTestDB(t)
	ctx := context.Background()
	owner := service.UsageExportOwner{UserID: 1, Kind: "scoped_key", KeyID: "selected", Revision: "v1"}
	require.NoError(t, r.Create(ctx, &service.UsageExportJob{ID: "old-visible", Owner: owner}))
	_, err := db.Exec("UPDATE usage_export_jobs SET status='failed',created_at=NOW()-INTERVAL '1 day'")
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO usage_export_jobs(id,owner_id,owner,spec,status,max_id,total_rows) SELECT 'other-'||i,1,'{"user_id":1,"kind":"scoped_key","key_id":"other","revision":"v2"}','{}','failed',0,0 FROM generate_series(1,60) i`)
	require.NoError(t, err)
	jobs, err := r.List(ctx, owner)
	require.NoError(t, err)
	require.Len(t, jobs, 1)
	require.Equal(t, "old-visible", jobs[0].ID)
	require.NoError(t, r.CanCreate(ctx, 1))
	require.NoError(t, r.Create(ctx, &service.UsageExportJob{ID: "active", Owner: owner}))
	require.Error(t, r.CanCreate(ctx, 1))
}
func TestUsageExportModelCursorMatchesSQLAndIndexes(t *testing.T) {
	_, db := usageExportTestDB(t)
	for _, requested := range []string{"", "  ", " gpt ", "\tgpt", "gpt\t", "\ngpt\n", "　gpt　", " gpt\t "} {
		var sqlModel string
		require.NoError(t, db.QueryRow(`SELECT COALESCE(NULLIF(TRIM($1::text),''),'fallback')`, requested).Scan(&sqlModel))
		require.Equal(t, sqlModel, usageExportModelCursor(requested, "fallback"), "requested %q", requested)
	}
	_, err := db.Exec(`CREATE TABLE usage_logs(id BIGINT PRIMARY KEY,requested_model TEXT,model TEXT,created_at TIMESTAMPTZ); INSERT INTO usage_logs SELECT i,'model-'||(i%7),'fallback',NOW()-i*INTERVAL '1 second' FROM generate_series(1,20000) i`)
	require.NoError(t, err)
	migration, err := os.ReadFile("../../migrations/304_usage_export_keyset_indexes_notx.sql")
	require.NoError(t, err)
	for range 2 {
		for _, statement := range strings.Split(string(migration), ";") {
			if strings.TrimSpace(statement) != "" {
				_, err = db.Exec(statement)
				require.NoError(t, err)
			}
		}
	}
	_, err = db.Exec("ANALYZE usage_logs")
	require.NoError(t, err)
	for _, tc := range []struct{ query, index string }{
		{`EXPLAIN SELECT id FROM usage_logs WHERE id<=20000 AND (COALESCE(NULLIF(TRIM(requested_model),''),model),id)>('model-3',10000) ORDER BY COALESCE(NULLIF(TRIM(requested_model),''),model),id LIMIT 1000`, "idx_usage_logs_export_model_id"},
		{`EXPLAIN SELECT id FROM usage_logs WHERE id<=20000 ORDER BY created_at,id LIMIT 1000`, "idx_usage_logs_export_created_id"},
	} {
		rows, err := db.Query(tc.query)
		require.NoError(t, err)
		var plan strings.Builder
		for rows.Next() {
			var line string
			require.NoError(t, rows.Scan(&line))
			plan.WriteString(line)
			plan.WriteByte('\n')
		}
		require.NoError(t, rows.Err())
		rows.Close()
		require.Contains(t, plan.String(), tc.index)
		t.Log(plan.String())
	}
}
