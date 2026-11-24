//go:build integration

package service

import (
	"context"
	"database/sql"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	_ "github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

func TestSubscriptionTopupMailPostgresLeasesAndRetry(t *testing.T) {
	db := newTopupMailTestDB(t)
	ctx := context.Background()
	_, err := db.ExecContext(ctx, `INSERT INTO topup_mails(order_id,recipient,message_id) VALUES
(1,'a@example.invalid','<topup-1@sub2api.local>'),(2,'b@example.invalid','<topup-2@sub2api.local>'),(3,'c@example.invalid','<topup-3@sub2api.local>')`)
	require.NoError(t, err)
	type claimResult struct {
		item *topupMailItem
		err  error
	}
	results := make(chan claimResult, 4)
	var workers sync.WaitGroup
	for range 4 {
		workers.Go(func() {
			svc := NewSubscriptionTopupMailService(db, nil, nil)
			defer svc.Stop()
			item, err := svc.claim(ctx)
			results <- claimResult{item, err}
		})
	}
	workers.Wait()
	close(results)
	var claimed []*topupMailItem
	for result := range results {
		require.NoError(t, result.err)
		if result.item != nil {
			claimed = append(claimed, result.item)
		}
	}
	require.Len(t, claimed, 2, "all instances share at most two SMTP leases")
	require.NotEqual(t, claimed[0].id, claimed[1].id)
	svc := NewSubscriptionTopupMailService(db, nil, nil)
	defer svc.Stop()
	stale := claimed[0]
	_, err = db.ExecContext(ctx, `UPDATE topup_mails SET lease_until=NOW()-INTERVAL '1 second',available_at=NOW()-INTERVAL '1 day' WHERE id=$1`, stale.id)
	require.NoError(t, err)
	recovered, err := svc.claim(ctx)
	require.NoError(t, err)
	require.NotNil(t, recovered)
	require.Equal(t, stale.id, recovered.id)
	require.NotEqual(t, stale.claimToken, recovered.claimToken)
	require.Equal(t, stale.messageID, recovered.messageID, "crash recovery preserves SMTP Message-ID")
	require.Equal(t, stale.attempts+1, recovered.attempts)
	svc.deliver(stale)
	var status, token string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT status,claim_token FROM topup_mails WHERE id=$1`, stale.id).Scan(&status, &token))
	require.Equal(t, "sending", status)
	require.Equal(t, recovered.claimToken, token, "stale worker cannot deliver or finish another lease")
	_, err = db.ExecContext(ctx, `UPDATE topup_mails SET status='sent',lease_until=NULL,claim_token=''`)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO topup_mails(order_id,recipient,message_id) VALUES(4,'','<topup-4@sub2api.local>')`)
	require.NoError(t, err)
	failed, err := svc.claim(ctx)
	require.NoError(t, err)
	require.NotNil(t, failed)
	svc.deliver(failed)
	var available time.Time
	var diagnostic, messageID string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT status,available_at,last_error,message_id FROM topup_mails WHERE id=$1`, failed.id).Scan(&status, &available, &diagnostic, &messageID))
	require.Equal(t, "queued", status)
	require.Equal(t, "delivery_read_failed", diagnostic)
	require.Equal(t, failed.messageID, messageID)
	require.True(t, available.After(time.Now().Add(time.Minute)))
	noImmediateRetry, err := svc.claim(ctx)
	require.NoError(t, err)
	require.Nil(t, noImmediateRetry)
	_, err = db.ExecContext(ctx, `UPDATE topup_mails SET attempts=4,available_at=NOW()-INTERVAL '1 second' WHERE id=$1`, failed.id)
	require.NoError(t, err)
	lastAttempt, err := svc.claim(ctx)
	require.NoError(t, err)
	require.NotNil(t, lastAttempt)
	require.Equal(t, 5, lastAttempt.attempts)
	svc.deliver(lastAttempt)
	require.NoError(t, db.QueryRowContext(ctx, `SELECT status FROM topup_mails WHERE id=$1`, failed.id).Scan(&status))
	require.Equal(t, "failed", status)
	_, err = db.ExecContext(ctx, `UPDATE topup_mails SET status='sending',lease_until=NOW()-INTERVAL '1 second' WHERE id=$1`, failed.id)
	require.NoError(t, err)
	_, err = svc.claim(ctx)
	require.NoError(t, err)
	require.NoError(t, db.QueryRowContext(ctx, `SELECT status FROM topup_mails WHERE id=$1`, failed.id).Scan(&status))
	require.Equal(t, "failed", status, "crash on the final attempt must not retry indefinitely")
}

func newTopupMailTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("TOPUP_TEST_DSN")
	if dsn == "" {
		t.Skip("set TOPUP_TEST_DSN to an isolated PostgreSQL database")
	}
	setup, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	schema := "topup_mail_" + uuid.NewString()[:8]
	_, err = setup.Exec(`CREATE SCHEMA ` + schema)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = setup.Exec(`DROP SCHEMA ` + schema + ` CASCADE`)
		_ = setup.Close()
	})
	if strings.Contains(dsn, "://") {
		parsed, err := url.Parse(dsn)
		require.NoError(t, err)
		query := parsed.Query()
		query.Set("search_path", schema)
		parsed.RawQuery = query.Encode()
		dsn = parsed.String()
	}
	if !strings.Contains(dsn, "://") {
		dsn += " search_path=" + schema
	}
	db, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	db.SetMaxOpenConns(8)
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.Exec(`CREATE TABLE topup_mails (
id bigserial PRIMARY KEY,order_id bigint UNIQUE NOT NULL,recipient text NOT NULL,locale text NOT NULL DEFAULT 'zh',status text NOT NULL DEFAULT 'queued',
attempts integer NOT NULL DEFAULT 0,available_at timestamptz NOT NULL DEFAULT NOW(),lease_until timestamptz,claim_token text NOT NULL DEFAULT '',
message_id text NOT NULL,last_error text NOT NULL DEFAULT '',sent_at timestamptz,created_at timestamptz NOT NULL DEFAULT NOW(),updated_at timestamptz NOT NULL DEFAULT NOW());
CREATE INDEX ON topup_mails(status,available_at)`)
	require.NoError(t, err)
	return db
}
