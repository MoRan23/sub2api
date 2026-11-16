-- Independent user/key summaries; history is built by the background rollup worker.
CREATE TABLE IF NOT EXISTS usage_subject_rollup_state (
 id SMALLINT PRIMARY KEY CHECK(id=1), closed_before DATE NOT NULL DEFAULT '1970-01-01',
 retained_from TIMESTAMPTZ NOT NULL DEFAULT '1970-01-01 UTC',
 timezone_name TEXT NOT NULL DEFAULT 'Asia/Shanghai', updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 revision BIGINT NOT NULL DEFAULT 0, build_token UUID, building_before DATE
);
INSERT INTO usage_subject_rollup_state(id) VALUES(1) ON CONFLICT DO NOTHING;
DROP TRIGGER IF EXISTS usage_subject_rollup_revision ON usage_subject_rollup_state;
CREATE TRIGGER usage_subject_rollup_revision BEFORE UPDATE ON usage_subject_rollup_state
 FOR EACH ROW EXECUTE FUNCTION advance_usage_rollup_revision();
CREATE TABLE IF NOT EXISTS usage_subject_daily_rollups (
 bucket_date DATE NOT NULL, user_id BIGINT NOT NULL, api_key_id BIGINT NOT NULL,
 group_id BIGINT NOT NULL, account_id BIGINT NOT NULL,
 request_count BIGINT NOT NULL DEFAULT 0,
 input_tokens BIGINT NOT NULL DEFAULT 0,
 output_tokens BIGINT NOT NULL DEFAULT 0,
 cache_creation_tokens BIGINT NOT NULL DEFAULT 0,
 cache_read_tokens BIGINT NOT NULL DEFAULT 0,
 total_cost NUMERIC(30,10) NOT NULL DEFAULT 0,
 actual_cost NUMERIC(30,10) NOT NULL DEFAULT 0,
 duration_sum_ms NUMERIC(30,0) NOT NULL DEFAULT 0,
 duration_count BIGINT NOT NULL DEFAULT 0,
 charged_tokens BIGINT NOT NULL DEFAULT 0,
 charged_cost NUMERIC(30,10) NOT NULL DEFAULT 0,
 success_count BIGINT NOT NULL DEFAULT 0,
 computed_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 PRIMARY KEY(bucket_date,user_id,api_key_id,group_id,account_id)
);
CREATE INDEX IF NOT EXISTS usage_subject_rollup_user_date ON usage_subject_daily_rollups(user_id,bucket_date);
CREATE INDEX IF NOT EXISTS usage_subject_rollup_key_date ON usage_subject_daily_rollups(api_key_id,bucket_date);
CREATE TABLE IF NOT EXISTS usage_subject_rollup_stage (LIKE usage_subject_daily_rollups INCLUDING DEFAULTS);
ALTER TABLE usage_subject_rollup_stage ADD COLUMN IF NOT EXISTS build_token UUID NOT NULL;
CREATE INDEX IF NOT EXISTS usage_subject_rollup_stage_token ON usage_subject_rollup_stage(build_token);
CREATE OR REPLACE FUNCTION invalidate_subject_usage_rollup_state() RETURNS TRIGGER LANGUAGE plpgsql AS $$
DECLARE affected DATE; boundary DATE; tz TEXT;
BEGIN
 SELECT GREATEST(closed_before,building_before),timezone_name INTO boundary,tz
 FROM usage_subject_rollup_state WHERE id=1 FOR UPDATE;
 affected := (OLD.created_at AT TIME ZONE tz)::date;
 IF TG_OP='UPDATE' THEN affected := LEAST(affected,(NEW.created_at AT TIME ZONE tz)::date); END IF;
 IF affected < boundary THEN
  UPDATE usage_subject_rollup_state SET closed_before=LEAST(closed_before,affected),updated_at=NOW() WHERE id=1;
 END IF;
 IF TG_OP='DELETE' THEN RETURN OLD; END IF; RETURN NEW;
END $$;
CREATE OR REPLACE FUNCTION invalidate_subject_usage_rollup_after_insert() RETURNS TRIGGER LANGUAGE plpgsql AS $$
DECLARE affected DATE; boundary DATE; tz TEXT;
BEGIN
 SELECT GREATEST(closed_before,building_before),timezone_name INTO boundary,tz
 FROM usage_subject_rollup_state WHERE id=1 FOR KEY SHARE;
 SELECT MIN((created_at AT TIME ZONE tz)::date) INTO affected FROM inserted_usage_logs;
 IF affected < boundary THEN
  UPDATE usage_subject_rollup_state SET closed_before=LEAST(closed_before,affected),updated_at=NOW() WHERE id=1;
 END IF;
 RETURN NULL;
END $$;
DROP TRIGGER IF EXISTS usage_logs_subject_rollup_insert ON usage_logs;
CREATE TRIGGER usage_logs_subject_rollup_insert AFTER INSERT ON usage_logs
 REFERENCING NEW TABLE AS inserted_usage_logs FOR EACH STATEMENT EXECUTE FUNCTION invalidate_subject_usage_rollup_after_insert();
DROP TRIGGER IF EXISTS usage_logs_subject_rollup_delete ON usage_logs;
CREATE TRIGGER usage_logs_subject_rollup_delete AFTER DELETE ON usage_logs
 FOR EACH ROW EXECUTE FUNCTION invalidate_subject_usage_rollup_state();
DROP TRIGGER IF EXISTS usage_logs_subject_rollup_update ON usage_logs;
CREATE TRIGGER usage_logs_subject_rollup_update AFTER UPDATE OF user_id,api_key_id,group_id,account_id,created_at,input_tokens,output_tokens,cache_creation_tokens,cache_read_tokens,total_cost,actual_cost,duration_ms,grok_safety_rejection ON usage_logs
 FOR EACH ROW WHEN (
 ROW(OLD.user_id,OLD.api_key_id,OLD.group_id,OLD.account_id,OLD.created_at,OLD.input_tokens,OLD.output_tokens,OLD.cache_creation_tokens,OLD.cache_read_tokens,OLD.total_cost,OLD.actual_cost,OLD.duration_ms,OLD.grok_safety_rejection)
 IS DISTINCT FROM
 ROW(NEW.user_id,NEW.api_key_id,NEW.group_id,NEW.account_id,NEW.created_at,NEW.input_tokens,NEW.output_tokens,NEW.cache_creation_tokens,NEW.cache_read_tokens,NEW.total_cost,NEW.actual_cost,NEW.duration_ms,NEW.grok_safety_rejection)
 ) EXECUTE FUNCTION invalidate_subject_usage_rollup_state();
-- Old maintenance code explicitly invalidates the group state for partition DROP.
-- Mirror that invalidation so both versions can safely perform retention.
CREATE OR REPLACE FUNCTION propagate_subject_rollup_invalidation() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.closed_before < OLD.closed_before THEN
  UPDATE usage_subject_rollup_state SET closed_before=LEAST(closed_before,NEW.closed_before),updated_at=NOW() WHERE id=1;
 END IF;
 RETURN NEW;
END $$;
DROP TRIGGER IF EXISTS usage_group_rollup_subject_invalidation ON usage_group_rollup_state;
CREATE TRIGGER usage_group_rollup_subject_invalidation AFTER UPDATE ON usage_group_rollup_state
 FOR EACH ROW EXECUTE FUNCTION propagate_subject_rollup_invalidation();
