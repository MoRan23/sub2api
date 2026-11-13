ALTER TABLE usage_logs ADD COLUMN IF NOT EXISTS grok_safety_rejection JSONB;
