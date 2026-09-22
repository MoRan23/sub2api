package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/lib/pq"
)

const openAIOAuthSlotColumns = `account_id,os_family,credentials,authorization_generation::text,revision,state_generation::text,credential_epoch::text,status,authorized_at,expires_at,last_error,refresh_retry_after`

func readOpenAIOAuthOSCredentials(ctx context.Context, client *dbent.Client, id int64, os string) ([]*service.OpenAIOAuthOSCredential, error) {
	query := `SELECT ` + openAIOAuthSlotColumns + ` FROM account_openai_oauth_os_credentials WHERE account_id=$1 AND EXISTS (SELECT 1 FROM accounts a WHERE a.id=$1 AND a.deleted_at IS NULL AND ` + codexTurnStateOwnerExpression("a.credentials") + `)`
	args := []any{id}
	if os != "" {
		query += ` AND os_family=$2`
		args = append(args, os)
	}
	rows, err := client.QueryContext(ctx, query+` ORDER BY os_family`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]*service.OpenAIOAuthOSCredential, 0, 3)
	for rows.Next() {
		slot := &service.OpenAIOAuthOSCredential{}
		var credentials []byte
		var authorized, expires, retry sql.NullTime
		if err := rows.Scan(&slot.OwnerAccountID, &slot.OSFamily, &credentials, &slot.AuthorizationGeneration, &slot.Revision, &slot.StateGeneration, &slot.CredentialEpoch, &slot.Status, &authorized, &expires, &slot.LastError, &retry); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(credentials, &slot.Credentials); err != nil {
			return nil, err
		}
		if authorized.Valid {
			slot.AuthorizedAt = &authorized.Time
		}
		if expires.Valid {
			slot.ExpiresAt = &expires.Time
		} else {
			slot.ExpiresAt = openAIOAuthCredentialExpiry(slot.Credentials)
		}
		if retry.Valid {
			slot.RefreshRetryAfter = &retry.Time
		}
		out = append(out, slot)
	}
	return out, rows.Err()
}

func (r *accountRepository) GetOpenAIOAuthOSCredential(ctx context.Context, id int64, os string) (*service.OpenAIOAuthOSCredential, error) {
	if service.NormalizeOpenAIOSFamily(os) == "" {
		return nil, service.ErrOpenAIOAuthOSUnauthorized
	}
	slots, err := readOpenAIOAuthOSCredentials(ctx, clientFromContext(ctx, r.client), id, os)
	if err != nil || len(slots) == 0 {
		return nil, err
	}
	return slots[0], nil
}

func (r *accountRepository) ListOpenAIOAuthOSCredentials(ctx context.Context, id int64) ([]*service.OpenAIOAuthOSCredential, error) {
	return readOpenAIOAuthOSCredentials(ctx, clientFromContext(ctx, r.client), id, "")
}

func openAIOAuthCredentialExpiry(credentials map[string]any) *time.Time {
	value, _ := credentials["expires_at"].(string)
	if parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(value)); err == nil {
		return &parsed
	}
	return nil
}

// A backup may contain independent grants for several systems. Creation owns
// one transaction, so a bad second grant cannot leave a partially imported row.
func initializeOpenAIOAuthOSCredentialsLocked(ctx context.Context, client *dbent.Client, account *service.Account) error {
	for os := range account.OpenAIOAuthInitialCredentials {
		if service.NormalizeOpenAIOSFamily(os) == "" || service.NormalizeOpenAIOSFamily(os) != os {
			return service.ErrOpenAIOAuthOSUnauthorized
		}
	}
	for _, os := range service.OpenAIOAuthOSFamilies() {
		credentials, exists := account.OpenAIOAuthInitialCredentials[os]
		if !exists {
			continue
		}
		if service.OpenAIOAuthCredentialSubject(credentials, "access_token") == "" && service.OpenAIOAuthCredentialSubject(credentials, "refresh_token") == "" {
			return service.ErrOpenAIOAuthOSUnauthorized
		}
		if err := validateOpenAIOAuthOSSubjectLocked(ctx, client, account.ID, os, credentials); err != nil {
			return err
		}
		now := time.Now().UTC()
		slot := &service.OpenAIOAuthOSCredential{OwnerAccountID: account.ID, OSFamily: os, Credentials: service.OpenAIOAuthProviderCredentials(credentials), AuthorizationGeneration: uuid.NewString(), Revision: 1, StateGeneration: uuid.NewString(), CredentialEpoch: uuid.NewString(), Status: service.OpenAIOAuthAuthorizationAuthorized, AuthorizedAt: &now, ExpiresAt: openAIOAuthCredentialExpiry(credentials)}
		if err := saveOpenAIOAuthOSCredentialLocked(ctx, client, slot, "initial"); err != nil {
			return err
		}
	}
	if _, err := client.ExecContext(ctx, `UPDATE account_openai_oauth_os_credentials SET source='initial' WHERE account_id=$1 AND source='legacy_migration'`, account.ID); err != nil {
		return err
	}
	if err := mirrorOpenAIOAuthOSCredentialLocked(ctx, client, account.ID, account.OpenAIOAuthOSProfiles.DefaultOS); err != nil {
		return err
	}
	profiles, err := loadOpenAIOAuthOSProfiles(ctx, client, []int64{account.ID})
	if err != nil {
		return err
	}
	service.ApplyOpenAIOAuthOSProfiles(account, profiles[account.ID])
	if credentials, exists := account.OpenAIOAuthInitialCredentials[account.OpenAIOAuthOSProfiles.DefaultOS]; exists {
		account.Credentials = service.PreserveOpenAIOAuthProviderCredentials(credentials, account.Credentials)
	}
	account.OpenAIOAuthInitialCredentials = nil
	return nil
}

// Marker insertion and legacy copy share the account row lock. The marker is
// retained across revoke and account-type conversions, preventing resurrection.
func migrateOpenAIOAuthOSCredentialsLocked(ctx context.Context, client *dbent.Client, account *service.Account, profiles *service.OpenAIOAuthOSProfiles) (bool, error) {
	result, err := client.ExecContext(ctx, `INSERT INTO account_openai_oauth_authorization_migrations(account_id,chatgpt_account_id,chatgpt_user_id) VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, account.ID, service.OpenAIOAuthCredentialSubject(account.Credentials, "chatgpt_account_id"), service.OpenAIOAuthCredentialSubject(account.Credentials, "chatgpt_user_id"))
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	if err != nil || n == 0 {
		return false, err
	}
	if service.OpenAIOAuthCredentialSubject(account.Credentials, "access_token") == "" && service.OpenAIOAuthCredentialSubject(account.Credentials, "refresh_token") == "" {
		return true, nil
	}
	slot := &service.OpenAIOAuthOSCredential{OwnerAccountID: account.ID, OSFamily: profiles.DefaultOS, Credentials: service.OpenAIOAuthProviderCredentials(account.Credentials), AuthorizationGeneration: uuid.NewString(), Revision: 1, StateGeneration: uuid.NewString(), CredentialEpoch: uuid.NewString(), Status: service.OpenAIOAuthAuthorizationAuthorized}
	now := time.Now().UTC()
	slot.AuthorizedAt = &now
	slot.ExpiresAt = openAIOAuthCredentialExpiry(slot.Credentials)
	if err = saveOpenAIOAuthOSCredentialLocked(ctx, client, slot, "legacy_migration"); err != nil {
		return false, err
	}
	// Accounts without installation profiles during SQL migrations are resumed
	// here. Move their singleton state once; never clone it into a second OS.
	rows, err := client.QueryContext(ctx, `SELECT EXISTS(SELECT 1 FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='openai_codex_state' AND column_name='os_family')`)
	if err != nil {
		return false, err
	}
	var scopedState bool
	if rows.Next() {
		err = rows.Scan(&scopedState)
	}
	rows.Close()
	if err != nil {
		return false, err
	}
	if scopedState {
		legacyGeneration, _ := account.Extra["codex_turn_state_generation"].(string)
		_, err = client.ExecContext(ctx, `UPDATE openai_codex_state_business_leases l SET generation=$2 FROM openai_codex_state s WHERE l.owner_account_id=$1 AND s.owner_account_id=l.owner_account_id AND s.os_family=l.os_family AND s.model=l.model AND s.generation=l.generation AND s.generation=$3 AND $3<>''`, account.ID, slot.StateGeneration, legacyGeneration)
		if err != nil {
			return false, err
		}
		_, err = client.ExecContext(ctx, `UPDATE openai_codex_state SET os_family=$2,generation=$3 WHERE owner_account_id=$1 AND generation=$4 AND $4<>''`, account.ID, slot.OSFamily, slot.StateGeneration, legacyGeneration)
		if err != nil {
			return false, err
		}
	}
	return true, nil
}

func saveOpenAIOAuthOSCredentialLocked(ctx context.Context, client *dbent.Client, slot *service.OpenAIOAuthOSCredential, source string) error {
	payload, err := json.Marshal(slot.Credentials)
	if err != nil {
		return err
	}
	_, err = client.ExecContext(ctx, `INSERT INTO account_openai_oauth_os_credentials
	(account_id,os_family,credentials,authorization_generation,revision,state_generation,credential_epoch,status,source,authorized_at,expires_at,last_error,refresh_retry_after)
	VALUES($1,$2,$3::jsonb,$4::uuid,$5,$6::uuid,$7::uuid,$8,$9,$10,$11,$12,$13)
	ON CONFLICT(account_id,os_family) DO UPDATE SET credentials=EXCLUDED.credentials,authorization_generation=EXCLUDED.authorization_generation,revision=EXCLUDED.revision,state_generation=EXCLUDED.state_generation,credential_epoch=EXCLUDED.credential_epoch,status=EXCLUDED.status,source=CASE WHEN EXCLUDED.source='' THEN account_openai_oauth_os_credentials.source ELSE EXCLUDED.source END,authorized_at=EXCLUDED.authorized_at,expires_at=EXCLUDED.expires_at,last_error=EXCLUDED.last_error,refresh_retry_after=EXCLUDED.refresh_retry_after,updated_at=NOW()`, slot.OwnerAccountID, slot.OSFamily, string(payload), slot.AuthorizationGeneration, slot.Revision, slot.StateGeneration, slot.CredentialEpoch, slot.Status, source, slot.AuthorizedAt, slot.ExpiresAt, slot.LastError, slot.RefreshRetryAfter)
	return err
}

func mirrorOpenAIOAuthOSCredentialLocked(ctx context.Context, client *dbent.Client, id int64, os string) error {
	_, err := client.ExecContext(ctx, `UPDATE accounts a SET credentials=(COALESCE(a.credentials,'{}'::jsonb)-$3::text[]) || COALESCE((SELECT c.credentials FROM account_openai_oauth_os_credentials c WHERE c.account_id=a.id AND c.os_family=$2 AND c.status<>'unauthorized'),'{}'::jsonb),updated_at=NOW() WHERE a.id=$1 AND EXISTS(SELECT 1 FROM account_openai_oauth_os_profiles p WHERE p.account_id=a.id AND p.os_family=$2 AND p.is_default)`, id, os, pq.Array(service.OpenAIOAuthProviderCredentialKeys()))
	return err
}

func (r *accountRepository) mutateOpenAIOAuthOSCredential(ctx context.Context, id int64, os string, mutate func(context.Context, *dbent.Client, *service.Account, *service.OpenAIOAuthOSProfiles) (bool, error)) error {
	if service.NormalizeOpenAIOSFamily(os) == "" || service.NormalizeOpenAIOSFamily(os) != os {
		return service.ErrOpenAIOAuthOSUnauthorized
	}
	baseCtx := ctx
	client := clientFromContext(ctx, r.client)
	var tx *dbent.Tx
	if dbent.TxFromContext(ctx) == nil {
		var err error
		tx, err = r.client.Tx(ctx)
		if err != nil && !errors.Is(err, dbent.ErrTxStarted) {
			return err
		}
		if tx != nil {
			defer tx.Rollback()
			ctx = dbent.NewTxContext(ctx, tx)
			client = tx.Client()
		}
	}
	account, err := lockAccountConfiguration(ctx, client, id)
	if err != nil {
		return err
	}
	if !service.IsOpenAIOAuthOSProfileOwner(account) {
		return service.ErrOpenAIOAuthOSUnauthorized
	}
	profiles, _, err := ensureOpenAIOAuthOSProfilesLocked(ctx, client, account)
	if err != nil {
		return err
	}
	changed, err := mutate(ctx, client, account, profiles)
	if err != nil {
		return err
	}
	if changed {
		if err = mirrorOpenAIOAuthOSCredentialLocked(ctx, client, id, os); err != nil {
			return err
		}
		if err = enqueueSchedulerOutbox(ctx, client, service.SchedulerOutboxEventAccountChanged, &id, nil, nil); err != nil {
			return err
		}
		notifyCodexTurnStateAccountAfterCommit(ctx, id)
	}
	if tx != nil {
		if err = tx.Commit(); err != nil {
			return err
		}
		if changed {
			r.syncSchedulerAccountSnapshot(baseCtx, id)
		}
	}
	return nil
}

func validateOpenAIOAuthOSSubjectLocked(ctx context.Context, client *dbent.Client, id int64, os string, credentials map[string]any) error {
	accountID, userID := service.OpenAIOAuthCredentialSubject(credentials, "chatgpt_account_id"), service.OpenAIOAuthCredentialSubject(credentials, "chatgpt_user_id")
	if accountID == "" || userID == "" {
		return service.ErrOpenAIOAuthOSSubjectMismatch
	}
	rows, err := client.QueryContext(ctx, `SELECT chatgpt_account_id,chatgpt_user_id FROM account_openai_oauth_authorization_migrations WHERE account_id=$1`, id)
	if err != nil {
		return err
	}
	var anchorAccount, anchorUser string
	if rows.Next() {
		err = rows.Scan(&anchorAccount, &anchorUser)
	}
	closeErr := rows.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if (anchorAccount != "" && anchorAccount != accountID) || (anchorUser != "" && anchorUser != userID) {
		return service.ErrOpenAIOAuthOSSubjectMismatch
	}
	slots, err := readOpenAIOAuthOSCredentials(ctx, client, id, "")
	if err != nil {
		return err
	}
	refresh := service.OpenAIOAuthCredentialSubject(credentials, "refresh_token")
	for _, slot := range slots {
		if slot.Status == service.OpenAIOAuthAuthorizationUnauthorized {
			continue
		}
		for _, key := range []string{"chatgpt_account_id", "chatgpt_user_id"} {
			existing := service.OpenAIOAuthCredentialSubject(slot.Credentials, key)
			if slot.OSFamily != os && existing == "" {
				return service.ErrOpenAIOAuthOSSubjectMismatch
			}
			if existing != "" && existing != service.OpenAIOAuthCredentialSubject(credentials, key) {
				return service.ErrOpenAIOAuthOSSubjectMismatch
			}
		}
		if slot.OSFamily != os && refresh != "" && refresh == service.OpenAIOAuthCredentialSubject(slot.Credentials, "refresh_token") {
			return service.ErrOpenAIOAuthOSRefreshTokenReused
		}
	}
	_, err = client.ExecContext(ctx, `UPDATE account_openai_oauth_authorization_migrations SET chatgpt_account_id=$2,chatgpt_user_id=$3 WHERE account_id=$1`, id, accountID, userID)
	return err
}

func (r *accountRepository) BindOpenAIOAuthOSCredentials(ctx context.Context, id int64, os string, credentials map[string]any, source string) (*service.OpenAIOAuthOSCredential, error) {
	return r.bindOpenAIOAuthOSCredentials(ctx, id, os, nil, credentials, source)
}
func (r *accountRepository) BindOpenAIOAuthOSCredentialsIfGeneration(ctx context.Context, id int64, os, expected string, credentials map[string]any, source string) (*service.OpenAIOAuthOSCredential, error) {
	return r.bindOpenAIOAuthOSCredentials(ctx, id, os, &expected, credentials, source)
}
func (r *accountRepository) bindOpenAIOAuthOSCredentials(ctx context.Context, id int64, os string, expected *string, credentials map[string]any, source string) (*service.OpenAIOAuthOSCredential, error) {
	var result *service.OpenAIOAuthOSCredential
	err := r.mutateOpenAIOAuthOSCredential(ctx, id, os, func(ctx context.Context, client *dbent.Client, account *service.Account, profiles *service.OpenAIOAuthOSProfiles) (bool, error) {
		slots, err := readOpenAIOAuthOSCredentials(ctx, client, id, os)
		if err != nil {
			return false, err
		}
		generation := ""
		revision := int64(0)
		if len(slots) > 0 {
			generation = slots[0].AuthorizationGeneration
			revision = slots[0].Revision
		}
		if expected != nil && *expected != generation {
			return false, service.ErrOpenAIOAuthOSAuthorizationChanged
		}
		if service.OpenAIOAuthCredentialSubject(credentials, "access_token") == "" && service.OpenAIOAuthCredentialSubject(credentials, "refresh_token") == "" {
			return false, service.ErrOpenAIOAuthOSUnauthorized
		}
		if err := validateOpenAIOAuthOSSubjectLocked(ctx, client, id, os, credentials); err != nil {
			return false, err
		}
		now := time.Now().UTC()
		result = &service.OpenAIOAuthOSCredential{OwnerAccountID: id, OSFamily: os, Credentials: service.OpenAIOAuthProviderCredentials(credentials), AuthorizationGeneration: uuid.NewString(), Revision: revision + 1, StateGeneration: uuid.NewString(), CredentialEpoch: uuid.NewString(), Status: service.OpenAIOAuthAuthorizationAuthorized, AuthorizedAt: &now, ExpiresAt: openAIOAuthCredentialExpiry(credentials)}
		return true, saveOpenAIOAuthOSCredentialLocked(ctx, client, result, source)
	})
	return result, err
}

func (r *accountRepository) PatchOpenAIOAuthOSCredentialsIfUnchanged(ctx context.Context, id int64, os, generation string, revision int64, expectedProxyID *int64, patch map[string]any, removed []string) (bool, error) {
	applied := false
	err := r.mutateOpenAIOAuthOSCredential(ctx, id, os, func(ctx context.Context, client *dbent.Client, account *service.Account, profiles *service.OpenAIOAuthOSProfiles) (bool, error) {
		// Proxy is part of the physical refresh attempt, even though slots share it.
		rows, err := client.QueryContext(ctx, `SELECT proxy_id FROM accounts WHERE id=$1`, id)
		if err != nil {
			return false, err
		}
		var proxy sql.NullInt64
		if rows.Next() {
			err = rows.Scan(&proxy)
		}
		rows.Close()
		if err != nil {
			return false, err
		}
		if (expectedProxyID == nil) != (!proxy.Valid) || expectedProxyID != nil && *expectedProxyID != proxy.Int64 {
			return false, nil
		}
		slots, err := readOpenAIOAuthOSCredentials(ctx, client, id, os)
		if err != nil {
			return false, err
		}
		if len(slots) == 0 {
			return false, nil
		}
		slot := slots[0]
		if slot.Status == service.OpenAIOAuthAuthorizationUnauthorized || slot.AuthorizationGeneration != generation || slot.Revision != revision {
			return false, nil
		}
		next := service.OpenAIOAuthProviderCredentials(slot.Credentials)
		allowed := make(map[string]bool)
		for _, key := range service.OpenAIOAuthProviderCredentialKeys() {
			allowed[key] = true
		}
		for _, key := range removed {
			if allowed[key] {
				delete(next, key)
			}
		}
		for key, value := range service.OpenAIOAuthProviderCredentials(patch) {
			next[key] = value
		}
		if service.OpenAIOAuthCredentialSubject(next, "access_token") == "" && service.OpenAIOAuthCredentialSubject(next, "refresh_token") == "" {
			return false, service.ErrOpenAIOAuthOSUnauthorized
		}
		for _, key := range []string{"chatgpt_account_id", "chatgpt_user_id"} {
			old := service.OpenAIOAuthCredentialSubject(slot.Credentials, key)
			current := service.OpenAIOAuthCredentialSubject(next, key)
			if old != "" && current != "" && old != current {
				return false, service.ErrOpenAIOAuthOSSubjectMismatch
			}
			if current == "" && old != "" {
				next[key] = old
			}
		}
		otherSlots, err := readOpenAIOAuthOSCredentials(ctx, client, id, "")
		if err != nil {
			return false, err
		}
		refreshToken := service.OpenAIOAuthCredentialSubject(next, "refresh_token")
		for _, other := range otherSlots {
			if other.OSFamily != os && other.Status != service.OpenAIOAuthAuthorizationUnauthorized && refreshToken != "" && refreshToken == service.OpenAIOAuthCredentialSubject(other.Credentials, "refresh_token") {
				return false, service.ErrOpenAIOAuthOSRefreshTokenReused
			}
		}
		if !reflect.DeepEqual(next, slot.Credentials) {
			slot.StateGeneration = uuid.NewString()
			slot.CredentialEpoch = uuid.NewString()
		}
		slot.Credentials = next
		slot.Revision++
		slot.Status = service.OpenAIOAuthAuthorizationAuthorized
		slot.LastError = ""
		slot.RefreshRetryAfter = nil
		slot.ExpiresAt = openAIOAuthCredentialExpiry(next)
		if err = saveOpenAIOAuthOSCredentialLocked(ctx, client, slot, ""); err != nil {
			return false, err
		}
		applied = true
		return true, nil
	})
	return applied, err
}

func (r *accountRepository) mutateOpenAIOAuthOSCredentialStateCAS(ctx context.Context, id int64, os, generation string, revision int64, status, message string, until *time.Time) (bool, error) {
	applied := false
	err := r.mutateOpenAIOAuthOSCredential(ctx, id, os, func(ctx context.Context, client *dbent.Client, account *service.Account, profiles *service.OpenAIOAuthOSProfiles) (bool, error) {
		slots, err := readOpenAIOAuthOSCredentials(ctx, client, id, os)
		if err != nil {
			return false, err
		}
		if len(slots) == 0 {
			return false, nil
		}
		slot := slots[0]
		if slot.Status == service.OpenAIOAuthAuthorizationUnauthorized || slot.AuthorizationGeneration != generation || slot.Revision != revision {
			return false, nil
		}
		slot.Status = status
		slot.LastError = message
		slot.RefreshRetryAfter = until
		slot.Revision++
		if status != service.OpenAIOAuthAuthorizationAuthorized {
			slot.StateGeneration = uuid.NewString()
			slot.CredentialEpoch = uuid.NewString()
		}
		if err = saveOpenAIOAuthOSCredentialLocked(ctx, client, slot, ""); err != nil {
			return false, err
		}
		applied = true
		return true, nil
	})
	return applied, err
}

func (r *accountRepository) SetOpenAIOAuthOSCredentialErrorIfUnchanged(ctx context.Context, id int64, os, generation string, revision int64, lastError string) (bool, error) {
	return r.mutateOpenAIOAuthOSCredentialStateCAS(ctx, id, os, generation, revision, service.OpenAIOAuthAuthorizationReauthRequired, "OAuth authorization must be renewed", nil)
}
func (r *accountRepository) SetOpenAIOAuthOSCredentialCooldownIfUnchanged(ctx context.Context, id int64, os, generation string, revision int64, until time.Time, reason string) (bool, error) {
	return r.mutateOpenAIOAuthOSCredentialStateCAS(ctx, id, os, generation, revision, service.OpenAIOAuthAuthorizationAuthorized, "OAuth token refresh temporarily unavailable", &until)
}

func (r *accountRepository) RevokeOpenAIOAuthOSCredentials(ctx context.Context, id int64, os string) error {
	return r.mutateOpenAIOAuthOSCredential(ctx, id, os, func(ctx context.Context, client *dbent.Client, account *service.Account, profiles *service.OpenAIOAuthOSProfiles) (bool, error) {
		slots, err := readOpenAIOAuthOSCredentials(ctx, client, id, os)
		if err != nil {
			return false, err
		}
		revision := int64(1)
		if len(slots) > 0 {
			revision = slots[0].Revision + 1
		}
		slot := &service.OpenAIOAuthOSCredential{OwnerAccountID: id, OSFamily: os, Credentials: map[string]any{}, AuthorizationGeneration: uuid.NewString(), Revision: revision, StateGeneration: uuid.NewString(), CredentialEpoch: uuid.NewString(), Status: service.OpenAIOAuthAuthorizationUnauthorized}
		return true, saveOpenAIOAuthOSCredentialLocked(ctx, client, slot, "revoked")
	})
}

func (r *accountRepository) SetDefaultOpenAIOAuthOS(ctx context.Context, id int64, os string) (*service.OpenAIOAuthOSProfiles, error) {
	var result *service.OpenAIOAuthOSProfiles
	err := r.mutateOpenAIOAuthOSCredential(ctx, id, os, func(ctx context.Context, client *dbent.Client, account *service.Account, profiles *service.OpenAIOAuthOSProfiles) (bool, error) {
		slots, err := readOpenAIOAuthOSCredentials(ctx, client, id, os)
		if err != nil {
			return false, err
		}
		if len(slots) == 0 || slots[0].Status != service.OpenAIOAuthAuthorizationAuthorized || (slots[0].RefreshRetryAfter != nil && slots[0].RefreshRetryAfter.After(time.Now())) {
			return false, service.ErrOpenAIOAuthOSUnauthorized
		}
		previous := service.CloneOpenAIOAuthOSProfiles(profiles)
		profiles.DefaultOS = os
		_, err = saveOpenAIOAuthOSProfilesLocked(ctx, client, account, previous, profiles)
		result = service.CloneOpenAIOAuthOSProfiles(profiles)
		return true, err
	})
	return result, err
}
