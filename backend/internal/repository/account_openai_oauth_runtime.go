package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

// MutateOpenAIOAuthAccountStateIfUnchanged applies runtime penalties and recovery
// to the business account only while the request's shared grant is unchanged.
// It deliberately does not change credentials or the private grant's status.
func (r *accountRepository) MutateOpenAIOAuthAccountStateIfUnchanged(
	ctx context.Context,
	accountID int64,
	snapshot service.OpenAIOAuthAccountStateSnapshot,
	change service.OpenAIOAuthAccountStateChange,
) (*service.OpenAIOAuthAccountStateResult, error) {
	result := &service.OpenAIOAuthAccountStateResult{}
	if accountID <= 0 || snapshot.OwnerAccountID <= 0 || snapshot.AuthorizationGeneration == "" || snapshot.CredentialRevision <= 0 {
		return result, nil
	}
	if r == nil || r.client == nil {
		return nil, errors.New("account repository client is not configured")
	}
	switch change.Kind {
	case service.OpenAIOAuthAccountStateError, service.OpenAIOAuthAccountStateCooldown,
		service.OpenAIOAuthAccountStateRecover, service.OpenAIOAuthAccountStateClearTemp,
		service.OpenAIOAuthAccountStateClearError:
	case service.OpenAIOAuthAccountStateModelCooldown:
		if change.ModelKey == "" {
			return result, nil
		}
	default:
		return nil, fmt.Errorf("unsupported OpenAI OAuth account state change %q", change.Kind)
	}

	baseCtx := ctx
	client := clientFromContext(ctx, r.client)
	var ownedTx *dbent.Tx
	if dbent.TxFromContext(ctx) == nil {
		var err error
		ownedTx, err = r.client.Tx(ctx)
		if err != nil && !errors.Is(err, dbent.ErrTxStarted) {
			return nil, err
		}
		if ownedTx != nil {
			defer ownedTx.Rollback()
			ctx = dbent.NewTxContext(ctx, ownedTx)
			client = ownedTx.Client()
		}
	}

	// Refresh and authorization writers lock the owner account before the shared
	// grant. Keep that order, including when the affected account is a shadow.
	owner, err := lockAccountConfiguration(ctx, client, snapshot.OwnerAccountID)
	if errors.Is(err, service.ErrAccountNotFound) {
		return result, nil
	}
	if err != nil {
		return nil, err
	}
	if !service.IsOpenAIOAuthOSProfileOwner(owner) {
		return result, nil
	}
	matched, err := lockOpenAIOAuthRuntimeGrant(ctx, client, snapshot)
	if err != nil || !matched {
		return result, err
	}
	state, err := lockOpenAIOAuthRuntimeAccount(ctx, client, accountID, snapshot.OwnerAccountID)
	if err != nil || state == nil {
		return result, err
	}
	var authPause *openAIOAuthRuntimePauseState
	if change.Kind == service.OpenAIOAuthAccountStateError && change.AuthFailure && accountID == snapshot.OwnerAccountID {
		authPause, err = readOpenAIOAuthRuntimePauseState(ctx, client, accountID)
		if err != nil {
			return nil, err
		}
	}

	var query string
	var args []any
	switch change.Kind {
	case service.OpenAIOAuthAccountStateError:
		query = `UPDATE accounts SET status=$2,error_message=$3,schedulable=false,updated_at=NOW() WHERE id=$1`
		args = []any{accountID, service.StatusError, change.ErrorMessage}
	case service.OpenAIOAuthAccountStateCooldown:
		// A shorter penalty must not replace either a later deadline or its reason.
		query = `UPDATE accounts SET temp_unschedulable_until=$2,temp_unschedulable_reason=$3,updated_at=NOW()
			WHERE id=$1 AND (temp_unschedulable_until IS NULL OR temp_unschedulable_until<$2)`
		args = []any{accountID, change.Until, change.Reason}
	case service.OpenAIOAuthAccountStateModelCooldown:
		payload := map[string]string{
			"rate_limited_at":     time.Now().UTC().Format(time.RFC3339),
			"rate_limit_reset_at": change.Until.UTC().Format(time.RFC3339),
		}
		if reason := strings.TrimSpace(change.Reason); reason != "" {
			payload["reason"] = reason
		}
		raw, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		query = `UPDATE accounts SET extra=jsonb_set(
			jsonb_set(COALESCE(extra,'{}'::jsonb),'{model_rate_limits}'::text[],COALESCE(extra->'model_rate_limits','{}'::jsonb),true),
			ARRAY['model_rate_limits',$2]::text[],$3::jsonb,true),updated_at=NOW() WHERE id=$1`
		args = []any{accountID, change.ModelKey, string(raw)}
	case service.OpenAIOAuthAccountStateRecover:
		result.ClearedError = state.status == service.StatusError
		result.ClearedRateLimit = state.recoverable
		if !result.ClearedError && !result.ClearedRateLimit {
			return result, nil
		}
		// Match ClearError: recovery does not enable a manually unschedulable
		// account. Empty model/quota maps alone are not recoverable state.
		query = `UPDATE accounts SET
			status=CASE WHEN $2 THEN $4 ELSE status END,
			error_message=CASE WHEN $2 THEN '' ELSE error_message END,
			rate_limited_at=CASE WHEN $3 THEN NULL ELSE rate_limited_at END,
			rate_limit_reset_at=CASE WHEN $3 THEN NULL ELSE rate_limit_reset_at END,
			overload_until=CASE WHEN $3 THEN NULL ELSE overload_until END,
			temp_unschedulable_until=CASE WHEN $3 THEN NULL ELSE temp_unschedulable_until END,
			temp_unschedulable_reason=CASE WHEN $3 THEN NULL ELSE temp_unschedulable_reason END,
			extra=CASE WHEN $3 THEN COALESCE(extra,'{}'::jsonb)-'model_rate_limits'-'antigravity_quota_scopes' ELSE extra END,
			updated_at=NOW() WHERE id=$1`
		args = []any{accountID, result.ClearedError, result.ClearedRateLimit, service.StatusActive}
	case service.OpenAIOAuthAccountStateClearTemp:
		query = `UPDATE accounts SET temp_unschedulable_until=NULL,temp_unschedulable_reason=NULL,
			extra=COALESCE(extra,'{}'::jsonb)-'model_rate_limits',updated_at=NOW() WHERE id=$1`
		args = []any{accountID}
	case service.OpenAIOAuthAccountStateClearError:
		if state.status != service.StatusError {
			return result, nil
		}
		result.ClearedError = true
		query = `UPDATE accounts SET status=$2,error_message='',updated_at=NOW() WHERE id=$1`
		args = []any{accountID, service.StatusActive}
	}
	updated, err := client.ExecContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	affected, err := updated.RowsAffected()
	if err != nil || affected == 0 {
		return result, err
	}
	if authPause != nil {
		// The account-state trigger surrenders prior ownership, including for
		// same-value writes. Reclaim only this owner's authentication error after
		// that trigger, retaining the state from before its first auth failure.
		_, err := client.ExecContext(ctx, `UPDATE account_openai_oauth_credentials SET auth_pause_owned=true,
			auth_pause_previous_status=$2,auth_pause_previous_schedulable=$3,auth_pause_previous_error_message=$4 WHERE account_id=$1`,
			accountID, authPause.status, authPause.schedulable, authPause.errorMessage)
		if err != nil {
			return nil, err
		}
	}
	if err := enqueueSchedulerOutbox(ctx, client, service.SchedulerOutboxEventAccountChanged, &accountID, nil, nil); err != nil {
		return nil, err
	}
	result.Applied = true

	// Caller-owned transactions publish only after their actual commit. If the
	// repository itself wraps a transaction without a context handle, its outbox
	// remains the publication mechanism; never expose an uncommitted snapshot.
	if dbent.TxFromContext(ctx) != nil {
		publishCtx := dbent.NewTxContext(baseCtx, nil)
		afterAccountConfigurationCommit(ctx, func() {
			r.syncSchedulerAccountSnapshotDetached(publishCtx, accountID)
		})
	}
	if ownedTx != nil {
		if err := ownedTx.Commit(); err != nil {
			return nil, err
		}
	}
	return result, nil
}

type openAIOAuthRuntimePauseState struct {
	status       string
	schedulable  bool
	errorMessage string
}

// Caller holds the account and shared-grant locks. Ordinary account errors and
// shadow penalties do not own the owner's authorization pause.
func readOpenAIOAuthRuntimePauseState(ctx context.Context, client *dbent.Client, accountID int64) (*openAIOAuthRuntimePauseState, error) {
	rows, err := client.QueryContext(ctx, `SELECT CASE WHEN c.auth_pause_owned THEN c.auth_pause_previous_status ELSE a.status END,
		CASE WHEN c.auth_pause_owned THEN c.auth_pause_previous_schedulable ELSE a.schedulable END,
		CASE WHEN c.auth_pause_owned THEN c.auth_pause_previous_error_message ELSE COALESCE(a.error_message,'') END
		FROM accounts a JOIN account_openai_oauth_credentials c ON c.account_id=a.id WHERE a.id=$1`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, err
		}
		return nil, service.ErrAccountNotFound
	}
	state := &openAIOAuthRuntimePauseState{}
	if err := rows.Scan(&state.status, &state.schedulable, &state.errorMessage); err != nil {
		return nil, err
	}
	return state, rows.Err()
}

func lockOpenAIOAuthRuntimeGrant(ctx context.Context, client *dbent.Client, snapshot service.OpenAIOAuthAccountStateSnapshot) (bool, error) {
	rows, err := client.QueryContext(ctx, `SELECT authorization_generation::text,revision
		FROM account_openai_oauth_credentials WHERE account_id=$1 FOR UPDATE`, snapshot.OwnerAccountID)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	if !rows.Next() {
		return false, rows.Err()
	}
	var generation string
	var revision int64
	if err := rows.Scan(&generation, &revision); err != nil {
		return false, err
	}
	return generation == snapshot.AuthorizationGeneration && revision == snapshot.CredentialRevision, rows.Err()
}

type openAIOAuthRuntimeAccountState struct {
	status      string
	recoverable bool
}

func lockOpenAIOAuthRuntimeAccount(ctx context.Context, client *dbent.Client, accountID, ownerID int64) (*openAIOAuthRuntimeAccountState, error) {
	rows, err := client.QueryContext(ctx, `SELECT status,rate_limited_at,rate_limit_reset_at,overload_until,temp_unschedulable_until,extra
		FROM accounts WHERE id=$1 AND deleted_at IS NULL
		AND (id=$2 OR (parent_account_id=$2 AND platform='openai' AND type='oauth' AND quota_dimension='spark'))
		FOR NO KEY UPDATE`, accountID, ownerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, rows.Err()
	}
	state := &openAIOAuthRuntimeAccountState{}
	var limited, reset, overload, temporary sql.NullTime
	var extraJSON []byte
	if err := rows.Scan(&state.status, &limited, &reset, &overload, &temporary, &extraJSON); err != nil {
		return nil, err
	}
	state.recoverable = limited.Valid || reset.Valid || overload.Valid || temporary.Valid
	if !state.recoverable && len(extraJSON) > 0 {
		var extra map[string]any
		if err := json.Unmarshal(extraJSON, &extra); err != nil {
			return nil, err
		}
		for _, key := range []string{"model_rate_limits", "antigravity_quota_scopes"} {
			switch value := extra[key].(type) {
			case nil:
			case map[string]any:
				state.recoverable = state.recoverable || len(value) > 0
			case []any:
				state.recoverable = state.recoverable || len(value) > 0
			default:
				state.recoverable = true
			}
		}
	}
	return state, rows.Err()
}
