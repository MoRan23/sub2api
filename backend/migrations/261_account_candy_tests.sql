-- Candy benchmarks are independent of scheduled tests and business account state.
CREATE TABLE account_candy_test_batches (
    id UUID PRIMARY KEY,
    idempotency_key VARCHAR(128) NOT NULL UNIQUE,
    request_snapshot JSONB NOT NULL,
    model VARCHAR(256) NOT NULL,
    reasoning_effort VARCHAR(32) NOT NULL DEFAULT '',
    prompt_version VARCHAR(64) NOT NULL,
    total INTEGER NOT NULL CHECK (total > 0),
    counts JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    finished_at TIMESTAMPTZ
);

CREATE TABLE account_candy_test_items (
    id BIGSERIAL PRIMARY KEY,
    batch_id UUID NOT NULL REFERENCES account_candy_test_batches(id) ON DELETE CASCADE,
    account_id BIGINT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    account_name TEXT NOT NULL DEFAULT '',
    model VARCHAR(256) NOT NULL,
    reasoning_effort VARCHAR(32) NOT NULL DEFAULT '',
    prompt_version VARCHAR(64) NOT NULL,
    status VARCHAR(16) NOT NULL CHECK (status IN ('queued','running','normal','abnormal','failed','cancelled','skipped')),
    answers JSONB NOT NULL DEFAULT '{}'::jsonb,
    response_text TEXT NOT NULL DEFAULT '' CHECK (octet_length(response_text) <= 1048576),
    failure_code VARCHAR(96) NOT NULL DEFAULT '',
    execution JSONB,
    cancel_requested BOOLEAN NOT NULL DEFAULT FALSE,
    claim_id UUID,
    lease_until TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    started_at TIMESTAMPTZ,
    finished_at TIMESTAMPTZ,
    UNIQUE (batch_id, account_id)
);

CREATE INDEX account_candy_test_queue_idx ON account_candy_test_items (id) WHERE status = 'queued';
CREATE UNIQUE INDEX account_candy_test_running_account_idx ON account_candy_test_items (account_id) WHERE status = 'running';
CREATE INDEX account_candy_test_history_idx ON account_candy_test_items (account_id, finished_at DESC, id DESC);
CREATE INDEX account_candy_test_batch_idx ON account_candy_test_items (batch_id, id);
