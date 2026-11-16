-- Build outside the publication lock. Every state mutation fences old builders.
ALTER TABLE usage_group_rollup_state ADD COLUMN IF NOT EXISTS revision BIGINT NOT NULL DEFAULT 0;
ALTER TABLE usage_group_rollup_state ADD COLUMN IF NOT EXISTS build_token UUID;
ALTER TABLE usage_group_rollup_state ADD COLUMN IF NOT EXISTS building_before DATE;
CREATE TABLE IF NOT EXISTS usage_group_rollup_stage (
 build_token UUID NOT NULL, bucket_date DATE NOT NULL, group_id BIGINT NOT NULL,
 actual_cost NUMERIC(20,10) NOT NULL, computed_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 PRIMARY KEY(build_token, bucket_date, group_id)
);
CREATE OR REPLACE FUNCTION advance_usage_rollup_revision() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN NEW.revision := OLD.revision + 1; RETURN NEW; END $$;
DROP TRIGGER IF EXISTS usage_group_rollup_revision ON usage_group_rollup_state;
CREATE TRIGGER usage_group_rollup_revision BEFORE UPDATE ON usage_group_rollup_state
 FOR EACH ROW EXECUTE FUNCTION advance_usage_rollup_revision();

CREATE OR REPLACE FUNCTION invalidate_group_usage_rollup_state() RETURNS TRIGGER LANGUAGE plpgsql AS $$
DECLARE affected DATE; boundary DATE; tz TEXT;
BEGIN
 SELECT GREATEST(closed_before, building_before), timezone_name INTO boundary, tz
 FROM usage_group_rollup_state WHERE id=1 FOR UPDATE;
 affected := (OLD.created_at AT TIME ZONE tz)::date;
 IF TG_OP='UPDATE' THEN affected := LEAST(affected,(NEW.created_at AT TIME ZONE tz)::date); END IF;
 IF affected < boundary THEN
  UPDATE usage_group_rollup_state SET closed_before=LEAST(closed_before,affected),updated_at=NOW() WHERE id=1;
 END IF;
 IF TG_OP='DELETE' THEN RETURN OLD; END IF; RETURN NEW;
END $$;
CREATE OR REPLACE FUNCTION invalidate_group_usage_rollup_state_after_insert() RETURNS TRIGGER LANGUAGE plpgsql AS $$
DECLARE affected DATE; boundary DATE; tz TEXT;
BEGIN
 SELECT GREATEST(closed_before, building_before),timezone_name INTO boundary,tz
 FROM usage_group_rollup_state WHERE id=1 FOR KEY SHARE;
 SELECT MIN((created_at AT TIME ZONE tz)::date) INTO affected FROM inserted_usage_logs WHERE group_id IS NOT NULL;
 IF affected < boundary THEN
  UPDATE usage_group_rollup_state SET closed_before=LEAST(closed_before,affected),updated_at=NOW() WHERE id=1;
 END IF;
 RETURN NULL;
END $$;
