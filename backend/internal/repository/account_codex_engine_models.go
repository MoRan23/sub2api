package repository

import (
	"bytes"
	"context"
	"encoding/json"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
)

// ApplyCodexEngineModels commits only observations made against the same
// transport identity, model configuration and model observation generation.
// Scheduling changes (including the concurrent capacity observation) are unrelated.
func (r *accountRepository) ApplyCodexEngineModels(ctx context.Context, expected *service.Account, models []string) (bool, error) {
	if expected == nil || expected.ID <= 0 || !expected.IsCodexEngine() || expected.IsShadow() {
		return false, nil
	}
	baseCtx := ctx
	client := clientFromContext(ctx, r.client)
	var tx *dbent.Tx
	if dbent.TxFromContext(ctx) == nil {
		var err error
		tx, err = r.client.Tx(ctx)
		if err != nil {
			return false, err
		}
		defer func() { _ = tx.Rollback() }()
		ctx = dbent.NewTxContext(ctx, tx)
		client = tx.Client()
	}
	matches, err := lockAndMatchCodexEngineProxyIdentity(ctx, client, expected)
	if err != nil || !matches {
		return false, err
	}
	current, err := lockAccountConfiguration(ctx, client, expected.ID)
	if err != nil {
		return false, err
	}
	if err := readCodexEngineCapacityFields(ctx, client, current); err != nil {
		return false, err
	}
	if !current.IsCodexEngine() || current.IsShadow() || current.Status != service.StatusActive || service.CodexEngineCapacityIdentity(current) != service.CodexEngineCapacityIdentity(expected) {
		return false, nil
	}
	if !sameEngineModelJSON(current.Extra[codexEngineSourceRevisionKey], expected.Extra[codexEngineSourceRevisionKey]) {
		return false, nil
	}
	if !sameEngineModelJSON(current.Extra[service.CodexEngineModelsSyncExtraKey], expected.Extra[service.CodexEngineModelsSyncExtraKey]) || !sameEngineModelJSON(current.Credentials["model_mapping"], expected.Credentials["model_mapping"]) {
		return false, nil
	}
	identity := service.CodexEngineCapacityIdentity(current)
	var saved service.CodexEngineModelsSyncState
	raw, err := json.Marshal(current.Extra[service.CodexEngineModelsSyncExtraKey])
	if err != nil {
		return false, err
	}
	if json.Unmarshal(raw, &saved) == nil && saved.Known && saved.Identity == identity && saved.ModelCount == len(models) && engineModelMappingMatches(current.Credentials["model_mapping"], models) {
		return false, nil
	}
	mapping := make(map[string]string, len(models))
	for _, model := range models {
		mapping[model] = model
	}
	state := service.CodexEngineModelsSyncState{Identity: identity, Known: true, ModelCount: len(mapping), ObservationID: uuid.NewString()}
	stateJSON, err := json.Marshal(state)
	if err != nil {
		return false, err
	}
	mappingJSON, err := json.Marshal(mapping)
	if err != nil {
		return false, err
	}
	if _, err := client.ExecContext(ctx, `UPDATE accounts SET credentials=jsonb_set(COALESCE(credentials,'{}'::jsonb),'{model_mapping}',$2::jsonb,true),extra=jsonb_set(COALESCE(extra,'{}'::jsonb),'{codex_engine_models_sync}',$3::jsonb,true),updated_at=NOW() WHERE id=$1`, current.ID, string(mappingJSON), string(stateJSON)); err != nil {
		return false, err
	}
	if err := enqueueSchedulerOutbox(ctx, client, service.SchedulerOutboxEventAccountChanged, &expected.ID, nil, nil); err != nil {
		return false, err
	}
	if tx != nil {
		if err := tx.Commit(); err != nil {
			return false, err
		}
		r.syncSchedulerAccountSnapshotDetached(baseCtx, expected.ID)
	} else {
		afterAccountConfigurationCommit(ctx, func() { r.syncSchedulerAccountSnapshotDetached(dbent.NewTxContext(baseCtx, nil), expected.ID) })
	}
	return true, nil
}

func sameEngineModelJSON(a, b any) bool {
	left, err := json.Marshal(a)
	if err != nil {
		return false
	}
	right, err := json.Marshal(b)
	return err == nil && bytes.Equal(left, right)
}

// The row lock held by updateLockedAccount makes preservation and explicit
// administrator invalidation atomic with all other account configuration writes.
func preserveCodexEngineModelsLocked(ctx context.Context, current, target *service.Account) {
	delete(target.Extra, service.CodexEngineModelsSyncExtraKey)
	if !current.IsCodexEngine() || current.IsShadow() || !target.IsCodexEngine() || target.IsShadow() {
		return
	}
	// PreserveAccountConfiguration has already given this update independent
	// credentials/extra maps; only immutable stored values are shared here.
	if target.Credentials == nil {
		target.Credentials = make(map[string]any)
	}
	if target.Extra == nil {
		target.Extra = make(map[string]any)
	}
	explicit := service.AccountConfigurationIntentFromContext(ctx, current.ID).ModelMapping
	if explicit && !sameEngineModelJSON(current.Credentials["model_mapping"], target.Credentials["model_mapping"]) {
		// Even before the first observation, manual A -> B -> A must invalidate
		// work which began at the first A. This state is not capability evidence.
		target.Extra[service.CodexEngineModelsSyncExtraKey] = service.CodexEngineModelsSyncState{ObservationID: uuid.NewString()}
		return
	}
	if !explicit {
		delete(target.Credentials, "model_mapping")
		if mapping, exists := current.Credentials["model_mapping"]; exists {
			target.Credentials["model_mapping"] = mapping
		}
	}
	if state, exists := current.Extra[service.CodexEngineModelsSyncExtraKey]; exists {
		target.Extra[service.CodexEngineModelsSyncExtraKey] = state
	}
}

const codexEngineModelOwnerSQL = "platform = 'openai' AND type = 'apikey' AND parent_account_id IS NULL AND extra ->> 'openai_api_key_mode' = 'codex_engine'"

func guardedCodexEngineModelsCredentialsExpression(expression string, explicit bool) string {
	if explicit {
		return expression
	}
	return "CASE WHEN " + codexEngineModelOwnerSQL + " THEN ((" + expression + ") - 'model_mapping') || CASE WHEN credentials ? 'model_mapping' THEN jsonb_build_object('model_mapping', credentials -> 'model_mapping') ELSE '{}'::jsonb END ELSE (" + expression + ") END"
}

func codexEngineModelsEditedExtraExpression(expression, credentials string) string {
	// UUID is generated locally once for the SQL operation; each target account
	// compares its own observation, so sharing this value across a batch is safe.
	return "CASE WHEN " + codexEngineModelOwnerSQL + " AND credentials -> 'model_mapping' IS DISTINCT FROM (" + credentials + ") -> 'model_mapping' THEN jsonb_set(COALESCE((" + expression + "),'{}'::jsonb),'{codex_engine_models_sync}',jsonb_build_object('known',false,'model_count',0,'identity','','observation_id','" + uuid.NewString() + "'),true) ELSE (" + expression + ") END"
}

func codexEngineModelsExplicitIntent(ctx context.Context, ids []int64) bool {
	if len(ids) == 0 {
		return false
	}
	for _, id := range ids {
		if !service.AccountConfigurationIntentFromContext(ctx, id).ModelMapping {
			return false
		}
	}
	return true
}

// Current is decoded from the locked database row; compare without allocating
// a second model map on the unchanged periodic observation path.
func engineModelMappingMatches(value any, models []string) bool {
	current, ok := value.(map[string]any)
	if !ok || len(current) != len(models) {
		return false
	}
	for _, model := range models {
		if current[model] != model {
			return false
		}
	}
	return true
}
