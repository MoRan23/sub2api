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

func TestCodexStateOSPostgresCASAndLeasesStayInSlot(t *testing.T) {
	ctx := context.Background()
	windows := createCodexStateFixture(t)
	linux := windows
	linux.OSFamily, linux.Generation = "linux", "00000000-0000-4000-8000-000000000003"
	_, err := integrationDB.ExecContext(ctx, `INSERT INTO account_openai_oauth_os_credentials
		(account_id,os_family,credentials,status,state_generation,authorization_generation,credential_epoch)
		SELECT account_id,'linux','{}','authorized',$2,authorization_generation,credential_epoch
		FROM account_openai_oauth_credentials WHERE account_id=$1`, windows.OwnerAccountID, linux.Generation)
	require.NoError(t, err)
	repo := NewOpenAICodexStateRepository(integrationDB, integrationRedis)
	now := time.Now().UTC()
	w, err := repo.BeginBusiness(ctx, windows, "same-attempt", now, now.Add(time.Minute))
	require.NoError(t, err)
	require.NotNil(t, w)
	l, err := repo.BeginBusiness(ctx, linux, "same-attempt", now, now.Add(time.Minute))
	require.NoError(t, err)
	require.NotNil(t, l)
	require.NoError(t, repo.EndBusiness(ctx, windows, "same-attempt"))
	active, err := repo.HasBusiness(ctx, linux, now)
	require.NoError(t, err)
	require.True(t, active)
	w.EncryptedToken, w.ModelPolicyRevision = "windows-ciphertext", codexStateModelPolicyRevisionForTest(t)
	updated, err := repo.SaveCAS(ctx, *w, w.Version)
	require.NoError(t, err)
	require.True(t, updated)
	l, err = repo.Get(ctx, linux)
	require.NoError(t, err)
	require.Empty(t, l.EncryptedToken)
	_, err = integrationDB.ExecContext(ctx, `UPDATE account_openai_oauth_os_credentials SET state_generation=gen_random_uuid() WHERE account_id=$1 AND os_family='linux'`, windows.OwnerAccountID)
	require.NoError(t, err)
	w, err = repo.Get(ctx, windows)
	require.NoError(t, err)
	require.Equal(t, "windows-ciphertext", w.EncryptedToken)
	l.ModelPolicyRevision = w.ModelPolicyRevision
	updated, err = repo.SaveCAS(ctx, *l, l.Version)
	require.NoError(t, err)
	require.False(t, updated)
}

func TestCodexStateOSMigrationKeepsOnlyLegacyDefault(t *testing.T) {
	ctx := context.Background()
	tx, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = tx.Rollback() })
	_, err = tx.ExecContext(ctx, `CREATE TEMP TABLE accounts(id bigint PRIMARY KEY,extra jsonb);
	CREATE TEMP TABLE account_openai_oauth_os_profiles(account_id bigint,os_family text,is_default boolean);
	CREATE TEMP TABLE account_openai_oauth_os_credentials(account_id bigint,os_family text,state_generation uuid,source text);
	CREATE TEMP TABLE openai_codex_state(owner_account_id bigint REFERENCES accounts(id),model text,generation text,next_collect_at timestamptz,last_error text,PRIMARY KEY(owner_account_id,model));
	CREATE TEMP TABLE openai_codex_state_business_leases(owner_account_id bigint,model text,generation text,attempt_id text,PRIMARY KEY(owner_account_id,model,generation,attempt_id),FOREIGN KEY(owner_account_id,model) REFERENCES openai_codex_state(owner_account_id,model) ON DELETE CASCADE);
	INSERT INTO accounts VALUES(1,'{"codex_turn_state_generation":"old-generation"}');
	INSERT INTO account_openai_oauth_os_profiles VALUES(1,'macos',true),(1,'windows',false),(1,'linux',false);
	INSERT INTO account_openai_oauth_os_credentials VALUES(1,'macos','00000000-0000-4000-8000-000000000001','legacy_migration');
	INSERT INTO openai_codex_state VALUES(1,'gpt-5.4','old-generation',NOW()+INTERVAL '1 hour','collector_rate_limited');
	INSERT INTO openai_codex_state_business_leases VALUES(1,'gpt-5.4','old-generation','old-attempt');`)
	require.NoError(t, err)
	migration, err := os.ReadFile("../../migrations/252_openai_codex_state_os_credentials.sql")
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, string(migration))
	require.NoError(t, err)
	var family, generation string
	var count int
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT os_family,generation FROM openai_codex_state`).Scan(&family, &generation))
	require.Equal(t, "macos", family)
	require.Equal(t, "00000000-0000-4000-8000-000000000001", generation)
	var cooldown time.Time
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT codex_turn_state_retry_after FROM accounts WHERE id=1`).Scan(&cooldown))
	require.True(t, cooldown.After(time.Now()))
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT count(*) FROM openai_codex_state`).Scan(&count))
	require.Equal(t, 1, count)
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT os_family,generation FROM openai_codex_state_business_leases`).Scan(&family, &generation))
	require.Equal(t, "macos", family)
	require.Equal(t, "00000000-0000-4000-8000-000000000001", generation)
	_, err = tx.ExecContext(ctx, `INSERT INTO openai_codex_state(owner_account_id,os_family,model,generation) VALUES(1,'linux','gpt-5.4','linux-generation')`)
	require.NoError(t, err)
}

func TestCodexStateOSCooldownSurvivesSlotLifecycleAndSuccess(t *testing.T) {
	ctx := context.Background()
	key := createCodexStateFixture(t)
	repo := NewOpenAICodexStateRepository(integrationDB, integrationRedis)
	cooldowns := repo.(service.CodexTurnStateCooldownRepository)
	now := time.Now().UTC().Truncate(time.Microsecond)
	w, err := repo.BeginBusiness(ctx, key, "cooldown", now, now.Add(time.Minute))
	require.NoError(t, err)
	w.ModelPolicyRevision = codexStateModelPolicyRevisionForTest(t)
	w.LastError, w.NextCollectAt = "collector_rate_limited", now.Add(2*time.Hour)
	ok, err := repo.SaveCAS(ctx, *w, w.Version)
	require.NoError(t, err)
	require.True(t, ok)
	_, err = integrationDB.ExecContext(ctx, `UPDATE account_openai_oauth_credentials SET status='unauthorized' WHERE account_id=$1`, key.OwnerAccountID)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `UPDATE account_openai_oauth_os_credentials SET state_generation=gen_random_uuid(),status='unauthorized' WHERE account_id=$1`, key.OwnerAccountID)
	require.NoError(t, err)
	require.NoError(t, cooldowns.ExtendCollectorCooldown(ctx, key.OwnerAccountID, now.Add(time.Hour)))
	until, err := cooldowns.GetCollectorCooldowns(ctx, []int64{key.OwnerAccountID})
	require.NoError(t, err)
	require.Equal(t, w.NextCollectAt, until[key.OwnerAccountID])
	// Reauthorization and successful/unknown-error state writes may not clear it.
	_, err = integrationDB.ExecContext(ctx, `UPDATE account_openai_oauth_credentials SET status='authorized' WHERE account_id=$1`, key.OwnerAccountID)
	require.NoError(t, err)
	require.NoError(t, integrationDB.QueryRowContext(ctx, `UPDATE account_openai_oauth_os_credentials SET state_generation=gen_random_uuid(),status='authorized' WHERE account_id=$1 AND os_family=$2 RETURNING state_generation::text`, key.OwnerAccountID, key.OSFamily).Scan(&key.Generation))
	fresh, err := repo.BeginBusiness(ctx, key, "reauthorized", now, now.Add(time.Minute))
	require.NoError(t, err)
	for _, reason := range []string{"", "collection_failed"} {
		fresh.LastError, fresh.NextCollectAt = reason, time.Time{}
		fresh.ModelPolicyRevision = w.ModelPolicyRevision
		ok, err := repo.SaveCAS(ctx, *fresh, fresh.Version)
		require.NoError(t, err)
		require.True(t, ok)
		fresh.Version++
	}
	until, err = cooldowns.GetCollectorCooldowns(ctx, []int64{key.OwnerAccountID})
	require.NoError(t, err)
	require.Equal(t, w.NextCollectAt, until[key.OwnerAccountID])
	active, err := repo.ListActive(ctx, now.Add(-time.Minute), 1000)
	require.NoError(t, err)
	for _, record := range active {
		require.NotEqual(t, key.OwnerAccountID, record.OwnerAccountID)
	}
}
