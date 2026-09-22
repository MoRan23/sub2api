//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestCodexHistoryDemandPostgresConsumesOnceWithoutExtendingBusinessActivity(t *testing.T) {
	ctx := context.Background()
	key := createCodexStateFixture(t)
	r := &openAICodexStateRepository{db: integrationDB, rdb: integrationRedis}
	now := time.Now().UTC().Truncate(time.Microsecond)
	proof := codexHistoryProofFixture(now)
	proof.OwnerAccountID, proof.Model, proof.Generation = key.OwnerAccountID, key.Model, key.Generation
	proof.ModelPolicyRevision = codexStateModelPolicyRevisionForTest(t)
	_, err := integrationDB.ExecContext(ctx, `UPDATE accounts SET extra=extra || '{"codex_turn_state_credential_epoch":"credential-1"}'::jsonb WHERE id=$1`, key.OwnerAccountID)
	require.NoError(t, err)
	created, err := r.CreateHistoryDemand(ctx, proof, now)
	require.NoError(t, err)
	require.True(t, created)
	record, err := r.Get(ctx, key)
	require.NoError(t, err)
	require.Equal(t, proof.BusinessAt, record.LastBusinessAt)
	require.Equal(t, proof.ObservedAt, record.HistoryProofObservedAt)
	require.Equal(t, "extended_shape", record.DemandReason)
	initialVersion := record.Version
	created, err = r.CreateHistoryDemand(ctx, proof, now)
	require.NoError(t, err)
	require.False(t, created)
	loaded, err := r.Get(ctx, key)
	require.NoError(t, err)
	require.Equal(t, initialVersion, loaded.Version)
	// Completing a demand never resets its consumption proof.
	record.DemandReason, record.DemandAt = "", time.Time{}
	record.ModelPolicyRevision = proof.ModelPolicyRevision
	saved, err := r.SaveCAS(ctx, *record, record.Version)
	require.NoError(t, err)
	require.True(t, saved)
	created, err = r.CreateHistoryDemand(ctx, proof, now)
	require.NoError(t, err)
	require.False(t, created)
	loaded, err = r.Get(ctx, key)
	require.NoError(t, err)
	require.Empty(t, loaded.DemandReason)
	require.Equal(t, proof.BusinessAt, loaded.LastBusinessAt)
	// Renewing an in-flight lease cannot manufacture newer activity either.
	_, err = r.BeginBusiness(ctx, key, "heartbeat", now, now.Add(time.Minute))
	require.NoError(t, err)
	loaded, err = r.Get(ctx, key)
	require.NoError(t, err)
	require.Equal(t, proof.BusinessAt, loaded.LastBusinessAt)
}

func TestCodexHistoryDemandPostgresHealthyCacheConsumesProofAndFencesSlowWriter(t *testing.T) {
	ctx := context.Background()
	key := createCodexStateFixture(t)
	r := &openAICodexStateRepository{db: integrationDB, rdb: integrationRedis}
	now := time.Now().UTC().Truncate(time.Microsecond)
	proof := codexHistoryProofFixture(now)
	proof.OwnerAccountID, proof.Model, proof.Generation = key.OwnerAccountID, key.Model, key.Generation
	proof.ModelPolicyRevision = codexStateModelPolicyRevisionForTest(t)
	_, err := integrationDB.ExecContext(ctx, `UPDATE accounts SET extra=extra || '{"codex_turn_state_credential_epoch":"credential-1"}'::jsonb WHERE id=$1`, key.OwnerAccountID)
	require.NoError(t, err)
	record, err := r.BeginBusiness(ctx, key, "natural", proof.BusinessAt, now.Add(time.Minute))
	require.NoError(t, err)
	require.NoError(t, r.EndBusiness(ctx, key, "natural"))
	record.EncryptedToken, record.TokenLength, record.CipherBlocks = "opaque-encrypted-target", 292, 10
	record.BundleBinding = service.CodexTurnStateBundleBinding{WireMode: "responses", EgressKind: "direct"}
	record.IssuedAt, record.ExpiresAt = now.Add(-time.Minute), now.Add(service.CodexTurnStateLifetime-time.Minute)
	record.ModelPolicyRevision = proof.ModelPolicyRevision
	saved, err := r.SaveCAS(ctx, *record, record.Version)
	require.NoError(t, err)
	require.True(t, saved)
	before, err := r.Get(ctx, key)
	require.NoError(t, err)
	created, err := r.CreateHistoryDemand(ctx, proof, now)
	require.NoError(t, err)
	require.False(t, created, "history cannot supersede a current valid target")
	loaded, err := r.Get(ctx, key)
	require.NoError(t, err)
	require.Equal(t, before.EncryptedToken, loaded.EncryptedToken)
	require.Equal(t, proof.ObservedAt, loaded.HistoryProofObservedAt)
	require.Empty(t, loaded.DemandReason)
	before.ModelPolicyRevision, before.LastError = proof.ModelPolicyRevision, "slow_collector_result"
	saved, err = r.SaveCAS(ctx, *before, before.Version)
	require.NoError(t, err)
	require.False(t, saved)
	// Even after that cache is removed, replaying the consumed proof cannot
	// recreate demand. A new business observation is required.
	loaded.EncryptedToken, loaded.ExpiresAt = "", time.Time{}
	loaded.ModelPolicyRevision = proof.ModelPolicyRevision
	saved, err = r.SaveCAS(ctx, *loaded, loaded.Version)
	require.NoError(t, err)
	require.True(t, saved)
	created, err = r.CreateHistoryDemand(ctx, proof, now)
	require.NoError(t, err)
	require.False(t, created)
}

func TestCodexHistoryDemandPostgresRejectsStaleScopeAndWrongSubscription(t *testing.T) {
	ctx := context.Background()
	key := createCodexStateFixture(t)
	r := &openAICodexStateRepository{db: integrationDB, rdb: integrationRedis}
	now := time.Now().UTC().Truncate(time.Microsecond)
	proof := codexHistoryProofFixture(now)
	proof.OwnerAccountID, proof.Model, proof.Generation = key.OwnerAccountID, key.Model, key.Generation
	proof.ModelPolicyRevision = codexStateModelPolicyRevisionForTest(t)
	_, err := integrationDB.ExecContext(ctx, `UPDATE accounts SET extra=extra || '{"codex_turn_state_credential_epoch":"credential-1"}'::jsonb WHERE id=$1`, key.OwnerAccountID)
	require.NoError(t, err)
	for name, mutate := range map[string]func(*service.CodexTurnStateHistoryProof){
		"generation":       func(p *service.CodexTurnStateHistoryProof) { p.Generation = "replaced" },
		"credential epoch": func(p *service.CodexTurnStateHistoryProof) { p.CredentialEpoch = "replaced" },
		"policy":           func(p *service.CodexTurnStateHistoryProof) { p.ModelPolicyRevision = "replaced" },
		"subscription": func(p *service.CodexTurnStateHistoryProof) {
			p.AccountType = "team_business"
			p.TokenLength = 356
			p.CipherBlocks = 13
		},
	} {
		t.Run(name, func(t *testing.T) {
			candidate := proof
			mutate(&candidate)
			created, err := r.CreateHistoryDemand(ctx, candidate, now)
			require.NoError(t, err)
			require.False(t, created)
		})
	}
	record, err := r.Get(ctx, key)
	require.NoError(t, err)
	require.Nil(t, record, "rejected metadata must not create state")
}

func TestCodexHistoryDemandPostgresConsumesDuringBusiness(t *testing.T) {
	ctx := context.Background()
	key := createCodexStateFixture(t)
	r := &openAICodexStateRepository{db: integrationDB, rdb: integrationRedis}
	now := time.Now().UTC().Truncate(time.Microsecond)
	proof := codexHistoryProofFixture(now)
	proof.OwnerAccountID, proof.Model, proof.Generation = key.OwnerAccountID, key.Model, key.Generation
	proof.ModelPolicyRevision = codexStateModelPolicyRevisionForTest(t)
	_, err := integrationDB.ExecContext(ctx, `UPDATE accounts SET extra=extra || '{"codex_turn_state_credential_epoch":"credential-1"}'::jsonb WHERE id=$1`, key.OwnerAccountID)
	require.NoError(t, err)
	before, err := r.BeginBusiness(ctx, key, "natural", now, now.Add(time.Minute))
	require.NoError(t, err)
	require.NoError(t, r.MarkBusinessSent(ctx, key, now))
	created, err := r.CreateHistoryDemand(ctx, proof, now)
	require.NoError(t, err)
	require.True(t, created, "trusted historical demand does not wait for in-flight business")
	during, err := r.Get(ctx, key)
	require.NoError(t, err)
	require.Equal(t, before.Version+1, during.Version)
	require.Equal(t, proof.ObservedAt, during.HistoryProofObservedAt)
	require.Equal(t, "extended_shape", during.DemandReason)
	require.Equal(t, now, during.LastBusinessAt, "historical activity cannot replace a newer real business send")
	require.True(t, during.BusinessInFlight)
	require.NoError(t, r.EndBusiness(ctx, key, "natural"))
	created, err = r.CreateHistoryDemand(ctx, proof, now)
	require.NoError(t, err)
	require.False(t, created, "ending a business lease cannot consume the same history twice")
	after, err := r.Get(ctx, key)
	require.NoError(t, err)
	require.Equal(t, proof.ObservedAt, after.HistoryProofObservedAt)
	require.Equal(t, during.Version, after.Version)
	require.False(t, after.BusinessInFlight)
}

func TestCodexDemandPostgresIdleResumeDropsDemandPreservesWatermarkAndCooldown(t *testing.T) {
	ctx := context.Background()
	for _, outcome := range []string{"no_target_state", "collector_auth_rejected", "collector_rate_limited", "account_cooldown"} {
		t.Run(outcome, func(t *testing.T) {
			key := createCodexStateFixture(t)
			r := &openAICodexStateRepository{db: integrationDB, rdb: integrationRedis}
			now := time.Now().UTC().Truncate(time.Microsecond)
			old := now.Add(-31 * time.Minute)
			record, err := r.BeginBusiness(ctx, key, "old-business", old, old.Add(time.Minute))
			require.NoError(t, err)
			require.NoError(t, r.MarkBusinessSent(ctx, key, old))
			require.NoError(t, r.MarkEligibleCollectionSent(ctx, key, old))
			record.DemandReason, record.DemandAt, record.HistoryProofObservedAt = "extended_shape", old, old
			record.NextCollectAt = now.Add(time.Hour)
			record.LastError, record.CollectionReason, record.CollectionStatus = outcome, outcome, "backoff"
			if outcome == "collector_auth_rejected" {
				record.CollectorPaused, record.CollectionStatus = true, "paused"
			}
			record.ModelPolicyRevision = codexStateModelPolicyRevisionForTest(t)
			saved, err := r.SaveCAS(ctx, *record, record.Version)
			require.NoError(t, err)
			require.True(t, saved)
			before, err := r.Get(ctx, key)
			require.NoError(t, err)
			resumed, err := r.BeginBusiness(ctx, key, "new-business", now, now.Add(time.Minute))
			require.NoError(t, err)
			require.Empty(t, resumed.DemandReason)
			require.True(t, resumed.DemandAt.IsZero())
			require.Equal(t, old, resumed.HistoryProofObservedAt)
			require.Equal(t, old, resumed.LastBusinessAt, "preparation is not new activity")
			require.Greater(t, resumed.Version, before.Version, "an idle slow collector must lose CAS")
			if outcome == "no_target_state" {
				require.True(t, resumed.NextCollectAt.IsZero())
				require.Equal(t, "idle", resumed.CollectionStatus)
				require.Equal(t, "waiting_business_response", resumed.CollectionReason)
			} else {
				require.Equal(t, before.NextCollectAt, resumed.NextCollectAt)
				require.Equal(t, before.CollectorPaused, resumed.CollectorPaused)
			}
			require.NoError(t, r.MarkBusinessSent(ctx, key, now))
			resumed, err = r.Get(ctx, key)
			require.NoError(t, err)
			require.Equal(t, now, resumed.LastBusinessAt)
			require.Empty(t, resumed.DemandReason, "new ordinary business must not revive an old abnormal response")
		})
	}
}
