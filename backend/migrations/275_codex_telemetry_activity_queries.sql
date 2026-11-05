-- Pool transactions read only touched keys and indexed transient subsets.
CREATE INDEX IF NOT EXISTS codex_telemetry_activities_pool_due_idx
    ON codex_telemetry_activities(pool_id, due_at, kind COLLATE "C", activity_key COLLATE "C")
    WHERE due_at IS NOT NULL;

-- Permanent thread/metrics/policy markers never enter retention or capacity scans.
CREATE INDEX IF NOT EXISTS codex_telemetry_activities_runtime_age_idx
    ON codex_telemetry_activities(pool_id, updated_at, kind, activity_key)
    WHERE kind IN ('turn', 'attempt', 'sampling');
