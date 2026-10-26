//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func attributionSetAutomatic(t *testing.T, id int64, enabled bool) {
	t.Helper()
	_, err := integrationDB.ExecContext(context.Background(), `UPDATE accounts SET extra=jsonb_set(extra,'{openai_api_key_auto_attribution_enabled}',to_jsonb($2::boolean)) WHERE id=$1`, id, enabled)
	require.NoError(t, err)
}

func TestAttributionManualDisabledOnlyRecords(t *testing.T) {
	for _, mode := range []string{"generic", "codex_engine"} {
		for _, schedule := range []bool{false, true} {
			t.Run(mode+"/"+map[bool]string{false: "whitelist", true: "schedule"}[schedule], func(t *testing.T) {
				ctx := context.Background()
				r, ar, _ := attributionFixture(t)
				a := attributionAPIKeyAccount(t, ar)
				_, err := integrationDB.ExecContext(ctx, `UPDATE accounts SET extra=jsonb_build_object('openai_api_key_mode',$2::text) WHERE id=$1`, a.ID, mode)
				require.NoError(t, err)
				attributionSetScheduleMode(t, a.ID, schedule)
				var callbacks, initialOutbox, afterOutbox int
				r.SetAccountChangedCallback(func(context.Context, int64) { callbacks++ })
				require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT count(*) FROM scheduler_outbox WHERE account_id=$1`, a.ID).Scan(&initialOutbox))
				for i, status := range []string{"mismatch", "passed", "passed"} {
					if i == 1 {
						attributionSetAutomatic(t, a.ID, false) // Missing and explicit false both stay read-only.
					}
					j := attributionRun(t, r, a.ID, status)
					require.Equal(t, status, j.Status)
					require.Equal(t, "record_only", j.Result.Action)
					require.Equal(t, "manual_auto_attribution_disabled", j.Reason)
					require.NotNil(t, j.Snapshot.ManualResultUpdatesAllowed)
					require.False(t, *j.Snapshot.ManualResultUpdatesAllowed)
					require.Zero(t, j.Result.PassStreak)
					require.Empty(t, j.Result.Before)
					require.Empty(t, j.Result.After)
					require.Nil(t, j.Result.SchedulingBefore)
					require.Equal(t, a.Credentials["model_mapping"], attributionMapping(t, ar, a.ID))
					schedulable, _, pause := attributionSchedulingState(t, a.ID)
					require.True(t, schedulable)
					require.False(t, pause.Valid)
				}
				var verdict string
				var streak int
				require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT verdict,pass_streak FROM model_attribution_state WHERE account_id=$1`, a.ID).Scan(&verdict, &streak))
				require.Empty(t, verdict, "diagnostics must not consume first-result initialization")
				require.Zero(t, streak)
				require.Zero(t, callbacks)
				require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT count(*) FROM scheduler_outbox WHERE account_id=$1`, a.ID).Scan(&afterOutbox))
				require.Equal(t, initialOutbox, afterOutbox)
				attributionSetAutomatic(t, a.ID, true)
				first := attributionRun(t, r, a.ID, "passed")
				if schedule {
					require.Equal(t, "awaiting_confirmation", first.Result.Action)
				} else {
					require.Equal(t, "high", first.Result.Action, "first eligible whitelist result still applies immediately")
				}
				require.Equal(t, 1, first.Result.PassStreak)
			})
		}
	}
}

func TestAttributionManualPermissionSurvivesQueueAndRetry(t *testing.T) {
	for _, initial := range []bool{false, true} {
		for _, phase := range []string{"queued", "running", "retry_wait"} {
			t.Run(map[bool]string{false: "enable_after_start", true: "disable_after_start"}[initial]+"/"+phase, func(t *testing.T) {
				ctx := context.Background()
				r, ar, _ := attributionFixture(t)
				a := attributionAPIKeyAccount(t, ar)
				attributionSetAutomatic(t, a.ID, initial)
				jobs, err := r.Enqueue(ctx, []int64{a.ID}, true)
				require.NoError(t, err)
				j := jobs[0]
				if phase != "queued" {
					j, err = r.Claim(ctx)
					require.NoError(t, err)
				}
				if phase == "retry_wait" {
					j.Checkpoint = attributionRetryCheckpoint()
					ok, err := r.DeferRetry(ctx, j, time.Now().Add(-time.Second))
					require.NoError(t, err)
					require.True(t, ok)
				}
				attributionSetAutomatic(t, a.ID, !initial)
				duplicate, err := r.Enqueue(ctx, []int64{a.ID}, true)
				require.NoError(t, err)
				require.Equal(t, j.ID, duplicate[0].ID)
				require.Equal(t, initial, *duplicate[0].Snapshot.ManualResultUpdatesAllowed)
				if phase != "running" {
					// A new repository models a worker restart and checkpoint reload.
					r = &attributionRepository{db: integrationDB}
					j, err = r.Claim(ctx)
					require.NoError(t, err)
				}
				require.Equal(t, initial, *j.Snapshot.ManualResultUpdatesAllowed)
				valid, err := r.Validate(ctx, j)
				require.NoError(t, err)
				require.True(t, valid, "switch changes do not interrupt the diagnostic")
				j.Status = "passed"
				attributionSuccessfulProbeEvidence(j, "")
				j.Result.Analysis = &service.AttributionAnalysis{Prediction: "actual-model", Probability: new(0.9)}
				require.NoError(t, r.Finish(ctx, j))
				got, err := r.Get(ctx, j.ID)
				require.NoError(t, err)
				require.Equal(t, "passed", got.Status)
				require.Equal(t, "record_only", got.Result.Action)
				require.Equal(t, "actual-model", got.Result.Analysis.Prediction)
				require.Zero(t, got.Result.PassStreak)
				require.Equal(t, a.Credentials["model_mapping"], attributionMapping(t, ar, a.ID))
			})
		}
	}
}

func TestAttributionManualDisabledDoesNotResumeOwnedPause(t *testing.T) {
	ctx := context.Background()
	r, ar, _ := attributionFixture(t)
	a := attributionAPIKeyAccount(t, ar)
	attributionSetAutomatic(t, a.ID, true)
	attributionSetScheduleMode(t, a.ID, true)
	require.Equal(t, "schedule_paused", attributionRun(t, r, a.ID, "mismatch").Result.Action)
	require.Equal(t, 1, attributionRun(t, r, a.ID, "passed").Result.PassStreak)
	_, revision, pause := attributionSchedulingState(t, a.ID)
	attributionSetAutomatic(t, a.ID, false)
	for range 2 {
		j := attributionRun(t, r, a.ID, "passed")
		require.Equal(t, "record_only", j.Result.Action)
		require.Zero(t, j.Result.PassStreak)
	}
	schedulable, currentRevision, currentPause := attributionSchedulingState(t, a.ID)
	require.False(t, schedulable)
	require.Equal(t, revision, currentRevision)
	require.Equal(t, pause, currentPause)
	attributionSetAutomatic(t, a.ID, true)
	require.Equal(t, "awaiting_confirmation", attributionRun(t, r, a.ID, "passed").Result.Action)
	require.Equal(t, "schedule_resumed", attributionRun(t, r, a.ID, "passed").Result.Action)
	stored, err := ar.GetByID(ctx, a.ID)
	require.NoError(t, err)
	require.True(t, stored.Schedulable)
}

func TestAttributionManualLegacyMissingPermissionOnlyRecords(t *testing.T) {
	ctx := context.Background()
	r, ar, _ := attributionFixture(t)
	a := attributionAPIKeyAccount(t, ar)
	attributionSetAutomatic(t, a.ID, true)
	jobs, err := r.Enqueue(ctx, []int64{a.ID}, true)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `UPDATE model_attribution_jobs SET snapshot=snapshot-'manual_result_updates_allowed' WHERE id=$1`, jobs[0].ID)
	require.NoError(t, err)
	j, err := r.Claim(ctx)
	require.NoError(t, err)
	require.Nil(t, j.Snapshot.ManualResultUpdatesAllowed)
	j.Status = "mismatch"
	attributionSuccessfulProbeEvidence(j, "")
	require.NoError(t, r.Finish(ctx, j))
	require.Equal(t, "mismatch", j.Status)
	require.Equal(t, "record_only", j.Result.Action)
	require.Equal(t, "manual_auto_attribution_unrecorded", j.Reason)
	require.Equal(t, a.Credentials["model_mapping"], attributionMapping(t, ar, a.ID))
}
