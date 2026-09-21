-- Private runtime state, deliberately separate from account import/export.
-- The settings row is the shared publication barrier; it is not a user option.
INSERT INTO settings (key, value, updated_at)
SELECT 'codex_telemetry_runtime_policy', jsonb_build_object(
    'epoch', 1,
    'enabled', COALESCE((SELECT lower(btrim(value)) = 'true' FROM settings WHERE key = 'codex_telemetry_enabled'), true),
    'simulation', COALESCE((SELECT lower(btrim(value)) = 'true' FROM settings WHERE key = 'codex_telemetry_simulation_enabled'), true),
    'observation', COALESCE((SELECT lower(btrim(value)) = 'true' FROM settings WHERE key = 'codex_telemetry_observation_enabled'), true)
)::text, NOW()
ON CONFLICT (key) DO NOTHING;

CREATE TABLE IF NOT EXISTS codex_telemetry_pools (
    id UUID PRIMARY KEY,
    owner_account_id BIGINT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    -- unknown is an observation-only partition, never a fourth simulator.
    os_family TEXT NOT NULL CHECK (os_family IN ('windows', 'macos', 'linux', 'unknown')),
    installation_id TEXT NOT NULL DEFAULT '' CHECK (length(installation_id) <= 256),
    seed UUID NOT NULL,
    template_version INTEGER NOT NULL DEFAULT 1 CHECK (template_version > 0),
    version BIGINT NOT NULL DEFAULT 0 CHECK (version >= 0),
    last_business_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (owner_account_id, os_family, installation_id)
);

CREATE TABLE IF NOT EXISTS codex_telemetry_activities (
    pool_id UUID NOT NULL REFERENCES codex_telemetry_pools(id) ON DELETE CASCADE,
    kind TEXT NOT NULL CHECK (length(kind) BETWEEN 1 AND 64),
    activity_key TEXT NOT NULL CHECK (length(activity_key) BETWEEN 1 AND 512),
    state JSONB NOT NULL,
    due_at TIMESTAMPTZ,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (pool_id, kind, activity_key)
);
CREATE INDEX IF NOT EXISTS codex_telemetry_activities_due_idx
    ON codex_telemetry_activities(due_at, pool_id) WHERE due_at IS NOT NULL;

CREATE TABLE IF NOT EXISTS codex_telemetry_batches (
    id UUID PRIMARY KEY,
    sequence BIGSERIAL NOT NULL UNIQUE,
    pool_id UUID NOT NULL REFERENCES codex_telemetry_pools(id) ON DELETE CASCADE,
    policy_epoch BIGINT NOT NULL CHECK (policy_epoch > 0),
    account_id BIGINT NOT NULL,
    proxy_id BIGINT,
    type TEXT NOT NULL CHECK (type IN ('analytics', 'metrics')),
    source TEXT NOT NULL CHECK (source IN ('observed', 'simulated', 'mixed')),
    user_agent TEXT NOT NULL DEFAULT '',
    originator TEXT NOT NULL DEFAULT '',
    client_version TEXT NOT NULL DEFAULT '',
    payload JSONB NOT NULL,
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    status TEXT NOT NULL DEFAULT 'queued' CHECK (status IN ('queued', 'claimed', 'sending', 'sent', 'failed', 'dropped', 'cancelled', 'skipped', 'unknown')),
    claim_id UUID,
    lease_until TIMESTAMPTZ,
    started_at TIMESTAMPTZ,
    completed_at TIMESTAMPTZ,
    http_status INTEGER NOT NULL DEFAULT 0 CHECK (http_status BETWEEN 0 AND 599),
    error_code TEXT NOT NULL DEFAULT '',
    not_before TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
-- No FK for proxy_id: a removed frozen route must be reported as unavailable,
-- never silently rewritten to NULL and sent directly.
CREATE INDEX IF NOT EXISTS codex_telemetry_batches_dispatch_idx
    ON codex_telemetry_batches(not_before, created_at) WHERE status IN ('queued', 'claimed', 'sending');
CREATE INDEX IF NOT EXISTS codex_telemetry_batches_pool_order_idx
    ON codex_telemetry_batches(pool_id, sequence) WHERE status IN ('queued', 'claimed', 'sending');
CREATE INDEX IF NOT EXISTS codex_telemetry_batches_retention_idx
    ON codex_telemetry_batches(completed_at) WHERE completed_at IS NOT NULL;
