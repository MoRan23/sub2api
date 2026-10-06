-- Do not infer a streak from historical verdicts or change existing allowlists.
ALTER TABLE model_attribution_state
    ADD COLUMN IF NOT EXISTS pass_streak SMALLINT NOT NULL DEFAULT 0
    CHECK (pass_streak BETWEEN 0 AND 2);
