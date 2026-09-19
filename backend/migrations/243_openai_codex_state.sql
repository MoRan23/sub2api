-- Experimental account/model-scoped Codex turn-state maintenance. Opaque tokens
-- are encrypted by SecretEncryptor before persistence and never enter accounts.extra.
CREATE TABLE IF NOT EXISTS openai_codex_state (
    owner_account_id BIGINT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    model TEXT NOT NULL,
    generation TEXT NOT NULL,
    version BIGINT NOT NULL DEFAULT 1 CHECK (version > 0),
    encrypted_token TEXT NOT NULL DEFAULT '',
    issued_at TIMESTAMPTZ,
    expires_at TIMESTAMPTZ,
    token_length INTEGER NOT NULL DEFAULT 0,
    cipher_blocks INTEGER NOT NULL DEFAULT 0,
    source TEXT NOT NULL DEFAULT '',
    shape TEXT NOT NULL DEFAULT '',
    refresh_reason TEXT NOT NULL DEFAULT '',
    last_business_at TIMESTAMPTZ NOT NULL,
    last_collected_at TIMESTAMPTZ,
    next_collect_at TIMESTAMPTZ,
    collector_paused BOOLEAN NOT NULL DEFAULT FALSE,
    last_error TEXT NOT NULL DEFAULT '',
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (owner_account_id, model)
);

CREATE INDEX IF NOT EXISTS openai_codex_state_recent_business_idx
    ON openai_codex_state (last_business_at DESC);

-- Each physical business attempt owns its own expiring lease. A process crash
-- cannot permanently suppress collection, and another instance sees active
-- natural requests before attempting a cold-start collection.
CREATE TABLE IF NOT EXISTS openai_codex_state_business_leases (
    owner_account_id BIGINT NOT NULL,
    model TEXT NOT NULL,
    generation TEXT NOT NULL,
    attempt_id TEXT NOT NULL,
    lease_until TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (owner_account_id, model, generation, attempt_id),
    FOREIGN KEY (owner_account_id, model)
        REFERENCES openai_codex_state(owner_account_id, model) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS openai_codex_state_business_leases_expiry_idx
    ON openai_codex_state_business_leases (lease_until);
