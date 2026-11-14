-- Bound each current account's contribution to the public group overview.
-- Existing jobs and retained history are unchanged. Concurrent creation avoids
-- holding a write-blocking lock during upgrades of large attribution tables.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_model_attribution_jobs_account_finished
    ON model_attribution_jobs (account_id, finished_at DESC, id DESC)
    WHERE finished_at IS NOT NULL;
