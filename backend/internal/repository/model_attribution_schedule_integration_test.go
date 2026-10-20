//go:build integration

package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func attributionSetScheduleMode(t *testing.T, id int64, schedule bool) {
	t.Helper()
	_, err := integrationDB.ExecContext(context.Background(), `UPDATE accounts SET extra=COALESCE(extra,'{}'::jsonb)||jsonb_build_object('openai_attribution_replace_whitelist_enabled',$2::boolean) WHERE id=$1`, id, !schedule)
	require.NoError(t, err)
}

func attributionSchedulingState(t *testing.T, id int64) (bool, int64, sql.NullInt64) {
	t.Helper()
	var schedulable bool
	var revision int64
	var pause sql.NullInt64
	require.NoError(t, integrationDB.QueryRowContext(context.Background(), `SELECT a.schedulable,a.scheduling_revision,s.pause_revision FROM accounts a LEFT JOIN model_attribution_state s ON s.account_id=a.id WHERE a.id=$1`, id).Scan(&schedulable, &revision, &pause))
	return schedulable, revision, pause
}

func attributionClaimFor(t *testing.T, r *attributionRepository, id int64) *service.AttributionJob {
	t.Helper()
	jobs, err := r.Enqueue(context.Background(), []int64{id}, true)
	require.NoError(t, err)
	require.Len(t, jobs, 1)
	j, err := r.Claim(context.Background())
	require.NoError(t, err)
	require.NotNil(t, j)
	require.Equal(t, jobs[0].ID, j.ID)
	return j
}

func TestAttributionSchedulePauseAndTwoPassRecovery(t *testing.T) {
	ctx := context.Background()
	r, ar, a := attributionFixture(t)
	attributionSetScheduleMode(t, a.ID, true)
	_, initialRevision, _ := attributionSchedulingState(t, a.ID)
	var callbacks int
	r.SetAccountChangedCallback(func(ctx context.Context, id int64) {
		callbacks++
		var running int
		require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT count(*) FROM model_attribution_jobs WHERE account_id=$1 AND status='running'`, id).Scan(&running))
		require.Zero(t, running, "cache refresh must run after the complete result transaction commits")
	})
	mismatch := attributionRun(t, r, a.ID, "mismatch")
	require.Equal(t, 4, mismatch.Snapshot.Version)
	require.Equal(t, service.AttributionResultModeSchedule, mismatch.Snapshot.ResultMode)
	require.Equal(t, initialRevision, mismatch.Snapshot.SchedulingRevision)
	require.Equal(t, "schedule_paused", mismatch.Result.Action)
	require.True(t, *mismatch.Result.SchedulingBefore)
	require.False(t, *mismatch.Result.SchedulingAfter)
	schedulable, revision, pause := attributionSchedulingState(t, a.ID)
	require.False(t, schedulable)
	require.Greater(t, revision, initialRevision)
	require.Equal(t, sql.NullInt64{Valid: true, Int64: revision}, pause)
	require.Equal(t, a.Credentials["model_mapping"], attributionMapping(t, ar, a.ID))
	summaries, err := r.Summaries(ctx, []int64{a.ID})
	require.NoError(t, err)
	require.True(t, summaries[a.ID].SchedulingPausedByAttribution)

	// Repeated mismatch must not write schedulable again and revoke its own claim.
	require.Equal(t, "schedule_already_paused", attributionRun(t, r, a.ID, "mismatch").Result.Action)
	_, afterMismatch, afterPause := attributionSchedulingState(t, a.ID)
	require.Equal(t, revision, afterMismatch)
	require.Equal(t, pause, afterPause)
	first := attributionRun(t, r, a.ID, "passed")
	require.Equal(t, "awaiting_confirmation", first.Result.Action)
	require.Equal(t, 1, first.Result.PassStreak)
	require.Equal(t, revision, first.Snapshot.SchedulingRevision)
	second := attributionRun(t, r, a.ID, "passed")
	require.Equal(t, "schedule_resumed", second.Result.Action)
	require.Equal(t, 2, second.Result.PassStreak)
	require.False(t, *second.Result.SchedulingBefore)
	require.True(t, *second.Result.SchedulingAfter)
	schedulable, afterResume, pause := attributionSchedulingState(t, a.ID)
	require.True(t, schedulable)
	require.Greater(t, afterResume, revision)
	require.False(t, pause.Valid)
	require.Equal(t, 2, callbacks, "only actual changes refresh caches")
	require.Equal(t, a.Credentials["model_mapping"], attributionMapping(t, ar, a.ID))
	summaries, err = r.Summaries(ctx, []int64{a.ID})
	require.NoError(t, err)
	require.False(t, summaries[a.ID].SchedulingPausedByAttribution)
}

func TestAttributionScheduleManualPauseOwnership(t *testing.T) {
	ctx := context.Background()
	r, ar, a := attributionFixture(t)
	attributionSetScheduleMode(t, a.ID, true)
	require.NoError(t, ar.SetSchedulable(ctx, a.ID, false))
	require.Equal(t, "schedule_already_paused", attributionRun(t, r, a.ID, "mismatch").Result.Action)
	_, revision, pause := attributionSchedulingState(t, a.ID)
	require.False(t, pause.Valid, "a detector must not claim an existing manual pause")
	require.Equal(t, "awaiting_confirmation", attributionRun(t, r, a.ID, "passed").Result.Action)
	require.Equal(t, "schedule_manual_pause", attributionRun(t, r, a.ID, "passed").Result.Action)
	schedulable, current, _ := attributionSchedulingState(t, a.ID)
	require.False(t, schedulable)
	require.Equal(t, revision, current)

	require.NoError(t, ar.SetSchedulable(ctx, a.ID, true))
	require.Equal(t, "schedule_paused", attributionRun(t, r, a.ID, "mismatch").Result.Action)
	require.Equal(t, "awaiting_confirmation", attributionRun(t, r, a.ID, "passed").Result.Action)
	j := attributionClaimFor(t, r, a.ID)
	require.NoError(t, ar.SetSchedulable(ctx, a.ID, false), "same-value explicit admin pause revokes ownership")
	valid, err := r.Validate(ctx, j)
	require.NoError(t, err)
	require.True(t, valid, "scheduling changes do not interrupt probe execution")
	j.Status = "passed"
	attributionSuccessfulProbeEvidence(j, "")
	require.NoError(t, r.Finish(ctx, j))
	require.Equal(t, "passed", j.Status)
	require.Equal(t, "stale_policy", j.Result.Action)
	require.Equal(t, "scheduling_changed", j.Reason)
	require.Zero(t, j.Result.PassStreak)
	summaries, err := r.Summaries(ctx, []int64{a.ID})
	require.NoError(t, err)
	require.False(t, summaries[a.ID].SchedulingPausedByAttribution)
	require.Equal(t, "awaiting_confirmation", attributionRun(t, r, a.ID, "passed").Result.Action)
	require.Equal(t, "schedule_manual_pause", attributionRun(t, r, a.ID, "passed").Result.Action)
}

func TestAttributionScheduleModeAndRevisionFences(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(map[bool]string{false: "v4", true: "legacy"}[legacy], func(t *testing.T) {
			ctx := context.Background()
			r, ar, a := attributionFixture(t)
			j := attributionClaimFor(t, r, a.ID)
			if legacy {
				j.Snapshot.Version, j.Snapshot.ResultMode, j.Snapshot.SchedulingRevision = 3, "", 0
			}
			attributionSetScheduleMode(t, a.ID, true)
			valid, err := r.Validate(ctx, j)
			require.NoError(t, err)
			require.True(t, valid)
			j.Status = "mismatch"
			attributionSuccessfulProbeEvidence(j, "")
			require.NoError(t, r.Finish(ctx, j))
			require.Equal(t, "mismatch", j.Status)
			require.Equal(t, "stale_policy", j.Result.Action)
			require.Equal(t, "result_mode_changed", j.Reason)
			require.Equal(t, a.Credentials["model_mapping"], attributionMapping(t, ar, a.ID))
			schedulable, _, pause := attributionSchedulingState(t, a.ID)
			require.True(t, schedulable)
			require.False(t, pause.Valid)
		})
	}
	t.Run("mode round trip and pause retained", func(t *testing.T) {
		ctx := context.Background()
		r, _, a := attributionFixture(t)
		attributionSetScheduleMode(t, a.ID, true)
		require.Equal(t, "schedule_paused", attributionRun(t, r, a.ID, "mismatch").Result.Action)
		j := attributionClaimFor(t, r, a.ID)
		attributionSetScheduleMode(t, a.ID, false)
		schedulable, _, _ := attributionSchedulingState(t, a.ID)
		require.False(t, schedulable, "switching to replacement never resumes an account")
		attributionSetScheduleMode(t, a.ID, true)
		j.Status = "passed"
		attributionSuccessfulProbeEvidence(j, "")
		require.NoError(t, r.Finish(ctx, j))
		require.Equal(t, "scheduling_changed", j.Reason)
		require.Equal(t, "awaiting_confirmation", attributionRun(t, r, a.ID, "passed").Result.Action)
		require.Equal(t, "schedule_manual_pause", attributionRun(t, r, a.ID, "passed").Result.Action)
	})
}

func TestAttributionScheduleAbnormalDoesNotActAndBreaksStreak(t *testing.T) {
	for _, terminal := range []string{"failed", "abnormal", "skipped", "cancelled"} {
		t.Run(terminal, func(t *testing.T) {
			r, _, a := attributionFixture(t)
			attributionSetScheduleMode(t, a.ID, true)
			require.Equal(t, "none", attributionRun(t, r, a.ID, terminal).Result.Action)
			schedulable, _, _ := attributionSchedulingState(t, a.ID)
			require.True(t, schedulable)
			require.Equal(t, "schedule_paused", attributionRun(t, r, a.ID, "mismatch").Result.Action)
			require.Equal(t, "awaiting_confirmation", attributionRun(t, r, a.ID, "passed").Result.Action)
			_, before, owner := attributionSchedulingState(t, a.ID)
			require.Equal(t, "none", attributionRun(t, r, a.ID, terminal).Result.Action)
			schedulable, after, pause := attributionSchedulingState(t, a.ID)
			require.False(t, schedulable)
			require.Equal(t, before, after)
			require.Equal(t, owner, pause)
			require.Equal(t, "awaiting_confirmation", attributionRun(t, r, a.ID, "passed").Result.Action)
			require.Equal(t, "schedule_resumed", attributionRun(t, r, a.ID, "passed").Result.Action)
		})
	}
}

func TestAttributionScheduleRetryRecoveryAndDuplicateFinish(t *testing.T) {
	ctx := context.Background()
	r, _, a := attributionFixture(t)
	attributionSetScheduleMode(t, a.ID, true)
	require.Equal(t, "schedule_paused", attributionRun(t, r, a.ID, "mismatch").Result.Action)
	require.Equal(t, "awaiting_confirmation", attributionRun(t, r, a.ID, "passed").Result.Action)
	j := attributionClaimFor(t, r, a.ID)
	j.Checkpoint = attributionRetryCheckpoint()
	j.Result.CompletedProbes = 2
	ok, err := r.DeferRetry(ctx, j, time.Now().Add(-time.Second))
	require.NoError(t, err)
	require.True(t, ok)
	schedulable, revision, owner := attributionSchedulingState(t, a.ID)
	require.False(t, schedulable)
	var streak int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT pass_streak FROM model_attribution_state WHERE account_id=$1`, a.ID).Scan(&streak))
	require.Equal(t, 1, streak, "503 waiting is not a completed failing test")
	r = &attributionRepository{db: integrationDB}
	resumed, err := r.Claim(ctx)
	require.NoError(t, err)
	require.Equal(t, j.Snapshot, resumed.Snapshot)
	require.Equal(t, j.Checkpoint, resumed.Checkpoint)
	resumed.Status = "passed"
	attributionSuccessfulProbeEvidence(resumed, "")
	// Two replicas attempting the same completion must publish exactly one action.
	var wg sync.WaitGroup
	errors := make(chan error, 2)
	for range 2 {
		copy := *resumed
		wg.Add(1)
		go func() { defer wg.Done(); errors <- r.Finish(ctx, &copy) }()
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		require.NoError(t, err)
	}
	finished, err := r.Get(ctx, j.ID)
	require.NoError(t, err)
	require.Equal(t, "schedule_resumed", finished.Result.Action)
	schedulable, after, pause := attributionSchedulingState(t, a.ID)
	require.True(t, schedulable)
	require.Equal(t, revision+1, after)
	require.True(t, owner.Valid)
	require.False(t, pause.Valid)
}

func TestAttributionScheduleRecoveryPreservesHealthRestrictions(t *testing.T) {
	for _, change := range []string{
		`status='disabled'`,
		`expires_at=NOW()-interval '1 hour'`,
		`rate_limit_reset_at=NOW()+interval '1 hour'`,
		`extra=extra||'{"model_rate_limits":{"gpt-6-astra":{"rate_limit_reset_at":"2999-01-01T00:00:00Z"}}}'::jsonb`,
	} {
		t.Run(change, func(t *testing.T) {
			ctx := context.Background()
			r, _, a := attributionFixture(t)
			attributionSetScheduleMode(t, a.ID, true)
			require.Equal(t, "schedule_paused", attributionRun(t, r, a.ID, "mismatch").Result.Action)
			require.Equal(t, "awaiting_confirmation", attributionRun(t, r, a.ID, "passed").Result.Action)
			_, err := integrationDB.ExecContext(ctx, `UPDATE accounts SET `+change+` WHERE id=$1`, a.ID)
			require.NoError(t, err)
			require.Equal(t, "schedule_ineligible", attributionRun(t, r, a.ID, "passed").Result.Action)
			schedulable, revision, owner := attributionSchedulingState(t, a.ID)
			require.False(t, schedulable)
			require.Equal(t, sql.NullInt64{Valid: true, Int64: revision}, owner)
			_, err = integrationDB.ExecContext(ctx, `UPDATE accounts SET status='active',expires_at=NULL,rate_limit_reset_at=NULL,extra=extra-'model_rate_limits' WHERE id=$1`, a.ID)
			require.NoError(t, err)
			require.Equal(t, "schedule_resumed", attributionRun(t, r, a.ID, "passed").Result.Action)
		})
	}
}

func TestAttributionScheduleAtomicRollback(t *testing.T) {
	ctx := context.Background()
	r, _, a := attributionFixture(t)
	attributionSetScheduleMode(t, a.ID, true)
	j := attributionClaimFor(t, r, a.ID)
	_, before, _ := attributionSchedulingState(t, a.ID)
	var beforeOutbox, afterOutbox int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT count(*) FROM scheduler_outbox WHERE account_id=$1`, a.ID).Scan(&beforeOutbox))
	_, err := integrationDB.ExecContext(ctx, `CREATE FUNCTION attribution_schedule_test_reject() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'synthetic failure'; END $$; CREATE TRIGGER attribution_schedule_test_reject BEFORE UPDATE ON model_attribution_jobs FOR EACH ROW EXECUTE FUNCTION attribution_schedule_test_reject()`)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(ctx, `DROP TRIGGER IF EXISTS attribution_schedule_test_reject ON model_attribution_jobs; DROP FUNCTION IF EXISTS attribution_schedule_test_reject()`)
	})
	callbacks := 0
	r.SetAccountChangedCallback(func(context.Context, int64) { callbacks++ })
	j.Status = "mismatch"
	attributionSuccessfulProbeEvidence(j, "")
	require.Error(t, r.Finish(ctx, j))
	schedulable, after, pause := attributionSchedulingState(t, a.ID)
	require.True(t, schedulable)
	require.Equal(t, before, after)
	require.False(t, pause.Valid)
	require.Zero(t, callbacks)
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT count(*) FROM scheduler_outbox WHERE account_id=$1`, a.ID).Scan(&afterOutbox))
	require.Equal(t, beforeOutbox, afterOutbox)
}

func TestAttributionScheduleRecoveryKeepsOAuthPause(t *testing.T) {
	for _, change := range []string{"auth_pause_owned=true", "status='reauth_required'", "status='unauthorized'"} {
		t.Run(change, func(t *testing.T) {
			ctx := context.Background()
			r, _, a := attributionFixture(t)
			attributionSetScheduleMode(t, a.ID, true)
			require.Equal(t, "schedule_paused", attributionRun(t, r, a.ID, "mismatch").Result.Action)
			// Keep the account row active to exercise the independent credential guard.
			_, err := integrationDB.ExecContext(ctx, `UPDATE account_openai_oauth_credentials SET `+change+` WHERE account_id=$1`, a.ID)
			require.NoError(t, err)
			require.Equal(t, "awaiting_confirmation", attributionRun(t, r, a.ID, "passed").Result.Action)
			require.Equal(t, "schedule_ineligible", attributionRun(t, r, a.ID, "passed").Result.Action)
			schedulable, _, pause := attributionSchedulingState(t, a.ID)
			require.False(t, schedulable)
			require.True(t, pause.Valid)
			var healthUnchanged bool
			require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT `+change+` FROM account_openai_oauth_credentials WHERE account_id=$1`, a.ID).Scan(&healthUnchanged))
			require.True(t, healthUnchanged, "attribution must not clear another health subsystem's pause")
		})
	}
}

func TestAttributionScheduleAutomaticPausedEligibility(t *testing.T) {
	ctx := context.Background()
	r, ar, oauth := attributionFixture(t)
	expected := []int64{oauth.ID}
	for _, tc := range []struct {
		mode            string
		flag            any
		optIn, eligible bool
	}{
		{"generic", false, true, true}, {"codex_engine", false, true, true},
		{"generic", false, false, false}, {"codex_engine", false, false, false},
		{"generic", true, true, false}, {"generic", "false", true, false}, {"generic", nil, true, false},
	} {
		a := attributionAPIKeyAccount(t, ar)
		extra := map[string]any{"openai_api_key_mode": tc.mode, "openai_api_key_auto_attribution_enabled": tc.optIn, "openai_attribution_replace_whitelist_enabled": tc.flag}
		encoded, err := json.Marshal(extra)
		require.NoError(t, err)
		_, err = integrationDB.ExecContext(ctx, `UPDATE accounts SET schedulable=false,extra=$2::jsonb WHERE id=$1`, a.ID, encoded)
		require.NoError(t, err)
		if tc.eligible {
			expected = append(expected, a.ID)
		}
	}
	jobs, err := r.Enqueue(ctx, nil, false)
	require.NoError(t, err)
	var ids []int64
	for _, j := range jobs {
		ids = append(ids, j.AccountID)
	}
	require.ElementsMatch(t, expected, ids)
}

func TestAttributionScheduleInitialDoesNotChangePelicanGate(t *testing.T) {
	r, ar, _ := attributionFixture(t)
	a := initialTestAccount(t, ar, func(a *service.Account) {
		a.Schedulable = false
		a.Extra["openai_attribution_replace_whitelist_enabled"] = false
	})
	require.NoError(t, r.EnqueueNewAccounts(context.Background()))
	attribution, pelican := initialTestCounts(t, a.ID)
	require.Equal(t, 1, attribution)
	require.Zero(t, pelican, "pause-mode eligibility is specific to attribution")
}

func TestAttributionScheduleManualWithoutAllowlistConfiguration(t *testing.T) {
	ctx := context.Background()
	r, _, a := attributionFixture(t)
	c, err := r.Config(ctx)
	require.NoError(t, err)
	c.Enabled = false
	c.Default.HighModels, c.Default.LowModels = nil, nil
	_, err = r.SaveConfig(ctx, c)
	require.NoError(t, err)
	attributionSetScheduleMode(t, a.ID, true)
	require.Equal(t, "schedule_paused", attributionRun(t, r, a.ID, "mismatch").Result.Action)
	require.Equal(t, "awaiting_confirmation", attributionRun(t, r, a.ID, "passed").Result.Action)
	require.Equal(t, "schedule_resumed", attributionRun(t, r, a.ID, "passed").Result.Action)
}
