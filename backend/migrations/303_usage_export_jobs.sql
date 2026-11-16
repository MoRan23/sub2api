CREATE TABLE IF NOT EXISTS usage_export_jobs (
 id TEXT PRIMARY KEY,
 owner_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 owner JSONB NOT NULL,
 spec JSONB NOT NULL,
 status TEXT NOT NULL DEFAULT 'queued' CHECK(status IN ('queued','running','completed','failed','cancelled','deleting')),
 max_id BIGINT NOT NULL,
 total_rows BIGINT NOT NULL,
 rows_written BIGINT NOT NULL DEFAULT 0,
 size_bytes BIGINT NOT NULL DEFAULT 0,
 file_key TEXT NOT NULL DEFAULT '',
 claim_token TEXT NOT NULL DEFAULT '',
 attempts INTEGER NOT NULL DEFAULT 0,
 lease_until TIMESTAMPTZ,
 started_at TIMESTAMPTZ,
 error_code TEXT NOT NULL DEFAULT '',
 ticket_hash TEXT,
 ticket_until TIMESTAMPTZ,
 created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 expires_at TIMESTAMPTZ
);
CREATE UNIQUE INDEX IF NOT EXISTS usage_export_active_owner ON usage_export_jobs(owner_id) WHERE status IN ('queued','running');
CREATE INDEX IF NOT EXISTS usage_export_claim ON usage_export_jobs(status,created_at);
CREATE INDEX IF NOT EXISTS usage_export_expiry ON usage_export_jobs(expires_at);
CREATE INDEX IF NOT EXISTS usage_export_live_lease ON usage_export_jobs(lease_until) WHERE lease_until IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS usage_export_ticket ON usage_export_jobs(ticket_hash) WHERE ticket_hash IS NOT NULL;
CREATE INDEX IF NOT EXISTS usage_export_owner_list ON usage_export_jobs(owner_id, (owner->>'kind'), (COALESCE(owner->>'key_id','')), (COALESCE(owner->>'revision','')), created_at DESC, id DESC);

-- Reservations survive lost workers and user/job deletion until the actual
-- artifact has been removed. Deliberately no cascading foreign key.
CREATE TABLE IF NOT EXISTS usage_export_artifacts (
 job_id TEXT NOT NULL,
 claim_token TEXT NOT NULL,
 file_key TEXT NOT NULL UNIQUE,
 reserved_bytes BIGINT NOT NULL CHECK(reserved_bytes >= 0),
 lease_until TIMESTAMPTZ,
 download_token TEXT NOT NULL DEFAULT '',
 download_until TIMESTAMPTZ,
 created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 PRIMARY KEY(job_id,claim_token)
);
CREATE INDEX IF NOT EXISTS usage_export_artifact_cleanup ON usage_export_artifacts(created_at);
