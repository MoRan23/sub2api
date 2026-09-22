-- Legacy state belongs only to the credential's original default OS. Never
-- clone opaque state or leases into the other two authorization slots.
-- Account-wide cooldown deliberately survives slot reauthorization/revocation
-- and state generation changes. Only a later deadline may replace it.
ALTER TABLE accounts ADD COLUMN codex_turn_state_retry_after TIMESTAMPTZ;
UPDATE accounts a SET codex_turn_state_retry_after = cooldown.until
FROM (SELECT owner_account_id, MAX(next_collect_at) AS until FROM openai_codex_state
      WHERE last_error IN ('collector_rate_limited','account_cooldown')
      GROUP BY owner_account_id) cooldown WHERE cooldown.owner_account_id=a.id;

ALTER TABLE openai_codex_state ADD COLUMN os_family TEXT;
ALTER TABLE openai_codex_state_business_leases ADD COLUMN os_family TEXT;

UPDATE openai_codex_state s SET os_family = COALESCE(
    (SELECT p.os_family FROM account_openai_oauth_os_profiles p
     WHERE p.account_id=s.owner_account_id AND p.is_default), 'windows');
UPDATE openai_codex_state_business_leases l SET os_family=s.os_family
    FROM openai_codex_state s WHERE s.owner_account_id=l.owner_account_id AND s.model=l.model;

ALTER TABLE openai_codex_state_business_leases
    DROP CONSTRAINT openai_codex_state_business_leases_owner_account_id_model_fkey,
    DROP CONSTRAINT openai_codex_state_business_leases_pkey;
ALTER TABLE openai_codex_state DROP CONSTRAINT openai_codex_state_pkey;
ALTER TABLE openai_codex_state ALTER COLUMN os_family SET NOT NULL,
    ADD CHECK (os_family IN ('windows','macos','linux')),
    ADD PRIMARY KEY (owner_account_id,os_family,model);
ALTER TABLE openai_codex_state_business_leases ALTER COLUMN os_family SET NOT NULL,
    ADD CHECK (os_family IN ('windows','macos','linux')),
    ADD PRIMARY KEY (owner_account_id,os_family,model,generation,attempt_id),
    ADD FOREIGN KEY (owner_account_id,os_family,model)
        REFERENCES openai_codex_state(owner_account_id,os_family,model) ON DELETE CASCADE ON UPDATE CASCADE;

-- The slot migration is authoritative about the persisted credential epoch.
-- Rebind only the original state and its matching leases to that slot's fence.
UPDATE openai_codex_state_business_leases l SET generation=c.state_generation::text
    FROM openai_codex_state s, account_openai_oauth_os_credentials c, accounts a
    WHERE l.owner_account_id=s.owner_account_id AND l.os_family=s.os_family AND l.model=s.model
      AND l.generation=s.generation AND c.account_id=s.owner_account_id AND c.os_family=s.os_family
      AND c.source='legacy_migration' AND a.id=s.owner_account_id
      AND s.generation=a.extra->>'codex_turn_state_generation';
UPDATE openai_codex_state s SET generation=c.state_generation::text
    FROM account_openai_oauth_os_credentials c, accounts a
    WHERE c.account_id=s.owner_account_id AND c.os_family=s.os_family AND c.source='legacy_migration'
      AND a.id=s.owner_account_id AND s.generation=a.extra->>'codex_turn_state_generation';
