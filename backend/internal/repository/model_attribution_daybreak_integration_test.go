//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func setAttributionDaybreakPreferences(t *testing.T, accountID int64, blue, red bool) {
	t.Helper()
	_, err := integrationDB.ExecContext(context.Background(), `UPDATE accounts SET extra=COALESCE(extra,'{}'::jsonb)||jsonb_build_object('openai_daybreak_blue_enabled',$2::bool,'openai_daybreak_red_enabled',$3::bool) WHERE id=$1`, accountID, blue, red)
	require.NoError(t, err)
}

func attributionDaybreakRun(t *testing.T, r *attributionRepository, accountID int64, cyber string) *service.AttributionJob {
	t.Helper()
	ctx := context.Background()
	_, err := r.Enqueue(ctx, []int64{accountID}, true)
	require.NoError(t, err)
	j, err := r.Claim(ctx)
	require.NoError(t, err)
	require.NotNil(t, j)
	j.Status = "passed"
	attributionSuccessfulProbeEvidence(j, cyber)
	require.NoError(t, r.Finish(ctx, j))
	return j
}

func TestAttributionDaybreakPreferencesFreezeAtStartAndSurviveRetry(t *testing.T) {
	ctx := context.Background()
	r, ar, a := attributionFixture(t)
	jobs, err := r.Enqueue(ctx, []int64{a.ID}, true)
	require.NoError(t, err)
	require.Equal(t, 3, jobs[0].Snapshot.Version)
	require.Nil(t, jobs[0].Snapshot.Daybreak, "queueing must not freeze preferences")
	setAttributionDaybreakPreferences(t, a.ID, true, true)
	j, err := r.Claim(ctx)
	require.NoError(t, err)
	require.Equal(t, &service.DiagnosticDaybreakPreferences{Blue: true, Red: true}, j.Snapshot.Daybreak)
	j.Checkpoint = attributionRetryCheckpoint()
	j.Result.CompletedProbes = 2
	j.Result.Daybreak = []*service.DiagnosticDaybreakEvidence{{Cyber: "daybreak_blue", Reason: "injected"}, {Cyber: "daybreak_blue", Reason: "injected"}}
	ok, err := r.DeferRetry(ctx, j, time.Now().Add(-time.Second))
	require.NoError(t, err)
	require.True(t, ok)
	setAttributionDaybreakPreferences(t, a.ID, false, false)
	r = &attributionRepository{db: integrationDB}
	resumed, err := r.Claim(ctx)
	require.NoError(t, err)
	require.NotNil(t, resumed)
	require.Equal(t, j.Snapshot.Daybreak, resumed.Snapshot.Daybreak)
	require.Equal(t, j.Result.Daybreak, resumed.Result.Daybreak)
	require.Equal(t, j.Checkpoint.Outputs, resumed.Checkpoint.Outputs)
	valid, err := r.Validate(ctx, resumed)
	require.NoError(t, err)
	require.True(t, valid, "changing preferences must not interrupt execution")
	resumed.Status = "passed"
	resumed.Result.CompletedProbes = 3
	resumed.Result.Daybreak = append(resumed.Result.Daybreak, &service.DiagnosticDaybreakEvidence{Cyber: "daybreak_blue", Reason: "injected"})
	require.NoError(t, r.Finish(ctx, resumed))
	require.Equal(t, "passed", resumed.Status)
	require.Equal(t, "stale_policy", resumed.Result.Action)
	require.Equal(t, "daybreak_preferences_changed", resumed.Reason)
	require.Zero(t, resumed.Result.PassStreak)
	require.Equal(t, a.Credentials["model_mapping"], attributionMapping(t, ar, a.ID))
	saved, err := r.Get(ctx, resumed.ID)
	require.NoError(t, err)
	require.Equal(t, resumed.Snapshot.Daybreak, saved.Snapshot.Daybreak)
	require.Equal(t, resumed.Result.Daybreak, saved.Result.Daybreak)
	assertAttributionPrivateCheckpoint(t, resumed.ID, nil)
}

func TestAttributionDaybreakEvidenceBlocksMixedOrMissingWriteback(t *testing.T) {
	for _, scenario := range []string{"mixed", "missing", "unknown", "unfinished", "snapshot_missing"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			r, ar, a := attributionFixture(t)
			require.Equal(t, "awaiting_confirmation", attributionRun(t, r, a.ID, "passed").Result.Action)
			_, err := r.Enqueue(ctx, []int64{a.ID}, true)
			require.NoError(t, err)
			j, err := r.Claim(ctx)
			require.NoError(t, err)
			j.Status = "mismatch"
			attributionSuccessfulProbeEvidence(j, "")
			reason := "daybreak_evidence_missing"
			switch scenario {
			case "mixed":
				j.Result.Daybreak[1].Cyber = "daybreak_blue"
				reason = "daybreak_conditions_mixed"
			case "missing":
				j.Result.Daybreak[1] = nil
			case "unknown":
				j.Result.Daybreak[1].Cyber = "unsupported"
			case "unfinished":
				j.Result.CompletedProbes = 2
			case "snapshot_missing":
				j.Snapshot.Daybreak = nil
			}
			require.NoError(t, r.Finish(ctx, j))
			require.Equal(t, "mismatch", j.Status, "analysis verdict must remain visible")
			require.Equal(t, "stale_policy", j.Result.Action)
			require.Equal(t, reason, j.Reason)
			require.Equal(t, a.Credentials["model_mapping"], attributionMapping(t, ar, a.ID))
			var streak int
			require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT pass_streak FROM model_attribution_state WHERE account_id=$1`, a.ID).Scan(&streak))
			require.Zero(t, streak)
			require.Equal(t, "awaiting_confirmation", attributionRun(t, r, a.ID, "passed").Result.Action)
		})
	}
}

func TestAttributionDaybreakBaselineSeparatesPreferenceAndActualCyber(t *testing.T) {
	r, ar, a := attributionFixture(t)
	setAttributionDaybreakPreferences(t, a.ID, true, true)
	// Capability may be unavailable in one round, so preferences alone cannot
	// identify equivalent successful executions.
	require.Equal(t, 1, attributionDaybreakRun(t, r, a.ID, "").Result.PassStreak)
	require.Equal(t, 1, attributionDaybreakRun(t, r, a.ID, "daybreak_blue").Result.PassStreak)
	confirmed := attributionDaybreakRun(t, r, a.ID, "daybreak_blue")
	require.Equal(t, "high", confirmed.Result.Action)
	require.Equal(t, 2, confirmed.Result.PassStreak)
	require.Contains(t, attributionMapping(t, ar, a.ID), "high2")
	// A changed actual field or frozen preference starts a separate two-pass
	// baseline, even when the old baseline already reached two passes.
	require.Equal(t, 1, attributionDaybreakRun(t, r, a.ID, "").Result.PassStreak)
	setAttributionDaybreakPreferences(t, a.ID, false, false)
	require.Equal(t, 1, attributionDaybreakRun(t, r, a.ID, "").Result.PassStreak)
	require.Equal(t, 2, attributionDaybreakRun(t, r, a.ID, "").Result.PassStreak)
}

func TestAttributionDaybreakLegacyQueuedJobsDoNotInjectOrShareBaseline(t *testing.T) {
	ctx := context.Background()
	r, _, a := attributionFixture(t)
	setAttributionDaybreakPreferences(t, a.ID, true, true)
	jobs, err := r.Enqueue(ctx, []int64{a.ID}, true)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `UPDATE model_attribution_jobs SET snapshot=jsonb_set(snapshot-'daybreak','{version}','2') WHERE id=$1`, jobs[0].ID)
	require.NoError(t, err)
	legacy, err := r.Claim(ctx)
	require.NoError(t, err)
	require.Equal(t, 2, legacy.Snapshot.Version)
	require.Nil(t, legacy.Snapshot.Daybreak)
	legacy.Status = "passed"
	require.NoError(t, r.Finish(ctx, legacy))
	require.Equal(t, "awaiting_confirmation", legacy.Result.Action)
	require.Equal(t, "awaiting_confirmation", attributionDaybreakRun(t, r, a.ID, "").Result.Action)
	require.Equal(t, "high", attributionDaybreakRun(t, r, a.ID, "").Result.Action)
}

func TestAttributionDaybreakClaimFreezeRollsBackAtomically(t *testing.T) {
	ctx := context.Background()
	r, _, a := attributionFixture(t)
	jobs, err := r.Enqueue(ctx, []int64{a.ID}, true)
	require.NoError(t, err)
	setAttributionDaybreakPreferences(t, a.ID, true, false)
	_, err = integrationDB.ExecContext(ctx, `CREATE FUNCTION attribution_daybreak_reject() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.snapshot ? 'daybreak' THEN RAISE EXCEPTION 'synthetic failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER attribution_daybreak_reject BEFORE UPDATE ON model_attribution_jobs FOR EACH ROW EXECUTE FUNCTION attribution_daybreak_reject()`)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(ctx, `DROP TRIGGER IF EXISTS attribution_daybreak_reject ON model_attribution_jobs; DROP FUNCTION IF EXISTS attribution_daybreak_reject()`)
	})
	_, err = r.Claim(ctx)
	require.Error(t, err)
	queued, err := r.Get(ctx, jobs[0].ID)
	require.NoError(t, err)
	require.Equal(t, "queued", queued.Status)
	require.Nil(t, queued.StartedAt)
	require.Nil(t, queued.Snapshot.Daybreak)
	require.Empty(t, queued.Claim)
	_, err = integrationDB.ExecContext(ctx, `DROP TRIGGER attribution_daybreak_reject ON model_attribution_jobs; DROP FUNCTION attribution_daybreak_reject()`)
	require.NoError(t, err)
	j, err := r.Claim(ctx)
	require.NoError(t, err)
	require.Equal(t, &service.DiagnosticDaybreakPreferences{Blue: true}, j.Snapshot.Daybreak)
}

func TestAttributionDaybreakMalformedResumeDoesNotRefreeze(t *testing.T) {
	ctx := context.Background()
	r, _, a := attributionFixture(t)
	jobs, err := r.Enqueue(ctx, []int64{a.ID}, true)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `UPDATE model_attribution_jobs SET status='retry_wait',started_at=NOW()-interval '1 hour',next_attempt_at=NOW()-interval '1 second' WHERE id=$1`, jobs[0].ID)
	require.NoError(t, err)
	setAttributionDaybreakPreferences(t, a.ID, true, true)
	j, err := r.Claim(ctx)
	require.NoError(t, err)
	require.Nil(t, j.Snapshot.Daybreak, "resuming incomplete evidence must not silently adopt current preferences")
}

func TestAttributionDaybreakNonOAuthPreferencesStayDisabled(t *testing.T) {
	ctx := context.Background()
	r, ar, _ := attributionFixture(t)
	a := attributionAPIKeyAccount(t, ar)
	setAttributionDaybreakPreferences(t, a.ID, true, true)
	_, err := r.Enqueue(ctx, []int64{a.ID}, true)
	require.NoError(t, err)
	j, err := r.Claim(ctx)
	require.NoError(t, err)
	require.Equal(t, &service.DiagnosticDaybreakPreferences{}, j.Snapshot.Daybreak)
}

func TestAttributionDaybreakSharedCredentialsUseBusinessAccountPreferences(t *testing.T) {
	ctx := context.Background()
	r, ar, parent := attributionFixture(t)
	shadow := &service.Account{Name: "daybreak-attribution-shadow", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Status: service.StatusActive, Schedulable: false, Concurrency: 1,
		ParentAccountID: &parent.ID, QuotaDimension: service.QuotaDimensionSpark, Credentials: map[string]any{"model_mapping": map[string]any{"old": "old"}}, Extra: map[string]any{}}
	require.NoError(t, ar.Create(ctx, shadow))
	t.Cleanup(func() { _, _ = integrationDB.ExecContext(ctx, `DELETE FROM accounts WHERE id=$1`, shadow.ID) })
	setAttributionDaybreakPreferences(t, shadow.ID, true, true)
	_, err := r.Enqueue(ctx, []int64{shadow.ID}, true)
	require.NoError(t, err)
	j, err := r.Claim(ctx)
	require.NoError(t, err)
	require.Equal(t, &service.DiagnosticDaybreakPreferences{Blue: true, Red: true}, j.Snapshot.Daybreak)
	// Changing the owner's business preferences does not change the shadow's
	// independently managed choices or its underlying authorization generation.
	setAttributionDaybreakPreferences(t, parent.ID, true, false)
	j.Status = "passed"
	attributionSuccessfulProbeEvidence(j, "daybreak_blue")
	require.NoError(t, r.Finish(ctx, j))
	require.Equal(t, "awaiting_confirmation", j.Result.Action)
	require.Empty(t, j.Reason)
}
