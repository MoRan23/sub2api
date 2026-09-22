-- accounts.credentials is the sole OAuth credential source. The former shared
-- store becomes private authorization/CAS metadata, with no token column.
ALTER TABLE account_openai_oauth_credentials
    ADD COLUMN IF NOT EXISTS state_generation UUID NOT NULL DEFAULT gen_random_uuid(),
    ADD COLUMN IF NOT EXISTS previous_state_generation UUID,
    ADD COLUMN IF NOT EXISTS auth_pause_owned BOOLEAN NOT NULL DEFAULT false,
    ADD COLUMN IF NOT EXISTS auth_pause_previous_status TEXT NOT NULL DEFAULT 'active',
    ADD COLUMN IF NOT EXISTS auth_pause_previous_schedulable BOOLEAN NOT NULL DEFAULT true,
    ADD COLUMN IF NOT EXISTS auth_pause_previous_error_message TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS auth_retry_owned BOOLEAN NOT NULL DEFAULT false,
    ADD COLUMN IF NOT EXISTS auth_retry_until TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS auth_retry_previous_until TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS auth_retry_previous_reason TEXT;

-- A dropped source column is also the one-time migration fence. Re-executing
-- this migration cannot re-pause a repaired account or resurrect old tokens.
DO $$ BEGIN
    IF EXISTS(SELECT 1 FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='account_openai_oauth_credentials' AND column_name='credentials') THEN
        UPDATE accounts a SET credentials=(COALESCE(a.credentials,'{}'::jsonb)-ARRAY['access_token','refresh_token','id_token','expires_at','expires_in','email','chatgpt_account_id','chatgpt_user_id','organization_id','plan_type','subscription_expires_at','client_id','token_type','chatgpt_account_is_fedramp','auth_mode','openai_auth_mode','_token_version']::text[])
            || CASE WHEN c.status<>'unauthorized' THEN c.credentials ELSE '{}'::jsonb END,updated_at=NOW()
        FROM account_openai_oauth_credentials c WHERE c.account_id=a.id
            AND a.platform='openai' AND a.type='oauth' AND a.parent_account_id IS NULL
            AND LOWER(BTRIM(COALESCE(a.credentials->>'auth_mode',''))) NOT IN ('personalaccesstoken','personal_access_token','agentidentity')
            AND LOWER(BTRIM(COALESCE(a.credentials->>'openai_auth_mode',''))) NOT IN ('personalaccesstoken','personal_access_token','agentidentity');

        -- Missing and failed grants must agree with account scheduling/UI state.
        -- Keep an explicit administrative disable intact; authorization cannot
        -- acquire ownership of that independent decision.
        UPDATE account_openai_oauth_credentials c SET
            auth_pause_owned=true,auth_pause_previous_status=a.status,
            auth_pause_previous_schedulable=a.schedulable,auth_pause_previous_error_message=COALESCE(a.error_message,'')
        FROM accounts a WHERE a.id=c.account_id AND c.status IN ('unauthorized','reauth_required')
            AND a.status IN ('active','error')
            AND a.platform='openai' AND a.type='oauth' AND a.parent_account_id IS NULL;
        UPDATE accounts a SET status='error',schedulable=false,error_message='OAuth authorization must be renewed',updated_at=NOW()
        FROM account_openai_oauth_credentials c WHERE c.account_id=a.id AND c.auth_pause_owned;

        UPDATE account_openai_oauth_credentials c SET auth_retry_owned=true,auth_retry_until=c.refresh_retry_after,
            auth_retry_previous_until=a.temp_unschedulable_until,auth_retry_previous_reason=a.temp_unschedulable_reason
        FROM accounts a WHERE a.id=c.account_id AND c.refresh_retry_after>NOW()
            AND (a.temp_unschedulable_until IS NULL OR a.temp_unschedulable_until<c.refresh_retry_after);
        UPDATE accounts a SET temp_unschedulable_until=c.auth_retry_until,temp_unschedulable_reason='OAuth token refresh temporarily unavailable',updated_at=NOW()
        FROM account_openai_oauth_credentials c WHERE c.account_id=a.id AND c.auth_retry_owned;

        -- Starting a new owner-level runtime fence invalidates previous per-OS
        -- state without changing a still valid authorization generation.
        UPDATE account_openai_oauth_os_credentials s SET previous_state_generation=s.state_generation,state_generation=c.state_generation
        FROM account_openai_oauth_credentials c WHERE c.account_id=s.account_id;
        ALTER TABLE account_openai_oauth_credentials DROP COLUMN credentials;
    END IF;
END $$;
ALTER TABLE account_openai_oauth_os_credentials DROP COLUMN IF EXISTS credentials;

-- Explicit account-state writes, including same-value manual pauses, surrender
-- OAuth ownership. Snapshot writers omit these fields while an auth pause is
-- owned. OAuth writers claim ownership only after their own account mutation.
CREATE OR REPLACE FUNCTION clear_openai_oauth_auth_pause_ownership() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    UPDATE account_openai_oauth_credentials SET auth_pause_owned=false WHERE account_id=NEW.id AND auth_pause_owned;
    RETURN NEW;
END;
$$;
DROP TRIGGER IF EXISTS account_openai_oauth_auth_pause_ownership ON accounts;
CREATE TRIGGER account_openai_oauth_auth_pause_ownership AFTER UPDATE OF status,schedulable,error_message ON accounts
    FOR EACH ROW EXECUTE FUNCTION clear_openai_oauth_auth_pause_ownership();

CREATE OR REPLACE FUNCTION clear_openai_oauth_auth_retry_ownership() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    UPDATE account_openai_oauth_credentials SET auth_retry_owned=false WHERE account_id=NEW.id AND auth_retry_owned;
    RETURN NEW;
END;
$$;
DROP TRIGGER IF EXISTS account_openai_oauth_auth_retry_ownership ON accounts;
CREATE TRIGGER account_openai_oauth_auth_retry_ownership AFTER UPDATE OF temp_unschedulable_until,temp_unschedulable_reason ON accounts
    FOR EACH ROW EXECUTE FUNCTION clear_openai_oauth_auth_retry_ownership();

-- Configuration changes fence the owner once; the legacy OS metadata is only
-- an adapter for consumers migrating to the owner-level runtime schema.
CREATE OR REPLACE FUNCTION fence_openai_oauth_os_credential_configuration() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    old_config JSONB := OLD.extra->'codex_turn_state';
    new_config JSONB := NEW.extra->'codex_turn_state';
BEGIN
    old_config := (old_config - 'collector_proxy_id' - 'collector_proxy_ids') || jsonb_build_object('collector_proxy_ids',
        CASE WHEN old_config ? 'collector_proxy_ids' THEN old_config->'collector_proxy_ids'
             WHEN old_config->'collector_proxy_id' IS NOT NULL AND old_config->'collector_proxy_id'<>'null'::jsonb THEN jsonb_build_array(old_config->'collector_proxy_id') ELSE '[]'::jsonb END);
    new_config := (new_config - 'collector_proxy_id' - 'collector_proxy_ids') || jsonb_build_object('collector_proxy_ids',
        CASE WHEN new_config ? 'collector_proxy_ids' THEN new_config->'collector_proxy_ids'
             WHEN new_config->'collector_proxy_id' IS NOT NULL AND new_config->'collector_proxy_id'<>'null'::jsonb THEN jsonb_build_array(new_config->'collector_proxy_id') ELSE '[]'::jsonb END);
    IF old_config IS DISTINCT FROM new_config OR OLD.proxy_id IS DISTINCT FROM NEW.proxy_id THEN
        UPDATE account_openai_oauth_credentials SET previous_state_generation=state_generation,state_generation=gen_random_uuid(),updated_at=NOW() WHERE account_id=NEW.id;
        UPDATE account_openai_oauth_os_credentials s SET previous_state_generation=s.state_generation,state_generation=c.state_generation,updated_at=NOW()
            FROM account_openai_oauth_credentials c WHERE c.account_id=NEW.id AND s.account_id=c.account_id;
    END IF;
    RETURN NEW;
END;
$$;
