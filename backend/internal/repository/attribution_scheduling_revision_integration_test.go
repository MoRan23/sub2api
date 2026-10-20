//go:build integration

package repository

import (
	"context"
	"database/sql"
	"testing"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestAttributionSchedulingRevisionDatabaseWrites(t *testing.T) {
	ctx := context.Background()
	_, ar, _ := attributionFixture(t)
	a := attributionAPIKeyAccount(t, ar)
	_, err := integrationDB.ExecContext(ctx, `INSERT INTO model_attribution_state(account_id,pause_revision,pass_streak) VALUES($1,42,1)`, a.ID)
	require.NoError(t, err)
	schedulable, revision, pause := attributionSchedulingState(t, a.ID)
	require.True(t, schedulable)
	require.Zero(t, revision, "new accounts start without any scheduling intent")
	require.Equal(t, sql.NullInt64{Valid: true, Int64: 42}, pause)
	stateJSON := func() string {
		var state string
		require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT to_jsonb(s)::text FROM model_attribution_state s WHERE account_id=$1`, a.ID).Scan(&state))
		return state
	}
	originalState := stateJSON()
	for _, value := range []string{`true`, `null`, `"false"`, `0`} {
		_, err = integrationDB.ExecContext(ctx, `UPDATE accounts SET extra=jsonb_set(extra,'{openai_attribution_replace_whitelist_enabled}',$2::jsonb) WHERE id=$1`, a.ID, value)
		require.NoError(t, err)
		_, got, _ := attributionSchedulingState(t, a.ID)
		require.Equal(t, revision, got, "all historical non-boolean-false values have the same effective mode")
	}
	for _, next := range []bool{false, true, false} {
		_, err = integrationDB.ExecContext(ctx, `UPDATE accounts SET extra=jsonb_set(extra,'{openai_attribution_replace_whitelist_enabled}',to_jsonb($2::boolean)) WHERE id=$1`, a.ID, next)
		require.NoError(t, err)
		revision++
		gotSchedulable, gotRevision, _ := attributionSchedulingState(t, a.ID)
		require.True(t, gotSchedulable, "switching result mode must not schedule or pause an account")
		require.Equal(t, revision, gotRevision)
		require.Equal(t, a.Credentials["model_mapping"], attributionMapping(t, ar, a.ID))
		require.Equal(t, originalState, stateJSON(), "account triggers must not update or acquire attribution state")
	}
	for range 2 {
		_, err = integrationDB.ExecContext(ctx, `UPDATE accounts SET schedulable=false WHERE id=$1`, a.ID)
		require.NoError(t, err)
		revision++
		gotSchedulable, gotRevision, _ := attributionSchedulingState(t, a.ID)
		require.False(t, gotSchedulable)
		require.Equal(t, revision, gotRevision, "an explicit false-to-false write revokes the older pause claim")
		require.Equal(t, originalState, stateJSON())
	}
}

func TestAttributionSchedulingRevisionStaleSnapshotAndExplicitIntent(t *testing.T) {
	ctx := context.Background()
	_, ar, _ := attributionFixture(t)
	a := attributionAPIKeyAccount(t, ar)
	stale, err := ar.GetByID(ctx, a.ID)
	require.NoError(t, err)
	require.True(t, stale.Schedulable)
	require.NoError(t, ar.SetSchedulable(ctx, a.ID, false))
	_, pausedRevision, _ := attributionSchedulingState(t, a.ID)
	stale.Name = "unrelated snapshot rename"
	require.NoError(t, ar.Update(ctx, stale))
	require.False(t, stale.Schedulable, "returned snapshot must carry the current scheduling state")
	require.Equal(t, pausedRevision, stale.SchedulingRevision)
	schedulable, revision, _ := attributionSchedulingState(t, a.ID)
	require.False(t, schedulable, "old full-account snapshots cannot revive a pause")
	require.Equal(t, pausedRevision, revision)
	require.NoError(t, ar.Update(service.WithAccountSchedulingIntent(ctx, a.ID, false), stale))
	_, revision, _ = attributionSchedulingState(t, a.ID)
	require.Equal(t, pausedRevision+1, revision)
	require.Equal(t, revision, stale.SchedulingRevision)
	require.NoError(t, ar.Update(service.WithAccountSchedulingIntent(ctx, a.ID+1, true), stale))
	gotSchedulable, gotRevision, _ := attributionSchedulingState(t, a.ID)
	require.False(t, gotSchedulable, "an intent for another account cannot change scheduling")
	require.Equal(t, revision, gotRevision)
	require.NoError(t, ar.Update(service.WithAccountSchedulingIntent(ctx, a.ID, true), stale))
	schedulable, revision, _ = attributionSchedulingState(t, a.ID)
	require.True(t, schedulable)
	require.Equal(t, pausedRevision+2, revision)
	require.True(t, stale.Schedulable)
	require.Equal(t, revision, stale.SchedulingRevision)
}

func TestAttributionSchedulingRevisionModeAdminGuardAndBulk(t *testing.T) {
	ctx := context.Background()
	_, ar, _ := attributionFixture(t)
	a := attributionAPIKeyAccount(t, ar)
	admin := service.NewAdminService(nil, nil, nil, ar, nil, nil, nil, nil, nil, nil, nil, nil, nil, integrationEntClient, nil, nil, nil, nil, nil, nil, nil, nil)
	stale, err := ar.GetByID(ctx, a.ID)
	require.NoError(t, err)
	require.NoError(t, admin.UpdateAccountExtra(ctx, a.ID, map[string]any{service.OpenAIAttributionReplaceWhitelistEnabledExtraKey: false}))
	_, modeRevision, _ := attributionSchedulingState(t, a.ID)
	require.NoError(t, ar.Update(ctx, stale))
	require.NoError(t, ar.UpdateExtra(ctx, a.ID, map[string]any{service.OpenAIAttributionReplaceWhitelistEnabledExtraKey: true}))
	stored, err := ar.GetByID(ctx, a.ID)
	require.NoError(t, err)
	require.False(t, stored.OpenAIAttributionReplacesWhitelist(), "background snapshots and observations cannot override administrator preferences")
	require.Equal(t, modeRevision, stored.SchedulingRevision)
	_, err = admin.BulkUpdateAccounts(ctx, &service.BulkUpdateAccountsInput{AccountIDs: []int64{a.ID}, Extra: map[string]any{service.OpenAIAttributionReplaceWhitelistEnabledExtraKey: true}})
	require.NoError(t, err, "a mode-only bulk update must load its current account configuration")
	stored, err = ar.GetByID(ctx, a.ID)
	require.NoError(t, err)
	require.True(t, stored.OpenAIAttributionReplacesWhitelist())
	require.Equal(t, modeRevision+1, stored.SchedulingRevision)
	require.True(t, stored.Schedulable)
	require.Equal(t, a.Credentials["model_mapping"], stored.Credentials["model_mapping"])
}

func TestAttributionSchedulingRevisionErrorSafetyPause(t *testing.T) {
	ctx := context.Background()
	_, ar, _ := attributionFixture(t)
	a := attributionAPIKeyAccount(t, ar)
	a.Status = service.StatusError
	a.ErrorMessage = "synthetic upstream authentication failure"
	require.NoError(t, ar.Update(ctx, a))
	require.False(t, a.Schedulable, "new error status retains the existing safety pause behavior")
	require.EqualValues(t, 1, a.SchedulingRevision)
	a.Name = "error account renamed"
	require.NoError(t, ar.Update(ctx, a))
	require.False(t, a.Schedulable)
	require.EqualValues(t, 1, a.SchedulingRevision, "an unrelated edit of an existing error must not take over the pause")
}

func TestAttributionSchedulingRevisionCallerTransactionCache(t *testing.T) {
	for _, commit := range []bool{false, true} {
		t.Run(map[bool]string{true: "commit", false: "rollback"}[commit], func(t *testing.T) {
			ctx := context.Background()
			_, ar, _ := attributionFixture(t)
			a := attributionAPIKeyAccount(t, ar)
			cache := &schedulerCacheRecorder{}
			ar.schedulerCache = cache
			tx, err := integrationEntClient.Tx(ctx)
			require.NoError(t, err)
			t.Cleanup(func() { _ = tx.Rollback() })
			require.NoError(t, ar.SetSchedulable(dbent.NewTxContext(ctx, tx), a.ID, false))
			require.Empty(t, cache.setAccounts, "uncommitted scheduling state must not enter the shared cache")
			if commit {
				require.NoError(t, tx.Commit())
				require.Len(t, cache.setAccounts, 1, "cache refresh must read through a non-transactional context after commit")
				require.False(t, cache.setAccounts[0].Schedulable)
				require.EqualValues(t, 1, cache.setAccounts[0].SchedulingRevision)
				require.NoError(t, cache.setCtxErr)
			} else {
				require.NoError(t, tx.Rollback())
				require.Empty(t, cache.setAccounts)
			}
			schedulable, revision, _ := attributionSchedulingState(t, a.ID)
			require.Equal(t, !commit, schedulable)
			if commit {
				require.EqualValues(t, 1, revision)
			} else {
				require.Zero(t, revision)
			}
		})
	}
}
