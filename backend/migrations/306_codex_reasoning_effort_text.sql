-- Codex catalogs may declare custom reasoning values longer than legacy names.
ALTER TABLE usage_logs
    ALTER COLUMN reasoning_effort TYPE TEXT,
    ALTER COLUMN requested_reasoning_effort TYPE TEXT;
