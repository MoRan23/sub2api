CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_user_subscriptions_reminder_expiry
    ON user_subscriptions(expires_at, id)
    WHERE deleted_at IS NULL AND status = 'active' AND expires_at IS NOT NULL;
