-- OAuth authorization is account-owned. OS rows retain installation/runtime
-- identity only; no refresh token may survive in the former per-OS store.
CREATE TABLE IF NOT EXISTS account_openai_oauth_credentials (
    account_id BIGINT PRIMARY KEY REFERENCES accounts(id) ON DELETE CASCADE,
    credentials JSONB NOT NULL DEFAULT '{}'::jsonb,
    authorization_generation UUID NOT NULL DEFAULT gen_random_uuid(),
    revision BIGINT NOT NULL DEFAULT 1 CHECK (revision > 0),
    credential_epoch UUID NOT NULL DEFAULT gen_random_uuid(),
    status TEXT NOT NULL DEFAULT 'unauthorized' CHECK (status IN ('unauthorized','authorized','reauth_required')),
    source TEXT NOT NULL DEFAULT '',
    authorized_at TIMESTAMPTZ,
    expires_at TIMESTAMPTZ,
    last_error TEXT NOT NULL DEFAULT '',
    refresh_retry_after TIMESTAMPTZ,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Parse old JSON expiry defensively. Invalid/missing expiry retains the prior
-- unknown-expiry behavior; it must not abort migration of unrelated accounts.
CREATE OR REPLACE FUNCTION pg_temp.openai_shared_migration_expiry(value TEXT)
RETURNS TIMESTAMPTZ LANGUAGE plpgsql AS $$
BEGIN
    RETURN NULLIF(BTRIM(value),'')::TIMESTAMPTZ;
EXCEPTION WHEN OTHERS THEN
    RETURN NULL;
END;
$$;

WITH candidates AS (
    SELECT a.id, a.credentials AS legacy_credentials,
        (a.deleted_at IS NULL AND a.platform='openai' AND a.type='oauth' AND a.parent_account_id IS NULL
         AND LOWER(BTRIM(COALESCE(a.credentials->>'auth_mode',''))) NOT IN ('personalaccesstoken','personal_access_token','agentidentity')
         AND LOWER(BTRIM(COALESCE(a.credentials->>'openai_auth_mode',''))) NOT IN ('personalaccesstoken','personal_access_token','agentidentity')) AS eligible,
        m.account_id IS NOT NULL AS previously_migrated,
        EXISTS(SELECT 1 FROM account_openai_oauth_os_credentials old WHERE old.account_id=a.id) AS had_os_rows
    FROM accounts a LEFT JOIN account_openai_oauth_authorization_migrations m ON m.account_id=a.id
    WHERE NOT EXISTS(SELECT 1 FROM account_openai_oauth_credentials shared WHERE shared.account_id=a.id)
), chosen AS (
    SELECT a.*, picked.credentials AS picked_credentials,picked.authorized_at,picked.expires_at,picked.refresh_retry_after,
        failed.credentials AS failed_credentials,failed.authorized_at AS failed_authorized_at,failed.expires_at AS failed_expires_at,
        EXISTS(SELECT 1 FROM account_openai_oauth_os_credentials failed WHERE failed.account_id=a.id AND failed.status IN ('reauth_required','authorized')) AS had_failed_grant
    FROM candidates a LEFT JOIN LATERAL (
        SELECT old.credentials,old.authorized_at,COALESCE(old.expires_at,pg_temp.openai_shared_migration_expiry(old.credentials->>'expires_at')) AS expires_at,old.refresh_retry_after
        FROM account_openai_oauth_os_credentials old
        LEFT JOIN account_openai_oauth_os_profiles profile ON profile.account_id=old.account_id AND profile.os_family=old.os_family
        WHERE old.account_id=a.id AND a.eligible AND old.status='authorized'
          AND (BTRIM(COALESCE(old.credentials->>'refresh_token',''))<>'' OR
              (BTRIM(COALESCE(old.credentials->>'access_token',''))<>'' AND
               COALESCE(old.expires_at,pg_temp.openai_shared_migration_expiry(old.credentials->>'expires_at'),'infinity'::TIMESTAMPTZ)>NOW()))
        ORDER BY COALESCE(profile.is_default,false) DESC,old.authorized_at DESC NULLS LAST,old.updated_at DESC,old.os_family
        LIMIT 1
    ) picked ON true
    LEFT JOIN LATERAL (
        SELECT old.credentials,old.authorized_at,COALESCE(old.expires_at,pg_temp.openai_shared_migration_expiry(old.credentials->>'expires_at')) AS expires_at
        FROM account_openai_oauth_os_credentials old
        LEFT JOIN account_openai_oauth_os_profiles profile ON profile.account_id=old.account_id AND profile.os_family=old.os_family
        WHERE old.account_id=a.id AND a.eligible AND old.status<>'unauthorized'
          AND (BTRIM(COALESCE(old.credentials->>'refresh_token',''))<>'' OR BTRIM(COALESCE(old.credentials->>'access_token',''))<>'')
        ORDER BY COALESCE(profile.is_default,false) DESC,old.authorized_at DESC NULLS LAST,old.updated_at DESC,old.os_family
        LIMIT 1
    ) failed ON true
), resolved AS (
    SELECT *, eligible AND NOT previously_migrated AND NOT had_os_rows
        AND (BTRIM(COALESCE(legacy_credentials->>'refresh_token',''))<>'' OR
             (BTRIM(COALESCE(legacy_credentials->>'access_token',''))<>'' AND
              COALESCE(pg_temp.openai_shared_migration_expiry(legacy_credentials->>'expires_at'),'infinity'::TIMESTAMPTZ)>NOW())) AS use_legacy,
        eligible AND NOT previously_migrated AND NOT had_os_rows
        AND (BTRIM(COALESCE(legacy_credentials->>'refresh_token',''))<>'' OR BTRIM(COALESCE(legacy_credentials->>'access_token',''))<>'') AS preserve_legacy
    FROM chosen
    WHERE eligible OR previously_migrated OR had_os_rows
)
INSERT INTO account_openai_oauth_credentials(account_id,credentials,status,source,authorized_at,expires_at,last_error,refresh_retry_after)
SELECT id,
    (SELECT COALESCE(jsonb_object_agg(key,value),'{}'::jsonb)
     FROM jsonb_each(COALESCE(picked_credentials,failed_credentials,CASE WHEN preserve_legacy THEN legacy_credentials ELSE '{}'::jsonb END))
     WHERE key=ANY(ARRAY['access_token','refresh_token','id_token','expires_at','expires_in','email','chatgpt_account_id','chatgpt_user_id','organization_id','plan_type','subscription_expires_at','client_id','token_type','chatgpt_account_is_fedramp','auth_mode','openai_auth_mode','_token_version'])),
    CASE WHEN picked_credentials IS NOT NULL OR use_legacy THEN 'authorized'
         WHEN eligible AND (had_failed_grant OR preserve_legacy) THEN 'reauth_required' ELSE 'unauthorized' END,
    'shared_migration',CASE WHEN picked_credentials IS NOT NULL THEN authorized_at WHEN failed_credentials IS NOT NULL THEN failed_authorized_at WHEN preserve_legacy THEN NOW() ELSE NULL END,
    CASE WHEN picked_credentials IS NOT NULL THEN expires_at WHEN failed_credentials IS NOT NULL THEN failed_expires_at WHEN preserve_legacy THEN pg_temp.openai_shared_migration_expiry(legacy_credentials->>'expires_at') ELSE NULL END,
    CASE WHEN picked_credentials IS NULL AND NOT use_legacy AND eligible AND (had_failed_grant OR preserve_legacy) THEN 'OAuth authorization must be renewed' ELSE '' END,
    CASE WHEN picked_credentials IS NOT NULL THEN refresh_retry_after ELSE NULL END
FROM resolved ON CONFLICT DO NOTHING;

INSERT INTO account_openai_oauth_authorization_migrations(account_id,chatgpt_account_id,chatgpt_user_id)
SELECT account_id,BTRIM(COALESCE(credentials->>'chatgpt_account_id','')),BTRIM(COALESCE(credentials->>'chatgpt_user_id',''))
FROM account_openai_oauth_credentials ON CONFLICT DO NOTHING;

-- Preserve OS-specific identities, while fencing every pre-migration attempt.
-- Comparing the new shared generation makes this repair idempotent on rerun.
INSERT INTO account_openai_oauth_os_credentials AS runtime
    (account_id,os_family,credentials,authorization_generation,revision,state_generation,credential_epoch,status,source,authorized_at,expires_at,last_error,refresh_retry_after)
SELECT shared.account_id,os,'{}'::jsonb,shared.authorization_generation,shared.revision,gen_random_uuid(),shared.credential_epoch,shared.status,'shared_runtime',shared.authorized_at,shared.expires_at,shared.last_error,shared.refresh_retry_after
FROM account_openai_oauth_credentials shared CROSS JOIN unnest(ARRAY['windows','macos','linux']) os
ON CONFLICT(account_id,os_family) DO UPDATE SET credentials='{}'::jsonb,
    previous_state_generation=CASE WHEN runtime.authorization_generation<>EXCLUDED.authorization_generation THEN runtime.state_generation ELSE runtime.previous_state_generation END,
    state_generation=CASE WHEN runtime.authorization_generation<>EXCLUDED.authorization_generation THEN gen_random_uuid() ELSE runtime.state_generation END,
    authorization_generation=EXCLUDED.authorization_generation,revision=EXCLUDED.revision,credential_epoch=EXCLUDED.credential_epoch,status=EXCLUDED.status,source='shared_runtime',authorized_at=EXCLUDED.authorized_at,expires_at=EXCLUDED.expires_at,last_error=EXCLUDED.last_error,refresh_retry_after=EXCLUDED.refresh_retry_after,updated_at=NOW();

UPDATE account_openai_oauth_os_credentials SET credentials='{}'::jsonb WHERE credentials<>'{}'::jsonb;

-- Keep legacy readers on the shared grant while preserving routing settings.
UPDATE accounts a SET credentials=(COALESCE(a.credentials,'{}'::jsonb)-ARRAY['access_token','refresh_token','id_token','expires_at','expires_in','email','chatgpt_account_id','chatgpt_user_id','organization_id','plan_type','subscription_expires_at','client_id','token_type','chatgpt_account_is_fedramp','auth_mode','openai_auth_mode','_token_version']::text[])
    || CASE WHEN shared.status<>'unauthorized' THEN shared.credentials ELSE '{}'::jsonb END
FROM account_openai_oauth_credentials shared WHERE shared.account_id=a.id
    AND a.platform='openai' AND a.type='oauth' AND a.parent_account_id IS NULL
    AND LOWER(BTRIM(COALESCE(a.credentials->>'auth_mode',''))) NOT IN ('personalaccesstoken','personal_access_token','agentidentity')
    AND LOWER(BTRIM(COALESCE(a.credentials->>'openai_auth_mode',''))) NOT IN ('personalaccesstoken','personal_access_token','agentidentity');

DO $$ BEGIN
    IF NOT EXISTS(SELECT 1 FROM pg_constraint WHERE conname='account_openai_oauth_os_credentials_runtime_only' AND conrelid='account_openai_oauth_os_credentials'::regclass) THEN
        ALTER TABLE account_openai_oauth_os_credentials ADD CONSTRAINT account_openai_oauth_os_credentials_runtime_only CHECK(credentials='{}'::jsonb);
    END IF;
END $$;
