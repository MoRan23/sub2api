//go:build integration

package repository

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func attributionEngineAccount(t *testing.T, ar *accountRepository) *service.Account {
	t.Helper()
	a := attributionAPIKeyAccount(t, ar)
	_, err := integrationDB.ExecContext(context.Background(), `UPDATE accounts SET extra='{"openai_api_key_mode":"codex_engine","openai_api_key_auto_attribution_enabled":true}' WHERE id=$1`, a.ID)
	require.NoError(t, err)
	return a
}

func TestAttributionEngineWhitelistRecordsOnly(t *testing.T) {
	for _, source := range []string{"manual", "scheduled", "initial"} {
		t.Run(source, func(t *testing.T) {
			ctx := context.Background()
			r, ar, _ := attributionFixture(t)
			a := attributionEngineAccount(t, ar)
			for _, verdict := range []string{"passed", "mismatch", "passed", "passed"} {
				j := attributionClaimFor(t, r, a.ID)
				j.Source = source
				// Simulate the independent catalog sync while this round is running.
				_, err := integrationDB.ExecContext(ctx, `UPDATE accounts SET credentials=jsonb_set(credentials,'{model_mapping}','{"upstream":"upstream"}') WHERE id=$1`, a.ID)
				require.NoError(t, err)
				ok, err := r.Validate(ctx, j)
				require.NoError(t, err)
				require.True(t, ok)
				j.Status = verdict
				attributionSuccessfulProbeEvidence(j, "")
				require.NoError(t, r.Finish(ctx, j))
				require.Equal(t, verdict, j.Status)
				require.Equal(t, "record_only", j.Result.Action)
				require.Equal(t, "engine_models_managed_by_upstream", j.Reason)
				require.Zero(t, j.Result.PassStreak)
				require.Nil(t, j.Result.GroupsAfter)
				require.Equal(t, map[string]any{"upstream": "upstream"}, attributionMapping(t, ar, a.ID))
			}
			var verdict string
			var streak int
			require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT verdict,pass_streak FROM model_attribution_state WHERE account_id=$1`, a.ID).Scan(&verdict, &streak))
			require.Empty(t, verdict)
			require.Zero(t, streak)
		})
	}
}

func TestAttributionEngineScheduleIgnoresCatalogChanges(t *testing.T) {
	ctx := context.Background()
	r, ar, _ := attributionFixture(t)
	a := attributionEngineAccount(t, ar)
	attributionSetScheduleMode(t, a.ID, true)
	require.Equal(t, "schedule_paused", attributionRun(t, r, a.ID, "mismatch").Result.Action)
	for i, action := range []string{"awaiting_confirmation", "schedule_resumed"} {
		j := attributionClaimFor(t, r, a.ID)
		_, err := integrationDB.ExecContext(ctx, `UPDATE accounts SET credentials=jsonb_set(credentials,'{model_mapping}',jsonb_build_object($2::text,$2::text)) WHERE id=$1`, a.ID, []string{"first", "second"}[i])
		require.NoError(t, err)
		ok, err := r.Validate(ctx, j)
		require.NoError(t, err)
		require.True(t, ok)
		j.Status = "passed"
		attributionSuccessfulProbeEvidence(j, "")
		require.NoError(t, r.Finish(ctx, j))
		require.Equal(t, action, j.Result.Action)
		require.Equal(t, i+1, j.Result.PassStreak)
	}
	schedulable, _, pause := attributionSchedulingState(t, a.ID)
	require.True(t, schedulable)
	require.False(t, pause.Valid)
}
