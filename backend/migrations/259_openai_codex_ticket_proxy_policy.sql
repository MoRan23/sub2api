-- Routing preference does not change a ticket bundle's identity or admission.
-- Keep existing bundles available when an administrator toggles the preference,
-- including the first write that normalizes the default for legacy accounts.
-- All other configuration and account proxy changes retain their existing fence.
CREATE OR REPLACE FUNCTION fence_openai_oauth_os_credential_configuration() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    old_config JSONB := OLD.extra->'codex_turn_state';
    new_config JSONB := NEW.extra->'codex_turn_state';
BEGIN
    old_config := (old_config - 'collector_proxy_id' - 'collector_proxy_ids' - 'use_ticket_proxy') || jsonb_build_object('collector_proxy_ids',
        CASE WHEN old_config ? 'collector_proxy_ids' THEN old_config->'collector_proxy_ids'
             WHEN old_config->'collector_proxy_id' IS NOT NULL AND old_config->'collector_proxy_id'<>'null'::jsonb THEN jsonb_build_array(old_config->'collector_proxy_id') ELSE '[]'::jsonb END);
    new_config := (new_config - 'collector_proxy_id' - 'collector_proxy_ids' - 'use_ticket_proxy') || jsonb_build_object('collector_proxy_ids',
        CASE WHEN new_config ? 'collector_proxy_ids' THEN new_config->'collector_proxy_ids'
             WHEN new_config->'collector_proxy_id' IS NOT NULL AND new_config->'collector_proxy_id'<>'null'::jsonb THEN jsonb_build_array(new_config->'collector_proxy_id') ELSE '[]'::jsonb END);
    IF old_config IS DISTINCT FROM new_config OR OLD.proxy_id IS DISTINCT FROM NEW.proxy_id THEN
        UPDATE account_openai_oauth_credentials SET previous_state_generation=state_generation,state_generation=gen_random_uuid(),updated_at=NOW() WHERE account_id=NEW.id;
        UPDATE account_openai_oauth_os_credentials s SET previous_state_generation=s.state_generation,state_generation=c.state_generation,updated_at=NOW()
            FROM account_openai_oauth_credentials c WHERE c.account_id=NEW.id AND s.account_id=c.account_id;
    END IF;
    RETURN NEW;
END;
$$;
