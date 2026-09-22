-- Cookie values and attributes stay encrypted. Only authorization scope, the
-- hashed cookie identity, absolute expiry, and invalidation revisions are clear.
CREATE SEQUENCE IF NOT EXISTS openai_http_cookie_revision_seq AS BIGINT;

CREATE TABLE IF NOT EXISTS openai_http_cookies (
    owner_account_id BIGINT NOT NULL,
    os_family VARCHAR(16) NOT NULL CHECK (os_family IN ('windows', 'macos', 'linux')),
    authorization_generation UUID NOT NULL,
    entry_key VARCHAR(64) NOT NULL CHECK (entry_key ~ '^[0-9a-f]{64}$'),
    encrypted_entry TEXT CHECK (encrypted_entry <> ''),
    expires_at TIMESTAMPTZ,
    revision BIGINT NOT NULL DEFAULT nextval('openai_http_cookie_revision_seq') CHECK (revision > 0),
    CHECK ((encrypted_entry IS NULL) = (expires_at IS NULL)),
    PRIMARY KEY (owner_account_id, os_family, authorization_generation, entry_key),
    FOREIGN KEY (owner_account_id, os_family)
        REFERENCES account_openai_oauth_os_credentials (account_id, os_family) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_openai_http_cookies_expires_at
    ON openai_http_cookies (expires_at);
