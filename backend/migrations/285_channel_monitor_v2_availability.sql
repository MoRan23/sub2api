-- New availability facts are rebuilt incrementally by the existing singleton
-- worker. NULL/legacy version means unknown, never a synthetic 100%.
ALTER TABLE channel_monitor_v2_metrics_1m
    ADD COLUMN IF NOT EXISTS availability_error_requests BIGINT,
    ADD COLUMN IF NOT EXISTS aggregation_version INTEGER NOT NULL DEFAULT 1;
ALTER TABLE channel_monitor_v2_user_metrics_1m
    ADD COLUMN IF NOT EXISTS availability_error_requests BIGINT,
    ADD COLUMN IF NOT EXISTS aggregation_version INTEGER NOT NULL DEFAULT 1;
ALTER TABLE channel_monitor_v2_metrics_rollup
    ADD COLUMN IF NOT EXISTS availability_error_requests BIGINT,
    ADD COLUMN IF NOT EXISTS aggregation_version INTEGER NOT NULL DEFAULT 1;
ALTER TABLE channel_monitor_v2_user_metrics_rollup
    ADD COLUMN IF NOT EXISTS availability_error_requests BIGINT,
    ADD COLUMN IF NOT EXISTS aggregation_version INTEGER NOT NULL DEFAULT 1;
ALTER TABLE channel_monitor_v2_watermarks
    ADD COLUMN IF NOT EXISTS aggregation_version INTEGER NOT NULL DEFAULT 1,
    ADD COLUMN IF NOT EXISTS availability_coverage_start TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS availability_source_start TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS availability_epoch_at TIMESTAMPTZ NOT NULL DEFAULT NOW();

COMMENT ON COLUMN channel_monitor_v2_metrics_1m.availability_error_requests IS
    'Service-side final failures, excluding explicit user rejection/cancellation; NULL before versioned rebuild.';
COMMENT ON COLUMN channel_monitor_v2_watermarks.availability_source_start IS
    'Earliest common raw-log coverage captured when rebuilding final-outcome facts; older rollups remain intact.';
