-- Collection requires explicit demand or an existing token approaching expiry.
-- Keep the consumed history watermark after demand completion and idle periods
-- so replaying the same safe observation cannot start another collection.
ALTER TABLE openai_codex_state
    ADD COLUMN IF NOT EXISTS demand_reason TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS demand_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS history_proof_observed_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS collection_status TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS collection_reason TEXT NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS openai_codex_state_pending_demand_idx
    ON openai_codex_state (next_collect_at, last_business_at DESC)
    WHERE demand_reason <> '' AND NOT collector_paused;
