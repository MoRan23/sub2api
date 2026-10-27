//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

func groupLockFixture(t *testing.T, ctx context.Context) (int64, int64, int64) {
	t.Helper()
	var accountID, oldGroupID, newGroupID int64
	name := fmt.Sprintf("binding-lock-%d", time.Now().UnixNano())
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO accounts(name,platform,type)
		VALUES($1,'openai','apikey') RETURNING id`, name).Scan(&accountID))
	for i, id := range []*int64{&oldGroupID, &newGroupID} {
		require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO groups(name,platform,status,subscription_type)
			VALUES($1,'openai','active','standard') RETURNING id`, fmt.Sprintf("%s-%d", name, i)).Scan(id))
	}
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM account_groups WHERE account_id=$1`, accountID)
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM accounts WHERE id=$1`, accountID)
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM groups WHERE id=ANY($1)`, pq.Array([]int64{oldGroupID, newGroupID}))
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM scheduler_outbox WHERE account_id=$1 OR group_id=ANY($2)`, accountID, pq.Array([]int64{oldGroupID, newGroupID}))
	})
	return accountID, oldGroupID, newGroupID
}

func TestGroupMembershipWritersLockAccountFirst(t *testing.T) {
	for _, name := range []string{"bind_empty_membership", "replace", "clear", "add", "remove", "bulk_add", "copy", "delete_account"} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			accountID, oldGroupID, newGroupID := groupLockFixture(t, ctx)
			if name != "bind_empty_membership" && name != "add" && name != "bulk_add" {
				_, err := integrationDB.ExecContext(ctx, `INSERT INTO account_groups(account_id,group_id,priority) VALUES($1,$2,7)`, accountID, oldGroupID)
				require.NoError(t, err)
			}
			accountRepo := newAccountRepositoryWithSQL(integrationEntClient, integrationDB, nil)
			groupRepo := newGroupRepositoryWithSQL(integrationEntClient, integrationDB)
			copyGroup := &service.Group{Name: fmt.Sprintf("copy-lock-%d", time.Now().UnixNano()), Platform: service.PlatformOpenAI, Status: service.StatusActive, SubscriptionType: service.SubscriptionTypeStandard, RateMultiplier: 1}
			t.Cleanup(func() {
				if copyGroup.ID > 0 {
					_, _ = integrationDB.ExecContext(context.Background(), `DELETE FROM account_groups WHERE group_id=$1`, copyGroup.ID)
					_, _ = integrationDB.ExecContext(context.Background(), `DELETE FROM groups WHERE id=$1`, copyGroup.ID)
					_, _ = integrationDB.ExecContext(context.Background(), `DELETE FROM scheduler_outbox WHERE group_id=$1`, copyGroup.ID)
				}
			})
			holder, err := integrationDB.BeginTx(ctx, nil)
			require.NoError(t, err)
			defer func() { _ = holder.Rollback() }()
			require.NoError(t, lockAccountGroupAccounts(ctx, holder, []int64{accountID}))
			var pid int
			require.NoError(t, holder.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&pid))
			done := make(chan error, 1)
			go func() {
				switch name {
				case "bind_empty_membership", "replace":
					done <- accountRepo.BindGroups(ctx, accountID, []int64{newGroupID})
				case "clear":
					done <- accountRepo.BindGroups(ctx, accountID, nil)
				case "add":
					done <- accountRepo.AddToGroup(ctx, accountID, newGroupID, 4)
				case "remove":
					done <- accountRepo.RemoveFromGroup(ctx, accountID, oldGroupID)
				case "bulk_add":
					done <- groupRepo.BindAccountsToGroup(ctx, newGroupID, []int64{accountID})
				case "copy":
					done <- groupRepo.CreateFromSource(ctx, copyGroup, oldGroupID)
				case "delete_account":
					done <- accountRepo.Delete(ctx, accountID)
				}
			}()
			require.Eventually(t, func() bool {
				var waiting bool
				err := integrationDB.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity
					WHERE query LIKE '/* account_group_account_lock */%' AND wait_event_type='Lock'
					AND $1=ANY(pg_blocking_pids(pid)))`, pid).Scan(&waiting)
				return err == nil && waiting
			}, 5*time.Second, 10*time.Millisecond)
			// The pending writer must not already hold a group/association lock:
			// attribution can still lock the group after owning the account row.
			var lockedGroupID int64
			require.NoError(t, holder.QueryRowContext(ctx, `SELECT id FROM groups WHERE id=$1 FOR UPDATE NOWAIT`, oldGroupID).Scan(&lockedGroupID))
			require.NoError(t, holder.Commit())
			require.NoError(t, <-done)
			var count int
			require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT count(*) FROM account_groups WHERE account_id=$1`, accountID).Scan(&count))
			if name == "clear" || name == "remove" || name == "delete_account" {
				require.Zero(t, count)
			} else if name == "copy" {
				require.Equal(t, 2, count)
			} else {
				require.Equal(t, 1, count)
			}
		})
	}
}

func TestGroupMembershipClearPublishesOldGroupsAndRollsBack(t *testing.T) {
	ctx := context.Background()
	accountID, oldGroupID, _ := groupLockFixture(t, ctx)
	_, err := integrationDB.ExecContext(ctx, `INSERT INTO account_groups(account_id,group_id,priority) VALUES($1,$2,1)`, accountID, oldGroupID)
	require.NoError(t, err)
	tx, err := integrationEntClient.Tx(ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	repo := newAccountRepositoryWithSQL(tx.Client(), tx, nil)
	require.NoError(t, repo.BindGroups(ctx, accountID, nil))
	var payload []byte
	require.NoError(t, scanSingleRow(ctx, tx, `SELECT payload FROM scheduler_outbox WHERE account_id=$1
		AND event_type=$2 ORDER BY id DESC LIMIT 1`, []any{accountID, service.SchedulerOutboxEventAccountGroupsChanged}, &payload))
	var event struct {
		GroupIDs []int64 `json:"group_ids"`
	}
	require.NoError(t, json.Unmarshal(payload, &event))
	require.Equal(t, []int64{oldGroupID}, event.GroupIDs)
	require.NoError(t, tx.Rollback())
	var count int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT count(*) FROM account_groups WHERE account_id=$1`, accountID).Scan(&count))
	require.Equal(t, 1, count)
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT count(*) FROM scheduler_outbox WHERE account_id=$1`, accountID).Scan(&count))
	require.Zero(t, count)
}

func TestGroupMembershipClearUsesGroupBarrier(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	accountID, oldGroupID, _ := groupLockFixture(t, ctx)
	_, err := integrationDB.ExecContext(ctx, `INSERT INTO account_groups(account_id,group_id,priority) VALUES($1,$2,1)`, accountID, oldGroupID)
	require.NoError(t, err)
	holder, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = holder.Rollback() }()
	require.NoError(t, lockAccountGroupAccounts(ctx, holder, []int64{accountID}))
	require.NoError(t, lockAccountGroupRows(ctx, holder, []int64{oldGroupID}))
	var pid int
	require.NoError(t, holder.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&pid))
	done := make(chan error, 1)
	go func() {
		_, err := newGroupRepositoryWithSQL(integrationEntClient, integrationDB).DeleteAccountGroupsByGroupID(ctx, oldGroupID)
		done <- err
	}()
	require.Eventually(t, func() bool {
		var waiting bool
		err := integrationDB.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity
			WHERE query='SELECT id FROM groups WHERE id=$1 FOR UPDATE' AND wait_event_type='Lock'
			AND $1=ANY(pg_blocking_pids(pid)))`, pid).Scan(&waiting)
		return err == nil && waiting
	}, 5*time.Second, 10*time.Millisecond)
	// Clearing cannot race through a routing transaction's membership snapshot.
	var count int
	require.NoError(t, holder.QueryRowContext(ctx, `SELECT count(*) FROM account_groups WHERE account_id=$1`, accountID).Scan(&count))
	require.Equal(t, 1, count)
	require.NoError(t, holder.Commit())
	require.NoError(t, <-done)
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT count(*) FROM account_groups WHERE account_id=$1`, accountID).Scan(&count))
	require.Zero(t, count)
}
