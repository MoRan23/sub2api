//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestCodexStatePostgresProxyChangeDefersOnlyValidTargets(t *testing.T) {
	ctx := context.Background()
	key := createCodexStateFixture(t)
	repo := NewOpenAICodexStateRepository(integrationDB, integrationRedis)
	_, err := integrationDB.ExecContext(ctx, `UPDATE accounts SET extra=jsonb_set(extra,
		'{codex_turn_state,collector_proxy_id}', '42'::jsonb) WHERE id=$1`, key.OwnerAccountID)
	require.NoError(t, err)
	now := time.Now().UTC().Truncate(time.Microsecond)
	models := []string{"retained", "expired", "anomaly", "missing-time", "renewal"}
	revision := installCodexStateModelPolicyFixture(t, models)
	for _, model := range models {
		modelKey := key
		modelKey.Model = model
		record, err := repo.BeginBusiness(ctx, modelKey, "seed", now, now.Add(time.Minute))
		require.NoError(t, err)
		require.NoError(t, repo.MarkBusinessSent(ctx, modelKey, now))
		require.NoError(t, repo.EndBusiness(ctx, modelKey, "seed"))
		record.ModelPolicyRevision = revision
		record.EncryptedToken, record.Shape = "synthetic-encrypted-target", service.CodexTurnStateShapeTarget
		record.IssuedAt, record.ExpiresAt = now.Add(-58*time.Minute), now.Add(2*time.Minute)
		record.TokenLength, record.CipherBlocks = 292, 10
		record.CollectionStatus, record.CollectionReason = "idle", "collector_proxy_changed"
		switch model {
		case "expired":
			record.IssuedAt, record.ExpiresAt = now.Add(-61*time.Minute), now.Add(-time.Minute)
		case "anomaly":
			record.EncryptedToken, record.Shape, record.DemandReason = "", service.CodexTurnStateShapeExtended, "extended_shape"
			record.TokenLength, record.CipherBlocks = 312, 11
		case "missing-time":
			record.IssuedAt, record.ExpiresAt = time.Time{}, time.Time{}
			record.DemandReason = "extended_shape"
		case "renewal":
			record.CollectionReason = ""
		}
		ok, err := repo.SaveCAS(ctx, *record, record.Version)
		require.NoError(t, err)
		require.True(t, ok)
	}
	active, err := repo.ListActive(ctx, now.Add(-service.CodexTurnStateActiveWindow), 1000)
	require.NoError(t, err)
	var due []string
	for _, record := range active {
		if record.OwnerAccountID == key.OwnerAccountID {
			due = append(due, record.Model)
		}
	}
	require.ElementsMatch(t, []string{"expired", "anomaly", "missing-time", "renewal"}, due)
}

func TestCodexStatePostgresProxyChangeSurvivesIdleBusinessResume(t *testing.T) {
	ctx := context.Background()
	key := createCodexStateFixture(t)
	repo := NewOpenAICodexStateRepository(integrationDB, integrationRedis)
	now := time.Now().UTC().Truncate(time.Microsecond)
	record, err := repo.BeginBusiness(ctx, key, "seed", now, now.Add(time.Minute))
	require.NoError(t, err)
	require.NoError(t, repo.EndBusiness(ctx, key, "seed"))
	record.ModelPolicyRevision = codexStateModelPolicyRevisionForTest(t)
	record.EncryptedToken, record.Shape = "synthetic-encrypted-target", service.CodexTurnStateShapeTarget
	record.IssuedAt, record.ExpiresAt = now.Add(-58*time.Minute), now.Add(2*time.Minute)
	record.TokenLength, record.CipherBlocks = 292, 10
	record.LastBusinessAt = now.Add(-40 * time.Minute)
	record.CollectionStatus, record.CollectionReason = "idle", "collector_proxy_changed"
	ok, err := repo.SaveCAS(ctx, *record, record.Version)
	require.NoError(t, err)
	require.True(t, ok)
	resumed, err := repo.BeginBusiness(ctx, key, "resume", now, now.Add(time.Minute))
	require.NoError(t, err)
	require.NotNil(t, resumed)
	require.Equal(t, "collector_proxy_changed", resumed.CollectionReason)
	require.Equal(t, record.EncryptedToken, resumed.EncryptedToken)
	require.Equal(t, record.ExpiresAt, resumed.ExpiresAt)
	require.Empty(t, resumed.DemandReason)
	require.Equal(t, record.LastBusinessAt, resumed.LastBusinessAt, "preparation does not extend the business activity window")
}
