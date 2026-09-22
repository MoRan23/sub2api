-- Private OAuth grants. As with accounts.credentials, secrets use the database's
-- existing JSONB credential storage; these records are never serialized publicly.
CREATE TABLE IF NOT EXISTS account_openai_oauth_authorization_migrations (
    account_id BIGINT PRIMARY KEY REFERENCES accounts(id) ON DELETE CASCADE,
    chatgpt_account_id TEXT NOT NULL DEFAULT '',
    chatgpt_user_id TEXT NOT NULL DEFAULT '',
    migrated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS account_openai_oauth_os_credentials (
    account_id BIGINT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    os_family TEXT NOT NULL CHECK (os_family IN ('windows','macos','linux')),
    credentials JSONB NOT NULL DEFAULT '{}'::jsonb,
    authorization_generation UUID NOT NULL DEFAULT gen_random_uuid(),
    revision BIGINT NOT NULL DEFAULT 1 CHECK (revision > 0),
    state_generation UUID NOT NULL DEFAULT gen_random_uuid(),
    previous_state_generation UUID,
    credential_epoch UUID NOT NULL DEFAULT gen_random_uuid(),
    status TEXT NOT NULL DEFAULT 'unauthorized' CHECK (status IN ('unauthorized','authorized','reauth_required')),
    source TEXT NOT NULL DEFAULT '',
    authorized_at TIMESTAMPTZ,
    expires_at TIMESTAMPTZ,
    last_error TEXT NOT NULL DEFAULT '',
    refresh_retry_after TIMESTAMPTZ,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (account_id,os_family)
);

-- Only an already established default profile receives the legacy grant. Rows
-- without profiles are resumed by the bounded application backfill under lock.
WITH owners AS (
    SELECT a.id, a.credentials, p.os_family FROM accounts a
    JOIN account_openai_oauth_os_profiles p ON p.account_id=a.id AND p.is_default
    WHERE a.deleted_at IS NULL AND a.platform='openai' AND a.type='oauth' AND a.parent_account_id IS NULL
      AND LOWER(BTRIM(COALESCE(a.credentials->>'auth_mode',''))) NOT IN ('personalaccesstoken','personal_access_token','agentidentity')
      AND LOWER(BTRIM(COALESCE(a.credentials->>'openai_auth_mode',''))) NOT IN ('personalaccesstoken','personal_access_token','agentidentity')
), marked AS (
    INSERT INTO account_openai_oauth_authorization_migrations(account_id,chatgpt_account_id,chatgpt_user_id)
    SELECT id, BTRIM(COALESCE(credentials->>'chatgpt_account_id','')), BTRIM(COALESCE(credentials->>'chatgpt_user_id','')) FROM owners
    ON CONFLICT DO NOTHING RETURNING account_id
)
INSERT INTO account_openai_oauth_os_credentials(account_id,os_family,credentials,status,source,authorized_at)
SELECT o.id,o.os_family,
    (SELECT COALESCE(jsonb_object_agg(key,value),'{}'::jsonb) FROM jsonb_each(o.credentials)
     WHERE key = ANY(ARRAY['access_token','refresh_token','id_token','expires_at','expires_in','email','chatgpt_account_id','chatgpt_user_id','organization_id','plan_type','subscription_expires_at','client_id','token_type','chatgpt_account_is_fedramp','auth_mode','openai_auth_mode','_token_version'])),
    'authorized','legacy_migration',NOW()
FROM owners o JOIN marked m ON m.account_id=o.id
WHERE BTRIM(COALESCE(o.credentials->>'access_token',''))<>'' OR BTRIM(COALESCE(o.credentials->>'refresh_token',''))<>''
ON CONFLICT DO NOTHING;

-- Configuration changes fence every slot in the same account transaction.
-- Authorization/refresh writers independently fence just their selected slot.
CREATE OR REPLACE FUNCTION fence_openai_oauth_os_credential_configuration() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    old_config JSONB := OLD.extra->'codex_turn_state';
    new_config JSONB := NEW.extra->'codex_turn_state';
BEGIN
    -- Keep the established singleton/list compatibility semantics: formatting
    -- an unchanged proxy list must not invalidate any operating system's cache.
    old_config := (old_config - 'collector_proxy_id' - 'collector_proxy_ids') || jsonb_build_object('collector_proxy_ids',
        CASE WHEN old_config ? 'collector_proxy_ids' THEN old_config->'collector_proxy_ids'
             WHEN old_config->'collector_proxy_id' IS NOT NULL AND old_config->'collector_proxy_id'<>'null'::jsonb THEN jsonb_build_array(old_config->'collector_proxy_id') ELSE '[]'::jsonb END);
    new_config := (new_config - 'collector_proxy_id' - 'collector_proxy_ids') || jsonb_build_object('collector_proxy_ids',
        CASE WHEN new_config ? 'collector_proxy_ids' THEN new_config->'collector_proxy_ids'
             WHEN new_config->'collector_proxy_id' IS NOT NULL AND new_config->'collector_proxy_id'<>'null'::jsonb THEN jsonb_build_array(new_config->'collector_proxy_id') ELSE '[]'::jsonb END);
    IF old_config IS DISTINCT FROM new_config
       OR OLD.proxy_id IS DISTINCT FROM NEW.proxy_id THEN
        UPDATE account_openai_oauth_os_credentials SET previous_state_generation=state_generation, state_generation=gen_random_uuid(), updated_at=NOW() WHERE account_id=NEW.id;
    END IF;
    RETURN NEW;
END;
$$;
DROP TRIGGER IF EXISTS account_openai_oauth_os_configuration_fence ON accounts;
CREATE TRIGGER account_openai_oauth_os_configuration_fence AFTER UPDATE OF extra,proxy_id ON accounts
    FOR EACH ROW EXECUTE FUNCTION fence_openai_oauth_os_credential_configuration();
