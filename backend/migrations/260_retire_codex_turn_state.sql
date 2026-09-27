-- Retire the server-owned ticket cache. Stop and drain all old processes before
-- this migration: older writers must never recreate cache configuration.
-- Native opaque protocol state and account OAuth credentials are unaffected.
DROP TRIGGER IF EXISTS account_openai_oauth_os_configuration_fence ON accounts;
DROP FUNCTION IF EXISTS fence_openai_oauth_os_credential_configuration();

DROP TABLE IF EXISTS openai_codex_state_business_leases;
DROP TABLE IF EXISTS openai_codex_state;
DROP TABLE IF EXISTS openai_http_cookies;
DROP SEQUENCE IF EXISTS openai_http_cookie_revision_seq;

ALTER TABLE accounts DROP COLUMN IF EXISTS codex_turn_state_retry_after;

UPDATE accounts a
SET extra = COALESCE((
    SELECT jsonb_object_agg(entry.key, entry.value)
    FROM jsonb_each(a.extra) AS entry
    WHERE entry.key <> 'codex_turn_state'
      AND left(entry.key, length('codex_turn_state_')) <> 'codex_turn_state_'
), '{}'::jsonb)
WHERE EXISTS (
    SELECT 1 FROM jsonb_object_keys(a.extra) AS entry(key)
    WHERE entry.key = 'codex_turn_state'
       OR left(entry.key, length('codex_turn_state_')) = 'codex_turn_state_'
);

DELETE FROM settings
WHERE key IN ('codex_turn_state_models', 'codex_turn_state_models_revision');

-- Keep shared OAuth generation/CAS/pause columns, OS identities/session roots,
-- proxy route generations and ordinary account status/rate-limit state intact.
