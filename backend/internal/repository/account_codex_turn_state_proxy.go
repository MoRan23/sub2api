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
	_, err := client.ExecContext(ctx, `WITH eligible AS (
		SELECT owner_account_id, model,
			COALESCE(encrypted_token <> '' AND shape = 'target' AND token_length = $4 AND cipher_blocks = $5
			AND issued_at IS NOT NULL AND issued_at <= NOW() + INTERVAL '30 seconds'
			AND expires_at > NOW() AND expires_at = issued_at + ($6 * INTERVAL '1 second'), FALSE) AS valid_target
		FROM openai_codex_state WHERE owner_account_id = $1 AND generation = $2 FOR UPDATE
	)
	UPDATE openai_codex_state s SET generation = $3, version = s.version + 1,
		encrypted_token = CASE WHEN eligible.valid_target THEN s.encrypted_token ELSE '' END,
		demand_reason = CASE WHEN eligible.valid_target THEN '' ELSE s.demand_reason END,
		demand_at = CASE WHEN eligible.valid_target THEN NULL ELSE s.demand_at END,
		refresh_reason = CASE WHEN eligible.valid_target THEN '' ELSE s.refresh_reason END,
		collector_paused = FALSE,
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
	FROM eligible WHERE s.owner_account_id = eligible.owner_account_id AND s.model = eligible.model
		AND s.generation = $2 AND (eligible.valid_target OR s.demand_reason <> '')`,
		current.ID, service.CodexTurnStateGenerationForAccount(current), service.CodexTurnStateGenerationForAccount(target),
		length, blocks, int64(service.CodexTurnStateLifetime/time.Second))
	return err
}
