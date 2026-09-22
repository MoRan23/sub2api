package repository

import (
	"context"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

// The caller holds the account configuration row lock and uses its transaction.
// Advancing generation still fences every result from the previous destination;
// only accepted live caches and already established demands survive the change.
func preserveCodexTurnStateOnCollectorProxyChange(ctx context.Context, client *dbent.Client, current, target *service.Account) error {
	if !service.CodexTurnStateCollectorProxyOnlyChanged(current, target) {
		return nil
	}
	length, blocks := 292, 10
	if service.CodexTurnStateAccountTypeForAccount(target) == "team_business" {
		length, blocks = 332, 12
	}
	var firstProxyID any
	if ids := service.CodexTurnStateCollectorProxyIDs(service.CodexTurnStateConfigForAccount(target)); len(ids) > 0 {
		firstProxyID = ids[0]
	}
	_, err := client.ExecContext(ctx, `WITH eligible AS (
		SELECT s.owner_account_id, s.os_family, s.model, s.generation AS previous_generation,
			c.state_generation::text AS next_generation,
			COALESCE(s.encrypted_token <> '' AND s.shape = 'target' AND s.token_length = $2 AND s.cipher_blocks = $3
			AND s.issued_at IS NOT NULL AND s.issued_at <= NOW() + INTERVAL '30 seconds'
			AND s.expires_at > NOW() AND s.expires_at = s.issued_at + ($4 * INTERVAL '1 second'), FALSE) AS valid_target
		FROM openai_codex_state s JOIN account_openai_oauth_os_credentials c
		ON c.account_id=s.owner_account_id AND c.os_family=s.os_family
		WHERE s.owner_account_id = $1 AND c.status='authorized'
		AND s.generation=c.previous_state_generation::text FOR UPDATE OF s
	)
	UPDATE openai_codex_state s SET generation = eligible.next_generation, version = s.version + 1,
		encrypted_token = CASE WHEN eligible.valid_target THEN s.encrypted_token ELSE '' END,
		demand_reason = CASE WHEN eligible.valid_target THEN '' ELSE s.demand_reason END,
		demand_at = CASE WHEN eligible.valid_target THEN NULL ELSE s.demand_at END,
		refresh_reason = CASE WHEN eligible.valid_target THEN '' ELSE s.refresh_reason END,
		collector_paused = FALSE,
		collector_proxy_id = $5, collector_extended_count = 0, collector_attempt_id = NULL,
		next_collect_at = CASE
			WHEN s.collector_paused THEN NULL
			WHEN NOT eligible.valid_target OR (s.last_error IN ('account_cooldown','collector_rate_limited') AND s.next_collect_at > NOW()) THEN s.next_collect_at
			ELSE NULL END,
		last_error = CASE
			WHEN s.collector_paused THEN ''
			WHEN NOT eligible.valid_target OR (s.last_error IN ('account_cooldown','collector_rate_limited') AND s.next_collect_at > NOW()) THEN s.last_error
			ELSE '' END,
		collection_status = CASE WHEN eligible.valid_target THEN 'idle' ELSE 'pending' END,
		collection_reason = CASE WHEN eligible.valid_target THEN 'collector_proxy_changed' ELSE 'queued' END,
		updated_at = NOW()
	FROM eligible WHERE s.owner_account_id = eligible.owner_account_id AND s.os_family=eligible.os_family AND s.model = eligible.model
		AND s.generation = eligible.previous_generation AND (eligible.valid_target OR s.demand_reason <> '')`,
		current.ID, length, blocks, int64(service.CodexTurnStateLifetime/time.Second), firstProxyID)
	return err
}
