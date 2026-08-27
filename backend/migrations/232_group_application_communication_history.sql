-- Persist only the bounded, extracted visible portion of correlated inbound
-- replies. The payload is encrypted by the application before it reaches SQL;
-- raw MIME messages are never stored.

ALTER TABLE group_application_inbound_receipts
    ADD COLUMN IF NOT EXISTS content_encrypted TEXT,
    ADD COLUMN IF NOT EXISTS content_truncated BOOLEAN NOT NULL DEFAULT FALSE;

COMMENT ON TABLE group_application_inbound_receipts IS
    'Stores IMAP correlation metadata and encrypted, bounded visible content for application communication history. Raw inbound MIME messages are never persisted.';
