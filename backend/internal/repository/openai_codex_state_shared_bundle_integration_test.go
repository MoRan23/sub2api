//go:build integration

package repository

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestCodexStateSharedBundlePublishesAtomicallyAndFencesReenable(t *testing.T) {
	ctx := context.Background()
	key := createCodexStateFixture(t)
	repo := NewOpenAICodexStateRepository(integrationDB, integrationRedis)
	now := time.Now().UTC().Truncate(time.Microsecond)
	record, err := repo.BeginBusiness(ctx, key, "windows", now, now.Add(time.Minute))
	require.NoError(t, err)
	require.NotNil(t, record)
	record.ModelPolicyRevision = codexStateModelPolicyRevisionForTest(t)
	record.EncryptedToken, record.EncryptedCookieBundle = "ticket-a", "cookies-a"
	record.IssuedAt, record.ExpiresAt = now, now.Add(service.CodexTurnStateLifetime)
	cookieExpiry := now.Add(time.Minute)
	record.CookieBundleExpiresAt = &cookieExpiry
	saved, err := repo.SaveCAS(ctx, *record, record.Version)
	require.NoError(t, err)
	require.True(t, saved)
	key.OSFamily = "macos"
	current, err := repo.Get(ctx, key)
	require.NoError(t, err)
	require.Equal(t, "ticket-a", current.EncryptedToken)
	require.Equal(t, "cookies-a", current.EncryptedCookieBundle)
	require.Equal(t, &cookieExpiry, current.CookieBundleExpiresAt)
	stale := *record
	stale.EncryptedToken, stale.EncryptedCookieBundle = "ticket-b", "cookies-b"
	saved, err = repo.SaveCAS(ctx, stale, stale.Version)
	require.NoError(t, err)
	require.False(t, saved)
	current.ModelPolicyRevision = record.ModelPolicyRevision
	wrongAuthorization := *current
	wrongAuthorization.AuthorizationGeneration = "00000000-0000-4000-8000-000000000099"
	saved, err = repo.SaveCAS(ctx, wrongAuthorization, wrongAuthorization.Version)
	require.NoError(t, err)
	require.False(t, saved)
	stillCurrent, err := repo.Get(ctx, key)
	require.NoError(t, err)
	require.Equal(t, "ticket-a", stillCurrent.EncryptedToken)
	require.Equal(t, "cookies-a", stillCurrent.EncryptedCookieBundle)
	// Reopening the switch must not resurrect a response prepared before disable.
	_, err = integrationDB.ExecContext(ctx, `UPDATE accounts SET extra=jsonb_set(extra,'{codex_turn_state,enabled}','false') WHERE id=$1`, key.OwnerAccountID)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `UPDATE accounts SET extra=jsonb_set(extra,'{codex_turn_state,enabled}','true') WHERE id=$1`, key.OwnerAccountID)
	require.NoError(t, err)
	saved, err = repo.SaveCAS(ctx, *current, current.Version)
	require.NoError(t, err)
	require.False(t, saved)
	missing, err := repo.Get(ctx, key)
	require.NoError(t, err)
	require.Nil(t, missing)
}

func TestCodexStateAuthorizedMetadataCannotReplaceAccountCredentials(t *testing.T) {
	ctx := context.Background()
	key := createCodexStateFixture(t)
	key.OSFamily = ""
	repo := NewOpenAICodexStateRepository(integrationDB, integrationRedis)
	now := time.Now().UTC()
	record, err := repo.BeginBusiness(ctx, key, "before-clear", now, now.Add(time.Minute))
	require.NoError(t, err)
	require.NotNil(t, record)
	_, err = integrationDB.ExecContext(ctx, `UPDATE accounts SET credentials='{}' WHERE id=$1`, key.OwnerAccountID)
	require.NoError(t, err)
	var status string
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT status FROM account_openai_oauth_credentials WHERE account_id=$1`, key.OwnerAccountID).Scan(&status))
	require.Equal(t, "authorized", status, "the test deliberately leaves stale metadata")
	missing, err := repo.Get(ctx, key)
	require.NoError(t, err)
	require.Nil(t, missing)
	started, err := repo.BeginBusiness(ctx, key, "after-clear", now, now.Add(time.Minute))
	require.NoError(t, err)
	require.Nil(t, started)
	record.ModelPolicyRevision = codexStateModelPolicyRevisionForTest(t)
	saved, err := repo.SaveCAS(ctx, *record, record.Version)
	require.NoError(t, err)
	require.False(t, saved)
}

func TestCodexStateAuthorizationFenceCannotRelabelPreviousBundle(t *testing.T) {
	ctx := context.Background()
	key := createCodexStateFixture(t)
	repo := NewOpenAICodexStateRepository(integrationDB, integrationRedis)
	now := time.Now().UTC()
	record, err := repo.BeginBusiness(ctx, key, "original", now, now.Add(time.Minute))
	require.NoError(t, err)
	record.EncryptedToken, record.EncryptedCookieBundle = "original-ticket", "original-cookies"
	record.ModelPolicyRevision = codexStateModelPolicyRevisionForTest(t)
	saved, err := repo.SaveCAS(ctx, *record, record.Version)
	require.NoError(t, err)
	require.True(t, saved)
	// Exercise the independent authorization barrier even if a future writer
	// fails to rotate the state generation along with the authorization.
	_, err = integrationDB.ExecContext(ctx, `UPDATE account_openai_oauth_credentials SET authorization_generation=gen_random_uuid() WHERE account_id=$1`, key.OwnerAccountID)
	require.NoError(t, err)
	missing, err := repo.Get(ctx, key)
	require.NoError(t, err)
	require.Nil(t, missing)
	fresh, err := repo.BeginBusiness(ctx, key, "replacement", now, now.Add(time.Minute))
	require.NoError(t, err)
	require.NotNil(t, fresh)
	require.Empty(t, fresh.EncryptedToken)
	require.Empty(t, fresh.EncryptedCookieBundle)
	require.Nil(t, fresh.CookieBundleExpiresAt)
	require.NotEqual(t, record.AuthorizationGeneration, fresh.AuthorizationGeneration)
	require.Greater(t, fresh.Version, record.Version+1)
}

func TestCodexStateSharedMigrationPreservesEvidenceAndRealCooldown(t *testing.T) {
	ctx := context.Background()
	tx, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = tx.Rollback() })
	_, err = tx.ExecContext(ctx, `CREATE TEMP TABLE accounts (
		id BIGINT PRIMARY KEY, deleted_at TIMESTAMPTZ, parent_account_id BIGINT,
		platform TEXT DEFAULT 'openai', type TEXT DEFAULT 'oauth',
		extra JSONB DEFAULT '{"codex_turn_state":{"enabled":true}}', codex_turn_state_retry_after TIMESTAMPTZ);
	CREATE TEMP TABLE account_openai_oauth_credentials (
		account_id BIGINT PRIMARY KEY, state_generation UUID, authorization_generation UUID, status TEXT DEFAULT 'authorized');
	CREATE TEMP TABLE account_openai_oauth_os_credentials (
		account_id BIGINT, os_family TEXT, state_generation UUID, previous_state_generation UUID, authorization_generation UUID);
	CREATE TEMP TABLE openai_codex_state (LIKE public.openai_codex_state INCLUDING DEFAULTS);
	ALTER TABLE openai_codex_state DROP COLUMN source_os, DROP COLUMN authorization_generation,
		DROP COLUMN encrypted_cookie_bundle, DROP COLUMN cookie_bundle_expires_at,
		ADD COLUMN os_family TEXT NOT NULL,
		ADD PRIMARY KEY(owner_account_id,os_family,model);
	CREATE TEMP TABLE openai_codex_state_business_leases (
		owner_account_id BIGINT, os_family TEXT, model TEXT, generation TEXT, attempt_id TEXT, lease_until TIMESTAMPTZ,
		PRIMARY KEY(owner_account_id,os_family,model,generation,attempt_id),
		FOREIGN KEY(owner_account_id,os_family,model) REFERENCES openai_codex_state(owner_account_id,os_family,model) ON DELETE CASCADE);
	CREATE TEMP TABLE openai_http_cookies (encrypted_entry TEXT);
	INSERT INTO accounts(id,codex_turn_state_retry_after) VALUES (1,NOW()+INTERVAL '4 hours'),(2,NULL),(3,NULL),(4,NULL),(5,NULL),(6,NULL);
	UPDATE accounts SET extra='{"codex_turn_state":{"enabled":false}}' WHERE id=3;
	INSERT INTO account_openai_oauth_credentials(account_id,state_generation,authorization_generation)
	SELECT id,'00000000-0000-4000-8000-000000000001','00000000-0000-4000-8000-000000000002' FROM accounts;
	INSERT INTO account_openai_oauth_os_credentials
	SELECT id,'windows','00000000-0000-4000-8000-000000000001','00000000-0000-4000-8000-000000000010','00000000-0000-4000-8000-000000000002'
	FROM accounts WHERE id IN (4,5,6);
	INSERT INTO openai_http_cookies VALUES('old-cookie');
	INSERT INTO openai_codex_state(owner_account_id,os_family,model,generation,version,encrypted_token,
		last_business_at,last_collected_at,history_proof_observed_at,collector_proxy_id,collector_extended_count,
		last_collector_proxy_id,updated_at,next_collect_at,last_error,collection_status,collector_attempt_id)
	VALUES
	(1,'windows','gpt-5.4','old-w',5,'old-ticket-w',NOW()-INTERVAL '1 minute',NOW()-INTERVAL '2 minutes',NOW()-INTERVAL '1 minute',11,2,10,NOW()-INTERVAL '3 seconds',NOW()+INTERVAL '3 hours','collector_rate_limited','backoff',NULL),
	(1,'macos','gpt-5.4','old-m',9,'old-ticket-m',NOW()-INTERVAL '2 minutes',NOW()-INTERVAL '1 minute',NOW()-INTERVAL '2 minutes',22,2,20,NOW()-INTERVAL '2 seconds',NULL,'','',NULL),
	(1,'linux','gpt-5.4','old-l',2,'old-ticket-l',NOW()-INTERVAL '3 minutes',NOW()-INTERVAL '3 minutes',NOW()-INTERVAL '3 minutes',33,1,30,NOW()-INTERVAL '1 second',NOW()+INTERVAL '6 hours','collector_rate_limited','collecting','00000000-0000-4000-8000-000000000003'),
	(2,'windows','gpt-5.4','old-w',1,'idle',NOW()-INTERVAL '2 hours',NULL,NULL,11,0,NULL,NOW(),NOW()+INTERVAL '3 hours','account_cooldown','backoff',NULL),
	(2,'linux','gpt-5.4','old-l',1,'idle',NOW()-INTERVAL '2 hours',NULL,NULL,22,0,NULL,NOW(),NOW()+INTERVAL '6 hours','collector_rate_limited','collecting','00000000-0000-4000-8000-000000000003'),
	(3,'windows','gpt-5.4','old-w',1,'disabled',NOW(),NULL,NULL,NULL,0,NULL,NOW(),NULL,'','',NULL);
	INSERT INTO openai_codex_state(owner_account_id,os_family,model,generation,last_business_at,collector_paused,last_error,collection_status)
	VALUES (4,'windows','gpt-5.4','00000000-0000-4000-8000-000000000010',NOW(),true,'collector_auth_rejected','paused'),
	(5,'windows','gpt-5.4','00000000-0000-4000-8000-000000000009',NOW(),true,'collector_auth_rejected','paused'),
	(6,'windows','gpt-5.4','00000000-0000-4000-8000-000000000010',NOW()-INTERVAL '2 hours',true,'collector_auth_rejected','paused');
	INSERT INTO openai_codex_state_business_leases VALUES(1,'windows','gpt-5.4','old-w','old-attempt',NOW()+INTERVAL '1 minute');`)
	require.NoError(t, err)
	var now time.Time
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT NOW()`).Scan(&now))
	migration, err := os.ReadFile("../../migrations/257_openai_codex_state_shared_bundle.sql")
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, string(migration))
	require.NoError(t, err)
	var count int
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT count(*) FROM openai_codex_state`).Scan(&count))
	require.Equal(t, 6, count)
	var sourceOS, generation, auth, token, cookies, demand, attempt, status string
	var version, proxy, previous int64
	var extended int
	var business, collected, history, next time.Time
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT source_os,generation,authorization_generation::text,version,
		encrypted_token,encrypted_cookie_bundle,demand_reason,COALESCE(collector_attempt_id::text,''),collection_status,
		collector_proxy_id,collector_extended_count,last_collector_proxy_id,last_business_at,last_collected_at,history_proof_observed_at,next_collect_at
		FROM openai_codex_state WHERE owner_account_id=1`).Scan(&sourceOS, &generation, &auth, &version,
		&token, &cookies, &demand, &attempt, &status, &proxy, &extended, &previous, &business, &collected, &history, &next))
	require.Equal(t, "linux", sourceOS)
	require.EqualValues(t, 10, version)
	require.Equal(t, "00000000-0000-4000-8000-000000000001", generation)
	require.Equal(t, "00000000-0000-4000-8000-000000000002", auth)
	require.Empty(t, token)
	require.Empty(t, cookies)
	require.Empty(t, attempt)
	require.Equal(t, "cache_miss", demand)
	require.Equal(t, "backoff", status)
	require.EqualValues(t, 33, proxy)
	require.Equal(t, 1, extended)
	require.EqualValues(t, 30, previous)
	for _, stamp := range []time.Time{business, collected, history} {
		require.Equal(t, now.Add(-time.Minute), stamp)
	}
	require.Equal(t, now.Add(4*time.Hour), next)
	var cooldown time.Time
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT codex_turn_state_retry_after FROM accounts WHERE id=2`).Scan(&cooldown))
	require.Equal(t, now.Add(3*time.Hour), cooldown, "a collecting reservation is not a real cooldown")
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT count(*) FROM openai_codex_state WHERE owner_account_id IN (2,3) AND demand_reason<>''`).Scan(&count))
	require.Zero(t, count, "idle and disabled accounts do not gain demand")
	for _, owner := range []int{4, 6} {
		var paused bool
		var reason string
		require.NoError(t, tx.QueryRowContext(ctx, `SELECT collector_paused,collection_status,last_error FROM openai_codex_state WHERE owner_account_id=$1`, owner).Scan(&paused, &status, &reason))
		require.True(t, paused, "a current authorization's permanent collector pause survives both active and idle migration")
		require.Equal(t, "paused", status)
		require.Equal(t, "collector_auth_rejected", reason)
	}
	var stalePaused bool
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT collector_paused,collection_status FROM openai_codex_state WHERE owner_account_id=5`).Scan(&stalePaused, &status))
	require.False(t, stalePaused, "an obsolete authorization's collector pause must not pause the current grant")
	require.Equal(t, "pending", status)
	for _, table := range []string{"openai_codex_state_business_leases", "openai_http_cookies"} {
		require.NoError(t, tx.QueryRowContext(ctx, `SELECT count(*) FROM `+table).Scan(&count))
		require.Zero(t, count)
	}
	// The rebuilt FK and PK identify only the shared owner/model state.
	_, err = tx.ExecContext(ctx, `INSERT INTO openai_codex_state_business_leases(owner_account_id,model,generation,attempt_id,lease_until,source_os)
		VALUES(1,'gpt-5.4','new','new-attempt',NOW()+INTERVAL '1 minute','macos'); DELETE FROM openai_codex_state WHERE owner_account_id=1;`)
	require.NoError(t, err)
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT count(*) FROM openai_codex_state_business_leases`).Scan(&count))
	require.Zero(t, count)
}
