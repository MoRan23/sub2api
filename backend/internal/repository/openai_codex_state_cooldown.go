package repository

import (
	"context"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
)

func (r *openAICodexStateRepository) GetCollectorCooldowns(ctx context.Context, ownerIDs []int64) (map[int64]time.Time, error) {
	result := make(map[int64]time.Time)
	if len(ownerIDs) == 0 {
		return result, nil
	}
	if err := r.databaseAvailable(); err != nil {
		return nil, err
	}
	rows, err := r.db.QueryContext(ctx, `SELECT id,codex_turn_state_retry_after FROM accounts
		WHERE id=ANY($1) AND deleted_at IS NULL AND codex_turn_state_retry_after IS NOT NULL`, pq.Array(ownerIDs))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var until time.Time
		if err := rows.Scan(&id, &until); err != nil {
			return nil, err
		}
		result[id] = until
	}
	return result, rows.Err()
}

func (r *openAICodexStateRepository) ExtendCollectorCooldown(ctx context.Context, ownerID int64, until time.Time) error {
	if err := r.databaseAvailable(); err != nil {
		return err
	}
	if ownerID <= 0 || until.IsZero() {
		return nil
	}
	_, err := r.db.ExecContext(ctx, `UPDATE accounts SET codex_turn_state_retry_after=
		GREATEST(codex_turn_state_retry_after,$2) WHERE id=$1 AND deleted_at IS NULL`, ownerID, until.UTC())
	return err
}

var _ service.CodexTurnStateCooldownRepository = (*openAICodexStateRepository)(nil)
