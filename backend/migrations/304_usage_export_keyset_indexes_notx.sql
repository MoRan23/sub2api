CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_usage_logs_export_model_id
    ON usage_logs ((COALESCE(NULLIF(BTRIM(requested_model), ''), model)), id);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_usage_logs_export_created_id
    ON usage_logs (created_at, id);
