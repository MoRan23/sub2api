CREATE TABLE IF NOT EXISTS model_attribution_config (
    id INTEGER PRIMARY KEY CHECK(id=1),
    version BIGINT NOT NULL DEFAULT 1,
    config JSONB NOT NULL DEFAULT '{"enabled":false,"base_url":"","default":{"model":"gpt-6-astra","high_models":[],"low_models":[]},"groups":[]}'::jsonb,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
INSERT INTO model_attribution_config(id) VALUES(1) ON CONFLICT DO NOTHING;

CREATE TABLE IF NOT EXISTS model_attribution_state (
    account_id BIGINT PRIMARY KEY REFERENCES accounts(id) ON DELETE CASCADE,
    next_due_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    config_version BIGINT NOT NULL DEFAULT 0,
    authorization_digest TEXT NOT NULL DEFAULT '',
    policy_digest TEXT NOT NULL DEFAULT '',
    verdict TEXT NOT NULL DEFAULT '' CHECK(verdict IN ('','passed','mismatch'))
);
CREATE TABLE IF NOT EXISTS model_attribution_jobs (
    id BIGSERIAL PRIMARY KEY,
    account_id BIGINT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    account_name TEXT NOT NULL,
    source TEXT NOT NULL CHECK(source IN ('manual','scheduled')),
    status TEXT NOT NULL CHECK(status IN ('queued','running','passed','mismatch','abnormal','failed','skipped')),
    reason TEXT NOT NULL DEFAULT '',
    snapshot JSONB NOT NULL,
    authorization_digest TEXT NOT NULL,
    fence_digest TEXT NOT NULL,
    result JSONB NOT NULL DEFAULT '{}'::jsonb,
    claim_id UUID,
    lease_until TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    started_at TIMESTAMPTZ,
    finished_at TIMESTAMPTZ
);
CREATE UNIQUE INDEX IF NOT EXISTS model_attribution_active_account ON model_attribution_jobs(account_id) WHERE status IN ('queued','running');
CREATE INDEX IF NOT EXISTS model_attribution_queue ON model_attribution_jobs(status,id);
CREATE INDEX IF NOT EXISTS model_attribution_account_history ON model_attribution_jobs(account_id,id DESC);
