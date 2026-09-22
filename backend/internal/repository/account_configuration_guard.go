package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"reflect"
	"slices"
	"strings"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
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
	filtered := service.StripCodexTurnStateManagedExtra(updates)
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
	if len(ids) > 0 {
		config := service.AccountConfigurationIntentFromContext(ctx, ids[0]).CodexTurnState
		for _, id := range ids[1:] {
			if !reflect.DeepEqual(config, service.AccountConfigurationIntentFromContext(ctx, id).CodexTurnState) {
				config = nil
				break
			}
		}
		if config != nil {
			if filtered == nil {
				filtered = make(map[string]any)
			}
			filtered[service.CodexTurnStateExtraKey] = service.CodexTurnStateConfigJSON(*config)
		}
	}
	return filtered
}

func codexTurnStateOwnerExpression(credentialsExpression string) string {
	return "platform = 'openai' AND type = 'oauth' AND parent_account_id IS NULL AND LOWER(BTRIM(COALESCE((" + credentialsExpression + ") ->> 'auth_mode', ''))) NOT IN ('personalaccesstoken', 'personal_access_token', 'agentidentity') AND LOWER(BTRIM(COALESCE((" + credentialsExpression + ") ->> 'openai_auth_mode', ''))) NOT IN ('personalaccesstoken', 'personal_access_token', 'agentidentity')"
}

// Both expressions are evaluated against the latest row under UPDATE's lock.
// The generation is changed in the same statement as its auth/config identity.
func guardedCodexTurnStateGenerationExpression(extraExpression, credentialsExpression string) string {
	changes := []string{canonicalCodexTurnStateConfigExpression("extra -> 'codex_turn_state'") + " IS DISTINCT FROM " + canonicalCodexTurnStateConfigExpression("("+extraExpression+") -> 'codex_turn_state'")}
	authChanges := make([]string, 0, len(service.CodexTurnStateCredentialKeys))
	if credentialsExpression != "" {
		for _, key := range service.CodexTurnStateCredentialKeys {
			authChanges = append(authChanges, "credentials -> '"+key+"' IS DISTINCT FROM ("+credentialsExpression+") -> '"+key+"'")
		}
		changes = append(changes, authChanges...)
		changes = append(changes, "(COALESCE(extra #>> '{codex_turn_state,account_type}', 'auto') = 'auto' AND credentials -> 'plan_type' IS DISTINCT FROM ("+credentialsExpression+") -> 'plan_type')")
	} else {
		credentialsExpression = "credentials"
	}
	eligible := codexTurnStateOwnerExpression(credentialsExpression)
	previousEligible := codexTurnStateOwnerExpression("credentials")
	changes = append(changes, "NOT ("+previousEligible+")", "COALESCE(BTRIM(extra ->> 'codex_turn_state_generation'), '') = ''")
	generation := "CASE WHEN " + eligible + " AND ((" + extraExpression + ") ? 'codex_turn_state') AND (" + strings.Join(changes, " OR ") + ") THEN COALESCE((" + extraExpression + "), '{}'::jsonb) || jsonb_build_object('codex_turn_state_generation', gen_random_uuid()::text) ELSE (" + extraExpression + ") END"
	epochChanges := append([]string{"NOT (" + previousEligible + ")", "jsonb_typeof(extra -> 'codex_turn_state_credential_epoch') IS DISTINCT FROM 'string'", "COALESCE(BTRIM(extra ->> 'codex_turn_state_credential_epoch'), '') = ''"}, authChanges...)
	// Preserve from the locked row, never a caller-supplied extra snapshot. The
	// credential-only fence exists even when cache configuration is absent.
	epoch := "CASE WHEN " + strings.Join(epochChanges, " OR ") + " THEN gen_random_uuid()::text ELSE extra ->> 'codex_turn_state_credential_epoch' END"
	return "CASE WHEN " + eligible + " THEN COALESCE((" + generation + "), '{}'::jsonb) || jsonb_build_object('codex_turn_state_credential_epoch', " + epoch + ") ELSE (" + extraExpression + ") - 'codex_turn_state_credential_epoch' - 'codex_turn_state_generation' - 'codex_turn_state' END"
}

// Compare legacy singleton and ordered-list settings by meaning, so a normal
// background write or format-only migration cannot revoke a usable cache.
func canonicalCodexTurnStateConfigExpression(config string) string {
	value := "(" + config + ")"
	return "CASE WHEN " + value + " IS NULL THEN NULL ELSE (" + value + " - 'collector_proxy_id' - 'collector_proxy_ids') || jsonb_build_object('collector_proxy_ids', CASE WHEN " + value + " ? 'collector_proxy_ids' THEN " + value + " -> 'collector_proxy_ids' WHEN " + value + " -> 'collector_proxy_id' IS NOT NULL AND " + value + " -> 'collector_proxy_id' <> 'null'::jsonb THEN jsonb_build_array(" + value + " -> 'collector_proxy_id') ELSE '[]'::jsonb END) END"
}

// Register after successful writes and before an owned transaction commits.
// A caller transaction must not notify observers until its actual commit.
func notifyCodexTurnStateAccountAfterCommit(ctx context.Context, ids ...int64) {
	unique := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		if id > 0 {
			unique[id] = struct{}{}
		}
	}
	notify := func() {
		for id := range unique {
			service.NotifyCodexTurnStateAccountConfigurationChanged(id)
		}
	}
	afterAccountConfigurationCommit(ctx, notify)
}

func afterAccountConfigurationCommit(ctx context.Context, notify func()) {
	if tx := dbent.TxFromContext(ctx); tx != nil {
		tx.OnCommit(func(next dbent.Committer) dbent.Committer {
			return dbent.CommitFunc(func(ctx context.Context, tx *dbent.Tx) error {
				if err := next.Commit(ctx, tx); err != nil {
					return err
				}
				notify()
				return nil
			})
		})
		return
	}
	notify()
}

// Collector references use JSONB, so take the same lock used by proxy deletion.
func lockCodexTurnStateCollectorProxy(ctx context.Context, client *dbent.Client, account *service.Account) error {
	config := service.CodexTurnStateConfigForAccount(account)
	ids := service.CodexTurnStateCollectorProxyIDs(config)
	if len(ids) == 0 {
		return nil
	}
	slices.Sort(ids)
	ids = slices.Compact(ids)
	rows, err := client.QueryContext(ctx, `SELECT id FROM proxies WHERE id = ANY($1) AND deleted_at IS NULL ORDER BY id FOR KEY SHARE`, pq.Array(ids))
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	count := 0
	for rows.Next() {
		count++
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if count != len(ids) {
		return service.ErrProxyNotFound
	}
	return nil
}

func guardedAccountExtraExpression(expression string) string {
	return "CASE WHEN " + installationOwnerSQL + " THEN (" + expression + ") - 'openai_installation_rotate_enabled'" +
		" ELSE (" + expression + ") - 'openai_installation_rotate_enabled' - 'openai_pinned_installation_id' - 'openai_installation_pin_enabled' END"
}

// SQL UPDATE evaluates these expressions against the locked, latest row, so an
// asynchronous token/credential snapshot cannot restore a stale environment UA.
func guardedAccountCredentialsExpression(expression string, allowModeChange ...bool) string {
	protectedKeys := service.OpenAIOAuthProviderCredentialKeys()
	quoted := make([]string, 0, len(protectedKeys))
	for _, key := range protectedKeys {
		quoted = append(quoted, "'"+key+"'")
	}
	keys := "ARRAY[" + strings.Join(quoted, ",") + "]::text[]"
	predicate := codexTurnStateOwnerExpression("credentials")
	if len(allowModeChange) > 0 && allowModeChange[0] {
		predicate += " AND " + codexTurnStateOwnerExpression(expression)
	}
	protected := "CASE WHEN " + predicate + " THEN ((" + expression + ") - " + keys + ") || (SELECT COALESCE(jsonb_object_agg(key,value),'{}'::jsonb) FROM jsonb_each(COALESCE(credentials,'{}'::jsonb)) WHERE key=ANY(" + keys + ")) ELSE (" + expression + ") END"
	return "CASE WHEN platform = 'openai' THEN ((" + protected + ") - 'user_agent') || " +
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
	if service.IsOpenAIOAuthOSProfileOwner(current) {
		profiles, _, ensureErr := ensureOpenAIOAuthOSProfilesLocked(ctx, client, current)
		if ensureErr != nil {
			return "", ensureErr
		}
		previous := service.CloneOpenAIOAuthOSProfiles(profiles)
		profile := profiles.Profiles[profiles.DefaultOS]
		profile.InstallationID = generatedID
		profiles.Profiles[profiles.DefaultOS] = profile
		_, err = saveOpenAIOAuthOSProfilesLocked(ctx, client, current, previous, profiles)
	} else {
		_, err = client.ExecContext(ctx, `UPDATE accounts SET extra = jsonb_set(
			COALESCE(extra, '{}'::jsonb) - 'openai_installation_rotate_enabled',
			'{openai_pinned_installation_id}', to_jsonb($2::text), true), updated_at = NOW()
			WHERE id = $1 AND deleted_at IS NULL`, id, generatedID)
	}
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
