-- Proxy observations outlive Redis and application containers. The route digest
-- binds each payload to one proxy configuration without persisting credentials.
CREATE TABLE IF NOT EXISTS proxy_geo_snapshots (
    proxy_id BIGINT PRIMARY KEY REFERENCES proxies(id) ON DELETE CASCADE,
    route_key TEXT NOT NULL,
    geo_result_unix_ms BIGINT NOT NULL DEFAULT 0,
    payload JSONB NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
