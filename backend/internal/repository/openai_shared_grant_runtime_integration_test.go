//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openaicookies"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestCodexStateSharedGrantAuthorityRejectsAuthorizedMetadataAfterRevocation(t *testing.T) {
	ctx := context.Background()
	key := createCodexStateFixture(t)
	repo := NewOpenAICodexStateRepository(integrationDB, integrationRedis)
	now := time.Now().UTC()
	record, err := repo.BeginBusiness(ctx, key, "shared-auth", now, now.Add(time.Minute))
	require.NoError(t, err)
	require.NotNil(t, record)
	_, err = integrationDB.ExecContext(ctx, `UPDATE account_openai_oauth_credentials SET status='unauthorized' WHERE account_id=$1`, key.OwnerAccountID)
	require.NoError(t, err)
	var metadataStatus string
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT status FROM account_openai_oauth_os_credentials WHERE account_id=$1 AND os_family=$2`, key.OwnerAccountID, key.OSFamily).Scan(&metadataStatus))
	require.Equal(t, "authorized", metadataStatus, "stale metadata must not grant authority")
	loaded, err := repo.Get(ctx, key)
	require.NoError(t, err)
	require.Nil(t, loaded)
	listed, err := repo.ListByAccount(ctx, key.OwnerAccountID)
	require.NoError(t, err)
	require.Empty(t, listed)
	record.ModelPolicyRevision = codexStateModelPolicyRevisionForTest(t)
	saved, err := repo.SaveCAS(ctx, *record, record.Version)
	require.NoError(t, err)
	require.False(t, saved)
	started, err := repo.BeginBusiness(ctx, key, "stale-auth", now, now.Add(time.Minute))
	require.NoError(t, err)
	require.Nil(t, started)
}

func TestCodexHistoryDemandReadsPlanFromSharedGrantWithEmptyOSMetadata(t *testing.T) {
	ctx := context.Background()
	key := createCodexStateFixture(t)
	var empty bool
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT credentials='{}'::jsonb FROM account_openai_oauth_os_credentials WHERE account_id=$1 AND os_family=$2`, key.OwnerAccountID, key.OSFamily).Scan(&empty))
	require.True(t, empty)
	_, err := integrationDB.ExecContext(ctx, `UPDATE accounts SET extra=jsonb_set(extra,'{codex_turn_state,account_type}','"auto"') WHERE id=$1`, key.OwnerAccountID)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `UPDATE account_openai_oauth_credentials SET credentials=jsonb_set(credentials,'{plan_type}','"business"') WHERE account_id=$1`, key.OwnerAccountID)
	require.NoError(t, err)
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT state_generation::text FROM account_openai_oauth_os_credentials WHERE account_id=$1 AND os_family=$2`, key.OwnerAccountID, key.OSFamily).Scan(&key.Generation))
	now := time.Now().UTC().Truncate(time.Microsecond)
	proof := codexHistoryProofFixture(now)
	proof.OwnerAccountID, proof.OSFamily, proof.Model, proof.Generation = key.OwnerAccountID, key.OSFamily, key.Model, key.Generation
	proof.ModelPolicyRevision = codexStateModelPolicyRevisionForTest(t)
	proof.AccountType, proof.TokenLength, proof.CipherBlocks = "team_business", 356, 13
	repo := NewOpenAICodexStateRepository(integrationDB, integrationRedis).(service.CodexTurnStateHistoryRepository)
	created, err := repo.CreateHistoryDemand(ctx, proof, now)
	require.NoError(t, err)
	require.True(t, created)
}

func TestOpenAIHTTPCookieStoreSharedGrantRetainsThreeOSBuckets(t *testing.T) {
	ctx := context.Background()
	windows, encryptor := createOpenAIHTTPCookieFixture(t)
	for _, family := range []string{"macos", "linux"} {
		_, err := integrationDB.ExecContext(ctx, `INSERT INTO account_openai_oauth_os_credentials
		(account_id,os_family,credentials,status,authorization_generation,credential_epoch)
		SELECT account_id,$2,'{}',status,authorization_generation,credential_epoch FROM account_openai_oauth_credentials WHERE account_id=$1`, windows.OwnerAccountID, family)
		require.NoError(t, err)
	}
	store := NewOpenAIHTTPCookieStore(integrationDB, encryptor)
	for _, family := range []string{"windows", "macos", "linux"} {
		scope := windows
		scope.OSFamily = family
		entry := openAIHTTPCookieEntry("__oailb", "/")
		entry.Value = "synthetic-" + family
		_, err := store.Merge(ctx, scope, []openaicookies.Mutation{{Key: entry.Key, Entry: &entry}})
		require.NoError(t, err)
	}
	for _, family := range []string{"windows", "macos", "linux"} {
		scope := windows
		scope.OSFamily = family
		snapshot, err := store.Load(ctx, scope)
		require.NoError(t, err)
		require.Len(t, snapshot.Entries, 1)
		require.True(t, snapshot.Entries[0].Value == "synthetic-"+family)
	}
	_, err := integrationDB.ExecContext(ctx, `UPDATE account_openai_oauth_credentials SET status='unauthorized' WHERE account_id=$1`, windows.OwnerAccountID)
	require.NoError(t, err)
	for _, family := range []string{"windows", "macos", "linux"} {
		scope := windows
		scope.OSFamily = family
		_, err = store.Load(ctx, scope)
		require.ErrorIs(t, err, openaicookies.ErrStaleScope)
	}
}
