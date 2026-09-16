-- Codex client telemetry is independently configurable. Keep an existing
-- explicit preference when upgrading or reapplying this migration.
INSERT INTO settings (key, value, updated_at)
VALUES ('codex_telemetry_enabled', 'true', NOW())
ON CONFLICT (key) DO NOTHING;
