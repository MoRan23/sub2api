//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestCodexStatePostgresRotationDurabilityAndFencing(t *testing.T) {
	ctx := context.Background()
	key := createCodexStateFixture(t)
	repo := NewOpenAICodexStateRepository(integrationDB, integrationRedis)
	now := time.Now().UTC().Truncate(time.Microsecond)
	record, err := repo.BeginBusiness(ctx, key, "business", now, now.Add(time.Minute))
	require.NoError(t, err)
	require.NoError(t, repo.MarkBusinessSent(ctx, key, now))
	record.ModelPolicyRevision = codexStateModelPolicyRevisionForTest(t)
	record.DemandReason, record.DemandAt = "extended_shape", now
	record.CollectorProxyID, record.LastCollectorProxyID = 202, 202
	record.CollectorExtendedCount = 2
	record.CollectorAttemptID = uuid.NewString()
	record.NextCollectAt = now.Add(service.CodexTurnStateRetryInterval)
	ok, err := repo.SaveCAS(ctx, *record, record.Version)
	require.NoError(t, err)
	require.True(t, ok)

	// A reconstructed instance reads the exact rotation and reserved attempt;
	// restart cannot reset the count or allocate the first proxy again.
	restarted := NewOpenAICodexStateRepository(integrationDB, integrationRedis)
	reserved, err := restarted.Get(ctx, key)
	require.NoError(t, err)
	require.Equal(t, int64(202), reserved.CollectorProxyID)
	require.Equal(t, int64(202), reserved.LastCollectorProxyID)
	require.Equal(t, 2, reserved.CollectorExtendedCount)
	require.Equal(t, record.CollectorAttemptID, reserved.CollectorAttemptID)
	require.Equal(t, record.NextCollectAt, reserved.NextCollectAt)
	reserved.ModelPolicyRevision = record.ModelPolicyRevision
	late := *reserved

	// Finishing the third anomalous attempt changes proxy and clears its UUID
	// in the same CAS; replaying that result cannot rotate a second time.
	reserved.CollectorProxyID, reserved.CollectorExtendedCount = 303, 0
	reserved.CollectorAttemptID = ""
	ok, err = restarted.SaveCAS(ctx, *reserved, reserved.Version)
	require.NoError(t, err)
	require.True(t, ok)
	late.CollectorProxyID, late.CollectorExtendedCount = 404, 0
	ok, err = repo.SaveCAS(ctx, late, late.Version)
	require.NoError(t, err)
	require.False(t, ok)
	loaded, err := repo.Get(ctx, key)
	require.NoError(t, err)
	require.Equal(t, int64(303), loaded.CollectorProxyID)
	require.Equal(t, int64(202), loaded.LastCollectorProxyID)
	require.Zero(t, loaded.CollectorExtendedCount)
	require.Empty(t, loaded.CollectorAttemptID)

	// Database validation prevents persisted counts outside the displayed 0..2.
	loaded.ModelPolicyRevision = record.ModelPolicyRevision
	loaded.CollectorExtendedCount = 3
	_, err = repo.SaveCAS(ctx, *loaded, loaded.Version)
	require.ErrorContains(t, err, "collector_extended_count")
}

func TestCodexStatePostgresRotationIdleAndGeneration(t *testing.T) {
	ctx := context.Background()
	key := createCodexStateFixture(t)
	repo := NewOpenAICodexStateRepository(integrationDB, integrationRedis)
	now := time.Now().UTC().Truncate(time.Microsecond)
	old := now.Add(-time.Hour)
	record, err := repo.BeginBusiness(ctx, key, "old", old, old.Add(time.Minute))
	require.NoError(t, err)
	require.NoError(t, repo.MarkBusinessSent(ctx, key, old))
	record.ModelPolicyRevision = codexStateModelPolicyRevisionForTest(t)
	record.DemandReason, record.DemandAt = "extended_shape", old
	record.CollectorProxyID, record.LastCollectorProxyID, record.CollectorExtendedCount = 202, 101, 2
	record.CollectorAttemptID = uuid.NewString()
	ok, err := repo.SaveCAS(ctx, *record, record.Version)
	require.NoError(t, err)
	require.True(t, ok)
	resumed, err := repo.BeginBusiness(ctx, key, "resume", now, now.Add(time.Minute))
	require.NoError(t, err)
	require.Equal(t, int64(202), resumed.CollectorProxyID)
	require.Equal(t, int64(101), resumed.LastCollectorProxyID)
	require.Equal(t, 2, resumed.CollectorExtendedCount)
	require.Empty(t, resumed.CollectorAttemptID, "idle resumes cannot keep a stale attempt reservation")
	require.Empty(t, resumed.DemandReason)

	_, err = integrationDB.ExecContext(ctx, `UPDATE account_openai_oauth_os_credentials SET state_generation='00000000-0000-4000-8000-000000000002'
		WHERE account_id=$1 AND os_family='windows'`, key.OwnerAccountID)
	require.NoError(t, err)
	key.Generation = "00000000-0000-4000-8000-000000000002"
	reset, err := repo.BeginBusiness(ctx, key, "new", now, now.Add(time.Minute))
	require.NoError(t, err)
	require.Zero(t, reset.CollectorProxyID)
	require.Zero(t, reset.LastCollectorProxyID)
	require.Zero(t, reset.CollectorExtendedCount)
	require.Empty(t, reset.CollectorAttemptID)
}

func TestCodexStatePostgresRotationScanConfigPrecedence(t *testing.T) {
	ctx := context.Background()
	key := createCodexStateFixture(t)
	repo := NewOpenAICodexStateRepository(integrationDB, integrationRedis)
	now := time.Now().UTC().Truncate(time.Microsecond)
	record, err := repo.BeginBusiness(ctx, key, "business", now, now.Add(time.Minute))
	require.NoError(t, err)
	require.NoError(t, repo.MarkBusinessSent(ctx, key, now))
	record.ModelPolicyRevision = codexStateModelPolicyRevisionForTest(t)
	record.DemandReason, record.DemandAt = "extended_shape", now
	record.CollectorProxyID, record.CollectorExtendedCount = 202, 2
	ok, err := repo.SaveCAS(ctx, *record, record.Version)
	require.NoError(t, err)
	require.True(t, ok)
	for _, tc := range []struct {
		name, config string
		want         bool
	}{
		{"ordered_list", `{"enabled":true,"collector_proxy_ids":[101,202]}`, true},
		{"new_over_legacy", `{"enabled":true,"collector_proxy_ids":[101],"collector_proxy_id":null}`, true},
		{"empty_over_legacy", `{"enabled":true,"collector_proxy_ids":[],"collector_proxy_id":101}`, false},
		{"null_over_legacy", `{"enabled":true,"collector_proxy_ids":null,"collector_proxy_id":101}`, false},
		{"invalid_over_legacy", `{"enabled":true,"collector_proxy_ids":"101","collector_proxy_id":101}`, false},
		{"invalid_entries", `{"enabled":true,"collector_proxy_ids":[0,-1,"101",null]}`, false},
		{"legacy", `{"enabled":true,"collector_proxy_id":101}`, true},
		{"natural_only", `{"enabled":true}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := integrationDB.ExecContext(ctx, `UPDATE accounts SET extra=jsonb_set(extra,
				'{codex_turn_state}', $2::jsonb) WHERE id=$1`, key.OwnerAccountID, tc.config)
			require.NoError(t, err)
			// This test exercises scan policy, retaining its prepared runtime row
			// under the newly committed configuration's slot fence.
			_, err = integrationDB.ExecContext(ctx, `UPDATE openai_codex_state s SET generation=c.state_generation::text FROM account_openai_oauth_os_credentials c WHERE s.owner_account_id=$1 AND c.account_id=s.owner_account_id AND c.os_family=s.os_family`, key.OwnerAccountID)
			require.NoError(t, err)
			rows, err := repo.ListActive(ctx, now.Add(-time.Minute), 1000)
			require.NoError(t, err)
			found := false
			for _, row := range rows {
				if row.OwnerAccountID == key.OwnerAccountID {
					found = true
					require.Equal(t, int64(202), row.CollectorProxyID)
					require.Equal(t, 2, row.CollectorExtendedCount)
				}
			}
			require.Equal(t, tc.want, found)
		})
	}
}

func TestCodexStateRedisRotationAttemptCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	key := createCodexStateFixture(t)
	first := NewOpenAICodexStateRepository(integrationDB, integrationRedis)
	second := NewOpenAICodexStateRepository(integrationDB, integrationRedis)
	received := make(chan service.CodexTurnStateKey, 2)
	finished := make(chan error, 1)
	go func() {
		finished <- second.SubscribeCancels(ctx, func(key service.CodexTurnStateKey) { received <- key })
	}()
	require.Eventually(t, func() bool {
		counts, err := integrationRedis.PubSubNumSub(ctx, codexStateCancelChannel).Result()
		return err == nil && counts[codexStateCancelChannel] == 1
	}, time.Second, time.Millisecond)
	key.CollectorAttemptID = uuid.NewString()
	require.NoError(t, first.PublishCancel(ctx, key))
	select {
	case got := <-received:
		require.Equal(t, key, got, "exact attempt must survive cross-instance notification")
	case <-time.After(time.Second):
		t.Fatal("attempt notification not delivered")
	}
	key.CollectorAttemptID = ""
	require.NoError(t, first.PublishCancel(ctx, key))
	select {
	case got := <-received:
		require.Equal(t, key, got, "legacy generation cancellation remains supported")
	case <-time.After(time.Second):
		t.Fatal("generation notification not delivered")
	}
	cancel()
	select {
	case err := <-finished:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(time.Second):
		t.Fatal("cancellation subscriber did not stop")
	}
}
