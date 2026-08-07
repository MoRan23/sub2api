-- Assign one stable Codex environment fingerprint to every active OpenAI
-- OAuth/API-key parent account that does not already have an account-level UA.
WITH fingerprint_pool AS (
    SELECT ARRAY[
        '(Ubuntu 22.4.0; x86_64) xterm-256color',
        '(Ubuntu 22.4.0; x86_64) screen-256color',
        '(Ubuntu 24.04.0; x86_64) xterm-256color',
        '(Ubuntu 24.04.0; arm64) xterm-256color',
        '(Mac OS X 14.7.0; arm64) iTerm.app',
        '(Mac OS X 15.1.0; arm64) iTerm.app',
        '(Windows 10.0.19045; x86_64) WindowsTerminal',
        '(Windows 11.0.26100; x86_64) WindowsTerminal'
    ]::text[] AS values
), generated AS (
    SELECT
        a.id,
        'codex-tui/0.146.0 ' || pool.values[
            1 + FLOOR(random() * array_length(pool.values, 1))::int
        ] AS user_agent
    FROM accounts AS a
    CROSS JOIN fingerprint_pool AS pool
    WHERE a.deleted_at IS NULL
      AND a.platform = 'openai'
      AND a.type IN ('oauth', 'apikey')
      AND a.parent_account_id IS NULL
      AND BTRIM(COALESCE(a.credentials ->> 'user_agent', '')) = ''
)
UPDATE accounts AS a
SET credentials = COALESCE(a.credentials, '{}'::jsonb)
        || jsonb_build_object('user_agent', generated.user_agent),
    updated_at = NOW()
FROM generated
WHERE a.id = generated.id;

-- Credential shadows use their parent identity and must never retain an
-- independently editable environment fingerprint.
UPDATE accounts
SET credentials = COALESCE(credentials, '{}'::jsonb) - 'user_agent',
    updated_at = NOW()
WHERE deleted_at IS NULL
  AND platform = 'openai'
  AND parent_account_id IS NOT NULL
  AND credentials ? 'user_agent';
