//go:build integration

package repository

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAttributionFirstVerdictInitializesWhitelist(t *testing.T) {
	for _, kind := range []string{"oauth", "api_key", "codex_engine"} {
		for _, verdict := range []string{"passed", "mismatch"} {
			t.Run(kind+"/"+verdict, func(t *testing.T) {
				ctx := context.Background()
				r, ar, a := attributionFixture(t)
				if kind != "oauth" {
					a = attributionAPIKeyAccount(t, ar)
					if kind == "codex_engine" {
						_, err := integrationDB.ExecContext(ctx, `UPDATE accounts SET extra='{"openai_api_key_mode":"codex_engine"}' WHERE id=$1`, a.ID)
						require.NoError(t, err)
					}
					// First-result initialization requires permission to manage the
					// whitelist; opt-out manual diagnostics are covered separately.
					attributionSetAutomatic(t, a.ID, true)
				}
				first := attributionRun(t, r, a.ID, verdict)
				want := map[string]any{"alias": "custom"}
				if verdict == "passed" {
					require.Equal(t, "high", first.Result.Action)
					require.Equal(t, 1, first.Result.PassStreak, "do not invent a second pass")
					want["high"], want["high2"] = "high", "high2"
				} else {
					want["low"] = "low"
					require.Equal(t, "low", first.Result.Action)
					require.Zero(t, first.Result.PassStreak)
				}
				if kind == "oauth" {
					want["wild*"], want["high"] = "target", "custom-high"
				}
				require.Equal(t, want, attributionMapping(t, ar, a.ID))
				// The exception is consumed by either valid verdict, not renewed
				// whenever an account needs to recover from a downgrade.
				if verdict == "passed" {
					require.Equal(t, "low", attributionRun(t, r, a.ID, "mismatch").Result.Action)
				}
				low := attributionMapping(t, ar, a.ID)
				require.Equal(t, "awaiting_confirmation", attributionRun(t, r, a.ID, "passed").Result.Action)
				require.Equal(t, low, attributionMapping(t, ar, a.ID))
				require.Equal(t, "high", attributionRun(t, r, a.ID, "passed").Result.Action)
			})
		}
	}
}

func TestAttributionFirstVerdictSurvivesNonVerdictResults(t *testing.T) {
	for _, status := range []string{"failed", "abnormal", "skipped", "cancelled"} {
		t.Run(status, func(t *testing.T) {
			r, ar, a := attributionFixture(t)
			if status == "cancelled" {
				j := attributionClaimFor(t, r, a.ID)
				_, err := r.Cancel(context.Background(), j.ID)
				require.NoError(t, err)
			} else {
				attributionRun(t, r, a.ID, status)
			}
			require.Equal(t, a.Credentials["model_mapping"], attributionMapping(t, ar, a.ID))
			first := attributionRun(t, r, a.ID, "passed")
			require.Equal(t, "high", first.Result.Action)
			require.Equal(t, 1, first.Result.PassStreak)
		})
	}
}

func TestAttributionFirstMatchingWhitelistConsumesException(t *testing.T) {
	ctx := context.Background()
	r, ar, a := attributionFixture(t)
	_, err := integrationDB.ExecContext(ctx, `UPDATE accounts SET credentials=jsonb_set(credentials,'{model_mapping}','{"high":"high","high2":"high2"}') WHERE id=$1`, a.ID)
	require.NoError(t, err)
	first := attributionRun(t, r, a.ID, "passed")
	require.Equal(t, "unchanged", first.Result.Action, "matching allowlists do not wait for confirmation")
	require.Equal(t, 1, first.Result.PassStreak)
	before := attributionMapping(t, ar, a.ID)
	// Persisted state, rather than retained job history or the current policy,
	// determines whether this account has ever applied a valid verdict.
	_, err = integrationDB.ExecContext(ctx, `DELETE FROM model_attribution_jobs WHERE account_id=$1`, a.ID)
	require.NoError(t, err)
	c, err := r.Config(ctx)
	require.NoError(t, err)
	c.Default.HighModels = []string{"new-high"}
	_, err = r.SaveConfig(ctx, c)
	require.NoError(t, err)
	r = &attributionRepository{db: integrationDB}
	require.Equal(t, "awaiting_confirmation", attributionRun(t, r, a.ID, "passed").Result.Action)
	require.Equal(t, before, attributionMapping(t, ar, a.ID))
	require.Equal(t, "high", attributionRun(t, r, a.ID, "passed").Result.Action)
}

func TestAttributionFirstVerdictDoesNotBypassSchedulingMode(t *testing.T) {
	r, ar, a := attributionFixture(t)
	attributionSetScheduleMode(t, a.ID, true)
	first := attributionRun(t, r, a.ID, "passed")
	require.Equal(t, "awaiting_confirmation", first.Result.Action)
	require.Equal(t, 1, first.Result.PassStreak)
	require.Equal(t, a.Credentials["model_mapping"], attributionMapping(t, ar, a.ID))
	attributionSetScheduleMode(t, a.ID, false)
	require.Equal(t, "awaiting_confirmation", attributionRun(t, r, a.ID, "passed").Result.Action,
		"changing result modes does not create another account-first verdict")
	require.Equal(t, a.Credentials["model_mapping"], attributionMapping(t, ar, a.ID))
	require.Equal(t, "high", attributionRun(t, r, a.ID, "passed").Result.Action)
}

func TestAttributionFirstVerdictRollbackAndConcurrentFinish(t *testing.T) {
	ctx := context.Background()
	r, ar, a := attributionFixture(t)
	j := attributionClaimFor(t, r, a.ID)
	j.Status = "passed"
	attributionSuccessfulProbeEvidence(j, "")
	var callbacks atomic.Int32
	r.SetAccountChangedCallback(func(context.Context, int64) { callbacks.Add(1) })
	// Isolate the finish event from the account creation event, which the
	// scheduler outbox may otherwise coalesce with it.
	_, err := integrationDB.ExecContext(ctx, `DELETE FROM scheduler_outbox WHERE account_id=$1`, a.ID)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `CREATE FUNCTION attribution_first_reject() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'synthetic failure'; END $$; CREATE TRIGGER attribution_first_reject BEFORE UPDATE ON model_attribution_jobs FOR EACH ROW EXECUTE FUNCTION attribution_first_reject()`)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(ctx, `DROP TRIGGER IF EXISTS attribution_first_reject ON model_attribution_jobs; DROP FUNCTION IF EXISTS attribution_first_reject()`)
	})
	var outboxBefore, outboxAfter int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT count(*) FROM scheduler_outbox WHERE account_id=$1`, a.ID).Scan(&outboxBefore))
	require.Error(t, r.Finish(ctx, j))
	var verdict string
	var streak int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT verdict,pass_streak FROM model_attribution_state WHERE account_id=$1`, a.ID).Scan(&verdict, &streak))
	require.Empty(t, verdict)
	require.Zero(t, streak)
	require.Equal(t, a.Credentials["model_mapping"], attributionMapping(t, ar, a.ID))
	require.Zero(t, callbacks.Load())
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT count(*) FROM scheduler_outbox WHERE account_id=$1`, a.ID).Scan(&outboxAfter))
	require.Equal(t, outboxBefore, outboxAfter, "rollback must not publish an account change")
	_, err = integrationDB.ExecContext(ctx, `DROP TRIGGER attribution_first_reject ON model_attribution_jobs; DROP FUNCTION attribution_first_reject()`)
	require.NoError(t, err)
	var wg sync.WaitGroup
	errors := make(chan error, 2)
	for range 2 {
		copy := *j
		wg.Add(1)
		go func() { defer wg.Done(); errors <- r.Finish(ctx, &copy) }()
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		require.NoError(t, err)
	}
	stored, err := r.Get(ctx, j.ID)
	require.NoError(t, err)
	require.Equal(t, "high", stored.Result.Action)
	require.Equal(t, 1, stored.Result.PassStreak)
	require.EqualValues(t, 1, callbacks.Load())
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT count(*) FROM scheduler_outbox WHERE account_id=$1`, a.ID).Scan(&outboxAfter))
	require.Equal(t, outboxBefore+1, outboxAfter)
}
