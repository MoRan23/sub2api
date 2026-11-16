-- Compute the small capacity/shape summary when a runtime row changes, rather
-- than repeatedly detoasting every retained profile during each pool mutation.
-- -1 preserves fail-closed handling of malformed fields consumed by aggregates;
-- point reads still validate the complete typed runtime state in Go.
CREATE OR REPLACE FUNCTION codex_telemetry_runtime_slots(kind TEXT, state JSONB)
RETURNS INTEGER LANGUAGE plpgsql IMMUTABLE PARALLEL SAFE AS $$
DECLARE
    slots INTEGER := 1;
BEGIN
    IF kind NOT IN ('turn', 'attempt', 'sampling') THEN
        RETURN NULL;
    END IF;
    IF kind = 'turn' THEN
        IF jsonb_typeof(state) NOT IN ('object', 'null')
            OR COALESCE(jsonb_typeof(state->'sealed'), 'null') NOT IN ('boolean', 'null')
            OR COALESCE(jsonb_typeof(state->'incomplete'), 'null') NOT IN ('boolean', 'null')
            OR COALESCE(jsonb_typeof(state->'inflight'), 'null') NOT IN ('object', 'null') THEN
            RETURN -1;
        END IF;
        IF COALESCE(state->'sealed', 'false'::jsonb) <> 'true'::jsonb
            AND COALESCE(state->'incomplete', 'false'::jsonb) <> 'true'::jsonb
            AND jsonb_typeof(state->'inflight') = 'object' THEN
            SELECT slots + count(*)::INTEGER INTO slots FROM jsonb_object_keys(state->'inflight');
        END IF;
    ELSIF kind = 'attempt' THEN
        IF jsonb_typeof(state) NOT IN ('object', 'null')
            OR COALESCE(jsonb_typeof(state->'finished'), 'null') NOT IN ('boolean', 'null')
            OR COALESCE(jsonb_typeof(state->'turn_key'), 'null') NOT IN ('string', 'null') THEN
            RETURN -1;
        END IF;
    END IF;
    RETURN slots;
END;
$$;

-- A generated column stays correct for older application writers, direct SQL,
-- transaction rollback, and concurrent instances without an in-process cache.
ALTER TABLE codex_telemetry_activities ADD COLUMN IF NOT EXISTS runtime_slots INTEGER
    GENERATED ALWAYS AS (codex_telemetry_runtime_slots(kind, state)) STORED;
