//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func codexStateCooldownDemandFixture(t *testing.T, repo service.CodexTurnStateRepository, key service.CodexTurnStateKey, now time.Time) *service.CodexTurnStateRecord {
	t.Helper()
	ctx := context.Background()
	record, err := repo.BeginBusiness(ctx, key, "cooldown-regression", now, now.Add(time.Minute))
	require.NoError(t, err)
	require.NotNil(t, record, "the fixture must use the current OS state generation")
	require.NoError(t, repo.MarkBusinessSent(ctx, key, now))
	record.DemandReason, record.DemandAt = "extended_shape", now
	record.CollectionStatus, record.CollectionReason = "pending", "queued"
	record.ModelPolicyRevision = codexStateModelPolicyRevisionForTest(t)
	return record
}

func TestCodexStatePostgresCollectorReservationDoesNotExtendExpiredCooldown(t *testing.T) {
	for _, reason := range []string{"collector_rate_limited", "account_cooldown"} {
		t.Run(reason, func(t *testing.T) {
			ctx := context.Background()
			key := createCodexStateFixture(t)
			installCodexStateModelPolicyFixture(t, []string{key.Model, "gpt-5-mini"})
			_, err := integrationDB.ExecContext(ctx, `UPDATE accounts SET extra=jsonb_set(extra,'{codex_turn_state,collector_proxy_id}','11'::jsonb) WHERE id=$1`, key.OwnerAccountID)
			require.NoError(t, err)
			// Collector configuration changes rotate the runtime generation.
			require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT state_generation::text
				FROM account_openai_oauth_os_credentials WHERE account_id=$1 AND os_family=$2`, key.OwnerAccountID, key.OSFamily).Scan(&key.Generation))
			repo := NewOpenAICodexStateRepository(integrationDB, integrationRedis)
			cooldowns := repo.(service.CodexTurnStateCooldownRepository)
			now := time.Now().UTC().Truncate(time.Microsecond)
			expired := now.Add(-time.Second)
			record := codexStateCooldownDemandFixture(t, repo, key, now)
			record.LastError, record.NextCollectAt, record.CollectionStatus = reason, expired, "backoff"
			saved, err := repo.SaveCAS(ctx, *record, record.Version)
			require.NoError(t, err)
			require.True(t, saved)
			record.Version++

			otherKey := key
			otherKey.Model = "gpt-5-mini"
			other := codexStateCooldownDemandFixture(t, repo, otherKey, now)
			saved, err = repo.SaveCAS(ctx, *other, other.Version)
			require.NoError(t, err)
			require.True(t, saved)

			// Starting again after the old limit expired must not promote this
			// attempt's 25-second crash reservation to an account-wide cooldown.
			record.NextCollectAt = now.Add(service.CodexTurnStateCollectTimeout + service.CodexTurnStateRetryInterval)
			record.LastCollectedAt = now
			record.CollectionStatus, record.CollectionReason = "collecting", "collecting"
			record.CollectorAttemptID = "00000000-0000-4000-8000-000000000031"
			saved, err = repo.SaveCAS(ctx, *record, record.Version)
			require.NoError(t, err)
			require.True(t, saved)
			record.Version++
			until, err := cooldowns.GetCollectorCooldowns(ctx, []int64{key.OwnerAccountID})
			require.NoError(t, err)
			require.Equal(t, expired, until[key.OwnerAccountID])
			active, err := repo.ListActive(ctx, now.Add(-time.Minute), 1000)
			require.NoError(t, err)
			var otherDue bool
			for _, row := range active {
				otherDue = otherDue || row.Key() == otherKey
			}
			require.True(t, otherDue, "a model-local reservation must not hide another due model in the owner query")

			record.NextCollectAt, record.LastError = now, "collection_timeout"
			record.CollectionStatus, record.CollectionReason, record.CollectorAttemptID = "pending", "collection_timeout", ""
			saved, err = repo.SaveCAS(ctx, *record, record.Version)
			require.NoError(t, err)
			require.True(t, saved)
			until, err = cooldowns.GetCollectorCooldowns(ctx, []int64{key.OwnerAccountID})
			require.NoError(t, err)
			require.Equal(t, expired, until[key.OwnerAccountID], "ordinary completion must not leave a hidden five-second owner cooldown")
		})
	}
}

func TestCodexStatePostgresRejectedCASDoesNotPublishCooldown(t *testing.T) {
	for _, rejection := range []string{"version", "version_with_existing_cooldown", "generation", "policy", "disabled"} {
		t.Run(rejection, func(t *testing.T) {
			ctx := context.Background()
			key := createCodexStateFixture(t)
			repo := NewOpenAICodexStateRepository(integrationDB, integrationRedis)
			cooldowns := repo.(service.CodexTurnStateCooldownRepository)
			now := time.Now().UTC().Truncate(time.Microsecond)
			record := codexStateCooldownDemandFixture(t, repo, key, now)
			saved, err := repo.SaveCAS(ctx, *record, record.Version)
			require.NoError(t, err)
			require.True(t, saved)
			record.Version++
			var prior time.Time
			if rejection == "version_with_existing_cooldown" {
				prior = now.Add(time.Minute)
				require.NoError(t, cooldowns.ExtendCollectorCooldown(ctx, key.OwnerAccountID, prior))
			}
			record.LastError, record.NextCollectAt = "collector_rate_limited", now.Add(time.Hour)
			record.CollectionStatus, record.CollectionReason = "backoff", "collector_rate_limited"
			switch rejection {
			case "version", "version_with_existing_cooldown":
				record.Version--
			case "generation":
				record.Generation = "replaced-generation"
			case "policy":
				record.ModelPolicyRevision = "replaced-policy"
			case "disabled":
				_, err = integrationDB.ExecContext(ctx, `UPDATE accounts SET extra=jsonb_set(extra,'{codex_turn_state,enabled}','false'::jsonb) WHERE id=$1`, key.OwnerAccountID)
				require.NoError(t, err)
			}
			saved, err = repo.SaveCAS(ctx, *record, record.Version)
			require.NoError(t, err)
			require.False(t, saved)
			until, err := cooldowns.GetCollectorCooldowns(ctx, []int64{key.OwnerAccountID})
			require.NoError(t, err)
			require.Equal(t, prior, until[key.OwnerAccountID], "a rejected record cannot create or extend account-wide scheduling state")
		})
	}
}

func TestCodexStatePostgresAcceptedCooldownRemainsMonotonic(t *testing.T) {
	for _, reason := range []string{"collector_rate_limited", "account_cooldown"} {
		t.Run(reason, func(t *testing.T) {
			ctx := context.Background()
			key := createCodexStateFixture(t)
			repo := NewOpenAICodexStateRepository(integrationDB, integrationRedis)
			cooldowns := repo.(service.CodexTurnStateCooldownRepository)
			now := time.Now().UTC().Truncate(time.Microsecond)
			record := codexStateCooldownDemandFixture(t, repo, key, now)
			deadline := now.Add(10 * time.Minute)
			record.LastError, record.NextCollectAt = reason, deadline
			record.CollectionStatus, record.CollectionReason = "backoff", reason
			for _, next := range []time.Time{deadline, now.Add(time.Minute)} {
				record.NextCollectAt = next
				saved, err := repo.SaveCAS(ctx, *record, record.Version)
				require.NoError(t, err)
				require.True(t, saved)
				record.Version++
				until, err := cooldowns.GetCollectorCooldowns(ctx, []int64{key.OwnerAccountID})
				require.NoError(t, err)
				require.Equal(t, deadline, until[key.OwnerAccountID])
			}
			record.LastError, record.NextCollectAt = "collection_timeout", now
			record.CollectionStatus, record.CollectionReason = "pending", "collection_timeout"
			saved, err := repo.SaveCAS(ctx, *record, record.Version)
			require.NoError(t, err)
			require.True(t, saved)
			until, err := cooldowns.GetCollectorCooldowns(ctx, []int64{key.OwnerAccountID})
			require.NoError(t, err)
			require.Equal(t, deadline, until[key.OwnerAccountID], "ordinary errors must not erase a real concurrent account cooldown")
		})
	}
}
