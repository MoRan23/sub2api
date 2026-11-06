-- Receipt ingestion and monetary settlement have different completion rules.
-- Retain canonical source facts even when a permanent gap or missing price
-- prevents charging them; one such event must not block later receipt pages.
ALTER TABLE codex_engine_billing_jobs ADD COLUMN IF NOT EXISTS source_seal JSONB;
ALTER TABLE codex_engine_billing_jobs ADD COLUMN IF NOT EXISTS settlement_cursor BIGINT NOT NULL DEFAULT 0 CHECK (settlement_cursor >= 0);

CREATE TABLE IF NOT EXISTS codex_engine_billing_received_events (
    job_id BIGINT NOT NULL REFERENCES codex_engine_billing_jobs(id),
    event_id VARCHAR(256) NOT NULL,
    sequence BIGINT NOT NULL CHECK (sequence > 0),
    fingerprint VARCHAR(128) NOT NULL,
    summary JSONB NOT NULL CHECK (jsonb_typeof(summary) = 'object'),
    facts JSONB NOT NULL CHECK (jsonb_typeof(facts) = 'object'),
    -- Acknowledged only after ledger commit and required cache refreshes succeed.
    -- Monetary idempotency is tracked separately by codex_engine_billing_events.
    settled_at TIMESTAMPTZ,
    received_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (job_id, event_id),
    UNIQUE (job_id, sequence)
);
CREATE INDEX IF NOT EXISTS codex_engine_billing_received_pending_idx
    ON codex_engine_billing_received_events (job_id, sequence) WHERE settled_at IS NULL;
CREATE INDEX IF NOT EXISTS codex_engine_billing_jobs_blocked_due_idx
    ON codex_engine_billing_jobs (next_attempt_at, id) WHERE state = 'blocked';

-- This migration accompanies the first release of the receipt consumer. Existing
-- ledger facts (if a pre-release build ran) remain independently retained; never
-- fabricate canonical upstream facts or mark an unpriced event as settled.
