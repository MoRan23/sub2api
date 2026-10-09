-- Existing and new groups require an explicit Daybreak opt-in.
ALTER TABLE groups
    ADD COLUMN IF NOT EXISTS openai_daybreak_blue_enabled BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN IF NOT EXISTS openai_daybreak_red_enabled BOOLEAN NOT NULL DEFAULT FALSE;

ALTER TABLE groups ADD CONSTRAINT groups_daybreak_red_requires_blue
    CHECK (NOT openai_daybreak_red_enabled OR openai_daybreak_blue_enabled);
