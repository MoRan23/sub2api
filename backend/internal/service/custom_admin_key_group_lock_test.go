//go:build unit

package service

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/pkg/adminaccess"
	"github.com/stretchr/testify/require"
)

func TestScopedAdminAccountGroupsLockBeforeBindings(t *testing.T) {
	for _, lockedGroup := range []int64{1, 2} {
		t.Run(map[int64]string{1: "existing", 2: "target"}[lockedGroup], func(t *testing.T) {
			login, db := scopedAdminAccountTestDB(t)
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			_, err := db.ExecContext(ctx, `CREATE TABLE account_groups(account_id BIGINT,group_id BIGINT);
				INSERT INTO accounts(id,name) VALUES(101,'lock-test'); INSERT INTO account_groups VALUES(101,1)`)
			require.NoError(t, err)
			repo := &scopedAdminWriteTestRepo{login: login}
			admin := &adminServiceImpl{accountRepo: repo, entClient: dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))}
			ctx = adminaccess.WithPrincipal(ctx, &adminaccess.Principal{Policy: adminaccess.Policy{Rules: []adminaccess.Rule{{Permissions: []string{"accounts.groups"}, GroupIDs: []int64{1, 2}, AccountTypes: []string{"oauth"}}}}})
			holder, err := db.BeginTx(ctx, nil)
			require.NoError(t, err)
			defer func() { _ = holder.Rollback() }()
			var id, pid int64
			require.NoError(t, holder.QueryRowContext(ctx, `SELECT id FROM groups WHERE id=$1 FOR UPDATE`, lockedGroup).Scan(&id))
			require.NoError(t, holder.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&pid))
			done := make(chan error, 1)
			go func() {
				_, err := admin.ScopedAdminWrite(ctx, 101, "", map[string]json.RawMessage{"group_ids": json.RawMessage(`[2]`)}, func(_ context.Context, a *Account) (*Account, error) { return a, nil })
				done <- err
			}()
			require.Eventually(t, func() bool {
				var waiting bool
				err := db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE query LIKE '/* scoped_admin_group_lock */%' AND wait_event_type='Lock' AND $1=ANY(pg_blocking_pids(pid)))`, pid).Scan(&waiting)
				return err == nil && waiting
			}, 5*time.Second, 10*time.Millisecond)
			// Group deletion/clear can still finish its association work while a
			// scoped account writer waits for the shared group barrier.
			require.NoError(t, holder.QueryRowContext(ctx, `SELECT account_id FROM account_groups WHERE account_id=101 FOR UPDATE NOWAIT`).Scan(&id))
			require.NoError(t, holder.Commit())
			require.NoError(t, <-done)
		})
	}
}
