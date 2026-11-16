package repository

import (
	"context"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

// ReleaseExecution runs only after all physical probes have exited. Cancellation
// retains this lease so another dispatcher cannot overbook the cluster meanwhile.
func (r *attributionRepository) ReleaseExecution(ctx context.Context, job *service.AttributionJob) error {
	_, err := r.db.ExecContext(ctx, `UPDATE model_attribution_jobs SET claim_id=NULL,lease_until=NULL WHERE id=$1 AND claim_id=$2 AND finished_at IS NOT NULL`, job.ID, job.Claim)
	return err
}

func (r *attributionRepository) MaintainHistory(ctx context.Context, cursor int64) (int64, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return cursor, err
	}
	defer tx.Rollback()
	var locked bool
	if err = tx.QueryRowContext(ctx, `SELECT pg_try_advisory_xact_lock(263, 2)`).Scan(&locked); err != nil {
		return cursor, err
	}
	if !locked {
		return cursor, nil
	}
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT account_id FROM model_attribution_jobs WHERE account_id>$1 ORDER BY account_id LIMIT 50`, cursor)
	if err != nil {
		return cursor, err
	}
	ids := make([]int64, 0, 50)
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return cursor, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return cursor, err
	}
	if len(ids) == 0 {
		return 0, tx.Commit()
	}
	if err = pruneAttribution(ctx, tx, ids...); err != nil {
		return cursor, err
	}
	return ids[len(ids)-1], tx.Commit()
}
