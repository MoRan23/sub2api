-- Give every live standard OpenAI OAuth credential owner an opaque epoch,
-- including accounts that have never configured turn-state caching. This value
-- identifies a credential generation; it contains no credential-derived data.
-- Preserve existing non-empty string epochs on rerun.
UPDATE accounts
SET extra = jsonb_set(
    COALESCE(extra, '{}'::jsonb),
    '{codex_turn_state_credential_epoch}',
    to_jsonb(gen_random_uuid()::text),
    true
)
WHERE deleted_at IS NULL
  AND platform = 'openai'
  AND type = 'oauth'
  AND parent_account_id IS NULL
  AND LOWER(btrim(COALESCE(credentials ->> 'auth_mode', ''))) NOT IN ('personalaccesstoken', 'personal_access_token', 'agentidentity')
  AND LOWER(btrim(COALESCE(credentials ->> 'openai_auth_mode', ''))) NOT IN ('personalaccesstoken', 'personal_access_token', 'agentidentity')
  AND (
      jsonb_typeof(extra -> 'codex_turn_state_credential_epoch') IS DISTINCT FROM 'string'
      OR btrim(COALESCE(extra ->> 'codex_turn_state_credential_epoch', '')) = ''
  );

-- Ineligible live accounts must not retain an owner epoch. Remove only this
-- server-owned key; leave credentials, turn-state configuration and its separate
-- configuration generation unchanged. Soft-deleted accounts remain untouched.
UPDATE accounts
SET extra = extra - 'codex_turn_state_credential_epoch'
WHERE deleted_at IS NULL
  AND extra ? 'codex_turn_state_credential_epoch'
  AND NOT (
      platform = 'openai'
      AND type = 'oauth'
      AND parent_account_id IS NULL
      AND LOWER(btrim(COALESCE(credentials ->> 'auth_mode', ''))) NOT IN ('personalaccesstoken', 'personal_access_token', 'agentidentity')
      AND LOWER(btrim(COALESCE(credentials ->> 'openai_auth_mode', ''))) NOT IN ('personalaccesstoken', 'personal_access_token', 'agentidentity')
  );
