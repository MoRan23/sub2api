package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
)

func (r *accountRepository) GetShadowParentHealthBatch(ctx context.Context, ids []int64) (map[int64]service.ShadowParentHealth, error) {
	if r == nil || r.sql == nil {
		return nil, fmt.Errorf("account repository SQL executor is not configured")
	}
	out := make(map[int64]service.ShadowParentHealth, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := r.sql.QueryContext(ctx, `SELECT id,platform,type,status,expires_at,auto_pause_on_expired,temp_unschedulable_until FROM accounts WHERE id=ANY($1) AND deleted_at IS NULL`, pq.Array(ids))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var p service.ShadowParentHealth
		if err := rows.Scan(&p.ID, &p.Platform, &p.Type, &p.Status, &p.ExpiresAt, &p.AutoPauseOnExpired, &p.TempUnschedulableUntil); err != nil {
			return nil, err
		}
		out[p.ID] = p
	}
	return out, rows.Err()
}

func (c *schedulerCache) GetShadowParentHealthBatch(ctx context.Context, ids []int64) (map[int64]service.ShadowParentHealth, error) {
	out := make(map[int64]service.ShadowParentHealth, len(ids))
	keys := make([]string, len(ids))
	for i, id := range ids {
		keys[i] = schedulerAccountMetaKey(strconv.FormatInt(id, 10))
	}
	values, err := c.mgetChunked(ctx, keys)
	if err != nil {
		return nil, err
	}
	for i, v := range values {
		if v == nil {
			continue
		}
		raw, err := schedulerCacheOptionalString(v)
		if err != nil {
			return nil, err
		}
		var p service.ShadowParentHealth
		if err := json.Unmarshal([]byte(raw), &p); err != nil {
			// A corrupt member falls back independently; healthy siblings do not
			// need another database read.
			continue
		}
		if p.ID == ids[i] {
			out[p.ID] = p
		}
	}
	return out, nil
}
