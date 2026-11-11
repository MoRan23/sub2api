-- Prompt-audit jailbreak/combination enforcement state.  These tables hold
-- compact policy facts only; prompts, rules and credentials are never copied.
CREATE TABLE IF NOT EXISTS prompt_audit_enforcement_states (
    user_id BIGINT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    epoch BIGINT NOT NULL DEFAULT 1 CHECK (epoch > 0),
    revision BIGINT NOT NULL DEFAULT 0 CHECK (revision >= 0),
    owned_ban_revision BIGINT,
    ban_event_id BIGINT,
    counting_since TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS prompt_audit_enforcement_hits (
    id BIGSERIAL PRIMARY KEY,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    epoch BIGINT NOT NULL CHECK (epoch > 0),
    request_key VARCHAR(192) NOT NULL,
    event_id BIGINT,
    kind VARCHAR(32) NOT NULL CHECK (kind IN ('jailbreak','combination')),
    counted BOOLEAN NOT NULL DEFAULT TRUE,
    rule_group_name VARCHAR(160) NOT NULL DEFAULT '',
    occurred_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    violation_count INT NOT NULL DEFAULT 0 CHECK (violation_count >= 0),
    window_hours INT NOT NULL DEFAULT 24 CHECK (window_hours > 0),
    ban_threshold INT NOT NULL DEFAULT 10 CHECK (ban_threshold > 0),
    auto_banned BOOLEAN NOT NULL DEFAULT FALSE,
    user_status VARCHAR(32) NOT NULL DEFAULT 'active',
    email_status VARCHAR(32) NOT NULL DEFAULT 'not_requested',
    notification_id BIGINT,
    notification_version BIGINT,
    reason VARCHAR(128) NOT NULL DEFAULT '',
    CONSTRAINT prompt_audit_enforcement_hits_unique UNIQUE (user_id, request_key)
);
CREATE INDEX IF NOT EXISTS prompt_audit_enforcement_hits_window_idx
    ON prompt_audit_enforcement_hits (user_id, epoch, occurred_at DESC);
CREATE INDEX IF NOT EXISTS prompt_audit_enforcement_hits_event_idx
    ON prompt_audit_enforcement_hits (event_id);
CREATE INDEX IF NOT EXISTS prompt_audit_enforcement_hits_retention_idx
    ON prompt_audit_enforcement_hits (occurred_at, id);
CREATE INDEX IF NOT EXISTS prompt_audit_enforcement_hits_notification_idx
    ON prompt_audit_enforcement_hits (notification_id, notification_version);

CREATE TABLE IF NOT EXISTS prompt_audit_enforcement_notifications (
    id BIGSERIAL PRIMARY KEY,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    epoch BIGINT NOT NULL CHECK (epoch > 0),
    available_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    merge_until TIMESTAMPTZ NOT NULL,
    merge_minutes INT NOT NULL DEFAULT 5 CHECK (merge_minutes BETWEEN 1 AND 60),
    first_hit_at TIMESTAMPTZ NOT NULL,
    last_hit_at TIMESTAMPTZ NOT NULL,
    kind VARCHAR(32) NOT NULL CHECK (kind IN ('jailbreak','combination','mixed')),
    categories JSONB NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(categories) = 'array'),
    rule_group_names JSONB NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(rule_group_names) = 'array'),
    hit_count INT NOT NULL DEFAULT 0 CHECK (hit_count >= 0),
    violation_count INT NOT NULL DEFAULT 0 CHECK (violation_count >= 0),
    window_hours INT NOT NULL DEFAULT 24 CHECK (window_hours > 0),
    ban_threshold INT NOT NULL DEFAULT 10 CHECK (ban_threshold > 0),
    auto_banned BOOLEAN NOT NULL DEFAULT FALSE,
    sent_at TIMESTAMPTZ,
    claimed_at TIMESTAMPTZ,
    claimed_by VARCHAR(128),
    claim_version BIGINT NOT NULL DEFAULT 0 CHECK (claim_version >= 0),
    payload_version BIGINT NOT NULL DEFAULT 1 CHECK (payload_version > 0),
    claimed_payload_version BIGINT,
    claimed_hit_count INT,
    claimed_auto_banned BOOLEAN,
    pending_categories JSONB NOT NULL DEFAULT '[]'::jsonb,
    pending_rule_group_names JSONB NOT NULL DEFAULT '[]'::jsonb,
    pending_first_hit_at TIMESTAMPTZ,
    attempts INT NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    last_error VARCHAR(512),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE UNIQUE INDEX IF NOT EXISTS prompt_audit_enforcement_notifications_open_idx
    ON prompt_audit_enforcement_notifications (user_id, epoch)
    WHERE sent_at IS NULL;
CREATE INDEX IF NOT EXISTS prompt_audit_enforcement_notifications_due_idx
    ON prompt_audit_enforcement_notifications (available_at, id)
    WHERE sent_at IS NULL;
CREATE INDEX IF NOT EXISTS prompt_audit_enforcement_notifications_lease_idx
    ON prompt_audit_enforcement_notifications (claimed_at)
    WHERE sent_at IS NULL AND claimed_at IS NOT NULL;
