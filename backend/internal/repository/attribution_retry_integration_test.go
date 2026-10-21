//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/Wei-Shaw/sub2api/migrations"
	"github.com/stretchr/testify/require"
)

func attributionRetryCheckpoint() *service.AttributionCheckpoint {
	return &service.AttributionCheckpoint{
		Version:         1,
		Challenges:      []service.AttributionChallenge{{ID: "one", Prompt: "first", ExpectedCount: 3}, {ID: "two", Prompt: "second", ExpectedCount: 3}, {ID: "three", Prompt: "third", ExpectedCount: 3}},
		Outputs:         []service.AttributionOutput{{Text: "private-answer-one", ExpectedCount: 3}, {Text: "private-answer-two", ExpectedCount: 3}},
		OrdinaryRetries: 1, UnavailableRetries: 4, RemainingBudgetMS: 431234,
	}
}

func assertAttributionPrivateCheckpoint(t *testing.T, id int64, expected *service.AttributionCheckpoint) {
	t.Helper()
	var raw []byte
	require.NoError(t, integrationDB.QueryRowContext(context.Background(), `SELECT checkpoint FROM model_attribution_jobs WHERE id=$1`, id).Scan(&raw))
	if expected == nil {
		require.Empty(t, raw)
		return
	}
	var actual service.AttributionCheckpoint
	require.NoError(t, json.Unmarshal(raw, &actual))
	require.Equal(t, *expected, actual)
}

func TestAttributionRetryWaitRecoveryAndClaimFences(t *testing.T) {
	ctx := context.Background()
	r, _, a := attributionFixture(t)
	_, err := r.Enqueue(ctx, []int64{a.ID}, true)
	require.NoError(t, err)
	j, err := r.Claim(ctx)
	require.NoError(t, err)
	require.NotNil(t, j)
	j.Checkpoint = attributionRetryCheckpoint()
	j.Result.CompletedProbes, j.Result.Retries = 2, 5
	j.Result.Usage = []*service.OpenAIUsage{{InputTokens: 11, OutputTokens: 22}, {InputTokens: 33, OutputTokens: 44}}
	ok, err := r.SaveCheckpoint(ctx, j)
	require.NoError(t, err)
	require.True(t, ok)
	started := j.StartedAt
	oldClaim := j.Claim
	ok, err = r.DeferRetry(ctx, j, time.Now().Add(time.Hour))
	require.NoError(t, err)
	require.True(t, ok)
	ok, err = r.SaveCheckpoint(ctx, j)
	require.NoError(t, err)
	require.False(t, ok, "the deferred worker no longer owns the job")
	ok, err = r.Heartbeat(ctx, j)
	require.NoError(t, err)
	require.False(t, ok)
	valid, err := r.Validate(ctx, j)
	require.NoError(t, err)
	require.False(t, valid)
	duplicate, err := r.Enqueue(ctx, []int64{a.ID}, true, service.AttributionProbeOptions{Model: "do-not-replace"})
	require.NoError(t, err)
	require.Equal(t, j.ID, duplicate[0].ID)
	require.Equal(t, "retry_wait", duplicate[0].Status)
	require.Equal(t, j.Snapshot.Policy.Model, duplicate[0].Snapshot.Policy.Model)
	detail, err := r.Get(ctx, j.ID)
	require.NoError(t, err)
	require.Nil(t, detail.Checkpoint, "polling public details must not load saved answers")
	assertAttributionPrivateCheckpoint(t, j.ID, j.Checkpoint)
	p, err := r.List(ctx, a.ID, 1, 20)
	require.NoError(t, err)
	require.Equal(t, 1, p.RetryWait)
	public, err := json.Marshal(p)
	require.NoError(t, err)
	require.NotContains(t, string(public), "private-answer")
	require.NotContains(t, string(public), "checkpoint")
	_, err = integrationDB.ExecContext(ctx, `UPDATE model_attribution_state SET next_due_at=NOW()-interval '1 hour' WHERE account_id=$1`, a.ID)
	require.NoError(t, err)
	automatic, err := r.Enqueue(ctx, nil, false)
	require.NoError(t, err)
	require.Empty(t, automatic, "waiting accounts must not be requeued by the scheduler")
	r = &attributionRepository{db: integrationDB}
	none, err := r.Claim(ctx)
	require.NoError(t, err)
	require.Nil(t, none, "waiting is durable across service replacement and is not due yet")
	_, err = integrationDB.ExecContext(ctx, `UPDATE model_attribution_jobs SET next_attempt_at=NOW()-interval '1 second',started_at=NOW()-interval '1 hour' WHERE id=$1`, j.ID)
	require.NoError(t, err)
	var wg sync.WaitGroup
	claims := make(chan *service.AttributionJob, 8)
	errs := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			claimed, e := (&attributionRepository{db: integrationDB}).Claim(ctx)
			claims <- claimed
			errs <- e
		}()
	}
	wg.Wait()
	close(claims)
	close(errs)
	for e := range errs {
		require.NoError(t, e)
	}
	var resumed *service.AttributionJob
	for claimed := range claims {
		if claimed != nil {
			require.Nil(t, resumed, "only one instance can claim the resumed job")
			resumed = claimed
		}
	}
	require.NotNil(t, resumed)
	require.Equal(t, j.ID, resumed.ID)
	require.NotEqual(t, oldClaim, resumed.Claim)
	require.True(t, resumed.StartedAt.Before(*started))
	require.Equal(t, j.Checkpoint, resumed.Checkpoint)
	require.Equal(t, j.Result, resumed.Result, "checkpointing must not double-count usage")
	require.Nil(t, resumed.NextAttemptAt)
	j.Status = "passed"
	attributionSuccessfulProbeEvidence(j, "")
	require.NoError(t, r.Finish(ctx, j))
	still, err := r.Get(ctx, j.ID)
	require.NoError(t, err)
	require.Equal(t, "running", still.Status)
	resumed.Status = "passed"
	attributionSuccessfulProbeEvidence(resumed, "")
	require.NoError(t, r.Finish(ctx, resumed))
	ended, err := r.Get(ctx, j.ID)
	require.NoError(t, err)
	require.Equal(t, "passed", ended.Status)
	require.Nil(t, ended.Checkpoint)
	assertAttributionPrivateCheckpoint(t, ended.ID, nil)
	require.Nil(t, ended.NextAttemptAt)
	require.Empty(t, ended.Claim)
}

func TestAttributionCancelIsIdempotentAndFencesWorkers(t *testing.T) {
	for _, state := range []string{"queued", "running", "retry_wait"} {
		t.Run(state, func(t *testing.T) {
			ctx := context.Background()
			r, ar, a := attributionFixture(t)
			require.Equal(t, "low", attributionRun(t, r, a.ID, "mismatch").Result.Action)
			low := attributionMapping(t, ar, a.ID)
			require.Equal(t, "awaiting_confirmation", attributionRun(t, r, a.ID, "passed").Result.Action)
			jobs, err := r.Enqueue(ctx, []int64{a.ID}, true)
			require.NoError(t, err)
			j := jobs[0]
			if state != "queued" {
				j, err = r.Claim(ctx)
				require.NoError(t, err)
				j.Checkpoint = attributionRetryCheckpoint()
				ok, err := r.SaveCheckpoint(ctx, j)
				require.NoError(t, err)
				require.True(t, ok)
			}
			if state == "retry_wait" {
				ok, err := r.DeferRetry(ctx, j, time.Now().Add(time.Hour))
				require.NoError(t, err)
				require.True(t, ok)
			}
			cancelled, err := r.Cancel(ctx, j.ID)
			require.NoError(t, err)
			require.Equal(t, "cancelled", cancelled.Status)
			require.Nil(t, cancelled.Checkpoint)
			assertAttributionPrivateCheckpoint(t, cancelled.ID, nil)
			require.Nil(t, cancelled.NextAttemptAt)
			require.Empty(t, cancelled.Claim)
			again, err := r.Cancel(ctx, j.ID)
			require.NoError(t, err)
			require.Equal(t, cancelled, again)
			j.Status = "passed"
			attributionSuccessfulProbeEvidence(j, "")
			require.NoError(t, r.Finish(ctx, j))
			require.Equal(t, low, attributionMapping(t, ar, a.ID))
			require.Equal(t, "awaiting_confirmation", attributionRun(t, r, a.ID, "passed").Result.Action)
			require.Equal(t, "high", attributionRun(t, r, a.ID, "passed").Result.Action)
		})
	}
}

func TestAttributionCancelConcurrentWithRetryWait(t *testing.T) {
	ctx := context.Background()
	r, ar, a := attributionFixture(t)
	_, err := r.Enqueue(ctx, []int64{a.ID}, true)
	require.NoError(t, err)
	j, err := r.Claim(ctx)
	require.NoError(t, err)
	j.Checkpoint = attributionRetryCheckpoint()
	start := make(chan struct{})
	errs := make(chan error, 2)
	go func() {
		<-start
		_, e := r.DeferRetry(ctx, j, time.Now().Add(time.Hour))
		errs <- e
	}()
	go func() {
		<-start
		_, e := (&attributionRepository{db: integrationDB}).Cancel(ctx, j.ID)
		errs <- e
	}()
	close(start)
	for range 2 {
		require.NoError(t, <-errs)
	}
	ended, err := r.Get(ctx, j.ID)
	require.NoError(t, err)
	require.Equal(t, "cancelled", ended.Status)
	require.Nil(t, ended.Checkpoint)
	assertAttributionPrivateCheckpoint(t, ended.ID, nil)
	require.Nil(t, ended.NextAttemptAt)
	require.Equal(t, a.Credentials["model_mapping"], attributionMapping(t, ar, a.ID))
}

func TestAttributionOperationalChangesDoNotInterruptAndCompatibleGroupsApply(t *testing.T) {
	ctx := context.Background()
	r, ar, a := attributionFixture(t)
	require.Equal(t, "low", attributionRun(t, r, a.ID, "mismatch").Result.Action)
	g := mustCreateGroup(t, integrationEntClient, &service.Group{Name: "attribution-equivalent-group", Platform: service.PlatformOpenAI})
	t.Cleanup(func() { _, _ = integrationDB.ExecContext(ctx, `DELETE FROM groups WHERE id=$1`, g.ID) })
	require.Equal(t, "awaiting_confirmation", attributionRun(t, r, a.ID, "passed").Result.Action)
	_, err := integrationDB.ExecContext(ctx, `UPDATE model_attribution_state SET next_due_at=NOW()-interval '1 second' WHERE account_id=$1`, a.ID)
	require.NoError(t, err)
	_, err = r.Enqueue(ctx, nil, false)
	require.NoError(t, err)
	j, err := r.Claim(ctx)
	require.NoError(t, err)
	require.NotNil(t, j)
	require.Equal(t, "scheduled", j.Source)
	// Administrative scheduling writes have their own v4 revision fence. Other
	// operational changes still allow a compatible policy to apply unchanged.
	_, err = integrationDB.ExecContext(ctx, `UPDATE accounts SET name='renamed',status='disabled',expires_at=NOW()-interval '1 hour',rate_limit_reset_at=NOW()+interval '1 hour' WHERE id=$1`, a.ID)
	require.NoError(t, err)
	require.NoError(t, ar.BindGroups(ctx, a.ID, []int64{g.ID}))
	valid, err := r.Validate(ctx, j)
	require.NoError(t, err)
	require.True(t, valid)
	j.Status = "passed"
	attributionSuccessfulProbeEvidence(j, "")
	require.NoError(t, r.Finish(ctx, j))
	require.Equal(t, "high", j.Result.Action)
	require.Equal(t, 2, j.Result.PassStreak, "equivalent policy across groups retains the first pass")
	require.Contains(t, attributionMapping(t, ar, a.ID), "high2")
}

func TestAttributionChangedPolicyOrMappingRecordsVerdictOnly(t *testing.T) {
	for _, mutation := range []string{"group", "mapping", "config"} {
		t.Run(mutation, func(t *testing.T) {
			ctx := context.Background()
			r, ar, a := attributionFixture(t)
			g := mustCreateGroup(t, integrationEntClient, &service.Group{Name: "attribution-different-group", Platform: service.PlatformOpenAI})
			t.Cleanup(func() { _, _ = integrationDB.ExecContext(ctx, `DELETE FROM groups WHERE id=$1`, g.ID) })
			c, err := r.Config(ctx)
			require.NoError(t, err)
			c.Groups = []service.AttributionGroupPolicy{{GroupID: g.ID, Enabled: true, AttributionPolicy: service.AttributionPolicy{Model: "different", HighModels: []string{"new-high"}, LowModels: []string{"new-low"}}}}
			_, err = r.SaveConfig(ctx, c)
			require.NoError(t, err)
			require.Equal(t, "high", attributionRun(t, r, a.ID, "passed").Result.Action)
			_, err = r.Enqueue(ctx, []int64{a.ID}, true)
			require.NoError(t, err)
			j, err := r.Claim(ctx)
			require.NoError(t, err)
			switch mutation {
			case "group":
				require.NoError(t, ar.BindGroups(ctx, a.ID, []int64{g.ID}))
			case "mapping":
				_, err = integrationDB.ExecContext(ctx, `UPDATE accounts SET credentials=jsonb_set(credentials,'{model_mapping}','{"admin":"admin"}') WHERE id=$1`, a.ID)
				require.NoError(t, err)
			case "config":
				c, err = r.Config(ctx)
				require.NoError(t, err)
				c.Default.HighModels = []string{"new-high"}
				_, err = r.SaveConfig(ctx, c)
				require.NoError(t, err)
			}
			before := attributionMapping(t, ar, a.ID)
			valid, err := r.Validate(ctx, j)
			require.NoError(t, err)
			require.True(t, valid)
			j.Status = "mismatch"
			attributionSuccessfulProbeEvidence(j, "")
			require.NoError(t, r.Finish(ctx, j))
			require.Equal(t, "mismatch", j.Status)
			require.Equal(t, "stale_policy", j.Result.Action)
			require.Equal(t, "policy_changed", j.Reason)
			require.Equal(t, before, attributionMapping(t, ar, a.ID))
			var streak int
			require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT pass_streak FROM model_attribution_state WHERE account_id=$1`, a.ID).Scan(&streak))
			require.Zero(t, streak)
		})
	}
}

func TestAttributionRetryMigrationPreservesHistoryAndInterruptsLegacy(t *testing.T) {
	ctx := context.Background()
	r, _, a := attributionFixture(t)
	finished := attributionRun(t, r, a.ID, "passed")
	jobs, err := r.Enqueue(ctx, []int64{a.ID}, true)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `UPDATE model_attribution_jobs SET snapshot=snapshot-'version' WHERE id=$1`, jobs[0].ID)
	require.NoError(t, err)
	body, err := migrations.FS.ReadFile("270_attribution_retry_checkpoints.sql")
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, string(body))
	require.NoError(t, err)
	legacy, err := r.Get(ctx, jobs[0].ID)
	require.NoError(t, err)
	require.Equal(t, "failed", legacy.Status)
	require.Equal(t, "interrupted", legacy.Reason)
	history, err := r.Get(ctx, finished.ID)
	require.NoError(t, err)
	require.Equal(t, finished, history)
	_, err = r.Enqueue(ctx, []int64{a.ID}, true)
	require.NoError(t, err)
	j, err := r.Claim(ctx)
	require.NoError(t, err)
	j.Checkpoint = attributionRetryCheckpoint()
	ok, err := r.DeferRetry(ctx, j, time.Now().Add(time.Hour))
	require.NoError(t, err)
	require.True(t, ok)
	_, err = integrationDB.ExecContext(ctx, string(body))
	require.NoError(t, err)
	require.NoError(t, ApplyMigrations(ctx, integrationDB))
	still, err := r.Get(ctx, j.ID)
	require.NoError(t, err)
	require.Equal(t, "retry_wait", still.Status)
	require.Nil(t, still.Checkpoint)
	assertAttributionPrivateCheckpoint(t, still.ID, j.Checkpoint)
}

func TestAttributionRetryCheckpointRollback(t *testing.T) {
	ctx := context.Background()
	r, _, a := attributionFixture(t)
	_, err := r.Enqueue(ctx, []int64{a.ID}, true)
	require.NoError(t, err)
	j, err := r.Claim(ctx)
	require.NoError(t, err)
	j.Checkpoint = attributionRetryCheckpoint()
	_, err = integrationDB.ExecContext(ctx, `CREATE FUNCTION attribution_checkpoint_reject() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'synthetic failure'; END $$; CREATE TRIGGER attribution_checkpoint_reject BEFORE UPDATE ON model_attribution_jobs FOR EACH ROW EXECUTE FUNCTION attribution_checkpoint_reject()`)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(ctx, `DROP TRIGGER IF EXISTS attribution_checkpoint_reject ON model_attribution_jobs; DROP FUNCTION IF EXISTS attribution_checkpoint_reject()`)
	})
	j.Result.CompletedProbes = 2
	ok, err := r.DeferRetry(ctx, j, time.Now().Add(time.Hour))
	require.Error(t, err)
	require.False(t, ok)
	stored, err := r.Get(ctx, j.ID)
	require.NoError(t, err)
	require.Equal(t, "running", stored.Status)
	require.Equal(t, j.Claim, stored.Claim)
	require.Nil(t, stored.Checkpoint)
	assertAttributionPrivateCheckpoint(t, stored.ID, nil)
	require.Nil(t, stored.NextAttemptAt)
	require.Zero(t, stored.Result.CompletedProbes)
}

func TestAttributionRunningCheckpointIsNotReplayedAfterLeaseLoss(t *testing.T) {
	ctx := context.Background()
	r, _, a := attributionFixture(t)
	_, err := r.Enqueue(ctx, []int64{a.ID}, true)
	require.NoError(t, err)
	j, err := r.Claim(ctx)
	require.NoError(t, err)
	j.Checkpoint = attributionRetryCheckpoint()
	j.Result.CompletedProbes = 2
	ok, err := r.SaveCheckpoint(ctx, j)
	require.NoError(t, err)
	require.True(t, ok)
	// The first two probes are known, but the third may have reached upstream.
	// Only an explicit 503 defer allows restart recovery.
	_, err = integrationDB.ExecContext(ctx, `UPDATE model_attribution_jobs SET lease_until=NOW()-interval '1 second' WHERE id=$1`, j.ID)
	require.NoError(t, err)
	ok, err = r.DeferRetry(ctx, j, time.Now().Add(time.Minute))
	require.NoError(t, err)
	require.False(t, ok)
	resumed, err := (&attributionRepository{db: integrationDB}).Claim(ctx)
	require.NoError(t, err)
	require.Nil(t, resumed)
	ended, err := r.Get(ctx, j.ID)
	require.NoError(t, err)
	require.Equal(t, "failed", ended.Status)
	require.Equal(t, "interrupted", ended.Reason)
	require.Nil(t, ended.Checkpoint)
	assertAttributionPrivateCheckpoint(t, ended.ID, nil)
	require.Nil(t, ended.NextAttemptAt)
	require.Equal(t, 2, ended.Result.CompletedProbes)
}

func TestAttributionOverridesSurviveChangedInheritedPolicy(t *testing.T) {
	for _, explicitEmpty := range []bool{false, true} {
		t.Run(map[bool]string{false: "inherit", true: "explicit_follow_probe"}[explicitEmpty], func(t *testing.T) {
			ctx := context.Background()
			r, ar, a := attributionFixture(t)
			g := mustCreateGroup(t, integrationEntClient, &service.Group{Name: "override-group", Platform: service.PlatformOpenAI})
			t.Cleanup(func() { _, _ = integrationDB.ExecContext(ctx, `DELETE FROM groups WHERE id=$1`, g.ID) })
			c, err := r.Config(ctx)
			require.NoError(t, err)
			p := c.Default
			p.Model, p.ExpectedModels = "group-probe", []string{"group-expected"}
			// Different whitelist ordering still represents the same policy.
			p.HighModels = []string{"high2", "high"}
			c.Groups = []service.AttributionGroupPolicy{{GroupID: g.ID, Enabled: true, AttributionPolicy: p}}
			_, err = r.SaveConfig(ctx, c)
			require.NoError(t, err)
			o := service.AttributionProbeOptions{Model: "manual-probe"}
			if explicitEmpty {
				empty := []string{}
				o.ExpectedModels = &empty
			}
			jobs, err := r.Enqueue(ctx, []int64{a.ID}, true, o)
			require.NoError(t, err)
			if explicitEmpty {
				require.NotNil(t, jobs[0].Snapshot.Overrides.ExpectedModels)
				require.Empty(t, *jobs[0].Snapshot.Overrides.ExpectedModels)
			} else {
				require.Nil(t, jobs[0].Snapshot.Overrides.ExpectedModels)
			}
			j, err := r.Claim(ctx)
			require.NoError(t, err)
			require.NoError(t, ar.BindGroups(ctx, a.ID, []int64{g.ID}))
			valid, err := r.Validate(ctx, j)
			require.NoError(t, err)
			require.True(t, valid)
			j.Status = "passed"
			attributionSuccessfulProbeEvidence(j, "")
			require.NoError(t, r.Finish(ctx, j))
			if explicitEmpty {
				require.Equal(t, "high", j.Result.Action)
				require.Equal(t, 1, j.Result.PassStreak)
				require.Contains(t, attributionMapping(t, ar, a.ID), "high2")
			} else {
				require.Equal(t, "stale_policy", j.Result.Action)
				require.Zero(t, j.Result.PassStreak)
			}
		})
	}
}
