-- Engine receipt events commit quota consumption in PostgreSQL. Keep their
-- contribution separate from the ordinary Redis snapshot to merge both safely.
ALTER TABLE user_platform_quotas
    ADD COLUMN IF NOT EXISTS engine_daily_usage_usd DECIMAL(20,10) NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS engine_weekly_usage_usd DECIMAL(20,10) NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS engine_monthly_usage_usd DECIMAL(20,10) NOT NULL DEFAULT 0;
