ALTER TABLE groups
    ADD COLUMN IF NOT EXISTS total_limit_usd DECIMAL(20,8);

CREATE TABLE IF NOT EXISTS user_subscription_quota_events (
    id BIGSERIAL PRIMARY KEY,
    user_subscription_id BIGINT NOT NULL REFERENCES user_subscriptions(id) ON DELETE CASCADE,
    quota_total_usd DECIMAL(20,10) NOT NULL,
    quota_used_usd DECIMAL(20,10) NOT NULL DEFAULT 0,
    starts_at TIMESTAMPTZ NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    source_kind VARCHAR(32) NOT NULL,
    source_ref TEXT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_user_subscription_quota_events_subscription_id
    ON user_subscription_quota_events(user_subscription_id);

CREATE INDEX IF NOT EXISTS idx_user_subscription_quota_events_expires_at
    ON user_subscription_quota_events(expires_at);

CREATE INDEX IF NOT EXISTS idx_user_subscription_quota_events_subscription_expires
    ON user_subscription_quota_events(user_subscription_id, expires_at, id);
