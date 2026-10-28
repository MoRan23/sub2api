package service

import (
	"context"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/lib/pq"
)

func lockScopedAdminAccount(ctx context.Context, tx *dbent.Tx, id int64) error {
	_, err := tx.Client().ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('scoped_admin_account:' || $1::text, 0))`, id)
	return err
}

// Scope checks and writes share the same account → ordered groups → bindings
// lock order as attribution and ordinary account edits. Lock before bindings so
// group deletion/clearing cannot race through authorization or form a lock cycle.
func lockScopedAdminAccountGroups(ctx context.Context, tx *dbent.Tx, id int64, targetGroupIDs []int64) error {
	rows, err := tx.Client().QueryContext(ctx, `/* scoped_admin_group_lock */
		SELECT id FROM groups WHERE id IN (
			SELECT group_id FROM account_groups WHERE account_id=$1
			UNION SELECT unnest($2::bigint[])
		) ORDER BY id FOR SHARE`, id, pq.Array(targetGroupIDs))
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var groupID int64
		if err := rows.Scan(&groupID); err != nil {
			return err
		}
	}
	return rows.Err()
}

// AfterAccountCommit 将不参与回滚的缓存和上游副作用延迟到事务提交成功后。
// 普通管理员没有外层事务时立即执行，保留原有行为。
func AfterAccountCommit(ctx context.Context, run func(context.Context)) {
	if tx := dbent.TxFromContext(ctx); tx != nil {
		tx.OnCommit(func(next dbent.Committer) dbent.Committer {
			return dbent.CommitFunc(func(commitCtx context.Context, tx *dbent.Tx) error {
				if err := next.Commit(commitCtx, tx); err != nil {
					return err
				}
				run(dbent.NewTxContext(ctx, nil))
				return nil
			})
		})
	} else {
		run(ctx)
	}
}
