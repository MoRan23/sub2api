CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_attribution_live_execution_lease
ON model_attribution_jobs (lease_until) WHERE claim_id IS NOT NULL;
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_attribution_dispatch_order
ON model_attribution_jobs (COALESCE(next_attempt_at,created_at),id)
WHERE status IN ('queued','retry_wait');
