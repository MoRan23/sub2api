-- A ticket and its Cookie snapshot are one publication tied to the exact wire
-- mode and physical egress. Legacy rows cannot establish either binding.
LOCK TABLE proxies, accounts, openai_codex_state, openai_codex_state_business_leases,
    openai_http_cookies IN ACCESS EXCLUSIVE MODE;

ALTER TABLE proxies ADD COLUMN route_generation BIGINT NOT NULL DEFAULT 1 CHECK (route_generation > 0);
ALTER TABLE openai_codex_state
    ADD COLUMN bundle_wire_mode TEXT NOT NULL DEFAULT '',
    ADD COLUMN bundle_egress_kind TEXT NOT NULL DEFAULT '',
    ADD COLUMN bundle_proxy_id BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN bundle_proxy_route_generation BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN bundle_invalidation_version BIGINT NOT NULL DEFAULT 1 CHECK (bundle_invalidation_version > 0),
    ADD COLUMN last_eligible_collection_at TIMESTAMPTZ,
    ADD CONSTRAINT openai_codex_state_bundle_binding_check CHECK (
        (bundle_wire_mode = '' AND bundle_egress_kind = '' AND bundle_proxy_id = 0 AND bundle_proxy_route_generation = 0)
        OR (bundle_wire_mode IN ('responses', 'lite') AND (
            (bundle_egress_kind = 'direct' AND bundle_proxy_id = 0 AND bundle_proxy_route_generation = 0)
            OR (bundle_egress_kind = 'proxy' AND bundle_proxy_id > 0 AND bundle_proxy_route_generation > 0))));

-- A crash reservation carrying an old rate-limit label is not a cooldown.
UPDATE accounts a SET codex_turn_state_retry_after = GREATEST(a.codex_turn_state_retry_after, limits.until)
FROM (
    SELECT owner_account_id, MAX(next_collect_at) AS until FROM openai_codex_state
    WHERE last_error IN ('collector_rate_limited','account_cooldown')
      AND collection_status <> 'collecting' AND collector_attempt_id IS NULL
    GROUP BY owner_account_id
) limits WHERE a.id=limits.owner_account_id;

DELETE FROM openai_codex_state_business_leases;
DELETE FROM openai_http_cookies;
UPDATE openai_codex_state s SET
    version=s.version+1, encrypted_token='', encrypted_cookie_bundle='', cookie_bundle_expires_at=NULL,
    issued_at=NULL, expires_at=NULL, token_length=0, cipher_blocks=0, source='', shape='', refresh_reason='',
    collector_attempt_id=NULL, demand_reason='', demand_at=NULL,
    next_collect_at=CASE WHEN a.codex_turn_state_retry_after > NOW() THEN a.codex_turn_state_retry_after ELSE NULL END,
    last_error=CASE WHEN s.collector_paused THEN s.last_error
        WHEN a.codex_turn_state_retry_after > NOW() THEN 'account_cooldown' ELSE '' END,
    collection_status=CASE WHEN s.collector_paused THEN 'paused'
        WHEN a.codex_turn_state_retry_after > NOW() THEN 'backoff' ELSE 'idle' END,
    collection_reason=CASE WHEN s.collector_paused THEN s.collection_reason
        WHEN a.codex_turn_state_retry_after > NOW() THEN 'account_cooldown' ELSE '' END,
    updated_at=NOW()
FROM accounts a WHERE a.id=s.owner_account_id;

-- Do not infer the old request mode from last_business_at or history proofs.
CREATE INDEX openai_codex_state_recent_eligible_collection_idx
    ON openai_codex_state (last_eligible_collection_at DESC)
    WHERE last_eligible_collection_at IS NOT NULL;
