package repository

import (
	"context"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
)

// Independent from successful capacity/model observations and scheduling writes.
// Keep this fence across mode changes so Engine -> generic -> Engine cannot ABA.
const codexEngineSourceRevisionKey = service.CodexEngineSourceRevisionExtraKey

func preserveCodexEngineSourceRevision(current, target *service.Account) {
	delete(target.Extra, codexEngineSourceRevisionKey)
	if target.Extra == nil {
		target.Extra = make(map[string]any)
	}
	if value, ok := current.Extra[codexEngineSourceRevisionKey]; ok {
		target.Extra[codexEngineSourceRevisionKey] = value
	}
	if (current.IsCodexEngine() || target.IsCodexEngine()) && service.CodexEngineCapacityIdentity(current) != service.CodexEngineCapacityIdentity(target) {
		target.Extra[codexEngineSourceRevisionKey] = uuid.NewString()
	}
}

func guardedCodexEngineSourceExpression(expression, credentials, proxy string) string {
	return "CASE WHEN (" + codexEngineModelOwnerSQL + " OR (platform='openai' AND type='apikey' AND (" + expression + ") ->> 'openai_api_key_mode'='codex_engine')) AND (credentials -> 'api_key' IS DISTINCT FROM (" + credentials + ") -> 'api_key' OR credentials -> 'base_url' IS DISTINCT FROM (" + credentials + ") -> 'base_url' OR extra -> 'openai_api_key_mode' IS DISTINCT FROM (" + expression + ") -> 'openai_api_key_mode' OR proxy_id IS DISTINCT FROM (" + proxy + ")) THEN jsonb_set(COALESCE((" + expression + "),'{}'::jsonb),'{codex_engine_source_revision}',to_jsonb('" + uuid.NewString() + "'::text),true) ELSE (" + expression + ") END"
}

func lockAndMatchCodexEngineProxyIdentity(ctx context.Context, client *dbent.Client, account *service.Account) (bool, error) {
	matched, err := lockAndMatchProbeProxyIdentity(ctx, client, account)
	if err != nil || !matched || account.ProxyID == nil {
		return matched, err
	}
	// The existing FOR SHARE lock above fences proxy edits through account commit.
	// UpdatedAt additionally rejects endpoint/credentials/status A -> B -> A.
	rows, err := client.QueryContext(ctx, "SELECT updated_at FROM proxies WHERE id=$1 AND deleted_at IS NULL", *account.ProxyID)
	if err != nil {
		return false, err
	}
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		return false, rows.Err()
	}
	var updated time.Time
	if err := rows.Scan(&updated); err != nil {
		return false, err
	}
	return account.Proxy != nil && updated.Equal(account.Proxy.UpdatedAt), rows.Err()
}
