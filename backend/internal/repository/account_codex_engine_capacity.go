package repository

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"

	entsql "entgo.io/ent/dialect/sql"
	"entgo.io/ent/dialect/sql/sqljson"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	dbaccount "github.com/Wei-Shaw/sub2api/ent/account"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
)

// ListCodexEngineCapacityCandidates includes paused and zero-capacity accounts:
// capacity recovery must not depend on the account already being schedulable.
func (r *accountRepository) ListCodexEngineCapacityCandidates(ctx context.Context, afterID int64, limit int) ([]service.Account, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	accounts, err := clientFromContext(ctx, r.client).Account.Query().Where(
		dbaccount.IDGT(afterID),
		dbaccount.StatusEQ(service.StatusActive),
		dbaccount.DeletedAtIsNil(),
		dbaccount.ParentAccountIDIsNil(),
		dbaccount.PlatformEQ(service.PlatformOpenAI),
		dbaccount.TypeEQ(service.AccountTypeAPIKey),
		func(s *entsql.Selector) {
			s.Where(sqljson.ValueEQ(dbaccount.FieldExtra, "codex_engine", sqljson.Path(service.OpenAIAPIKeyModeExtraKey)))
		},
	).Order(dbent.Asc(dbaccount.FieldID)).Limit(limit).WithProxy().All(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]service.Account, 0, len(accounts))
	for _, entity := range accounts {
		account := accountEntityToService(entity)
		account.Proxy = proxyEntityToService(entity.Edges.Proxy)
		result = append(result, *account)
	}
	return result, nil
}

// ApplyCodexEngineCapacity only applies the observation to its original row
// snapshot. The capacity state and its scheduler notification commit together.
// A zero observation pauses scheduling while retaining the last positive limit.
func (r *accountRepository) ApplyCodexEngineCapacity(ctx context.Context, expected *service.Account, limit int) (bool, error) {
	if expected == nil || expected.ID <= 0 || !expected.IsCodexEngine() || expected.IsShadow() || limit < 0 {
		return false, nil
	}
	baseCtx := ctx
	client := clientFromContext(ctx, r.client)
	var tx *dbent.Tx
	var err error
	if dbent.TxFromContext(ctx) == nil {
		tx, err = r.client.Tx(ctx)
		if err != nil {
			return false, err
		}
		defer func() { _ = tx.Rollback() }()
		ctx = dbent.NewTxContext(ctx, tx)
		client = tx.Client()
	}
	// A proxy can change its endpoint or credentials without changing its ID.
	// Match it under the same proxy-before-account lock order as proxy edits.
	proxyMatches, err := lockAndMatchProbeProxyIdentity(ctx, client, expected)
	if err != nil || !proxyMatches {
		return false, err
	}
	current, err := lockAccountConfiguration(ctx, client, expected.ID)
	if err != nil {
		return false, err
	}
	if err := readCodexEngineCapacityFields(ctx, client, current); err != nil {
		return false, err
	}
	if current.Status != service.StatusActive || current.IsShadow() || !service.CodexEngineCapacityIdentityMatches(current, expected) {
		return false, nil
	}
	identity := service.CodexEngineCapacityIdentity(current)
	previous, err := json.Marshal(current.Extra[service.CodexEngineCapacitySyncExtraKey])
	if err != nil {
		return false, err
	}
	expectedPrevious, err := json.Marshal(expected.Extra[service.CodexEngineCapacitySyncExtraKey])
	if err != nil {
		return false, err
	}
	// Positive observations do not advance scheduling_revision. Compare the
	// previous capacity as well so concurrent instances cannot overwrite a
	// newer observation using the same configuration and scheduling revision.
	if !bytes.Equal(previous, expectedPrevious) {
		return false, nil
	}
	var saved service.CodexEngineCapacitySyncState
	validSaved := json.Unmarshal(previous, &saved) == nil
	// Pause ownership follows the scheduling revision across Engine endpoint/key
	// edits. Only a fresh response for the current identity may release that pause;
	// the configuration/previous-state checks above reject responses from before
	// the edit. An explicit admin false write advances the revision and revokes it.
	owned := validSaved && saved.OwnsSchedulingPause(current)
	next := map[string]any{"limit": limit, "identity": identity}
	if saved.ObservationID != "" {
		next["observation_id"] = saved.ObservationID
	}
	if owned && limit == 0 {
		next["pause_revision"] = current.SchedulingRevision
	}
	schedulable := current.Schedulable
	if limit == 0 {
		schedulable = false
	} else if owned {
		schedulable = true
	}
	concurrency := current.Concurrency
	if limit > 0 {
		concurrency = limit
	}
	if schedulable != current.Schedulable {
		rows, err := client.QueryContext(ctx, `UPDATE accounts SET schedulable=$2,updated_at=NOW() WHERE id=$1 RETURNING scheduling_revision`, current.ID, schedulable)
		if err != nil {
			return false, err
		}
		var revision int64
		if !rows.Next() {
			err := rows.Err()
			_ = rows.Close()
			if err != nil {
				return false, err
			}
			return false, service.ErrAccountNotFound
		}
		err = rows.Scan(&revision)
		closeErr := rows.Close()
		if err != nil {
			return false, err
		}
		if closeErr != nil {
			return false, closeErr
		}
		if !schedulable {
			next["pause_revision"] = revision
		}
	}
	capacity, err := json.Marshal(next)
	if err != nil {
		return false, err
	}
	changed := schedulable != current.Schedulable || concurrency != current.Concurrency || !bytes.Equal(previous, capacity)
	if !changed {
		return false, nil
	}
	// A value can return to an earlier limit without changing the scheduling
	// revision (20 -> 30 -> 20). Give each committed state change a distinct
	// generation so an observation started at the first 20 cannot pass the old
	// metadata CAS after the second 20. Unchanged and legacy observations above
	// retain their existing generation without an extra write.
	next["observation_id"] = uuid.NewString()
	capacity, err = json.Marshal(next)
	if err != nil {
		return false, err
	}
	if _, err := client.ExecContext(ctx, `UPDATE accounts SET extra=jsonb_set(COALESCE(extra,'{}'::jsonb),'{codex_engine_capacity_sync}',$2::jsonb,true),concurrency=$3,updated_at=NOW() WHERE id=$1`, current.ID, string(capacity), concurrency); err != nil {
		return false, err
	}
	if err := enqueueSchedulerOutbox(ctx, client, service.SchedulerOutboxEventAccountChanged, &expected.ID, nil, nil); err != nil {
		return false, err
	}
	if tx != nil {
		if err := tx.Commit(); err != nil {
			return false, err
		}
		if changed {
			r.syncSchedulerAccountSnapshotDetached(baseCtx, expected.ID)
		}
	} else if changed {
		afterAccountConfigurationCommit(ctx, func() {
			// The committed transaction cannot be reused for the cache reload.
			r.syncSchedulerAccountSnapshotDetached(dbent.NewTxContext(baseCtx, nil), expected.ID)
		})
	}
	return changed, nil
}

// The caller already holds the account row lock. Full admin/background
// snapshots cannot overwrite a fresher capacity observation or concurrency.
func preserveCodexEngineCapacityLocked(ctx context.Context, client *dbent.Client, current, target *service.Account) error {
	delete(target.Extra, service.CodexEngineCapacitySyncExtraKey)
	if !current.IsCodexEngine() || current.IsShadow() || !target.IsCodexEngine() || target.IsShadow() {
		return nil
	}
	if err := readCodexEngineCapacityFields(ctx, client, current); err != nil {
		return err
	}
	if capacity, exists := current.Extra[service.CodexEngineCapacitySyncExtraKey]; exists {
		if target.Extra == nil {
			target.Extra = make(map[string]any)
		}
		// Preserve the last observation and pause ownership until the new Engine
		// identity has a successful observation. Editing credentials alone does not
		// resume traffic. A caller's new concurrency is protected only for the same
		// identity, where it may otherwise be an outdated full-row snapshot.
		target.Extra[service.CodexEngineCapacitySyncExtraKey] = capacity
		if service.CodexEngineCapacityIdentity(current) == service.CodexEngineCapacityIdentity(target) {
			target.Concurrency = current.Concurrency
		}
	}
	return nil
}

func readCodexEngineCapacityFields(ctx context.Context, client *dbent.Client, current *service.Account) error {
	rows, err := client.QueryContext(ctx, `SELECT proxy_id, concurrency FROM accounts WHERE id = $1 AND deleted_at IS NULL`, current.ID)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return err
		}
		return service.ErrAccountNotFound
	}
	var proxy sql.NullInt64
	if err := rows.Scan(&proxy, &current.Concurrency); err != nil {
		return err
	}
	if proxy.Valid {
		current.ProxyID = &proxy.Int64
	}
	return rows.Err()
}

func guardedCodexEngineCapacityExtraExpression(expression string) string {
	remainsEngine := "platform = 'openai' AND type = 'apikey' AND parent_account_id IS NULL" +
		" AND extra ->> 'openai_api_key_mode' = 'codex_engine'" +
		" AND (" + expression + ") ->> 'openai_api_key_mode' = 'codex_engine'"
	return "CASE WHEN " + remainsEngine + " THEN (" + expression + ") ELSE (" + expression + ") - 'codex_engine_capacity_sync' END"
}
