CREATE INDEX CONCURRENTLY IF NOT EXISTS codex_telemetry_activities_runtime_slots_idx
    ON codex_telemetry_activities(pool_id) INCLUDE (runtime_slots)
    WHERE kind IN ('turn', 'attempt', 'sampling');
