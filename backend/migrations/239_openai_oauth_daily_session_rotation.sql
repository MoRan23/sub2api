-- Daily OAuth session roots. business_date is the calendar date in Asia/Shanghai
-- (UTC+8), deliberately stored as text for portable Ent/SQLite test schemas.
CREATE TABLE IF NOT EXISTS openai_oauth_daily_session_pools (
  id BIGSERIAL PRIMARY KEY,
  account_id BIGINT NOT NULL,
  business_date VARCHAR(10) NOT NULL,
  generation TEXT NOT NULL,
  stream_session_0 TEXT NOT NULL,
  stream_session_1 TEXT NOT NULL,
  stream_session_2 TEXT NOT NULL,
  sync_session TEXT NOT NULL,
  active_streams INTEGER NOT NULL DEFAULT 0,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  CONSTRAINT openai_oauth_daily_session_pools_account_fk
    FOREIGN KEY (account_id) REFERENCES accounts(id) ON DELETE CASCADE,
  CONSTRAINT openai_oauth_daily_session_pools_account_date_uniq
    UNIQUE (account_id, business_date),
  CONSTRAINT openai_oauth_daily_session_pools_generation_uniq UNIQUE (generation),
  CONSTRAINT openai_oauth_daily_session_pools_stream0_uniq UNIQUE (stream_session_0),
  CONSTRAINT openai_oauth_daily_session_pools_stream1_uniq UNIQUE (stream_session_1),
  CONSTRAINT openai_oauth_daily_session_pools_stream2_uniq UNIQUE (stream_session_2),
  CONSTRAINT openai_oauth_daily_session_pools_sync_uniq UNIQUE (sync_session)
);

CREATE INDEX IF NOT EXISTS idx_openai_oauth_daily_session_pools_account_date
  ON openai_oauth_daily_session_pools(account_id, business_date);

CREATE TABLE IF NOT EXISTS openai_oauth_daily_session_affinities (
  id BIGSERIAL PRIMARY KEY,
  account_id BIGINT NOT NULL,
  api_key_id BIGINT NOT NULL DEFAULT 0,
  logical_session_key TEXT NOT NULL,
  business_date VARCHAR(10) NOT NULL,
  generation TEXT NOT NULL,
  slot_index INTEGER NOT NULL,
  stream_session_id TEXT NOT NULL,
  last_seen_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  active BOOLEAN NOT NULL DEFAULT TRUE,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  CONSTRAINT openai_oauth_daily_session_affinities_account_fk
    FOREIGN KEY (account_id) REFERENCES accounts(id) ON DELETE CASCADE,
  CONSTRAINT openai_oauth_daily_session_affinities_key_uniq
    UNIQUE (account_id, api_key_id, logical_session_key, business_date),
  CONSTRAINT openai_oauth_daily_session_affinities_slot_check
    CHECK (slot_index >= 0 AND slot_index < 3)
);

CREATE INDEX IF NOT EXISTS idx_openai_oauth_daily_session_affinities_generation
  ON openai_oauth_daily_session_affinities(account_id, business_date, generation);
