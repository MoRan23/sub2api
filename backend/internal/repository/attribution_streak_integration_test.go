//go:build integration

package repository

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestAttributionConsecutivePassInterrupted(t *testing.T) {
	for _, status := range []string{"mismatch", "failed", "abnormal", "skipped"} {
		t.Run(status, func(t *testing.T) {
			r, ar, a := attributionFixture(t)
			require.Equal(t, "low", attributionRun(t, r, a.ID, "mismatch").Result.Action)
			low := attributionMapping(t, ar, a.ID)
			first := attributionRun(t, r, a.ID, "passed")
			require.Equal(t, "awaiting_confirmation", first.Result.Action)
			require.Equal(t, 1, first.Result.PassStreak)
			interrupted := attributionRun(t, r, a.ID, status)
			require.Zero(t, interrupted.Result.PassStreak)
			require.Equal(t, low, attributionMapping(t, ar, a.ID))
			require.Equal(t, "awaiting_confirmation", attributionRun(t, r, a.ID, "passed").Result.Action)
			require.Equal(t, low, attributionMapping(t, ar, a.ID))
			confirmed := attributionRun(t, r, a.ID, "passed")
			require.Equal(t, "high", confirmed.Result.Action)
			require.Equal(t, 2, confirmed.Result.PassStreak)
		})
	}
}

func TestAttributionConsecutivePassRestartAndDuplicateFinish(t *testing.T) {
	ctx := context.Background()
	r, ar, a := attributionFixture(t)
	_, err := r.Enqueue(ctx, []int64{a.ID}, true)
	require.NoError(t, err)
	j, err := r.Claim(ctx)
	require.NoError(t, err)
	require.NotNil(t, j)
	j.Status = "passed"
	require.NoError(t, r.Finish(ctx, j))
	require.Equal(t, "awaiting_confirmation", j.Result.Action)
	// A different process and a duplicate completion must not count as another pass.
	r = &attributionRepository{db: integrationDB}
	require.NoError(t, r.Finish(ctx, j))
	var streak int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT pass_streak FROM model_attribution_state WHERE account_id=$1`, a.ID).Scan(&streak))
	require.Equal(t, 1, streak)
	require.Equal(t, a.Credentials["model_mapping"], attributionMapping(t, ar, a.ID))
	// State survives pruning all history and reapplying already-run migrations.
	_, err = integrationDB.ExecContext(ctx, `DELETE FROM model_attribution_jobs WHERE account_id=$1`, a.ID)
	require.NoError(t, err)
	require.NoError(t, ApplyMigrations(ctx, integrationDB))
	confirmed := attributionRun(t, r, a.ID, "passed")
	require.Equal(t, "high", confirmed.Result.Action)
	require.Equal(t, 2, confirmed.Result.PassStreak)
}

func TestAttributionConsecutivePassAuthorizationBaseline(t *testing.T) {
	ctx := context.Background()
	r, ar, a := attributionFixture(t)
	require.Equal(t, "awaiting_confirmation", attributionRun(t, r, a.ID, "passed").Result.Action)
	_, err := integrationDB.ExecContext(ctx, `UPDATE account_openai_oauth_credentials SET authorization_generation=$2 WHERE account_id=$1`, a.ID, uuid.NewString())
	require.NoError(t, err)
	require.Equal(t, "awaiting_confirmation", attributionRun(t, r, a.ID, "passed").Result.Action)
	require.Equal(t, a.Credentials["model_mapping"], attributionMapping(t, ar, a.ID))
	require.Equal(t, "high", attributionRun(t, r, a.ID, "passed").Result.Action)
}

func TestAttributionConsecutivePassRecoveryBreaksStreak(t *testing.T) {
	for _, reason := range []string{"interrupted", "timeout", "disabled", "account_inactive"} {
		t.Run(reason, func(t *testing.T) {
			ctx := context.Background()
			r, ar, a := attributionFixture(t)
			require.Equal(t, "awaiting_confirmation", attributionRun(t, r, a.ID, "passed").Result.Action)
			if reason == "account_inactive" {
				_, err := integrationDB.ExecContext(ctx, `UPDATE accounts SET status='disabled' WHERE id=$1`, a.ID)
				require.NoError(t, err)
			}
			tx, err := r.transaction(ctx)
			require.NoError(t, err)
			defer tx.Rollback()
			c, err := attributionConfig(ctx, tx)
			require.NoError(t, err)
			queued, err := enqueueAttribution(ctx, tx, a.ID, c, "scheduled", "")
			require.NoError(t, err)
			require.NoError(t, tx.Commit())
			switch reason {
			case "interrupted", "timeout":
				j, err := r.Claim(ctx)
				require.NoError(t, err)
				require.Equal(t, queued.ID, j.ID)
				if reason == "timeout" {
					_, err = integrationDB.ExecContext(ctx, `UPDATE model_attribution_jobs SET started_at=NOW()-interval '11 minutes' WHERE id=$1`, j.ID)
				} else {
					_, err = integrationDB.ExecContext(ctx, `UPDATE model_attribution_jobs SET lease_until=NOW()-interval '1 second' WHERE id=$1`, j.ID)
				}
				require.NoError(t, err)
			case "disabled":
				c.Enabled = false
				_, err = r.SaveConfig(ctx, c)
				require.NoError(t, err)
			}
			_, err = r.Claim(ctx)
			require.NoError(t, err)
			ended, err := r.Get(ctx, queued.ID)
			require.NoError(t, err)
			require.Equal(t, reason, ended.Reason)
			var streak int
			var verdict string
			require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT pass_streak,verdict FROM model_attribution_state WHERE account_id=$1`, a.ID).Scan(&streak, &verdict))
			require.Zero(t, streak)
			require.Equal(t, "passed", verdict)
			require.Equal(t, a.Credentials["model_mapping"], attributionMapping(t, ar, a.ID))
			require.Equal(t, "awaiting_confirmation", attributionRun(t, r, a.ID, "passed").Result.Action)
			require.Equal(t, "high", attributionRun(t, r, a.ID, "passed").Result.Action)
		})
	}
}

func TestAttributionConfirmationRollback(t *testing.T) {
	ctx := context.Background()
	r, ar, a := attributionFixture(t)
	require.Equal(t, "awaiting_confirmation", attributionRun(t, r, a.ID, "passed").Result.Action)
	_, err := r.Enqueue(ctx, []int64{a.ID}, true)
	require.NoError(t, err)
	j, err := r.Claim(ctx)
	require.NoError(t, err)
	require.NotNil(t, j)
	_, err = integrationDB.ExecContext(ctx, `CREATE FUNCTION attribution_confirmation_reject() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'synthetic failure'; END $$; CREATE TRIGGER attribution_confirmation_reject BEFORE UPDATE ON model_attribution_jobs FOR EACH ROW EXECUTE FUNCTION attribution_confirmation_reject()`)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(ctx, `DROP TRIGGER IF EXISTS attribution_confirmation_reject ON model_attribution_jobs; DROP FUNCTION IF EXISTS attribution_confirmation_reject()`)
	})
	var before, after int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT count(*) FROM scheduler_outbox WHERE account_id=$1`, a.ID).Scan(&before))
	j.Status = "passed"
	require.Error(t, r.Finish(ctx, j))
	var streak int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT pass_streak FROM model_attribution_state WHERE account_id=$1`, a.ID).Scan(&streak))
	require.Equal(t, 1, streak)
	require.Equal(t, a.Credentials["model_mapping"], attributionMapping(t, ar, a.ID))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT count(*) FROM scheduler_outbox WHERE account_id=$1`, a.ID).Scan(&after))
	require.Equal(t, before, after)
	_, err = integrationDB.ExecContext(ctx, `DROP TRIGGER attribution_confirmation_reject ON model_attribution_jobs; DROP FUNCTION attribution_confirmation_reject()`)
	require.NoError(t, err)
	require.NoError(t, r.Finish(ctx, j))
	require.Equal(t, "high", j.Result.Action)
	require.Equal(t, 2, j.Result.PassStreak)
}
