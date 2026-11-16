-- Durable, bounded subscription reminder dispatch. Existing notification delivery
-- keys remain authoritative for emails already sent by older versions.
CREATE TABLE IF NOT EXISTS subscription_reminder_outbox (
    id BIGSERIAL PRIMARY KEY,
    subscription_id BIGINT NOT NULL,
    user_id BIGINT NOT NULL,
    recipient_email TEXT NOT NULL,
    reminder_days INTEGER NOT NULL CHECK (reminder_days IN (1, 3, 7)),
    expires_at TIMESTAMPTZ NOT NULL,
    status TEXT NOT NULL DEFAULT 'queued' CHECK (status IN ('queued', 'running', 'sent', 'skipped', 'failed')),
    attempts INTEGER NOT NULL DEFAULT 0,
    available_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    claim_id TEXT,
    lease_until TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(subscription_id, recipient_email, reminder_days)
);
CREATE INDEX IF NOT EXISTS idx_subscription_reminder_pending
    ON subscription_reminder_outbox(available_at, id) WHERE status IN ('queued', 'running');
CREATE INDEX IF NOT EXISTS idx_subscription_reminder_lease
    ON subscription_reminder_outbox(lease_until) WHERE status = 'running';
