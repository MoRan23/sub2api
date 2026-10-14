-- An indexed pool-level schedule avoids grouping all due historical activities
-- on every runtime poll. Producers maintain this summary atomically thereafter.
ALTER TABLE codex_telemetry_pools ADD COLUMN IF NOT EXISTS next_due_at TIMESTAMPTZ;

UPDATE codex_telemetry_pools p
SET next_due_at = due.next_due_at
FROM (
    SELECT pool_id, MIN(due_at) AS next_due_at
    FROM codex_telemetry_activities
    WHERE due_at IS NOT NULL
    GROUP BY pool_id
) due
WHERE p.id = due.pool_id AND p.next_due_at IS DISTINCT FROM due.next_due_at;

CREATE INDEX IF NOT EXISTS codex_telemetry_pools_due_idx
    ON codex_telemetry_pools(next_due_at, id) WHERE next_due_at IS NOT NULL;

-- Cascading pool deletion also removes sent/cancelled history; the existing
-- pending-only ordering index cannot support those foreign-key lookups.
CREATE INDEX IF NOT EXISTS codex_telemetry_batches_pool_idx
    ON codex_telemetry_batches(pool_id);
