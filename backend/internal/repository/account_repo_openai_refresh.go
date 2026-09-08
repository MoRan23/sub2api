package repository

import (
	"context"
	"encoding/json"
	"errors"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
)

// PatchOpenAIOAuthCredentialsIfUnchanged preserves administrator-owned credential
// fields while replacing only provider auth fields from the attempted refresh.
func (r *accountRepository) PatchOpenAIOAuthCredentialsIfUnchanged(
	ctx context.Context,
	id int64,
	expectedAuth map[string]any,
	expectedProxyID *int64,
	patch map[string]any,
	removedKeys []string,
) (bool, error) {
	if r == nil {
		return false, errors.New("account repository SQL executor is not configured")
	}
	contextTx := dbent.TxFromContext(ctx)
	exec := r.sql
	if contextTx != nil {
		exec = contextTx.Client()
	}
	if exec == nil {
		return false, errors.New("account repository SQL executor is not configured")
	}
	if len(expectedAuth) == 0 {
		return false, errors.New("OpenAI OAuth refresh requires an expected auth snapshot")
	}
	expectedJSON, err := json.Marshal(expectedAuth)
	if err != nil {
		return false, err
	}
	patchJSON, err := json.Marshal(normalizeJSONMap(patch))
	if err != nil {
		return false, err
	}
	// A nil SQL array would nullify the JSON expression instead of removing no keys.
	removedKeys = append([]string{}, removedKeys...)
	result, err := exec.ExecContext(ctx, `
		WITH updated AS (
		UPDATE accounts AS a
		SET credentials = (COALESCE(a.credentials, '{}'::jsonb) - $1::text[]) || $2::jsonb,
			updated_at = NOW()
		WHERE a.id = $3
			AND a.deleted_at IS NULL
			AND a.platform = $4
			AND a.type = $5
			AND a.parent_account_id IS NULL
			AND a.proxy_id IS NOT DISTINCT FROM $6
			AND NOT EXISTS (
				SELECT 1 FROM jsonb_each($7::jsonb) AS expected(key, value)
				WHERE COALESCE(a.credentials -> expected.key, 'null'::jsonb) IS DISTINCT FROM expected.value
			)
		RETURNING a.id
		)
		INSERT INTO scheduler_outbox (event_type, account_id, group_id, payload)
		SELECT $8, updated.id, NULL, NULL FROM updated
	`, pq.Array(removedKeys), string(patchJSON), id, service.PlatformOpenAI,
		service.AccountTypeOAuth, expectedProxyID, string(expectedJSON),
		service.SchedulerOutboxEventAccountChanged)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	if err != nil || affected == 0 {
		return false, err
	}
	if contextTx == nil {
		r.syncSchedulerAccountSnapshotDetached(ctx, id)
	}
	return true, nil
}
