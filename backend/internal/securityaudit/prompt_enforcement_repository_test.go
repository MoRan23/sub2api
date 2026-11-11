package securityaudit

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/promptenforcement"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

// Each test uses an isolated schema so other agents and suites can use the same
// local PostgreSQL server without truncating one another's fixtures.
func openEnforcementDB(t *testing.T) (*sql.DB, *PostgreSQLRepository, int64) {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv(promptAuditPostgresTestEnv))
	if dsn == "" {
		t.Skip(promptAuditPostgresTestEnv + " is not set")
	}
	admin, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	schema := fmt.Sprintf("prompt_enforcement_%d", time.Now().UnixNano())
	_, err = admin.Exec(`CREATE SCHEMA ` + pq.QuoteIdentifier(schema))
	require.NoError(t, err)
	u, err := url.Parse(dsn)
	require.NoError(t, err)
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	db, err := sql.Open("postgres", u.String())
	require.NoError(t, err)
	db.SetMaxOpenConns(20)
	t.Cleanup(func() {
		require.NoError(t, db.Close())
		_, dropErr := admin.Exec(`DROP SCHEMA ` + pq.QuoteIdentifier(schema) + ` CASCADE`)
		require.NoError(t, dropErr)
		require.NoError(t, admin.Close())
	})
	_, err = db.Exec(`
		CREATE TABLE users (id BIGSERIAL PRIMARY KEY,status TEXT NOT NULL DEFAULT 'active',role TEXT NOT NULL DEFAULT 'user',email TEXT NOT NULL DEFAULT 'user@example.test',username TEXT NOT NULL DEFAULT 'test',deleted_at TIMESTAMPTZ,updated_at TIMESTAMPTZ NOT NULL DEFAULT now());
		CREATE TABLE groups (id BIGSERIAL PRIMARY KEY,status TEXT NOT NULL DEFAULT 'active',is_exclusive BOOLEAN DEFAULT FALSE,deleted_at TIMESTAMPTZ);
		CREATE TABLE api_keys (id BIGSERIAL PRIMARY KEY,user_id BIGINT,group_id BIGINT,key TEXT NOT NULL DEFAULT 'test-key',status TEXT NOT NULL DEFAULT 'active',name TEXT,deleted_at TIMESTAMPTZ,ip_whitelist TEXT,ip_blacklist TEXT,expires_at TIMESTAMPTZ);
		CREATE TABLE user_allowed_groups(user_id BIGINT,group_id BIGINT);
		CREATE TABLE mcp_oauth_grants(id BIGSERIAL PRIMARY KEY,user_id BIGINT,revoked_at TIMESTAMPTZ);
	`)
	require.NoError(t, err)
	for _, name := range []string{"181_prompt_audit.sql", "182_prompt_audit_full_prompt.sql", "184_auth_cache_invalidation_outbox.sql", "281_prompt_audit_enforcement.sql"} {
		migration, err := os.ReadFile(filepath.Join("..", "..", "migrations", name))
		require.NoError(t, err)
		_, err = db.Exec(string(migration))
		require.NoError(t, err, name)
		_, err = db.Exec(string(migration))
		require.NoError(t, err, "migration restart: "+name)
	}
	var userID int64
	require.NoError(t, db.QueryRow(`INSERT INTO users DEFAULT VALUES RETURNING id`).Scan(&userID))
	_, err = db.Exec(`INSERT INTO api_keys(user_id) VALUES($1),($1)`, userID)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO mcp_oauth_grants(user_id) VALUES($1)`, userID)
	require.NoError(t, err)
	repo := NewPostgreSQLRepository(db)
	repo.clock = fixedClock{now: time.Now().UTC().Truncate(time.Microsecond)}
	return db, repo, userID
}

func enforcementInput(userID int64, key string, now time.Time) EnforcementInput {
	snapshot := integrationSnapshot("enforce")
	snapshot.UserID = userID
	snapshot.RequestID = key
	return EnforcementInput{Snapshot: snapshot, ConfigVersion: 1, Result: integrationResult(EventCritical), RequestKey: key,
		StartedAt: now, Kind: "jailbreak", Policy: EnforcementPolicy{AutoBanEnabled: true, ViolationWindowHours: 24, BanThreshold: 10, EmailOnHit: true, EmailMergeMinutes: 5}}
}

func TestPromptEnforcementThresholdIdempotenceAndLiveOwnership(t *testing.T) {
	db, repo, userID := openEnforcementDB(t)
	ctx := context.Background()
	now := repo.clock.Now()
	for i := 1; i <= 10; i++ {
		input := enforcementInput(userID, fmt.Sprintf("server-turn-%d", i), now)
		if i%2 == 0 {
			input.Kind, input.RuleGroupName = "combination", "test policy"
		}
		out, err := repo.RecordEnforcedBlocking(ctx, input)
		require.NoError(t, err)
		require.True(t, out.Counted)
		require.Equal(t, i, out.ViolationCount)
		require.Equal(t, i == 10, out.UserChanged)
		require.Equal(t, i == 10, out.AutoBanned)
	}
	out, err := repo.RecordEnforcedBlocking(ctx, enforcementInput(userID, "server-turn-10", now))
	require.NoError(t, err)
	require.True(t, out.Counted, "duplicate returns the original committed outcome")
	require.False(t, out.UserChanged)
	var hits, events, notifications, invalidations int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM prompt_audit_enforcement_hits`).Scan(&hits))
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM prompt_audit_events`).Scan(&events))
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM prompt_audit_enforcement_notifications`).Scan(&notifications))
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM auth_cache_invalidation_outbox`).Scan(&invalidations))
	require.Equal(t, 10, hits)
	require.Equal(t, 10, events)
	require.Equal(t, 1, notifications)
	require.Equal(t, 2, invalidations, "all user keys receive durable invalidation")
	var revoked bool
	require.NoError(t, db.QueryRow(`SELECT revoked_at IS NOT NULL FROM mcp_oauth_grants WHERE user_id=$1`, userID).Scan(&revoked))
	require.True(t, revoked)
	var eventID int64
	require.NoError(t, db.QueryRow(`SELECT event_id FROM prompt_audit_enforcement_hits WHERE request_key='server-turn-1'`).Scan(&eventID))
	details, err := repo.GetEventEnforcement(ctx, []int64{eventID})
	require.NoError(t, err)
	require.Equal(t, 1, details[eventID].ViolationCount, "historical count must not change")
	require.True(t, details[eventID].CanUnban)
	_, err = db.Exec(`DELETE FROM prompt_audit_events WHERE id=$1`, eventID)
	require.NoError(t, err)
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM prompt_audit_enforcement_hits`).Scan(&hits))
	require.Equal(t, 10, hits, "audit deletion must not erase strikes")
}

func TestPromptEnforcementUnbanResetsEpochAndFencesLateRequest(t *testing.T) {
	db, repo, userID := openEnforcementDB(t)
	ctx := context.Background()
	now := repo.clock.Now()
	input := enforcementInput(userID, "old-turn", now)
	input.Policy.BanThreshold = 1
	_, err := repo.RecordEnforcedBlocking(ctx, input)
	require.NoError(t, err)
	var oldEventID int64
	require.NoError(t, db.QueryRow(`SELECT event_id FROM prompt_audit_enforcement_hits WHERE request_key='old-turn'`).Scan(&oldEventID))
	repo.clock = fixedClock{now: now.Add(time.Second)}
	out, err := repo.UnbanPromptUser(ctx, userID)
	require.NoError(t, err)
	require.True(t, out.UserChanged)
	require.Equal(t, int64(2), out.Epoch)
	out, err = repo.UnbanPromptUser(ctx, userID)
	require.NoError(t, err)
	require.Equal(t, int64(2), out.Epoch, "repeat unban is idempotent")
	out, err = repo.RecordEnforcedBlocking(ctx, input)
	require.NoError(t, err)
	require.True(t, out.Counted, "old duplicate retains its original recorded facts")
	var historicalHits int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM prompt_audit_enforcement_hits`).Scan(&historicalHits))
	require.Equal(t, 1, historicalHits, "old-cycle duplicate must not create a second event")
	input.RequestKey = "late-turn"
	out, err = repo.RecordEnforcedBlocking(ctx, input)
	require.NoError(t, err)
	require.False(t, out.Counted)
	require.Equal(t, "cycle_reset", out.Reason)
	require.Equal(t, "active", out.UserStatus)
	var pending int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM prompt_audit_enforcement_notifications WHERE sent_at IS NULL`).Scan(&pending))
	require.Zero(t, pending)
	input.RequestKey, input.StartedAt = "new-turn", repo.clock.Now()
	out, err = repo.RecordEnforcedBlocking(ctx, input)
	require.NoError(t, err)
	require.Equal(t, 1, out.ViolationCount)
	require.True(t, out.AutoBanned)
	oldDetails, err := repo.GetEventEnforcement(ctx, []int64{oldEventID})
	require.NoError(t, err)
	require.Equal(t, int64(1), oldDetails[oldEventID].Epoch, "old event keeps its original cycle")
	require.Equal(t, 1, oldDetails[oldEventID].ViolationCount)
	require.True(t, oldDetails[oldEventID].CanUnban, "old event exposes current prompt-owned ban action")
	oldInput := enforcementInput(userID, "old-turn", now)
	duplicate, err := repo.RecordEnforcedBlocking(ctx, oldInput)
	require.NoError(t, err)
	require.True(t, duplicate.CanUnban, "duplicate outcome uses the same live ownership semantics")
	tx, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	_, err = tx.Exec(`UPDATE users SET status='disabled' WHERE id=$1`, userID)
	require.NoError(t, err)
	require.NoError(t, promptenforcement.ApplyExplicitUserStatus(ctx, tx, userID, "disabled", repo.clock.Now()))
	require.NoError(t, tx.Commit())
	oldDetails, err = repo.GetEventEnforcement(ctx, []int64{oldEventID})
	require.NoError(t, err)
	require.False(t, oldDetails[oldEventID].CanUnban, "administrator disable revokes action on all historical events")
	_, err = repo.UnbanPromptUser(ctx, userID)
	require.ErrorIs(t, err, ErrEnforcementUnbanOwnership, "explicit repeated disable revokes ownership")
}

func TestPromptEnforcementRollingWindowAndAdministratorProtection(t *testing.T) {
	db, repo, userID := openEnforcementDB(t)
	ctx := context.Background()
	now := repo.clock.Now()
	input := enforcementInput(userID, "first", now)
	input.Policy.AutoBanEnabled = false
	_, err := repo.RecordEnforcedBlocking(ctx, input)
	require.NoError(t, err)
	repo.clock = fixedClock{now: now.Add(24*time.Hour + time.Microsecond)}
	input.RequestKey, input.StartedAt = "second", repo.clock.Now()
	out, err := repo.RecordEnforcedBlocking(ctx, input)
	require.NoError(t, err)
	require.Equal(t, 1, out.ViolationCount)
	_, err = db.Exec(`UPDATE users SET role='admin' WHERE id=$1`, userID)
	require.NoError(t, err)
	input.RequestKey = "admin"
	input.Policy.AutoBanEnabled, input.Policy.BanThreshold = true, 1
	out, err = repo.RecordEnforcedBlocking(ctx, input)
	require.NoError(t, err)
	require.False(t, out.AutoBanned)
	require.Equal(t, "active", out.UserStatus)
}

func TestPromptEnforcementConcurrentRequestsAndAtomicRollback(t *testing.T) {
	db, repo, userID := openEnforcementDB(t)
	ctx := context.Background()
	now := repo.clock.Now()
	var wg sync.WaitGroup
	errs := make(chan error, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			_, err := repo.RecordEnforcedBlocking(ctx, enforcementInput(userID, fmt.Sprintf("same-%d", index%10), now))
			errs <- err
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	var hits, count, invalidations int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*),MAX(violation_count) FROM prompt_audit_enforcement_hits`).Scan(&hits, &count))
	require.Equal(t, 10, hits)
	require.Equal(t, 10, count)
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM auth_cache_invalidation_outbox`).Scan(&invalidations))
	require.Equal(t, 2, invalidations)
	input := enforcementInput(userID, "rollback", now)
	input.Result.ChunkTotal = -1
	_, err := repo.RecordEnforcedBlocking(ctx, input)
	require.Error(t, err)
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM prompt_audit_enforcement_hits`).Scan(&hits))
	require.Equal(t, 10, hits)
}

func TestPromptEnforcementMailMergeLeaseRetryAndReset(t *testing.T) {
	db, repo, userID := openEnforcementDB(t)
	ctx := context.Background()
	now := repo.clock.Now()
	input := enforcementInput(userID, "first", now)
	_, err := repo.RecordEnforcedBlocking(ctx, input)
	require.NoError(t, err)
	notification, err := repo.ClaimEnforcementNotification(ctx, "worker-a", now, time.Minute)
	require.NoError(t, err)
	require.Nil(t, notification, "first warning waits the fixed merge interval")
	repo.clock = fixedClock{now: now.Add(4 * time.Minute)}
	input.RequestKey, input.StartedAt, input.Kind, input.RuleGroupName = "second", repo.clock.Now(), "combination", "policy group"
	input.Policy.ViolationWindowHours, input.Policy.BanThreshold = 12, 20
	_, err = repo.RecordEnforcedBlocking(ctx, input)
	require.NoError(t, err)
	notification, err = repo.ClaimEnforcementNotification(ctx, "worker-a", now.Add(5*time.Minute), time.Minute)
	require.NoError(t, err)
	require.NotNil(t, notification)
	require.Equal(t, 2, notification.HitCount)
	require.Equal(t, 12, notification.WindowHours, "merged message uses latest policy window")
	require.Equal(t, 20, notification.BanThreshold, "merged message uses latest policy threshold")
	require.ElementsMatch(t, []string{"jailbreak", "combination"}, notification.Categories)
	other, err := repo.ClaimEnforcementNotification(ctx, "worker-b", now.Add(5*time.Minute), time.Minute)
	require.NoError(t, err)
	require.Nil(t, other)
	current, err := repo.EnforcementNotificationCurrent(ctx, notification.ID, notification.ClaimVersion, notification.Email)
	require.NoError(t, err)
	require.True(t, current)
	other, err = repo.ClaimEnforcementNotification(ctx, "worker-b", now.Add(7*time.Minute), time.Minute)
	require.NoError(t, err)
	require.NotNil(t, other, "restart reclaims stale lease")
	require.ErrorIs(t, repo.CompleteEnforcementNotification(ctx, notification.ID, notification.ClaimVersion, now), ErrEnforcementNotificationLeaseLost)
	require.NoError(t, repo.RetryEnforcementNotification(ctx, other.ID, other.ClaimVersion, now.Add(7*time.Minute), "secret raw smtp response"))
	var lastError string
	require.NoError(t, db.QueryRow(`SELECT last_error FROM prompt_audit_enforcement_notifications WHERE id=$1`, other.ID).Scan(&lastError))
	require.Equal(t, "delivery_retry", lastError)
	other, err = repo.ClaimEnforcementNotification(ctx, "worker-b", now.Add(10*time.Minute), time.Minute)
	require.NoError(t, err)
	require.NotNil(t, other)
	require.NoError(t, repo.CompleteEnforcementNotification(ctx, other.ID, other.ClaimVersion, now.Add(10*time.Minute)))
	other, err = repo.ClaimEnforcementNotification(ctx, "worker-b", now.Add(time.Hour), time.Minute)
	require.NoError(t, err)
	require.Nil(t, other)
}

func TestPromptEnforcementBanNoticePromotesWarningAndManualEnableCancels(t *testing.T) {
	db, repo, userID := openEnforcementDB(t)
	ctx := context.Background()
	now := repo.clock.Now()
	input := enforcementInput(userID, "first", now)
	input.Policy.BanThreshold = 2
	_, err := repo.RecordEnforcedBlocking(ctx, input)
	require.NoError(t, err)
	input.RequestKey = "second"
	_, err = repo.RecordEnforcedBlocking(ctx, input)
	require.NoError(t, err)
	n, err := repo.ClaimEnforcementNotification(ctx, "sender", now, time.Minute)
	require.NoError(t, err)
	require.NotNil(t, n)
	require.True(t, n.AutoBanned)
	require.Equal(t, 2, n.HitCount)
	tx, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	_, err = tx.Exec(`UPDATE users SET status='active' WHERE id=$1`, userID)
	require.NoError(t, err)
	require.NoError(t, promptenforcement.ApplyExplicitUserStatus(ctx, tx, userID, "active", now.Add(time.Second)))
	require.NoError(t, tx.Commit())
	current, err := repo.EnforcementNotificationCurrent(ctx, n.ID, n.ClaimVersion, n.Email)
	require.NoError(t, err)
	require.False(t, current)
	require.ErrorIs(t, repo.CompleteEnforcementNotification(ctx, n.ID, n.ClaimVersion, now), ErrEnforcementNotificationLeaseLost)
}

func TestPromptEnforcementMailRecipientChangeReleasesAndReclaims(t *testing.T) {
	db, repo, userID := openEnforcementDB(t)
	ctx := context.Background()
	now := repo.clock.Now()
	_, err := repo.RecordEnforcedBlocking(ctx, enforcementInput(userID, "recipient-change", now))
	require.NoError(t, err)
	due := now.Add(5 * time.Minute)
	old, err := repo.ClaimEnforcementNotification(ctx, "sender", due, time.Minute)
	require.NoError(t, err)
	require.NotNil(t, old)
	require.Equal(t, "user@example.test", old.Email)
	current, err := repo.EnforcementNotificationCurrent(ctx, old.ID, old.ClaimVersion, old.Email)
	require.NoError(t, err)
	require.True(t, current)

	_, err = db.Exec(`UPDATE users SET email='updated@example.test' WHERE id=$1`, userID)
	require.NoError(t, err)
	current, err = repo.EnforcementNotificationCurrent(ctx, old.ID, old.ClaimVersion, old.Email)
	require.NoError(t, err)
	require.False(t, current, "never send a claimed payload to a superseded email address")
	require.NoError(t, repo.ReleaseEnforcementNotification(ctx, old.ID, old.ClaimVersion, due))
	next, err := repo.ClaimEnforcementNotification(ctx, "sender", due, time.Minute)
	require.NoError(t, err)
	require.NotNil(t, next)
	require.Equal(t, old.ID, next.ID)
	require.Greater(t, next.ClaimVersion, old.ClaimVersion)
	require.Equal(t, "updated@example.test", next.Email)
	require.Equal(t, old.Attempts, next.Attempts, "recipient refresh is not a failed delivery")
	current, err = repo.EnforcementNotificationCurrent(ctx, next.ID, next.ClaimVersion, next.Email)
	require.NoError(t, err)
	require.True(t, current)
	require.NoError(t, repo.CompleteEnforcementNotification(ctx, next.ID, next.ClaimVersion, due))

	var sent int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM prompt_audit_enforcement_hits WHERE user_id=$1 AND email_status='sent'`, userID).Scan(&sent))
	require.Equal(t, 1, sent)
}

func TestPromptEnforcementMailAcknowledgesOnlyClaimedPayload(t *testing.T) {
	db, repo, userID := openEnforcementDB(t)
	ctx := context.Background()
	now := repo.clock.Now()
	input := enforcementInput(userID, "first", now)
	_, err := repo.RecordEnforcedBlocking(ctx, input)
	require.NoError(t, err)
	n, err := repo.ClaimEnforcementNotification(ctx, "sender", now.Add(5*time.Minute), time.Minute)
	require.NoError(t, err)
	require.NotNil(t, n)
	require.Equal(t, "active", n.UserStatus)
	// Model a new hit arriving after SMTP accepted the frozen first payload.
	repo.clock = fixedClock{now: now.Add(6 * time.Minute)}
	input.RequestKey, input.StartedAt, input.Kind, input.RuleGroupName = "second", repo.clock.Now(), "combination", "new rule only"
	_, err = repo.RecordEnforcedBlocking(ctx, input)
	require.NoError(t, err)
	require.NoError(t, repo.CompleteEnforcementNotification(ctx, n.ID, n.ClaimVersion, now.Add(6*time.Minute)))
	next, err := repo.ClaimEnforcementNotification(ctx, "sender", now.Add(10*time.Minute), time.Minute)
	require.NoError(t, err)
	require.Nil(t, next, "new hit gets its own fixed merge window")
	next, err = repo.ClaimEnforcementNotification(ctx, "sender", now.Add(11*time.Minute), time.Minute)
	require.NoError(t, err)
	require.NotNil(t, next)
	require.Equal(t, 1, next.HitCount, "the already delivered hit must not be sent again")
	require.Equal(t, []string{"combination"}, next.Categories)
	require.Equal(t, []string{"new rule only"}, next.RuleGroupNames)
	require.Greater(t, next.DeliveryVersion, n.DeliveryVersion)
	var firstEventID int64
	require.NoError(t, db.QueryRow(`SELECT event_id FROM prompt_audit_enforcement_hits WHERE request_key='first'`).Scan(&firstEventID))
	details, err := repo.GetEventEnforcement(ctx, []int64{firstEventID})
	require.NoError(t, err)
	require.Equal(t, "sent", details[firstEventID].EmailStatus)
}

func TestPromptEnforcementFailureRollsBackBanLedgerEventAndInvalidation(t *testing.T) {
	db, repo, userID := openEnforcementDB(t)
	ctx := context.Background()
	_, err := db.Exec(`CREATE FUNCTION reject_notice() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'test notification fault'; END $$; CREATE TRIGGER reject_notice BEFORE INSERT ON prompt_audit_enforcement_notifications FOR EACH ROW EXECUTE FUNCTION reject_notice()`)
	require.NoError(t, err)
	input := enforcementInput(userID, "rollback-ban", repo.clock.Now())
	input.Policy.BanThreshold = 1
	_, err = repo.RecordEnforcedBlocking(ctx, input)
	require.Error(t, err)
	var status string
	require.NoError(t, db.QueryRow(`SELECT status FROM users WHERE id=$1`, userID).Scan(&status))
	require.Equal(t, "active", status)
	for _, table := range []string{"prompt_audit_events", "prompt_audit_jobs", "prompt_audit_enforcement_hits", "prompt_audit_enforcement_states", "auth_cache_invalidation_outbox"} {
		var count int
		require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM `+table).Scan(&count))
		require.Zero(t, count, table)
	}
}

func TestPromptEnforcementBoundedMailRetriesAndReferencedHistoryRetention(t *testing.T) {
	db, repo, userID := openEnforcementDB(t)
	ctx := context.Background()
	now := repo.clock.Now()
	_, err := repo.RecordEnforcedBlocking(ctx, enforcementInput(userID, "retained", now))
	require.NoError(t, err)
	var id int64
	for attempt := 1; attempt <= 5; attempt++ {
		at := now.Add(time.Duration(attempt) * time.Hour)
		n, claimErr := repo.ClaimEnforcementNotification(ctx, "sender", at, time.Minute)
		require.NoError(t, claimErr)
		require.NotNil(t, n)
		id = n.ID
		require.Equal(t, attempt, n.Attempts)
		require.NoError(t, repo.RetryEnforcementNotification(ctx, n.ID, n.ClaimVersion, at, "smtp error"))
	}
	n, err := repo.ClaimEnforcementNotification(ctx, "sender", now.Add(10*time.Hour), time.Minute)
	require.NoError(t, err)
	require.Nil(t, n)
	deleted, err := repo.PruneEnforcementHistory(ctx, now.Add(40*24*time.Hour), 100)
	require.NoError(t, err)
	require.Zero(t, deleted, "existing audit detail retains its immutable enforcement snapshot")
	var notices int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM prompt_audit_enforcement_notifications WHERE id=$1`, id).Scan(&notices))
	require.Equal(t, 1, notices)
	_, err = db.Exec(`DELETE FROM prompt_audit_events`)
	require.NoError(t, err)
	deleted, err = repo.PruneEnforcementHistory(ctx, now.Add(40*24*time.Hour), 100)
	require.NoError(t, err)
	require.Equal(t, int64(1), deleted)
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM prompt_audit_enforcement_notifications WHERE id=$1`, id).Scan(&notices))
	require.Zero(t, notices)
}

func TestPromptEnforcementMailCleanupSkipsLockedExhaustedRows(t *testing.T) {
	db, repo, userID := openEnforcementDB(t)
	ctx := context.Background()
	now := repo.clock.Now()
	_, err := repo.RecordEnforcedBlocking(ctx, enforcementInput(userID, "normal", now))
	require.NoError(t, err)
	var exhaustedID int64
	require.NoError(t, db.QueryRow(`INSERT INTO prompt_audit_enforcement_notifications(user_id,epoch,available_at,merge_until,first_hit_at,last_hit_at,kind,attempts,claimed_at) VALUES($1,2,$2,$2,$2,$2,'jailbreak',5,$2) RETURNING id`, userID, now.Add(-time.Hour)).Scan(&exhaustedID))
	locked, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = locked.Rollback() }()
	var lockedID int64
	require.NoError(t, locked.QueryRow(`SELECT id FROM prompt_audit_enforcement_notifications WHERE id=$1 FOR UPDATE`, exhaustedID).Scan(&lockedID))
	bounded, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	n, err := repo.ClaimEnforcementNotification(bounded, "worker", now.Add(10*time.Minute), time.Minute)
	require.NoError(t, err, "exhausted cleanup must not wait on a concurrently held outbox row")
	require.NotNil(t, n)
	require.NoError(t, locked.Rollback())
	_, err = repo.ClaimEnforcementNotification(ctx, "worker2", now.Add(10*time.Minute), time.Minute)
	require.NoError(t, err)
	var closed bool
	require.NoError(t, db.QueryRow(`SELECT sent_at IS NOT NULL FROM prompt_audit_enforcement_notifications WHERE id=$1`, exhaustedID).Scan(&closed))
	require.True(t, closed)
}
