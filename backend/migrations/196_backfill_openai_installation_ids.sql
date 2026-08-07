-- Replace every legacy OpenAI OAuth parent installation_id with a fresh UUIDv4.
-- The value is generated server-side so it can never originate from a client
-- request. Shadow and non-OAuth accounts must not retain this system field.
WITH generated AS (
    SELECT id, gen_random_uuid()::text AS installation_id
    FROM accounts
    WHERE deleted_at IS NULL
      AND platform = 'openai'
      AND type = 'oauth'
      AND parent_account_id IS NULL
)
UPDATE accounts AS a
SET extra = (COALESCE(a.extra, '{}'::jsonb)
        - 'openai_pinned_installation_id'
        - 'openai_installation_rotate_enabled')
        || jsonb_build_object('openai_pinned_installation_id', generated.installation_id),
    updated_at = NOW()
FROM generated
WHERE a.id = generated.id;

UPDATE accounts AS a
SET extra = COALESCE(a.extra, '{}'::jsonb)
        - 'openai_pinned_installation_id'
        - 'openai_installation_rotate_enabled',
    updated_at = NOW()
WHERE (a.extra ? 'openai_pinned_installation_id'
       OR a.extra ? 'openai_installation_rotate_enabled')
  AND NOT (
      a.deleted_at IS NULL
      AND a.platform = 'openai'
      AND a.type = 'oauth'
      AND a.parent_account_id IS NULL
  );
