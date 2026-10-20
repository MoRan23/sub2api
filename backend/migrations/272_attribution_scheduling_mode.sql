ALTER TABLE accounts
    ADD COLUMN IF NOT EXISTS scheduling_revision BIGINT NOT NULL DEFAULT 0;

ALTER TABLE model_attribution_state
    ADD COLUMN IF NOT EXISTS pause_revision BIGINT;

-- Only the account row is modified here. Never acquire an attribution state
-- lock from an account trigger or couple independent transaction lock orders.
CREATE OR REPLACE FUNCTION advance_account_scheduling_revision() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    NEW.scheduling_revision := OLD.scheduling_revision + 1;
    RETURN NEW;
END;
$$;

-- UPDATE OF includes explicit no-op writes (false -> false). Such a write is an
-- administrator/runtime decision and revokes any older attribution pause.
DROP TRIGGER IF EXISTS account_scheduling_revision_write ON accounts;
CREATE TRIGGER account_scheduling_revision_write BEFORE UPDATE OF schedulable ON accounts
    FOR EACH ROW EXECUTE FUNCTION advance_account_scheduling_revision();

CREATE OR REPLACE FUNCTION advance_account_attribution_mode_revision() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    -- SQL NULL, JSON null and malformed historical values preserve legacy mode.
    IF COALESCE(OLD.extra -> 'openai_attribution_replace_whitelist_enabled' = 'false'::jsonb, false)
        IS DISTINCT FROM COALESCE(NEW.extra -> 'openai_attribution_replace_whitelist_enabled' = 'false'::jsonb, false) THEN
        NEW.scheduling_revision := OLD.scheduling_revision + 1;
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS account_attribution_mode_revision ON accounts;
CREATE TRIGGER account_attribution_mode_revision BEFORE UPDATE OF extra ON accounts
    FOR EACH ROW EXECUTE FUNCTION advance_account_attribution_mode_revision();
