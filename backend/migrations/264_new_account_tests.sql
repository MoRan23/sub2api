-- One durable creation event per physical OAuth account. Existing accounts are
-- intentionally not backfilled. Completed markers live only as long as accounts.
CREATE TABLE account_initial_tests (
    account_id BIGINT PRIMARY KEY REFERENCES accounts(id) ON DELETE CASCADE,
    attribution BOOLEAN NOT NULL,
    pelican BOOLEAN NOT NULL,
    model TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    processed_at TIMESTAMPTZ
);
CREATE INDEX account_initial_tests_pending ON account_initial_tests(account_id) WHERE processed_at IS NULL;
ALTER TABLE model_attribution_jobs DROP CONSTRAINT model_attribution_jobs_source_check;
ALTER TABLE model_attribution_jobs ADD CONSTRAINT model_attribution_jobs_source_check CHECK(source IN ('manual','scheduled','initial'));
