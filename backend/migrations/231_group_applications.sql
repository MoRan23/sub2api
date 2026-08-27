-- Group application policies, immutable PDF attachments, applications,
-- durable email outbox and idempotent IMAP receipts.

CREATE TABLE IF NOT EXISTS group_application_attachments (
    id BIGSERIAL PRIMARY KEY,
    filename VARCHAR(255) NOT NULL,
    content_type VARCHAR(100) NOT NULL DEFAULT 'application/pdf',
    byte_size BIGINT NOT NULL CHECK (byte_size > 0 AND byte_size <= 10485760),
    sha256 VARCHAR(64) NOT NULL,
    data BYTEA NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    created_by BIGINT REFERENCES users(id) ON DELETE SET NULL
);

CREATE TABLE IF NOT EXISTS group_application_policies (
    group_id BIGINT PRIMARY KEY REFERENCES groups(id) ON DELETE RESTRICT,
    enabled BOOLEAN NOT NULL DEFAULT FALSE,
    reply_phrase TEXT NOT NULL DEFAULT '',
    templates JSONB NOT NULL DEFAULT '{}'::jsonb,
    attachment_id BIGINT REFERENCES group_application_attachments(id) ON DELETE RESTRICT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_by BIGINT REFERENCES users(id) ON DELETE SET NULL
);

CREATE TABLE IF NOT EXISTS group_applications (
    id BIGSERIAL PRIMARY KEY,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    group_id BIGINT NOT NULL REFERENCES groups(id) ON DELETE RESTRICT,
    contact_email VARCHAR(320) NOT NULL,
    reason TEXT NOT NULL,
    locale VARCHAR(10) NOT NULL DEFAULT 'zh',
    status VARCHAR(32) NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'awaiting_reply', 'completed', 'rejected', 'revoked')),
    reply_phrase_snapshot TEXT NOT NULL,
    templates_snapshot JSONB NOT NULL,
    attachment_id BIGINT NOT NULL REFERENCES group_application_attachments(id) ON DELETE RESTRICT,
    reviewed_by BIGINT REFERENCES users(id) ON DELETE SET NULL,
    reviewed_at TIMESTAMPTZ,
    decision_reason TEXT,
    completed_at TIMESTAMPTZ,
    revoked_by BIGINT REFERENCES users(id) ON DELETE SET NULL,
    revoked_at TIMESTAMPTZ,
    last_email_kind VARCHAR(32),
    last_email_status VARCHAR(20),
    last_email_error TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX IF NOT EXISTS group_applications_one_active_per_user_group
    ON group_applications(user_id, group_id)
    WHERE status IN ('pending', 'awaiting_reply');
CREATE UNIQUE INDEX IF NOT EXISTS group_applications_one_completed_per_user_group
    ON group_applications(user_id, group_id)
    WHERE status = 'completed';
CREATE INDEX IF NOT EXISTS group_applications_admin_list_idx
    ON group_applications(status, created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS group_applications_user_list_idx
    ON group_applications(user_id, created_at DESC, id DESC);

CREATE TABLE IF NOT EXISTS group_application_mail_outbox (
    id BIGSERIAL PRIMARY KEY,
    application_id BIGINT NOT NULL REFERENCES group_applications(id) ON DELETE CASCADE,
    kind VARCHAR(32) NOT NULL
        CHECK (kind IN ('approval', 'completion', 'manual_rejection', 'reply_mismatch', 'revocation')),
    recipient VARCHAR(320) NOT NULL,
    subject TEXT NOT NULL,
    html_body TEXT NOT NULL,
    attachment_id BIGINT REFERENCES group_application_attachments(id) ON DELETE RESTRICT,
    message_id VARCHAR(255) NOT NULL UNIQUE,
    required_application_status VARCHAR(32) NOT NULL,
    status VARCHAR(20) NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'processing', 'sent', 'failed', 'cancelled')),
    attempts INTEGER NOT NULL DEFAULT 0,
    available_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    claimed_at TIMESTAMPTZ,
    claimed_by VARCHAR(100),
    last_error TEXT,
    sent_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS group_application_mail_outbox_claim_idx
    ON group_application_mail_outbox(status, available_at, id)
    WHERE status IN ('pending', 'processing');
CREATE INDEX IF NOT EXISTS group_application_mail_outbox_application_idx
    ON group_application_mail_outbox(application_id, created_at DESC);

CREATE TABLE IF NOT EXISTS group_application_inbound_receipts (
    id BIGSERIAL PRIMARY KEY,
    mailbox_fingerprint VARCHAR(64) NOT NULL,
    uid_validity BIGINT NOT NULL,
    uid BIGINT NOT NULL,
    message_id VARCHAR(255),
    from_address VARCHAR(320),
    in_reply_to VARCHAR(255),
    references_header TEXT,
    application_id BIGINT REFERENCES group_applications(id) ON DELETE SET NULL,
    result VARCHAR(40) NOT NULL,
    reply_sha256 VARCHAR(64),
    processed_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(mailbox_fingerprint, uid_validity, uid)
);
CREATE INDEX IF NOT EXISTS group_application_inbound_reply_idx
    ON group_application_inbound_receipts(application_id, processed_at DESC);

COMMENT ON TABLE group_application_inbound_receipts IS
    'Stores safe IMAP metadata only. Raw inbound messages and reply bodies are never persisted.';
