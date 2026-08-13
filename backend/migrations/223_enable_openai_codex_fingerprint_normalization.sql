-- Install the unified Codex fingerprint policy defaults without overriding an
-- operator's existing rollback choices. Migration 222 already installs the
-- UUIDv7 default for databases that do not have that setting yet; listing it
-- here keeps this migration independently safe and idempotent.
INSERT INTO settings (key, value, updated_at)
VALUES
    ('enable_openai_codex_fingerprint_normalization', 'true', NOW()),
    ('enable_openai_codex_installation_id_normalization', 'true', NOW()),
    ('enable_openai_uuidv7_session_identity', 'true', NOW()),
    ('enable_openai_codex_client_identity_normalization', 'true', NOW())
ON CONFLICT (key) DO NOTHING;

-- Active credential-owning OpenAI OAuth accounts use one fixed UUIDv4. Keep a
-- valid existing value; only repair a missing or malformed value. UUID text is
-- canonicalized to lowercase, and case-only duplicates are assigned fresh IDs
-- before the existing case-sensitive unique index sees the normalized value.
WITH active_ranked AS (
    SELECT
        id,
        lower(extra ->> 'openai_pinned_installation_id') AS canonical_id,
        row_number() OVER (
            PARTITION BY lower(extra ->> 'openai_pinned_installation_id')
            ORDER BY id
        ) AS canonical_rank
    FROM accounts
    WHERE deleted_at IS NULL
      AND status = 'active'
      AND platform = 'openai'
      AND type = 'oauth'
      AND parent_account_id IS NULL
      AND COALESCE(extra ->> 'openai_pinned_installation_id', '')
          ~* '^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'
), duplicate_active_ids AS (
    SELECT id
    FROM active_ranked
    WHERE canonical_rank > 1
), inactive_exact_conflicts AS (
    -- Inactive credential owners are intentionally left untouched below. If
    -- one already owns the lowercase spelling, re-key the active row before
    -- normalization so the existing case-sensitive unique index cannot fail.
    SELECT active.id
    FROM active_ranked AS active
    JOIN accounts AS inactive
      ON inactive.id <> active.id
     AND inactive.deleted_at IS NULL
     AND inactive.status IS DISTINCT FROM 'active'
     AND inactive.platform = 'openai'
     AND inactive.type = 'oauth'
     AND inactive.parent_account_id IS NULL
     AND inactive.extra ->> 'openai_pinned_installation_id' = active.canonical_id
), reassigned_ids AS (
    SELECT id FROM duplicate_active_ids
    UNION
    SELECT id FROM inactive_exact_conflicts
)
UPDATE accounts AS a
SET extra = (COALESCE(a.extra, '{}'::jsonb) - 'openai_pinned_installation_id')
        || jsonb_build_object('openai_pinned_installation_id', gen_random_uuid()::text),
    updated_at = NOW()
FROM reassigned_ids
WHERE a.id = reassigned_ids.id;

WITH normalized AS (
    SELECT
        id,
        CASE
            WHEN COALESCE(extra ->> 'openai_pinned_installation_id', '')
                ~* '^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'
            THEN lower(extra ->> 'openai_pinned_installation_id')
            ELSE gen_random_uuid()::text
        END AS installation_id
    FROM accounts
    WHERE deleted_at IS NULL
      AND status = 'active'
      AND platform = 'openai'
      AND type = 'oauth'
      AND parent_account_id IS NULL
)
UPDATE accounts AS a
SET extra = (COALESCE(a.extra, '{}'::jsonb)
        - 'codex_fingerprint_mode'
        - 'openai_installation_rotate_enabled')
        || jsonb_build_object(
            'openai_installation_pin_enabled', CASE
                WHEN jsonb_typeof(COALESCE(a.extra, '{}'::jsonb) -> 'openai_installation_pin_enabled') = 'boolean'
                THEN (a.extra ->> 'openai_installation_pin_enabled')::boolean
                ELSE true
            END,
            'openai_pinned_installation_id', normalized.installation_id
        ),
    credentials = COALESCE(a.credentials, '{}'::jsonb) - 'codex_fingerprint_mode',
    updated_at = NOW()
FROM normalized
WHERE a.id = normalized.id;

-- The removed mode and rotation fields never participate in runtime policy.
UPDATE accounts
SET extra = COALESCE(extra, '{}'::jsonb)
        - 'codex_fingerprint_mode'
        - 'openai_installation_rotate_enabled',
    credentials = COALESCE(credentials, '{}'::jsonb) - 'codex_fingerprint_mode',
    updated_at = NOW()
WHERE extra ? 'codex_fingerprint_mode'
   OR extra ? 'openai_installation_rotate_enabled'
   OR credentials ? 'codex_fingerprint_mode';

-- Shadows and non-OAuth accounts do not own an OAuth installation identity.
UPDATE accounts
SET extra = COALESCE(extra, '{}'::jsonb)
        - 'openai_installation_pin_enabled'
        - 'openai_pinned_installation_id'
        - 'openai_installation_rotate_enabled'
        - 'codex_fingerprint_mode',
    credentials = COALESCE(credentials, '{}'::jsonb) - 'codex_fingerprint_mode',
    updated_at = NOW()
WHERE (extra ? 'openai_installation_pin_enabled'
       OR extra ? 'openai_pinned_installation_id')
  AND NOT (
      deleted_at IS NULL
      AND platform = 'openai'
      AND type = 'oauth'
      AND parent_account_id IS NULL
  );
