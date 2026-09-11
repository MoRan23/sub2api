-- Keep daily OAuth session/root rotation opt-in. Existing operator values are
-- preserved so upgrades cannot change the current identity behavior.
INSERT INTO settings (key, value, updated_at)
VALUES ('enable_openai_oauth_daily_session_rotation', 'false', NOW())
ON CONFLICT (key) DO NOTHING;
