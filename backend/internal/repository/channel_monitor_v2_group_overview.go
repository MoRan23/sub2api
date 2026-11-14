package repository

import (
	"context"
	"fmt"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
)

// Each current member contributes at most 20 completed results. A result ranked
// below 20 for one account cannot enter any group's latest 20. This bounds JSON
// extraction and sort input without reading snapshots, answers or account names.
const channelMonitorV2GroupAttributionSQL = `
WITH members AS MATERIALIZED (
 SELECT ag.group_id, ag.account_id
 FROM account_groups ag
 JOIN accounts a ON a.id = ag.account_id AND a.deleted_at IS NULL
 WHERE ag.group_id = ANY($1)
), account_results AS MATERIALIZED (
 SELECT accounts.account_id, recent.*
 FROM (SELECT DISTINCT account_id FROM members) accounts
 CROSS JOIN LATERAL (
  SELECT j.id, j.finished_at, j.status,
         LEFT(COALESCE(j.snapshot #>> '{policy,model}', ''), 200) AS probe_model
  FROM model_attribution_jobs j
  WHERE j.account_id = accounts.account_id AND j.finished_at IS NOT NULL
  ORDER BY j.finished_at DESC, j.id DESC LIMIT 20
 ) recent
), ranked AS (
 SELECT m.group_id, r.finished_at, r.status, r.probe_model,
        ROW_NUMBER() OVER (PARTITION BY m.group_id ORDER BY r.finished_at DESC, r.id DESC) AS rank
 FROM members m JOIN account_results r ON r.account_id = m.account_id
)
SELECT group_id, finished_at, status, probe_model FROM ranked
WHERE rank <= 20 ORDER BY group_id, rank`

func (r *channelMonitorV2Repository) GetGroupAttributionHistory(ctx context.Context, groupIDs []int64) (map[int64][]service.ChannelMonitorV2AttributionEntry, error) {
	out := make(map[int64][]service.ChannelMonitorV2AttributionEntry, len(groupIDs))
	if len(groupIDs) == 0 {
		return out, nil
	}
	rows, err := r.db.QueryContext(ctx, channelMonitorV2GroupAttributionSQL, pq.Array(groupIDs))
	if err != nil {
		return nil, fmt.Errorf("load group attribution history: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var groupID int64
		var item service.ChannelMonitorV2AttributionEntry
		if err := rows.Scan(&groupID, &item.FinishedAt, &item.Status, &item.ProbeModel); err != nil {
			return nil, fmt.Errorf("scan group attribution history: %w", err)
		}
		// Never expose raw result.reason: it can contain account or upstream data.
		switch item.Status {
		case "passed":
			item.ReasonCode = "reference_match"
		case "mismatch":
			item.ReasonCode = "reference_mismatch"
		case "abnormal":
			item.ReasonCode = "analysis_abnormal"
		case "cancelled", "skipped":
			item.ReasonCode = item.Status
		default:
			item.Status, item.ReasonCode = "failed", "execution_failed"
		}
		out[groupID] = append(out[groupID], item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate group attribution history: %w", err)
	}
	return out, nil
}
