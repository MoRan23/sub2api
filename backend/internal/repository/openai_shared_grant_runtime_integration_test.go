//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestCodexStateSharedGrantAuthorityRejectsAuthorizedMetadataAfterRevocation(t *testing.T) {
	ctx := context.Background()
	key := createCodexStateFixture(t)
	key.OSFamily = ""
	repo := NewOpenAICodexStateRepository(integrationDB, integrationRedis)
	now := time.Now().UTC()
	record, err := repo.BeginBusiness(ctx, key, "shared-auth", now, now.Add(time.Minute))
	require.NoError(t, err)
	require.NotNil(t, record)
	_, err = integrationDB.ExecContext(ctx, `UPDATE account_openai_oauth_credentials SET status='unauthorized' WHERE account_id=$1`, key.OwnerAccountID)
	require.NoError(t, err)
	var metadataStatus string
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT status FROM account_openai_oauth_os_credentials WHERE account_id=$1 AND os_family='windows'`, key.OwnerAccountID).Scan(&metadataStatus))
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

func TestCodexHistoryDemandReadsPlanFromAccountWithTokenFreeMetadata(t *testing.T) {
	ctx := context.Background()
	key := createCodexStateFixture(t)
	var tokenColumns int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT count(*) FROM information_schema.columns
		WHERE table_schema='public' AND table_name IN ('account_openai_oauth_credentials','account_openai_oauth_os_credentials') AND column_name='credentials'`).Scan(&tokenColumns))
	require.Zero(t, tokenColumns)
	_, err := integrationDB.ExecContext(ctx, `UPDATE accounts SET extra=jsonb_set(extra,'{codex_turn_state,account_type}','"auto"') WHERE id=$1`, key.OwnerAccountID)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `UPDATE accounts SET credentials=jsonb_set(credentials,'{plan_type}','"business"') WHERE id=$1`, key.OwnerAccountID)
	require.NoError(t, err)
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT state_generation::text FROM account_openai_oauth_credentials WHERE account_id=$1`, key.OwnerAccountID).Scan(&key.Generation))
	key.OSFamily = ""
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
