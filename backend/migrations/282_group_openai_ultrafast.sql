ALTER TABLE groups
    ADD COLUMN IF NOT EXISTS force_openai_ultrafast BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN IF NOT EXISTS free_openai_ultrafast BOOLEAN NOT NULL DEFAULT FALSE;

COMMENT ON COLUMN groups.force_openai_ultrafast IS 'Force service_tier=ultrafast only when the selected account declares support';
COMMENT ON COLUMN groups.free_openai_ultrafast IS 'Bill user Ultrafast requests at Standard prices';
