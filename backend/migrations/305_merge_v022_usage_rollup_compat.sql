-- Finalize both fresh installs (251 -> 290/291) and upgrades (290/291 -> 251).
-- Retain the token/revision fenced builder and exact retention instants.
SET LOCAL lock_timeout = '5s';

ALTER TABLE usage_subject_rollup_state
    ADD COLUMN IF NOT EXISTS expired_before TIMESTAMPTZ NOT NULL DEFAULT TIMESTAMPTZ '1970-01-01 UTC';

CREATE OR REPLACE FUNCTION apply_group_usage_rollup_invalidation(affected_date DATE)
RETURNS VOID LANGUAGE plpgsql AS $$
BEGIN
    UPDATE usage_group_rollup_state
    SET closed_before = LEAST(closed_before, affected_date), build_token = NULL, building_before = NULL, updated_at = NOW()
    WHERE id = 1 AND affected_date < GREATEST(closed_before, building_before);
END $$;

CREATE OR REPLACE FUNCTION invalidate_group_usage_rollup_state()
RETURNS TRIGGER LANGUAGE plpgsql AS $$
DECLARE boundary DATE; tz TEXT; floor_at TIMESTAMPTZ; affected DATE; new_date DATE;
BEGIN
    SELECT GREATEST(closed_before, building_before), timezone_name, expired_before
    INTO boundary, tz, floor_at FROM usage_group_rollup_state WHERE id = 1 FOR KEY SHARE;
    IF OLD.group_id IS NOT NULL AND OLD.created_at >= floor_at THEN
        affected := (OLD.created_at AT TIME ZONE tz)::date;
    END IF;
    IF TG_OP = 'UPDATE' THEN
        IF NEW.group_id IS NOT NULL AND NEW.created_at >= floor_at THEN
            new_date := (NEW.created_at AT TIME ZONE tz)::date;
        END IF;
        affected := LEAST(affected, new_date);
    END IF;
    IF affected < boundary THEN
        UPDATE usage_group_rollup_state
        SET closed_before = LEAST(closed_before, affected), build_token = NULL, building_before = NULL, updated_at = NOW() WHERE id = 1;
    END IF;
    IF TG_OP = 'DELETE' THEN RETURN OLD; END IF;
    RETURN NEW;
END $$;

CREATE OR REPLACE FUNCTION invalidate_group_usage_rollup_state_after_insert()
RETURNS TRIGGER LANGUAGE plpgsql AS $$
DECLARE boundary DATE; tz TEXT; floor_at TIMESTAMPTZ; affected DATE;
BEGIN
    IF NOT EXISTS (SELECT 1 FROM inserted_usage_logs WHERE group_id IS NOT NULL) THEN RETURN NULL; END IF;
    SELECT GREATEST(closed_before, building_before), timezone_name, expired_before
    INTO boundary, tz, floor_at FROM usage_group_rollup_state WHERE id = 1 FOR KEY SHARE;
    SELECT MIN((created_at AT TIME ZONE tz)::date) INTO affected
    FROM inserted_usage_logs WHERE group_id IS NOT NULL AND created_at >= floor_at;
    IF affected < boundary THEN
        UPDATE usage_group_rollup_state
        SET closed_before = LEAST(closed_before, affected), build_token = NULL, building_before = NULL, updated_at = NOW() WHERE id = 1;
    END IF;
    RETURN NULL;
END $$;

CREATE OR REPLACE FUNCTION invalidate_subject_usage_rollup_state()
RETURNS TRIGGER LANGUAGE plpgsql AS $$
DECLARE boundary DATE; tz TEXT; floor_at TIMESTAMPTZ; affected DATE; new_date DATE;
BEGIN
    SELECT GREATEST(closed_before, building_before), timezone_name, expired_before
    INTO boundary, tz, floor_at FROM usage_subject_rollup_state WHERE id = 1 FOR KEY SHARE;
    IF OLD.created_at >= floor_at THEN affected := (OLD.created_at AT TIME ZONE tz)::date; END IF;
    IF TG_OP = 'UPDATE' THEN
        IF NEW.created_at >= floor_at THEN new_date := (NEW.created_at AT TIME ZONE tz)::date; END IF;
        affected := LEAST(affected, new_date);
    END IF;
    IF affected < boundary THEN
        UPDATE usage_subject_rollup_state
        SET closed_before = LEAST(closed_before, affected), build_token = NULL, building_before = NULL, updated_at = NOW() WHERE id = 1;
    END IF;
    IF TG_OP = 'DELETE' THEN RETURN OLD; END IF;
    RETURN NEW;
END $$;

CREATE OR REPLACE FUNCTION invalidate_subject_usage_rollup_after_insert()
RETURNS TRIGGER LANGUAGE plpgsql AS $$
DECLARE boundary DATE; tz TEXT; floor_at TIMESTAMPTZ; affected DATE;
BEGIN
    SELECT GREATEST(closed_before, building_before), timezone_name, expired_before
    INTO boundary, tz, floor_at FROM usage_subject_rollup_state WHERE id = 1 FOR KEY SHARE;
    SELECT MIN((created_at AT TIME ZONE tz)::date) INTO affected
    FROM inserted_usage_logs WHERE created_at >= floor_at;
    IF affected < boundary THEN
        UPDATE usage_subject_rollup_state
        SET closed_before = LEAST(closed_before, affected), build_token = NULL, building_before = NULL, updated_at = NOW() WHERE id = 1;
    END IF;
    RETURN NULL;
END $$;

-- Explicit invalidation also covers partition DROP and out-of-band maintenance.
-- Lock order is always group -> subject; ordinary source inserts only take a
-- compatible KEY SHARE barrier unless they invalidate a published/building day.
CREATE OR REPLACE FUNCTION invalidate_group_usage_rollups_at(affected_at TIMESTAMPTZ, bucket_timezone TEXT)
RETURNS VOID LANGUAGE plpgsql AS $$
DECLARE state_floor TIMESTAMPTZ; state_tz TEXT; affected DATE;
BEGIN
    IF affected_at IS NULL THEN RETURN; END IF;
    SELECT expired_before, timezone_name INTO state_floor, state_tz
    FROM usage_group_rollup_state WHERE id = 1 FOR KEY SHARE;
    IF FOUND THEN
        affected := (GREATEST(affected_at, state_floor) AT TIME ZONE COALESCE(state_tz, bucket_timezone))::date;
        PERFORM apply_group_usage_rollup_invalidation(affected);
    END IF;
    SELECT expired_before, timezone_name INTO state_floor, state_tz
    FROM usage_subject_rollup_state WHERE id = 1 FOR KEY SHARE;
    IF FOUND THEN
        affected := (GREATEST(affected_at, state_floor) AT TIME ZONE COALESCE(state_tz, bucket_timezone))::date;
        UPDATE usage_subject_rollup_state
        SET closed_before = LEAST(closed_before, affected), build_token = NULL, building_before = NULL, updated_at = NOW()
        WHERE id = 1 AND affected < GREATEST(closed_before, building_before);
    END IF;
END $$;

CREATE OR REPLACE FUNCTION expire_usage_rollups_before(cutoff TIMESTAMPTZ, bucket_timezone TEXT)
RETURNS VOID LANGUAGE plpgsql AS $$
BEGIN
    IF cutoff IS NULL THEN RETURN; END IF;
    -- Revision triggers fence a concurrent builder even when its watermark was
    -- already behind the partial boundary day. No unbounded source scan here.
    UPDATE usage_group_rollup_state
    SET expired_before = cutoff, retained_from = GREATEST(retained_from, cutoff),
        closed_before = LEAST(closed_before, (cutoff AT TIME ZONE COALESCE(timezone_name, bucket_timezone))::date),
        build_token = NULL, building_before = NULL, updated_at = NOW()
    WHERE id = 1 AND expired_before < cutoff;
    UPDATE usage_subject_rollup_state
    SET expired_before = cutoff, retained_from = GREATEST(retained_from, cutoff),
        closed_before = LEAST(closed_before, (cutoff AT TIME ZONE COALESCE(timezone_name, bucket_timezone))::date),
        build_token = NULL, building_before = NULL, updated_at = NOW()
    WHERE id = 1 AND expired_before < cutoff;
END $$;

CREATE OR REPLACE FUNCTION propagate_subject_rollup_invalidation()
RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.closed_before < OLD.closed_before THEN
        UPDATE usage_subject_rollup_state
        SET closed_before = LEAST(closed_before, NEW.closed_before),
            build_token = NULL, building_before = NULL, updated_at = NOW()
        WHERE id = 1 AND NEW.closed_before < GREATEST(closed_before, building_before);
    END IF;
    RETURN NEW;
END $$;

-- Each migration commits separately. During an upgrade, 251 temporarily uses
-- upstream build markers while an older instance can still finish a local
-- token-fenced build. Invalidate the retained range once, including a build
-- already published in that interval; the bounded background builder repairs
-- it without scanning usage_logs or deleting source data in this migration.
-- Always lock group before subject, as in the runtime invalidation path.
UPDATE usage_group_rollup_state
SET closed_before = LEAST(closed_before,
        (GREATEST(retained_from, expired_before) AT TIME ZONE timezone_name)::date),
    build_token = NULL, building_before = NULL,
    building_until = NULL, build_dirty_from = NULL, updated_at = NOW()
WHERE build_token IS NOT NULL OR building_before IS NOT NULL
    OR building_until IS NOT NULL OR build_dirty_from IS NOT NULL
    OR closed_before > (GREATEST(retained_from, expired_before) AT TIME ZONE timezone_name)::date;

UPDATE usage_subject_rollup_state
SET closed_before = LEAST(closed_before,
        (GREATEST(retained_from, expired_before) AT TIME ZONE timezone_name)::date),
    build_token = NULL, building_before = NULL, updated_at = NOW()
WHERE build_token IS NOT NULL OR building_before IS NOT NULL
    OR closed_before > (GREATEST(retained_from, expired_before) AT TIME ZONE timezone_name)::date;
