//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func attributionActionGroup(t *testing.T, name string) int64 {
	t.Helper()
	g := mustCreateGroup(t, integrationEntClient, &service.Group{Name: name, Platform: service.PlatformOpenAI})
	t.Cleanup(func() { _, _ = integrationDB.ExecContext(context.Background(), `DELETE FROM groups WHERE id=$1`, g.ID) })
	return g.ID
}

func attributionSetActions(t *testing.T, r *attributionRepository, high, low []int64) service.AttributionConfig {
	t.Helper()
	c, err := r.Config(context.Background())
	require.NoError(t, err)
	hm, lm := service.AttributionModelMappings{"alias": "high-target", "high": "mapped-high"}, service.AttributionModelMappings{}
	c.Default.HighModelMapping, c.Default.LowModelMapping = &hm, &lm
	if high != nil {
		c.Default.HighGroupIDs = &high
	}
	if low != nil {
		c.Default.LowGroupIDs = &low
	}
	c, err = r.SaveConfig(context.Background(), c)
	require.NoError(t, err)
	return c
}

func attributionAssertGroups(t *testing.T, ar *accountRepository, id int64, ids ...int64) {
	t.Helper()
	a, err := ar.GetByID(context.Background(), id)
	require.NoError(t, err)
	require.Equal(t, ids, a.GroupIDs)
	for i, g := range a.AccountGroups {
		require.Equal(t, i+1, g.Priority)
	}
}

func TestAttributionActionsApplyAtomicTierAndPreserveStreak(t *testing.T) {
	ctx := context.Background()
	r, ar, a := attributionFixture(t)
	old := attributionActionGroup(t, "actions-original")
	high, low := attributionActionGroup(t, "actions-high"), attributionActionGroup(t, "actions-low")
	require.NoError(t, ar.BindGroups(ctx, a.ID, []int64{old}))
	attributionSetActions(t, r, []int64{high, low}, []int64{low})
	_, err := integrationDB.ExecContext(ctx, `DELETE FROM scheduler_outbox WHERE account_id=$1`, a.ID)
	require.NoError(t, err)
	first := attributionRun(t, r, a.ID, "passed")
	require.Equal(t, "high", first.Result.Action)
	require.Equal(t, "high", first.Result.AppliedTier)
	require.Equal(t, 1, first.Result.PassStreak)
	require.Equal(t, []int64{old}, *first.Result.GroupsBefore)
	require.Equal(t, []int64{high, low}, *first.Result.GroupsAfter)
	require.Equal(t, map[string]any{"high": "mapped-high", "high2": "high2", "alias": "high-target"}, attributionMapping(t, ar, a.ID))
	attributionAssertGroups(t, ar, a.ID, high, low)
	var raw []byte
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT payload FROM scheduler_outbox WHERE account_id=$1 ORDER BY id DESC LIMIT 1`, a.ID).Scan(&raw))
	var payload struct {
		GroupIDs []int64 `json:"group_ids"`
	}
	require.NoError(t, json.Unmarshal(raw, &payload))
	require.ElementsMatch(t, []int64{old, high, low}, payload.GroupIDs)
	second := attributionRun(t, r, a.ID, "passed")
	require.Equal(t, 2, second.Result.PassStreak, "same policy in the new group keeps the streak")
	require.Equal(t, "unchanged", second.Result.Action)
	require.Equal(t, "low", attributionRun(t, r, a.ID, "mismatch").Result.Action)
	require.Equal(t, map[string]any{"low": "low"}, attributionMapping(t, ar, a.ID))
	attributionAssertGroups(t, ar, a.ID, low)
	require.Equal(t, "awaiting_confirmation", attributionRun(t, r, a.ID, "passed").Result.Action)
	attributionAssertGroups(t, ar, a.ID, low)
	require.Equal(t, "high", attributionRun(t, r, a.ID, "passed").Result.Action)
	attributionAssertGroups(t, ar, a.ID, high, low)
}

func TestAttributionActionsGroupOnlyAndNextPolicy(t *testing.T) {
	ctx := context.Background()
	r, ar, a := attributionFixture(t)
	target := attributionActionGroup(t, "actions-next-policy")
	c, err := r.Config(ctx)
	require.NoError(t, err)
	ids := []int64{target}
	c.Default.HighGroupIDs = &ids
	c.Groups = []service.AttributionGroupPolicy{{GroupID: target, Enabled: true, AttributionPolicy: service.AttributionPolicy{Model: "other-probe", HighModels: []string{"other-high"}, LowModels: []string{"other-low"}}}}
	_, err = r.SaveConfig(ctx, c)
	require.NoError(t, err)
	want := service.AttributionModelMapping(a.Credentials["model_mapping"].(map[string]any), c.Default.HighModels)
	b, err := json.Marshal(want)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `UPDATE accounts SET credentials=jsonb_set(credentials,'{model_mapping}',$2::jsonb) WHERE id=$1`, a.ID, b)
	require.NoError(t, err)
	first := attributionRun(t, r, a.ID, "passed")
	require.Equal(t, "high", first.Result.Action, "group-only changes still apply and publish")
	require.Equal(t, first.Result.Before, first.Result.After)
	require.Empty(t, *first.Result.GroupsBefore)
	attributionAssertGroups(t, ar, a.ID, target)
	next := attributionRun(t, r, a.ID, "passed")
	require.Equal(t, "other-probe", next.Snapshot.Policy.Model)
	require.Equal(t, "awaiting_confirmation", next.Result.Action)
	require.Equal(t, 1, next.Result.PassStreak)
	require.Equal(t, want, attributionMapping(t, ar, a.ID))
}

func TestAttributionActionsRecordInitiallyEmptyModelConfiguration(t *testing.T) {
	ctx := context.Background()
	r, ar, a := attributionFixture(t)
	_, err := integrationDB.ExecContext(ctx, `UPDATE accounts SET credentials=credentials-'model_mapping' WHERE id=$1`, a.ID)
	require.NoError(t, err)
	attributionSetActions(t, r, nil, nil)
	j := attributionRun(t, r, a.ID, "passed")
	require.NotNil(t, j.Result.Before)
	require.Empty(t, j.Result.Before)
	require.Equal(t, attributionMapping(t, ar, a.ID), j.Result.After)
	stored, err := r.Get(ctx, j.ID)
	require.NoError(t, err)
	require.NotNil(t, stored.Result.Before, "an observed empty configuration must survive JSON persistence")
	require.Empty(t, stored.Result.Before)
}

func TestAttributionActionsDoNotOverrideManualGroupsOrInvalidTargets(t *testing.T) {
	for _, change := range []string{"manual-group", "manual-priority", "deleted-target", "incompatible-target"} {
		t.Run(change, func(t *testing.T) {
			ctx := context.Background()
			r, ar, a := attributionFixture(t)
			original, target, manual := attributionActionGroup(t, "actions-before"), attributionActionGroup(t, "actions-target"), attributionActionGroup(t, "actions-manual")
			require.NoError(t, ar.BindGroups(ctx, a.ID, []int64{original}))
			attributionSetActions(t, r, []int64{target}, []int64{target})
			j := attributionClaimFor(t, r, a.ID)
			var err error
			switch change {
			case "manual-group":
				err = ar.BindGroups(ctx, a.ID, []int64{manual})
			case "manual-priority":
				_, err = integrationDB.ExecContext(ctx, `UPDATE account_groups SET priority=9 WHERE account_id=$1`, a.ID)
			case "deleted-target":
				_, err = integrationDB.ExecContext(ctx, `UPDATE groups SET deleted_at=NOW() WHERE id=$1`, target)
			case "incompatible-target":
				_, err = integrationDB.ExecContext(ctx, `UPDATE groups SET platform='anthropic' WHERE id=$1`, target)
			}
			require.NoError(t, err)
			valid, err := r.Validate(ctx, j)
			require.NoError(t, err)
			require.True(t, valid, "routing changes must not interrupt execution")
			j.Status = "passed"
			attributionSuccessfulProbeEvidence(j, "")
			require.NoError(t, r.Finish(ctx, j))
			require.Equal(t, "passed", j.Status)
			if change == "manual-group" || change == "manual-priority" {
				require.Equal(t, "account_groups_changed", j.Reason)
			} else {
				require.Equal(t, "actions_blocked", j.Result.Action)
			}
			require.Equal(t, a.Credentials["model_mapping"], attributionMapping(t, ar, a.ID))
			var verdict string
			require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT verdict FROM model_attribution_state WHERE account_id=$1`, a.ID).Scan(&verdict))
			require.Empty(t, verdict, "blocked actions do not consume the first applicable result")
			require.Zero(t, j.Result.PassStreak)
		})
	}
}

func TestAttributionActionsRespectAccountModeAndManualPermission(t *testing.T) {
	for _, mode := range []string{"schedule", "manual-opt-out", "engine-opt-out", "api-oauth-only"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			r, ar, a := attributionFixture(t)
			target := attributionActionGroup(t, "actions-gated")
			attributionSetActions(t, r, []int64{target}, []int64{target})
			if mode == "schedule" {
				attributionSetScheduleMode(t, a.ID, true)
			} else {
				a = attributionAPIKeyAccount(t, ar)
				if mode == "engine-opt-out" {
					_, err := integrationDB.ExecContext(ctx, `UPDATE accounts SET extra='{"openai_api_key_mode":"codex_engine"}' WHERE id=$1`, a.ID)
					require.NoError(t, err)
				}
				if mode == "api-oauth-only" {
					attributionSetAutomatic(t, a.ID, true)
					_, err := integrationDB.ExecContext(ctx, `UPDATE groups SET require_oauth_only=true WHERE id=$1`, target)
					require.NoError(t, err)
				}
			}
			j := attributionRun(t, r, a.ID, "mismatch")
			require.Equal(t, a.Credentials["model_mapping"], attributionMapping(t, ar, a.ID))
			fresh, err := ar.GetByID(ctx, a.ID)
			require.NoError(t, err)
			require.Empty(t, fresh.GroupIDs)
			switch mode {
			case "schedule":
				require.Equal(t, "schedule_paused", j.Result.Action)
			case "api-oauth-only":
				require.Equal(t, "target_groups_incompatible", j.Reason)
			default:
				require.Equal(t, "record_only", j.Result.Action)
			}
		})
	}
}

func TestAttributionActionsValidateConfigAndSimpleMode(t *testing.T) {
	ctx := context.Background()
	r, _, _ := attributionFixture(t)
	target := attributionActionGroup(t, "actions-validation")
	_, err := integrationDB.ExecContext(ctx, `UPDATE groups SET platform='composite' WHERE id=$1`, target)
	require.NoError(t, err)
	c, err := r.Config(ctx)
	require.NoError(t, err)
	ids := []int64{target}
	c.Default.HighGroupIDs = &ids
	r.cfg = &config.Config{RunMode: config.RunModeSimple}
	_, err = r.SaveConfig(ctx, c)
	require.ErrorIs(t, err, service.ErrAttributionInvalid)
	r.cfg = nil
	_, err = r.SaveConfig(ctx, c)
	require.NoError(t, err)
	c, err = r.Config(ctx)
	require.NoError(t, err)
	missing := []int64{9223372036854775807}
	c.Default.LowGroupIDs = &missing
	_, err = r.SaveConfig(ctx, c)
	require.ErrorIs(t, err, service.ErrAttributionInvalid)
}

func TestAttributionActionsRetryRecoveryAndLegacySnapshot(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(map[bool]string{false: "recovery", true: "legacy"}[legacy], func(t *testing.T) {
			ctx := context.Background()
			r, ar, a := attributionFixture(t)
			target := attributionActionGroup(t, "actions-recovery")
			attributionSetActions(t, r, []int64{target}, []int64{target})
			j := attributionClaimFor(t, r, a.ID)
			if legacy {
				j.Snapshot.Version = 4
				j.Snapshot.Policy = j.Snapshot.Policy.WithoutActions()
				j.Snapshot.AccountGroups = nil
			} else {
				j.Checkpoint = attributionRetryCheckpoint()
				j.Result.CompletedProbes = 2
				ok, err := r.DeferRetry(ctx, j, time.Now().Add(-time.Second))
				require.NoError(t, err)
				require.True(t, ok)
				r = &attributionRepository{db: integrationDB}
				j, err = r.Claim(ctx)
				require.NoError(t, err)
				require.NotNil(t, j)
				require.NotNil(t, j.Snapshot.Policy.HighGroupIDs)
				require.Equal(t, []int64{target}, *j.Snapshot.Policy.HighGroupIDs)
			}
			j.Status = "passed"
			attributionSuccessfulProbeEvidence(j, "")
			require.NoError(t, r.Finish(ctx, j))
			if legacy {
				fresh, err := ar.GetByID(ctx, a.ID)
				require.NoError(t, err)
				require.Empty(t, fresh.GroupIDs)
				require.Equal(t, "custom", attributionMapping(t, ar, a.ID)["alias"])
			} else {
				attributionAssertGroups(t, ar, a.ID, target)
				require.Equal(t, "high-target", attributionMapping(t, ar, a.ID)["alias"])
			}
		})
	}
}

func TestAttributionActionsRollbackRetryAndDuplicateCompletion(t *testing.T) {
	ctx := context.Background()
	r, ar, a := attributionFixture(t)
	target := attributionActionGroup(t, "actions-transaction")
	attributionSetActions(t, r, []int64{target}, []int64{target})
	j := attributionClaimFor(t, r, a.ID)
	j.Status = "passed"
	attributionSuccessfulProbeEvidence(j, "")
	_, err := integrationDB.ExecContext(ctx, `CREATE SEQUENCE attribution_action_fail_once;
CREATE FUNCTION attribution_action_fail_once() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF nextval('attribution_action_fail_once')=1 THEN RAISE EXCEPTION 'synthetic retry' USING ERRCODE='40001'; END IF; RETURN NEW; END $$;
CREATE TRIGGER attribution_action_fail_once BEFORE UPDATE ON model_attribution_jobs FOR EACH ROW EXECUTE FUNCTION attribution_action_fail_once()`)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(ctx, `DROP TRIGGER IF EXISTS attribution_action_fail_once ON model_attribution_jobs; DROP FUNCTION IF EXISTS attribution_action_fail_once(); DROP SEQUENCE IF EXISTS attribution_action_fail_once`)
	})
	_, err = integrationDB.ExecContext(ctx, `DELETE FROM scheduler_outbox WHERE account_id=$1`, a.ID)
	require.NoError(t, err)
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for range 2 {
		copy := *j
		wg.Add(1)
		go func() { defer wg.Done(); errs <- r.Finish(ctx, &copy) }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	stored, err := r.Get(ctx, j.ID)
	require.NoError(t, err)
	require.Equal(t, "high", stored.Result.AppliedTier)
	require.Empty(t, *stored.Result.GroupsBefore, "rolled-back target membership must not leak into the retry")
	attributionAssertGroups(t, ar, a.ID, target)
	require.Equal(t, 1, stored.Result.PassStreak)
	var events int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT count(*) FROM scheduler_outbox WHERE account_id=$1`, a.ID).Scan(&events))
	require.Equal(t, 1, events)
}

func TestAttributionActionsShadowLocksOwnerBeforeChildAndGroups(t *testing.T) {
	for _, operation := range []string{"enqueue", "enqueue_batch", "validate", "finish"} {
		t.Run(operation, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			r, ar, parent := attributionFixture(t)
			var other *service.Account
			if operation == "enqueue_batch" {
				other = &service.Account{Name: "actions-lock-batch", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth,
					Status: service.StatusActive, Schedulable: true, Concurrency: 1, Credentials: map[string]any{"access_token": "synthetic-batch"}, Extra: map[string]any{}}
				require.NoError(t, ar.Create(ctx, other))
				t.Cleanup(func() {
					_, _ = integrationDB.ExecContext(context.Background(), `DELETE FROM accounts WHERE id=$1`, other.ID)
				})
			}
			shadow := &service.Account{Name: "actions-lock-shadow", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth,
				Status: service.StatusActive, Schedulable: false, Concurrency: 1, ParentAccountID: &parent.ID,
				QuotaDimension: service.QuotaDimensionSpark, Credentials: map[string]any{"model_mapping": map[string]any{"old": "old"}}, Extra: map[string]any{}}
			require.NoError(t, ar.Create(ctx, shadow))
			t.Cleanup(func() {
				_, _ = integrationDB.ExecContext(context.Background(), `DELETE FROM accounts WHERE id=$1`, shadow.ID)
			})
			groupID := attributionActionGroup(t, "actions-shadow-group")
			require.NoError(t, ar.BindGroups(ctx, shadow.ID, []int64{groupID}))
			var job *service.AttributionJob
			if operation == "validate" || operation == "finish" {
				job = attributionClaimFor(t, r, shadow.ID)
				job.Status = "passed"
				attributionSuccessfulProbeEvidence(job, "")
			}
			holder, err := integrationDB.BeginTx(ctx, nil)
			require.NoError(t, err)
			defer func() { _ = holder.Rollback() }()
			require.NoError(t, lockAccountGroupAccounts(ctx, holder, []int64{parent.ID}))
			var pid int
			require.NoError(t, holder.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&pid))
			done := make(chan error, 1)
			go func() {
				switch operation {
				case "enqueue":
					_, err := r.Enqueue(ctx, []int64{shadow.ID}, true)
					done <- err
				case "enqueue_batch":
					_, err := r.Enqueue(ctx, []int64{other.ID, shadow.ID}, true)
					done <- err
				case "validate":
					_, err := r.Validate(ctx, job)
					done <- err
				case "finish":
					done <- r.Finish(ctx, job)
				}
			}()
			require.Eventually(t, func() bool {
				var waiting bool
				err := integrationDB.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity
					WHERE query LIKE '/* account_group_account_lock */%' AND wait_event_type='Lock'
					AND $1=ANY(pg_blocking_pids(pid)))`, pid).Scan(&waiting)
				return err == nil && waiting
			}, 5*time.Second, 10*time.Millisecond)
			// A bulk binder holding the earlier owner row can still acquire the
			// child and its groups. The old child→owner order formed a cycle here.
			var locked int64
			if other != nil {
				require.NoError(t, holder.QueryRowContext(ctx, `SELECT id FROM accounts WHERE id=$1 FOR UPDATE NOWAIT`, other.ID).Scan(&locked))
			}
			require.NoError(t, holder.QueryRowContext(ctx, `SELECT id FROM accounts WHERE id=$1 FOR UPDATE NOWAIT`, shadow.ID).Scan(&locked))
			require.NoError(t, holder.QueryRowContext(ctx, `SELECT id FROM groups WHERE id=$1 FOR UPDATE NOWAIT`, groupID).Scan(&locked))
			require.NoError(t, holder.Commit())
			require.NoError(t, <-done)
		})
	}
}
