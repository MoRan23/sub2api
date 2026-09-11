-- Stable account-owned roots for synchronous OpenAI OAuth requests.
-- The account_id uniqueness makes initialization an atomic first-writer-wins
-- operation across instances; deleting an account removes its root as well.
CREATE TABLE IF NOT EXISTS openai_oauth_sync_sessions (
    id BIGSERIAL PRIMARY KEY,
    account_id BIGINT NOT NULL UNIQUE,
    session_id TEXT NOT NULL UNIQUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT openai_oauth_sync_sessions_account_fk
        FOREIGN KEY (account_id) REFERENCES accounts(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS openai_oauth_sync_sessions_session_id_idx
    ON openai_oauth_sync_sessions (session_id);
