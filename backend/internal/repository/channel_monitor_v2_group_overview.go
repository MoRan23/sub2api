package repository

import (
	"context"
	"fmt"
	"time"

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
	 JOIN accounts a ON a.id = ag.account_id AND a.deleted_at IS NULL AND a.schedulable = TRUE
 WHERE ag.group_id = ANY($1)
), account_results AS MATERIALIZED (
 SELECT accounts.account_id, recent.*
 FROM (SELECT DISTINCT account_id FROM members) accounts
 CROSS JOIN LATERAL (
  SELECT j.id, j.finished_at, j.status,
         LEFT(COALESCE(j.snapshot #>> '{policy,model}', ''), 200) AS probe_model
  FROM model_attribution_jobs j
	  WHERE j.account_id = accounts.account_id AND j.finished_at IS NOT NULL AND j.finished_at <= $2
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
	return r.getGroupAttributionHistoryAt(ctx, groupIDs, time.Now().UTC())
}

func (r *channelMonitorV2Repository) getGroupAttributionHistoryAt(ctx context.Context, groupIDs []int64, asOf time.Time) (map[int64][]service.ChannelMonitorV2AttributionEntry, error) {
	out := make(map[int64][]service.ChannelMonitorV2AttributionEntry, len(groupIDs))
	if len(groupIDs) == 0 {
		return out, nil
	}
	rows, err := r.db.QueryContext(ctx, channelMonitorV2GroupAttributionSQL, pq.Array(groupIDs), asOf)
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

// Only scalar fields leave PostgreSQL. Read each distinct current account once,
// using index 286 for window rows and the one pre-window API-key anchor. Convert
// results into validity intervals before projecting them onto sample indices:
// OAuth intervals cover at most two samples; API-key intervals never overlap.
// This avoids rescanning account job histories for each group or each bucket.
const channelMonitorV2GroupAttributionOverviewSQL = `
WITH requested_groups AS MATERIALIZED (
 SELECT DISTINCT unnest($1::bigint[]) AS group_id
), members AS MATERIALIZED (
 SELECT ag.group_id, a.id AS account_id, a.type,
        a.platform = 'openai' AND a.parent_account_id IS NULL AND (
         (a.type = 'oauth' AND NOT CASE
          WHEN jsonb_typeof(a.extra->'openai_passthrough') = 'boolean'
           THEN a.extra->'openai_passthrough' = 'true'::jsonb
          ELSE COALESCE(a.extra->'openai_oauth_passthrough' = 'true'::jsonb, FALSE) END)
         OR (a.type = 'apikey'
          AND (NOT (a.extra ? 'openai_api_key_mode') OR a.extra->'openai_api_key_mode' IN ('"generic"'::jsonb, '"codex_engine"'::jsonb))
          AND a.extra->'openai_api_key_auto_attribution_enabled' = 'true'::jsonb)
        ) AS auto_eligible
 FROM account_groups ag
 JOIN accounts a ON a.id = ag.account_id AND a.deleted_at IS NULL AND a.schedulable = TRUE
 WHERE ag.group_id = ANY($1)
), current_accounts AS MATERIALIZED (
 SELECT DISTINCT account_id, type FROM members WHERE type IN ('oauth', 'apikey')
), window_results AS MATERIALIZED (
 SELECT a.account_id, a.type, j.id, j.finished_at, j.status
 FROM current_accounts a
 CROSS JOIN LATERAL (
  SELECT j.id, j.finished_at, j.status FROM model_attribution_jobs j
  WHERE j.account_id = a.account_id AND j.finished_at IS NOT NULL
    AND j.finished_at >= $2::timestamptz - CASE WHEN a.type = 'oauth' THEN INTERVAL '205 minutes' ELSE INTERVAL '200 minutes' END
    AND j.finished_at <= $2::timestamptz
    AND j.status IN ('passed', 'mismatch', 'failed', 'abnormal')
 ) j
), api_results AS MATERIALIZED (
 SELECT account_id, id, finished_at, status FROM window_results WHERE type = 'apikey'
 UNION ALL
 SELECT a.account_id, j.id, j.finished_at, j.status
 FROM current_accounts a
 CROSS JOIN LATERAL (
  SELECT j.id, j.finished_at, j.status FROM model_attribution_jobs j
  WHERE j.account_id = a.account_id AND j.finished_at IS NOT NULL
    AND j.finished_at < $2::timestamptz - INTERVAL '200 minutes'
    AND j.status IN ('passed', 'mismatch', 'failed', 'abnormal')
  ORDER BY j.finished_at DESC, j.id DESC LIMIT 1
 ) j
 WHERE a.type = 'apikey'
), api_intervals AS (
 SELECT account_id, finished_at, status,
        LEAD(finished_at) OVER (PARTITION BY account_id ORDER BY finished_at, id) AS next_at
 FROM api_results
), sample_intervals AS (
 SELECT account_id, status,
        GREATEST(1, CEIL(EXTRACT(EPOCH FROM (finished_at - ($2::timestamptz - INTERVAL '200 minutes'))) / 600)::integer) AS first_sample,
        LEAST(20, FLOOR(EXTRACT(EPOCH FROM (finished_at + INTERVAL '15 minutes' - ($2::timestamptz - INTERVAL '200 minutes'))) / 600)::integer) AS last_sample
 FROM window_results WHERE type = 'oauth'
 UNION ALL
 SELECT account_id, status,
        GREATEST(1, CEIL(EXTRACT(EPOCH FROM (finished_at - ($2::timestamptz - INTERVAL '200 minutes'))) / 600)::integer),
        CASE WHEN next_at IS NULL THEN 20 ELSE LEAST(20, CEIL(EXTRACT(EPOCH FROM (next_at - ($2::timestamptz - INTERVAL '200 minutes'))) / 600)::integer - 1) END
 FROM api_intervals
), account_samples AS MATERIALIZED (
 SELECT i.account_id, s.sample,
        MAX(CASE i.status WHEN 'passed' THEN 3 WHEN 'mismatch' THEN 2 ELSE 1 END) AS priority
 FROM sample_intervals i CROSS JOIN LATERAL generate_series(i.first_sample, i.last_sample) s(sample)
 GROUP BY i.account_id, s.sample
), group_samples AS (
 SELECT m.group_id, s.sample, MAX(s.priority) AS priority
 FROM members m JOIN account_samples s ON s.account_id = m.account_id
 GROUP BY m.group_id, s.sample
), group_enabled AS (
 SELECT group_id, BOOL_OR(auto_eligible) AS enabled FROM members GROUP BY group_id
)
SELECT g.group_id,
       COALESCE((SELECT config->'enabled' = 'true'::jsonb FROM model_attribution_config WHERE id = 1), FALSE)
        AND COALESCE(e.enabled, FALSE) AS enabled,
       b.sample,
       CASE s.priority WHEN 3 THEN 'passed' WHEN 2 THEN 'mismatch' WHEN 1 THEN 'failed' ELSE 'no_data' END AS state
FROM requested_groups g CROSS JOIN generate_series(1, 20) b(sample)
LEFT JOIN group_samples s ON s.group_id = g.group_id AND s.sample = b.sample
LEFT JOIN group_enabled e ON e.group_id = g.group_id
ORDER BY g.group_id, b.sample`

func (r *channelMonitorV2Repository) GetGroupAttributionOverview(ctx context.Context, groupIDs []int64, asOf time.Time) (map[int64]service.ChannelMonitorV2OverviewAttribution, error) {
	out := make(map[int64]service.ChannelMonitorV2OverviewAttribution, len(groupIDs))
	if len(groupIDs) == 0 {
		return out, nil
	}
	for _, id := range groupIDs {
		out[id] = service.NewChannelMonitorV2OverviewAttribution(asOf)
	}
	rows, err := r.db.QueryContext(ctx, channelMonitorV2GroupAttributionOverviewSQL, pq.Array(groupIDs), asOf)
	if err != nil {
		return nil, fmt.Errorf("load group attribution overview: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var groupID int64
		var enabled bool
		var sample int
		var state string
		if err := rows.Scan(&groupID, &enabled, &sample, &state); err != nil {
			return nil, fmt.Errorf("scan group attribution overview: %w", err)
		}
		item, exists := out[groupID]
		if !exists || sample < 1 || sample > len(item.History) {
			return nil, fmt.Errorf("invalid group attribution sample")
		}
		item.Enabled = enabled
		item.History[sample-1].State = state
		if sample == len(item.History) {
			item.SummaryState = state
		}
		out[groupID] = item
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate group attribution overview: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close group attribution overview: %w", err)
	}
	// Retain the old latest/recent projection for clients that still display it.
	// These bounded display rows never feed the summary or its timeline.
	histories, err := r.getGroupAttributionHistoryAt(ctx, groupIDs, asOf)
	if err != nil {
		return nil, err
	}
	for id, recent := range histories {
		item := out[id]
		item.Recent = recent
		if len(recent) > 0 {
			item.Latest = &item.Recent[0]
		}
		out[id] = item
	}
	return out, nil
}
