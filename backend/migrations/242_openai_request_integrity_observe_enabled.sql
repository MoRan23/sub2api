-- Observe final OpenAI OAuth request integrity independently of fingerprint
-- collection and telemetry. Preserve an existing explicit preference.
INSERT INTO settings (key, value, updated_at)
VALUES ('openai_request_integrity_observe_enabled', 'true', NOW())
ON CONFLICT (key) DO NOTHING;
