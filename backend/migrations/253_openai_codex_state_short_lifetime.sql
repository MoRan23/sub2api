-- Turn-state expiry is a local policy, measured from the envelope's issuance.
-- Retain accepted-token history so shortened/expired caches can still establish
-- renewal demand. Never extend an earlier stored expiry or erase rate limits.
-- Account-wide retry_after is untouched, including legacy unknown-origin limits.
-- Collecting rows can retain an old rate-limit label; their next time is a
-- crash-recovery reservation, so it is cleared while the account limit survives.
WITH policy AS (
    SELECT owner_account_id, os_family, model,
        CASE WHEN issued_at IS NOT NULL AND expires_at IS NOT NULL
             THEN LEAST(expires_at, issued_at + INTERVAL '240 seconds')
             ELSE expires_at END AS effective_expiry,
        collector_paused OR (last_error IN ('collector_rate_limited', 'account_cooldown')
            AND collection_status <> 'collecting' AND collector_attempt_id IS NULL) AS preserve_retry
    FROM openai_codex_state
), revised AS (
    SELECT s.owner_account_id, s.os_family, s.model, p.effective_expiry, p.preserve_retry,
        s.collection_reason = 'collector_proxy_changed' AND s.encrypted_token <> '' AND s.shape = 'target'
            AND s.issued_at <= NOW() + INTERVAL '30 seconds'
            AND p.effective_expiry > s.issued_at AND p.effective_expiry > NOW() AS waits_proxy_expiry,
        s.demand_reason <> '' OR (
            s.encrypted_token <> '' AND s.shape = 'target'
            AND p.effective_expiry <= NOW() + INTERVAL '30 seconds'
            AND s.last_business_at >= NOW() - INTERVAL '30 minutes'
            AND NOT (s.collection_reason = 'collector_proxy_changed'
                AND s.issued_at <= NOW() + INTERVAL '30 seconds'
                AND p.effective_expiry > s.issued_at AND p.effective_expiry > NOW())
        ) AS has_demand,
        CASE
            WHEN p.preserve_retry THEN s.next_collect_at
            WHEN s.collection_status = 'collecting' OR s.collector_attempt_id IS NOT NULL THEN NULL
            WHEN s.last_error IN ('no_target_state', 'target_still_expiring', 'model_mismatch') AND s.next_collect_at IS NOT NULL
                THEN LEAST(s.next_collect_at, NOW() + INTERVAL '5 seconds')
            ELSE NULL
        END AS next_attempt
    FROM openai_codex_state s JOIN policy p USING (owner_account_id, os_family, model)
)
UPDATE openai_codex_state s SET
    expires_at = revised.effective_expiry,
    version = s.version + 1,
    collector_attempt_id = NULL,
    demand_reason = CASE WHEN s.demand_reason <> '' THEN s.demand_reason
                         WHEN revised.has_demand THEN 'expiring' ELSE '' END,
    demand_at = CASE WHEN s.demand_reason <> '' THEN s.demand_at
                     WHEN revised.has_demand THEN NOW() ELSE s.demand_at END,
    refresh_reason = CASE WHEN s.demand_reason = '' AND revised.has_demand THEN 'expiring' ELSE s.refresh_reason END,
    next_collect_at = revised.next_attempt,
    collection_status = CASE WHEN s.collector_paused THEN 'paused'
                             WHEN revised.next_attempt > NOW() THEN 'backoff'
                             WHEN revised.waits_proxy_expiry THEN 'idle'
                             WHEN revised.has_demand THEN 'pending' ELSE 'idle' END,
    collection_reason = CASE WHEN s.collector_paused OR revised.next_attempt > NOW() THEN s.last_error
                             WHEN revised.waits_proxy_expiry THEN 'collector_proxy_changed'
                             WHEN revised.has_demand THEN 'queued'
                             ELSE '' END,
    updated_at = NOW()
FROM revised WHERE s.owner_account_id = revised.owner_account_id
    AND s.os_family = revised.os_family AND s.model = revised.model;
