-- Three stable installation identities owned by a regular OpenAI OAuth account.
-- Existing account identities are migrated by the application under the account
-- row lock, preserving the valid legacy installation and synchronous root.
CREATE TABLE IF NOT EXISTS account_openai_oauth_os_profiles (
    account_id BIGINT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    os_family TEXT NOT NULL CHECK (os_family IN ('windows', 'macos', 'linux')),
    installation_id UUID NOT NULL UNIQUE CHECK (installation_id::text ~ '^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'),
    sync_session_id UUID NOT NULL UNIQUE CHECK (sync_session_id::text ~ '^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'),
    user_agent TEXT NOT NULL CHECK (BTRIM(user_agent) <> ''),
    is_default BOOLEAN NOT NULL DEFAULT FALSE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (account_id, os_family)
);

CREATE UNIQUE INDEX IF NOT EXISTS account_openai_oauth_os_profiles_default_idx
    ON account_openai_oauth_os_profiles (account_id) WHERE is_default;
