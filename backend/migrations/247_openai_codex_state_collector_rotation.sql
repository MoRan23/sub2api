-- Per-model collector rotation is runtime state, never account credentials or
-- import/export data. IDs intentionally have no FK: historical attempts remain
-- meaningful after a proxy is removed from the configured list and deleted.
ALTER TABLE openai_codex_state
    ADD COLUMN IF NOT EXISTS collector_proxy_id BIGINT,
    ADD COLUMN IF NOT EXISTS collector_extended_count INTEGER NOT NULL DEFAULT 0
        CHECK (collector_extended_count BETWEEN 0 AND 2),
    ADD COLUMN IF NOT EXISTS last_collector_proxy_id BIGINT,
    ADD COLUMN IF NOT EXISTS collector_attempt_id UUID;
