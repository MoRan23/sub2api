package repository

import (
	"context"

	"github.com/lib/pq"
)

// Account-scoped membership writers lock accounts before groups and join rows.
// The account row also serializes an initially empty membership set, for which
// locking existing account_groups rows would not protect against new bindings.
func lockAccountGroupAccounts(ctx context.Context, exec sqlExecutor, accountIDs []int64) error {
	if len(accountIDs) == 0 {
		return nil
	}
	rows, err := exec.QueryContext(ctx, `/* account_group_account_lock */
		SELECT id FROM accounts WHERE id = ANY($1) ORDER BY id FOR UPDATE`, pq.Array(accountIDs))
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return err
		}
	}
	return rows.Err()
}

// Lock the complete old/new group union before touching any join rows. Missing
// or soft-deleted old groups must not prevent removing a stale membership;
// callers separately validate live target groups with lockLiveGroups.
func lockAccountGroupRows(ctx context.Context, exec sqlExecutor, groupIDs []int64) error {
	if len(groupIDs) == 0 {
		return nil
	}
	rows, err := exec.QueryContext(ctx, `/* account_group_ordered_group_lock */
		SELECT id FROM groups WHERE id = ANY($1) ORDER BY id FOR SHARE`, pq.Array(groupIDs))
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return err
		}
	}
	return rows.Err()
}

// The caller holds the account lock; this read deliberately does not lock join
// rows before the corresponding group locks have been acquired.
func loadAccountGroupIDsLocked(ctx context.Context, exec sqlExecutor, accountID int64) ([]int64, error) {
	rows, err := exec.QueryContext(ctx,
		`SELECT group_id FROM account_groups WHERE account_id=$1 ORDER BY priority,group_id`, accountID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
