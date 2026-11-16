package repository

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	_ "github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

// Dedicated opt-in local database; every run creates and removes its own schema.
func TestUsageExportDatabaseLeasesAndTickets(t *testing.T) {
	dsn := os.Getenv("USAGE_EXPORT_TEST_DSN")
	if dsn == "" {
		t.Skip("USAGE_EXPORT_TEST_DSN is not set")
	}
	ctx := context.Background()
	db, e := sql.Open("postgres", dsn)
	require.NoError(t, e)
	defer db.Close()
	schema := fmt.Sprintf("usage_export_test_%d", time.Now().UnixNano())
	_, e = db.ExecContext(ctx, "CREATE SCHEMA "+schema)
	require.NoError(t, e)
	defer db.ExecContext(ctx, "DROP SCHEMA "+schema+" CASCADE")
	u, e := url.Parse(dsn)
	require.NoError(t, e)
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	local, e := sql.Open("postgres", u.String())
	require.NoError(t, e)
	defer local.Close()
	_, e = local.ExecContext(ctx, "CREATE TABLE users(id BIGINT PRIMARY KEY); INSERT INTO users VALUES(1),(2),(3)")
	require.NoError(t, e)
	migration, e := os.ReadFile("../../migrations/303_usage_export_jobs.sql")
	require.NoError(t, e)
	for range 2 {
		_, e = local.ExecContext(ctx, string(migration))
		require.NoError(t, e)
	}
	r := &usageExportRepository{db: local}
	for i := int64(1); i <= 3; i++ {
		require.NoError(t, r.Create(ctx, &service.UsageExportJob{ID: fmt.Sprint(i), Owner: service.UsageExportOwner{UserID: i, Kind: "jwt", Revision: "0"}, TotalRows: 1, MaxID: 1}))
	}
	require.Error(t, r.Create(ctx, &service.UsageExportJob{ID: "duplicate-owner", Owner: service.UsageExportOwner{UserID: 1}}))
	jobs := make(chan *service.UsageExportJob, 8)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			j, e := r.Claim(ctx, fmt.Sprintf("claim-%d", i), 256<<20)
			if e == nil {
				jobs <- j
			}
		}(i)
	}
	wg.Wait()
	close(jobs)
	claimed := make([]*service.UsageExportJob, 0, 2)
	for j := range jobs {
		claimed = append(claimed, j)
	}
	require.Len(t, claimed, 2)
	first := claimed[0]
	require.NoError(t, r.Cancel(ctx, first.ID))
	_, e = r.Claim(ctx, "cannot-steal-draining-capacity", 256<<20)
	require.ErrorIs(t, e, sql.ErrNoRows)
	first.Status = "completed"
	ok, e := r.Finish(ctx, first)
	require.NoError(t, e)
	require.False(t, ok)
	replacement, e := r.Claim(ctx, "replacement", 256<<20)
	require.NoError(t, e)
	_, e = local.ExecContext(ctx, "UPDATE usage_export_jobs SET lease_until=NOW()-INTERVAL '1 second' WHERE id=$1", replacement.ID)
	require.NoError(t, e)
	_, e = local.ExecContext(ctx, "UPDATE usage_export_artifacts SET lease_until=NOW()-INTERVAL '1 second' WHERE job_id=$1", replacement.ID)
	require.NoError(t, e)
	retried, e := r.Claim(ctx, "takeover", 256<<20)
	require.NoError(t, e)
	require.Equal(t, replacement.ID, retried.ID)
	require.Equal(t, 2, retried.Attempts)
	ok, e = r.Heartbeat(ctx, replacement.ID, replacement.ClaimToken, 99)
	require.NoError(t, e)
	require.False(t, ok)
	replacement.Status = "completed"
	replacement.FileKey = "old.xlsx"
	ok, e = r.Finish(ctx, replacement)
	require.NoError(t, e)
	require.False(t, ok)
	retried.Status = "completed"
	retried.FileKey = retried.ID + "-" + retried.ClaimToken + ".xlsx"
	retried.SizeBytes = 100
	retried.RowsWritten = 1
	ok, e = r.Finish(ctx, retried)
	require.NoError(t, e)
	require.True(t, ok)
	require.NoError(t, r.SetTicket(ctx, retried.ID, "digest"))
	got, e := r.GetByTicket(ctx, "digest", func(*service.UsageExportJob) error { return nil })
	require.NoError(t, e)
	require.Equal(t, retried.FileKey, got.FileKey)
	_, e = r.GetByTicket(ctx, "digest", func(*service.UsageExportJob) error { return nil })
	require.ErrorIs(t, e, sql.ErrNoRows)
	require.NoError(t, r.EndDownload(ctx, retried.ID, retried.ClaimToken, "digest"))
	keys := []string{}
	e = r.Delete(ctx, retried.ID, func(a service.UsageExportArtifact) error { keys = append(keys, a.FileKey); return nil })
	require.NoError(t, e)
	require.Contains(t, keys, retried.FileKey)
	// Maintenance must commit even when there is no job left to claim.
	_, e = local.ExecContext(ctx, "UPDATE usage_export_jobs SET status='failed',lease_until=NULL")
	require.NoError(t, e)
	require.NoError(t, r.Create(ctx, &service.UsageExportJob{ID: "expired-queue", Owner: service.UsageExportOwner{UserID: 1}}))
	_, e = local.ExecContext(ctx, "UPDATE usage_export_jobs SET created_at=NOW()-INTERVAL '2 hours' WHERE id='expired-queue'")
	require.NoError(t, e)
	_, e = r.Claim(ctx, "maintenance-only", 256<<20)
	require.ErrorIs(t, e, sql.ErrNoRows)
	expired, e := r.Get(ctx, "expired-queue")
	require.NoError(t, e)
	require.Equal(t, "failed", expired.Status)
	require.Equal(t, "QUEUE_TIMEOUT", expired.ErrorCode)
	require.NoError(t, r.Create(ctx, &service.UsageExportJob{ID: "twice-lost", Owner: service.UsageExportOwner{UserID: 1}}))
	_, e = local.ExecContext(ctx, "UPDATE usage_export_jobs SET status='running',attempts=2,lease_until=NOW()-INTERVAL '1 minute' WHERE id='twice-lost'")
	require.NoError(t, e)
	_, e = r.Claim(ctx, "maintenance-only", 256<<20)
	require.ErrorIs(t, e, sql.ErrNoRows)
	expired, e = r.Get(ctx, "twice-lost")
	require.NoError(t, e)
	require.Equal(t, "failed", expired.Status)
	require.Equal(t, "WORKER_LOST", expired.ErrorCode)
}
