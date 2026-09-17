package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"maps"
	"reflect"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

const installationOwnerSQL = "platform = 'openai' AND type IN ('oauth', 'setup-token') AND parent_account_id IS NULL"

func lockAccountConfiguration(ctx context.Context, client *dbent.Client, id int64) (*service.Account, error) {
	rows, err := client.QueryContext(ctx, `SELECT platform, type, parent_account_id, credentials, extra
		FROM accounts WHERE id = $1 AND deleted_at IS NULL FOR NO KEY UPDATE`, id)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, err
		}
		return nil, service.ErrAccountNotFound
	}
	current := &service.Account{ID: id}
	var parent sql.NullInt64
	var credentials, extra []byte
	if err := rows.Scan(&current.Platform, &current.Type, &parent, &credentials, &extra); err != nil {
		return nil, err
	}
	if parent.Valid {
		current.ParentAccountID = &parent.Int64
	}
	if len(credentials) > 0 {
		if err := json.Unmarshal(credentials, &current.Credentials); err != nil {
			return nil, err
		}
	}
	if len(extra) > 0 {
		if err := json.Unmarshal(extra, &current.Extra); err != nil {
			return nil, err
		}
	}
	return current, rows.Err()
}

// Extra patches have no implicit ownership: background snapshots cannot write
// managed fields. Admin intent is separate from, and scoped more narrowly than,
// the map being saved. Every target must have the same explicit intent.
func accountConfigurationExtraPatch(ctx context.Context, ids []int64, updates map[string]any) map[string]any {
	filtered := maps.Clone(updates)
	delete(filtered, "openai_pinned_installation_id")
	delete(filtered, "openai_installation_rotate_enabled")
	for _, key := range []string{"openai_installation_pin_enabled", "enable_tls_fingerprint", "tls_fingerprint_profile_id"} {
		delete(filtered, key)
		if len(ids) == 0 {
			continue
		}
		intent := service.AccountConfigurationIntentFromContext(ctx, ids[0])
		value, explicit := intent.Extra[key]
		for _, id := range ids[1:] {
			other, exists := service.AccountConfigurationIntentFromContext(ctx, id).Extra[key]
			explicit = explicit && exists && reflect.DeepEqual(value, other)
		}
		if explicit {
			if filtered == nil {
				filtered = make(map[string]any)
			}
			filtered[key] = value
		}
	}
	return filtered
}

func guardedAccountExtraExpression(expression string) string {
	return "CASE WHEN " + installationOwnerSQL + " THEN (" + expression + ") - 'openai_installation_rotate_enabled'" +
		" ELSE (" + expression + ") - 'openai_installation_rotate_enabled' - 'openai_pinned_installation_id' - 'openai_installation_pin_enabled' END"
}

// SQL UPDATE evaluates these expressions against the locked, latest row, so an
// asynchronous token/credential snapshot cannot restore a stale environment UA.
func guardedAccountCredentialsExpression(expression string) string {
	return "CASE WHEN platform = 'openai' THEN ((" + expression + ") - 'user_agent') || " +
		"CASE WHEN parent_account_id IS NULL AND credentials ? 'user_agent' THEN jsonb_build_object('user_agent', credentials -> 'user_agent') ELSE '{}'::jsonb END" +
		" ELSE (" + expression + ") END"
}

// RegenerateOpenAIInstallationID rechecks eligibility and pin state under the
// same lock as its write; no stale admin read can bypass a concurrent disable.
func (r *accountRepository) RegenerateOpenAIInstallationID(ctx context.Context, id int64, generatedID string) (string, error) {
	baseCtx := ctx
	contextTx := dbent.TxFromContext(ctx)
	client := clientFromContext(ctx, r.client)
	var tx *dbent.Tx
	if contextTx == nil {
		var err error
		tx, err = r.client.Tx(ctx)
		if err != nil {
			return "", err
		}
		defer func() { _ = tx.Rollback() }()
		ctx = dbent.NewTxContext(ctx, tx)
		client = tx.Client()
	}
	current, err := lockAccountConfiguration(ctx, client, id)
	if err != nil {
		return "", err
	}
	if current.Platform != service.PlatformOpenAI || current.ParentAccountID != nil ||
		(current.Type != service.AccountTypeOAuth && current.Type != service.AccountTypeSetupToken) {
		return "", infraerrors.BadRequest("OPENAI_INSTALLATION_REGENERATE_UNSUPPORTED", "only non-shadow OpenAI Codex credential accounts support installation_id regeneration")
	}
	if !current.IsOpenAIInstallationPinEnabled() {
		return "", infraerrors.BadRequest("OPENAI_INSTALLATION_REGENERATE_PIN_DISABLED", "enable fixed installation_id and save the account before regenerating")
	}
	_, err = client.ExecContext(ctx, `UPDATE accounts SET extra = jsonb_set(
		COALESCE(extra, '{}'::jsonb) - 'openai_installation_rotate_enabled',
		'{openai_pinned_installation_id}', to_jsonb($2::text), true), updated_at = NOW()
		WHERE id = $1 AND deleted_at IS NULL`, id, generatedID)
	if err != nil {
		return "", err
	}
	if err := enqueueSchedulerOutbox(ctx, client, service.SchedulerOutboxEventAccountChanged, &id, nil, nil); err != nil {
		return "", err
	}
	if tx != nil {
		if err := tx.Commit(); err != nil {
			return "", err
		}
		r.syncSchedulerAccountSnapshot(baseCtx, id)
	}
	return generatedID, nil
}
