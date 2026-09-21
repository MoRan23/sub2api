-- A daily OAuth pool owns one streaming and one synchronous root per OS.
-- Existing pools are upgraded atomically on their first outbound use, when
-- the credential owner's persisted default OS is available. Administrative
-- reads never provision roots or guess which OS owns the legacy sync root.
CREATE TABLE IF NOT EXISTS openai_oauth_daily_os_roots (
  pool_id BIGINT NOT NULL,
  os_family TEXT NOT NULL,
  stream_session_id TEXT NOT NULL UNIQUE,
  sync_session_id TEXT NOT NULL UNIQUE,
  PRIMARY KEY (pool_id, os_family),
  CONSTRAINT openai_oauth_daily_os_roots_pool_fk
    FOREIGN KEY (pool_id) REFERENCES openai_oauth_daily_session_pools(id) ON DELETE CASCADE,
  CONSTRAINT openai_oauth_daily_os_roots_os_check
    CHECK (os_family IN ('windows', 'macos', 'linux')),
  CONSTRAINT openai_oauth_daily_os_roots_independent_check
    CHECK (stream_session_id <> sync_session_id)
);
