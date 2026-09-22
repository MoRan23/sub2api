-- One immutable ticket/Cookie publication belongs to the credential owner and
-- final model. OS remains provenance only. Old per-OS publications cannot prove
-- a complete bundle, so preserve scheduling evidence but cold-start all tokens.
LOCK TABLE accounts, account_openai_oauth_credentials, account_openai_oauth_os_credentials, openai_codex_state,
    openai_codex_state_business_leases, openai_http_cookies IN ACCESS EXCLUSIVE MODE;

-- A collecting reservation can retain an old rate-limit label. Only completed
-- rate-limit outcomes may extend the account-wide deadline; never shorten it.
UPDATE accounts a SET codex_turn_state_retry_after = GREATEST(a.codex_turn_state_retry_after, limits.until)
FROM (
    SELECT owner_account_id, MAX(next_collect_at) AS until FROM openai_codex_state
    WHERE last_error IN ('collector_rate_limited','account_cooldown')
      AND collection_status <> 'collecting' AND collector_attempt_id IS NULL
    GROUP BY owner_account_id
) limits WHERE a.id=limits.owner_account_id;

CREATE TEMP TABLE codex_shared_state_migration ON COMMIT DROP AS
WITH evidence AS (
    SELECT owner_account_id, model, MAX(version) + 1 AS version,
        MAX(last_business_at) AS last_business_at,
        MAX(last_collected_at) AS last_collected_at,
        MAX(history_proof_observed_at) AS history_proof_observed_at
    FROM openai_codex_state GROUP BY owner_account_id, model
), latest AS (
    -- Keep the proxy and its rotation count from one complete row. Taking
    -- independent MAX values would invent a rotation that never happened.
    SELECT DISTINCT ON (owner_account_id, model) * FROM openai_codex_state
    ORDER BY owner_account_id, model, updated_at DESC, version DESC, os_family
), paused AS (
    -- Migration 256 retained the pre-migration OS fence as previous_generation.
    -- Only that live authorization's pause is inherited; an older revoked slot
    -- must not pause a newly authorized owner's shared state.
    SELECT DISTINCT ON (s.owner_account_id, s.model) s.owner_account_id, s.model,
        COALESCE(NULLIF(s.last_error,''),NULLIF(s.collection_reason,''),'collector_auth_rejected') AS reason
    FROM openai_codex_state s
    JOIN account_openai_oauth_os_credentials runtime
        ON runtime.account_id=s.owner_account_id AND runtime.os_family=s.os_family
    JOIN account_openai_oauth_credentials credential_meta ON credential_meta.account_id=s.owner_account_id
    WHERE s.collector_paused AND runtime.authorization_generation=credential_meta.authorization_generation
      AND (s.generation=runtime.state_generation::text OR s.generation=runtime.previous_state_generation::text)
    ORDER BY s.owner_account_id,s.model,s.updated_at DESC,s.version DESC,s.os_family
)
SELECT e.*, l.os_family AS source_os, l.collector_proxy_id,
    l.collector_extended_count, l.last_collector_proxy_id,
    p.owner_account_id IS NOT NULL AS collector_paused, p.reason AS pause_reason,
    COALESCE(credential_meta.state_generation::text, gen_random_uuid()::text) AS generation,
    COALESCE(credential_meta.authorization_generation, gen_random_uuid()) AS authorization_generation,
    a.codex_turn_state_retry_after,
    (a.deleted_at IS NULL AND a.parent_account_id IS NULL AND a.platform='openai' AND a.type='oauth'
        AND a.extra->'codex_turn_state'->>'enabled'='true' AND credential_meta.status='authorized'
        AND e.last_business_at >= NOW() - INTERVAL '30 minutes') IS TRUE AS active
FROM evidence e JOIN latest l USING (owner_account_id, model)
JOIN accounts a ON a.id=e.owner_account_id
LEFT JOIN account_openai_oauth_credentials credential_meta ON credential_meta.account_id=e.owner_account_id
LEFT JOIN paused p USING (owner_account_id,model);

-- Both foreign-key actions and in-flight leases are obsolete after consolidation.
-- Resolve the generated FK name from its actual relation instead of relying on
-- PostgreSQL identifier truncation rules for the old three-column name.
DO $$ DECLARE constraint_name TEXT; BEGIN
    FOR constraint_name IN SELECT conname FROM pg_constraint
        WHERE conrelid='openai_codex_state_business_leases'::regclass
          AND confrelid='openai_codex_state'::regclass AND contype='f'
    LOOP
        EXECUTE format('ALTER TABLE openai_codex_state_business_leases DROP CONSTRAINT %I', constraint_name);
    END LOOP;
END $$;
DELETE FROM openai_codex_state_business_leases;
DELETE FROM openai_codex_state;
DELETE FROM openai_http_cookies;

ALTER TABLE openai_codex_state DROP CONSTRAINT openai_codex_state_pkey,
    DROP COLUMN os_family,
    ADD COLUMN source_os TEXT NOT NULL DEFAULT '' CHECK (source_os IN ('','windows','macos','linux')),
    ADD COLUMN authorization_generation UUID NOT NULL,
    ADD COLUMN encrypted_cookie_bundle TEXT NOT NULL DEFAULT '',
    ADD COLUMN cookie_bundle_expires_at TIMESTAMPTZ,
    ADD PRIMARY KEY (owner_account_id, model);
ALTER TABLE openai_codex_state_business_leases DROP CONSTRAINT openai_codex_state_business_leases_pkey,
    DROP COLUMN os_family,
    ADD COLUMN source_os TEXT NOT NULL DEFAULT '' CHECK (source_os IN ('','windows','macos','linux')),
    ADD PRIMARY KEY (owner_account_id, model, generation, attempt_id),
    ADD CONSTRAINT openai_codex_state_business_leases_state_fk FOREIGN KEY (owner_account_id, model)
        REFERENCES openai_codex_state(owner_account_id, model) ON DELETE CASCADE ON UPDATE CASCADE;

INSERT INTO openai_codex_state
    (owner_account_id, model, generation, authorization_generation, version, source_os,
     last_business_at, last_collected_at, history_proof_observed_at,
     collector_proxy_id, collector_extended_count, last_collector_proxy_id,
     demand_reason, demand_at, refresh_reason, next_collect_at, collector_paused, last_error,
     collection_status, collection_reason)
SELECT owner_account_id, model, generation, authorization_generation, version, source_os,
    last_business_at, last_collected_at, history_proof_observed_at,
    collector_proxy_id, collector_extended_count, last_collector_proxy_id,
    CASE WHEN active THEN 'cache_miss' ELSE '' END,
    CASE WHEN active THEN NOW() ELSE NULL END,
    CASE WHEN active THEN 'cache_miss' ELSE '' END,
    CASE WHEN codex_turn_state_retry_after > NOW() THEN codex_turn_state_retry_after ELSE NULL END,
    collector_paused,
    CASE WHEN collector_paused THEN pause_reason WHEN codex_turn_state_retry_after > NOW() THEN 'account_cooldown' ELSE '' END,
    CASE WHEN collector_paused THEN 'paused' WHEN codex_turn_state_retry_after > NOW() THEN 'backoff' WHEN active THEN 'pending' ELSE 'idle' END,
    CASE WHEN collector_paused THEN pause_reason WHEN codex_turn_state_retry_after > NOW() THEN 'account_cooldown' WHEN active THEN 'queued' ELSE '' END
FROM codex_shared_state_migration;
