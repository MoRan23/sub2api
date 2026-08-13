-- Enable stable Codex UUIDv7 session/thread identity for installations that
-- have never stored an explicit preference. Existing false values are kept as
-- the operator-controlled rollback setting.
INSERT INTO settings (key, value, updated_at)
VALUES ('enable_openai_uuidv7_session_identity', 'true', NOW())
ON CONFLICT (key) DO NOTHING;
