-- Required for bounded per-request final-outcome lookup. Include successful
-- retry summaries as the latest row may have status 2xx.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_ops_error_monitor_request_identity
ON ops_error_logs (
    api_key_id,
    (COALESCE(group_id, 0)),
    (CASE WHEN NULLIF(client_request_id, '') IS NOT NULL THEN 'client:' || client_request_id ELSE 'local:' || NULLIF(request_id, '') END),
    created_at DESC, id DESC
)
WHERE NOT is_count_tokens;
