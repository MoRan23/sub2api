-- Engine receipts may finish after the downstream connection/process exits.
-- These rows are a durable billing inbox, not a best-effort usage queue. Do not
-- cascade account/key deletion into pending receipts or their dedup evidence.
CREATE TABLE IF NOT EXISTS codex_engine_billing_jobs (
    id BIGSERIAL PRIMARY KEY,
    client_request_id VARCHAR(128) NOT NULL UNIQUE,
    account_id BIGINT NOT NULL CHECK (account_id > 0),
    user_id BIGINT NOT NULL CHECK (user_id > 0),
    api_key_id BIGINT NOT NULL CHECK (api_key_id > 0),
    identity_digest VARCHAR(128) NOT NULL,
    instance_id VARCHAR(256) NOT NULL DEFAULT '',
    snapshot JSONB NOT NULL CHECK (jsonb_typeof(snapshot) = 'object'),
    receipt_id VARCHAR(256) NOT NULL DEFAULT '',
    state VARCHAR(16) NOT NULL DEFAULT 'pending'
        CHECK (state IN ('pending', 'blocked', 'sealed', 'linked', 'not_dispatched')),
    cursor TEXT NOT NULL DEFAULT '',
    revision BIGINT NOT NULL DEFAULT 0 CHECK (revision >= 0),
    consumed_sequence BIGINT NOT NULL DEFAULT 0 CHECK (consumed_sequence >= 0),
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    claim_token VARCHAR(64) NOT NULL DEFAULT '',
    lease_until TIMESTAMPTZ,
    attempt_count INTEGER NOT NULL DEFAULT 0,
    linked_job_id BIGINT REFERENCES codex_engine_billing_jobs(id),
    terminal_seal JSONB,
    last_error VARCHAR(2048) NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CHECK ((state = 'linked') = (linked_job_id IS NOT NULL)),
    CHECK ((state = 'sealed') = (terminal_seal IS NOT NULL)),
    CHECK ((claim_token = '') = (lease_until IS NULL))
);

CREATE UNIQUE INDEX IF NOT EXISTS codex_engine_billing_jobs_receipt_idx
    ON codex_engine_billing_jobs (instance_id, receipt_id)
    WHERE receipt_id <> '' AND state <> 'linked';
CREATE INDEX IF NOT EXISTS codex_engine_billing_jobs_due_idx
    ON codex_engine_billing_jobs (next_attempt_at, id)
    WHERE state = 'pending';

CREATE TABLE IF NOT EXISTS codex_engine_billing_events (
    job_id BIGINT NOT NULL REFERENCES codex_engine_billing_jobs(id),
    event_id VARCHAR(256) NOT NULL,
    sequence BIGINT NOT NULL CHECK (sequence > 0),
    summary JSONB NOT NULL CHECK (jsonb_typeof(summary) = 'object'),
    fingerprint VARCHAR(128) NOT NULL,
    billing_request_id VARCHAR(64) NOT NULL UNIQUE,
    usage_log_id BIGINT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (job_id, event_id),
    UNIQUE (job_id, sequence)
);
