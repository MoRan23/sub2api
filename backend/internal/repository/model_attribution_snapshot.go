package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
)

// Batch enqueue shares one transaction. Acquire its complete account/owner set
// before the first snapshot takes any group lock, even when input IDs are mixed
// or a later shadow belongs to an earlier owner.
func lockAttributionBatchAccounts(ctx context.Context, tx *sql.Tx, ids []int64, manual bool) error {
	if !manual {
		return lockAccountGroupAccounts(ctx, tx, ids)
	}
	loadParents := func() (map[int64]*int64, error) {
		rows, err := tx.QueryContext(ctx, `SELECT id,parent_account_id FROM accounts WHERE id=ANY($1)`, pq.Array(ids))
		if err != nil {
			return nil, err
		}
		defer func() { _ = rows.Close() }()
		parents := map[int64]*int64{}
		for rows.Next() {
			var id int64
			var parent *int64
			if err = rows.Scan(&id, &parent); err != nil {
				return nil, err
			}
			parents[id] = parent
		}
		return parents, rows.Err()
	}
	parents, err := loadParents()
	if err != nil {
		return err
	}
	accounts := append([]int64{}, ids...)
	for _, parent := range parents {
		if parent != nil {
			accounts = append(accounts, *parent)
		}
	}
	if err = lockAccountGroupAccounts(ctx, tx, accounts); err != nil {
		return err
	}
	current, err := loadParents()
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(parents, current) {
		return &pq.Error{Code: "40001", Message: "attribution credential owners changed while locking accounts"}
	}
	return nil
}

// Shadow and owner must be locked together before any group or association.
// The unlocked relationship read only discovers the lock set; the authoritative
// account read checks it again and retries the transaction if it changed.
func lockAttributionSnapshotAccounts(ctx context.Context, tx *sql.Tx, id int64, manual bool) (*int64, error) {
	var parentID *int64
	if manual {
		if err := tx.QueryRowContext(ctx, `SELECT parent_account_id FROM accounts WHERE id=$1`, id).Scan(&parentID); err != nil {
			return nil, err
		}
	}
	ids := []int64{id}
	if parentID != nil {
		ids = append(ids, *parentID)
	}
	return parentID, lockAccountGroupAccounts(ctx, tx, ids)
}

// All corresponding account rows are already locked. Keep the authorization
// shape unchanged so existing shadow jobs retain their original identity fence.
func attributionAccountAuthorization(ctx context.Context, tx *sql.Tx, a *service.Account) (map[string]any, error) {
	var generation, status string
	err := tx.QueryRowContext(ctx, `SELECT authorization_generation::text,status FROM account_openai_oauth_credentials WHERE account_id=$1 FOR UPDATE`, a.ID).Scan(&generation, &status)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	identity := map[string]any{"generation": generation, "status": status, "id": a.ID, "platform": a.Platform, "type": a.Type}
	for _, key := range []string{"chatgpt_account_id", "chatgpt_user_id", "organization_id", "auth_mode", "openai_auth_mode"} {
		identity[key] = a.Credentials[key]
	}
	if a.IsOpenAIPersonalAccessToken() || a.IsOpenAIAgentIdentity() || generation == "" {
		for _, key := range []string{"access_token", "agent_private_key", "agent_runtime_id"} {
			identity[key] = a.Credentials[key]
		}
	}
	if a.Platform == service.PlatformOpenAI && a.Type == service.AccountTypeAPIKey {
		identity["api_key"] = a.GetOpenAIProtocolAPIKey()
		identity["base_url"] = a.GetOpenAIBaseURL()
		identity["codex_engine"] = a.IsCodexEngine()
	}
	return identity, nil
}

// Parent routing groups do not participate in the child's authorization. Reading
// only the locked owner avoids recursively acquiring account locks after groups.
func attributionShadowOwnerAuthorization(ctx context.Context, tx *sql.Tx, id int64) (string, string, error) {
	a := &service.Account{}
	var credentials []byte
	err := tx.QueryRowContext(ctx, `SELECT id,platform,type,credentials FROM accounts WHERE id=$1 AND deleted_at IS NULL AND parent_account_id IS NULL AND platform='openai' AND type='oauth'`, id).Scan(&a.ID, &a.Platform, &a.Type, &credentials)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "authorization_changed", nil
	}
	if err != nil {
		return "", "", err
	}
	if err = json.Unmarshal(credentials, &a.Credentials); err != nil {
		return "", "", err
	}
	if skip := service.AttributionSkipReason(a, time.Now(), true); skip != "" {
		return "", skip, nil
	}
	identity, err := attributionAccountAuthorization(ctx, tx, a)
	if err != nil {
		return "", "", err
	}
	return service.AttributionDigest(identity), "", nil
}
