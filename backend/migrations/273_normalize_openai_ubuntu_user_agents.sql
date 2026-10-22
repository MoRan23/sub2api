-- Codex's os_info renders Ubuntu VERSION_ID=24.04 as 24.4.0. Repair only the
-- old preset's OS version in stored account and OAuth installation UAs.
-- Keep client versions, terminals, credentials, installation IDs and roots.
-- Historical request/telemetry snapshots are deliberately left intact.
WITH affected_accounts AS MATERIALIZED (
    SELECT a.id
    FROM accounts AS a
    WHERE a.platform = 'openai'
      AND a.deleted_at IS NULL
      AND (
          (jsonb_typeof(a.credentials -> 'user_agent') = 'string'
           AND strpos(a.credentials ->> 'user_agent', '(Ubuntu 24.04.4;') > 0)
          OR EXISTS (
              SELECT 1
              FROM account_openai_oauth_os_profiles AS p
              WHERE p.account_id = a.id
                AND strpos(p.user_agent, '(Ubuntu 24.04.4;') > 0
          )
      )
    -- Match the application's account-before-profile lock order.
    ORDER BY a.id
    FOR UPDATE OF a
), updated_accounts AS (
    UPDATE accounts AS a
    SET credentials = CASE
            WHEN jsonb_typeof(a.credentials -> 'user_agent') = 'string'
                 AND strpos(a.credentials ->> 'user_agent', '(Ubuntu 24.04.4;') > 0
            THEN jsonb_set(a.credentials, '{user_agent}',
                to_jsonb(replace(a.credentials ->> 'user_agent',
                    '(Ubuntu 24.04.4;', '(Ubuntu 24.4.0;')), false)
            ELSE a.credentials
        END,
        updated_at = NOW()
    FROM affected_accounts AS affected
    WHERE a.id = affected.id
    RETURNING a.id
), updated_profiles AS (
    UPDATE account_openai_oauth_os_profiles AS p
    SET user_agent = replace(p.user_agent, '(Ubuntu 24.04.4;', '(Ubuntu 24.4.0;'),
        updated_at = NOW()
    FROM updated_accounts AS a
    WHERE p.account_id = a.id
      AND strpos(p.user_agent, '(Ubuntu 24.04.4;') > 0
    RETURNING p.account_id
)
INSERT INTO scheduler_outbox (event_type, account_id)
SELECT 'account_changed', id
FROM updated_accounts;
