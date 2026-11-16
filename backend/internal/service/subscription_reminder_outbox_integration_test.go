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

func TestSubscriptionReminderOutboxPostgres(t *testing.T) {
	dsn := os.Getenv("SUBSCRIPTION_REMINDER_TEST_DSN")
	if dsn == "" {
		t.Skip("set SUBSCRIPTION_REMINDER_TEST_DSN to an isolated PostgreSQL database")
	}
	setupDB, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	defer setupDB.Close()
	ctx := context.Background()
	schema := "reminder_" + uuid.NewString()[:8]
	_, err = setupDB.ExecContext(ctx, `CREATE SCHEMA `+schema)
	require.NoError(t, err)
	defer setupDB.ExecContext(ctx, `DROP SCHEMA `+schema+` CASCADE`)
	if strings.Contains(dsn, "://") {
		parsed, parseErr := url.Parse(dsn)
		require.NoError(t, parseErr)
		query := parsed.Query()
		query.Set("search_path", schema)
		parsed.RawQuery = query.Encode()
		dsn = parsed.String()
	} else {
		dsn += " search_path=" + schema
	}
	db, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	defer db.Close()
	db.SetMaxOpenConns(8)
	_, err = db.ExecContext(ctx, `CREATE TABLE users(id bigint PRIMARY KEY,email text,username text,deleted_at timestamptz);
CREATE TABLE groups(id bigint PRIMARY KEY,name text,deleted_at timestamptz);
CREATE TABLE user_subscriptions(id bigint PRIMARY KEY,user_id bigint,group_id bigint,status text,expires_at timestamptz,deleted_at timestamptz);
INSERT INTO users VALUES(1,'test@example.invalid','test',NULL); INSERT INTO groups VALUES(1,'group',NULL);`)
	require.NoError(t, err)
	migration, err := os.ReadFile("../../migrations/296_subscription_reminder_outbox.sql")
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, string(migration))
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, string(migration))
	require.NoError(t, err, "migration is repeatable")
	now := time.Now().UTC().Truncate(time.Second)
	_, err = db.ExecContext(ctx, `INSERT INTO user_subscriptions VALUES
(1,1,1,'active',$1::timestamptz+INTERVAL '12 hours',NULL),(2,1,1,'active',$1::timestamptz+INTERVAL '60 hours',NULL),
(3,1,1,'active',$1::timestamptz+INTERVAL '150 hours',NULL),(4,1,1,'active',NULL,NULL),
(5,1,1,'active',$1::timestamptz+INTERVAL '4 days',NULL),(6,1,1,'expired',$1::timestamptz+INTERVAL '12 hours',NULL)`, now)
	require.NoError(t, err)
	svc := NewSubscriptionExpiryService(nil, time.Minute)
	svc.db = db
	require.NoError(t, svc.enqueueExpiryReminders(ctx, now))
	require.NoError(t, svc.enqueueExpiryReminders(ctx, now))
	var count int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM subscription_reminder_outbox`).Scan(&count))
	require.Equal(t, 3, count)
	var mu sync.Mutex
	var claimed []*subscriptionReminder
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			item, err := svc.claimReminder(ctx)
			require.NoError(t, err)
			if item != nil {
				mu.Lock()
				claimed = append(claimed, item)
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	require.Len(t, claimed, 2, "all instances share two SMTP leases")
	first := claimed[0]
	_, err = db.ExecContext(ctx, `UPDATE user_subscriptions SET expires_at=expires_at+INTERVAL '30 days' WHERE id=$1`, first.subscriptionID)
	require.NoError(t, err)
	svc.deliverReminder(first)
	var status string
	require.NoError(t, db.QueryRow(`SELECT status FROM subscription_reminder_outbox WHERE id=$1`, first.id).Scan(&status))
	require.Equal(t, "skipped", status, "renewal is rechecked before SMTP")
	next, err := svc.claimReminder(ctx)
	require.NoError(t, err)
	require.NotNil(t, next)
	_, err = db.ExecContext(ctx, `UPDATE subscription_reminder_outbox SET lease_until=NOW()-INTERVAL '1 second' WHERE id=$1`, next.id)
	require.NoError(t, err)
	recovered, err := svc.claimReminder(ctx)
	require.NoError(t, err)
	require.NotNil(t, recovered)
	require.Equal(t, next.id, recovered.id)
	require.NotEqual(t, next.claim, recovered.claim)
	// A paused worker must not send after another worker recovers its lease.
	// No mail service is configured, so reaching Send would also fail this test.
	svc.deliverReminder(next)
	var currentClaim string
	require.NoError(t, db.QueryRow(`SELECT status,claim_id FROM subscription_reminder_outbox WHERE id=$1`, next.id).Scan(&status, &currentClaim))
	require.Equal(t, "running", status)
	require.Equal(t, recovered.claim, currentClaim)
	// A revoke or recipient change while queued must suppress the old delivery.
	_, err = db.ExecContext(ctx, `UPDATE user_subscriptions SET status='cancelled' WHERE id=$1`, recovered.subscriptionID)
	require.NoError(t, err)
	svc.deliverReminder(recovered)
	require.NoError(t, db.QueryRow(`SELECT status FROM subscription_reminder_outbox WHERE id=$1`, recovered.id).Scan(&status))
	require.Equal(t, "skipped", status)
	other := claimed[1]
	_, err = db.ExecContext(ctx, `UPDATE users SET email='new@example.invalid' WHERE id=1`)
	require.NoError(t, err)
	svc.deliverReminder(other)
	require.NoError(t, db.QueryRow(`SELECT status FROM subscription_reminder_outbox WHERE id=$1`, other.id).Scan(&status))
	require.Equal(t, "skipped", status)
	// Disabled reminders remain retryable without exhausting delivery attempts.
	_, err = db.ExecContext(ctx, `UPDATE user_subscriptions SET status='active',expires_at=$1::timestamptz+INTERVAL '12 hours' WHERE id=$2`, now, other.subscriptionID)
	require.NoError(t, err)
	require.NoError(t, svc.enqueueExpiryReminders(ctx, now))
	paused, err := svc.claimReminder(ctx)
	require.NoError(t, err)
	require.NotNil(t, paused)
	svc.settingRepo = &subscriptionExpirySettingRepoStub{values: map[string]string{SettingKeySubscriptionExpiryNotifyEnabled: "false"}}
	svc.deliverReminder(paused)
	var attempts int
	require.NoError(t, db.QueryRow(`SELECT status,attempts FROM subscription_reminder_outbox WHERE id=$1`, paused.id).Scan(&status, &attempts))
	require.Equal(t, "queued", status)
	require.Zero(t, attempts)
	// A crashed final retry is retained as a failed task, never deleted/replayed.
	_, err = db.ExecContext(ctx, `UPDATE subscription_reminder_outbox SET status='running',attempts=5,lease_until=NOW()-INTERVAL '1 second' WHERE id=$1`, paused.id)
	require.NoError(t, err)
	_, err = svc.claimReminder(ctx)
	require.NoError(t, err)
	require.NoError(t, db.QueryRow(`SELECT status FROM subscription_reminder_outbox WHERE id=$1`, paused.id).Scan(&status))
	require.Equal(t, "failed", status)
}
