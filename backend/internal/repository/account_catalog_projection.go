package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// Explicit allowlists ensure a catalog lookup never decodes tokens or credentials
// belonging to accounts which are not selected as the actual directory source.
var catalogCredentialFields = []string{"model_mapping", "compact_model_mapping", "base_url", "oauth_type", "plan_type", "auth_mode", "openai_auth_mode", "openai_capabilities", "project_id"}
var catalogExtraFields = []string{"openai_passthrough", "openai_passthrough_model_filter", "openai_oauth_passthrough", "openai_responses_mode", "openai_responses_supported", service.OpenAIAPIKeyModeExtraKey, service.CodexEngineModelsSyncExtraKey, service.OpenAIDaybreakBlueEnabledKey, service.OpenAIDaybreakRedEnabledKey, service.UpstreamModelMetadataExtraKey}

func catalogJSONProjection(column string, keys []string) string {
	pairs := make([]string, 0, len(keys)*2)
	for _, key := range keys {
		pairs = append(pairs, "'"+key+"'", column+"->'"+key+"'")
	}
	return "jsonb_strip_nulls(jsonb_build_object(" + strings.Join(pairs, ",") + "))"
}

var catalogAccountProjectionSQL = fmt.Sprintf(`SELECT a.id,a.platform,a.type,a.status,a.schedulable,a.priority,a.last_used_at,a.expires_at,a.auto_pause_on_expired,a.rate_limit_reset_at,a.overload_until,a.temp_unschedulable_until,a.parent_account_id,a.updated_at,%s,%s FROM account_groups ag JOIN accounts a ON a.id=ag.account_id WHERE ag.group_id=$1 AND a.deleted_at IS NULL AND a.status='active' AND a.schedulable=true ORDER BY ag.priority,a.priority,a.id`, catalogJSONProjection("a.credentials", catalogCredentialFields), catalogJSONProjection("a.extra", catalogExtraFields))

func (r *accountRepository) ListCatalogAccountProjections(ctx context.Context, groupID int64) ([]service.Account, error) {
	if r == nil || r.sql == nil {
		return nil, fmt.Errorf("account repository SQL executor is not configured")
	}
	rows, err := r.sql.QueryContext(ctx, catalogAccountProjectionSQL, groupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	accounts := make([]service.Account, 0)
	for rows.Next() {
		var a service.Account
		var credentials, extra []byte
		if err := rows.Scan(&a.ID, &a.Platform, &a.Type, &a.Status, &a.Schedulable, &a.Priority, &a.LastUsedAt, &a.ExpiresAt, &a.AutoPauseOnExpired, &a.RateLimitResetAt, &a.OverloadUntil, &a.TempUnschedulableUntil, &a.ParentAccountID, &a.UpdatedAt, &credentials, &extra); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(credentials, &a.Credentials); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(extra, &a.Extra); err != nil {
			return nil, err
		}
		accounts = append(accounts, a)
	}
	return accounts, rows.Err()
}
