package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/lib/pq"
)

// All profile writers take the owning account lock first. Credentials refresh,
// administrator edits and profile initialization therefore share one fence.
func loadOpenAIOAuthOSProfiles(ctx context.Context, client *dbent.Client, ids []int64, eligibleOnly ...bool) (map[int64]*service.OpenAIOAuthOSProfiles, error) {
	out := make(map[int64]*service.OpenAIOAuthOSProfiles)
	if len(ids) == 0 {
		return out, nil
	}
	eligible := ""
	if len(eligibleOnly) > 0 && eligibleOnly[0] {
		eligible = " AND EXISTS (SELECT 1 FROM accounts WHERE accounts.id=p.account_id AND deleted_at IS NULL AND " + codexTurnStateOwnerExpression("credentials") + ")"
	}
	rows, err := client.QueryContext(ctx, `SELECT p.account_id, p.os_family, p.installation_id::text,
		p.user_agent, p.sync_session_id::text, p.is_default, COALESCE(c.status,'unauthorized'),
		c.authorized_at,c.expires_at,COALESCE(c.last_error,''),c.refresh_retry_after,a.credentials->>'expires_at'
		FROM account_openai_oauth_os_profiles p LEFT JOIN account_openai_oauth_credentials c
		ON c.account_id=p.account_id
		JOIN accounts a ON a.id=p.account_id
		WHERE p.account_id = ANY($1)`+eligible+` ORDER BY p.account_id, p.os_family`, pq.Array(ids))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var accountID int64
		var profile service.OpenAIOAuthOSProfile
		var isDefault bool
		var authorized, expires, retry sql.NullTime
		var legacyExpiry sql.NullString
		if err := rows.Scan(&accountID, &profile.OSFamily, &profile.InstallationID, &profile.UserAgent, &profile.SyncSessionID, &isDefault,
			&profile.Authorization.Status, &authorized, &expires, &profile.Authorization.LastError, &retry, &legacyExpiry); err != nil {
			return nil, err
		}
		if authorized.Valid {
			profile.Authorization.AuthorizedAt = &authorized.Time
		}
		if expires.Valid {
			profile.Authorization.ExpiresAt = &expires.Time
		} else if legacyExpiry.Valid {
			profile.Authorization.ExpiresAt = openAIOAuthCredentialExpiry(map[string]any{"expires_at": legacyExpiry.String})
		}
		if retry.Valid {
			profile.Authorization.RefreshRetryAfter = &retry.Time
		}
		profiles := out[accountID]
		if profiles == nil {
			profiles = &service.OpenAIOAuthOSProfiles{Profiles: make(map[string]service.OpenAIOAuthOSProfile)}
			out[accountID] = profiles
		}
		profiles.Profiles[profile.OSFamily] = profile
		summary := service.CloneOpenAIOAuthOSAuthorizationSummary(profile.Authorization)
		profiles.Authorization = &summary
		if isDefault {
			profiles.DefaultOS = profile.OSFamily
		}
	}
	return out, rows.Err()
}

// GetOpenAIOAuthOSProfiles is deliberately read-only: rendering or exporting an
// account must never initialize or rotate any credential identity.
func (r *accountRepository) GetOpenAIOAuthOSProfiles(ctx context.Context, accountID int64) (*service.OpenAIOAuthOSProfiles, error) {
	profiles, err := loadOpenAIOAuthOSProfiles(ctx, clientFromContext(ctx, r.client), []int64{accountID}, true)
	if err != nil {
		return nil, err
	}
	return profiles[accountID], nil
}

func loadLegacyOpenAIOAuthSyncSession(ctx context.Context, client *dbent.Client, accountID int64) (string, error) {
	rows, err := client.QueryContext(ctx, `SELECT session_id FROM openai_oauth_sync_sessions WHERE account_id=$1`, accountID)
	if err != nil {
		return "", err
	}
	defer func() { _ = rows.Close() }()
	var value string
	if rows.Next() {
		if err := rows.Scan(&value); err != nil {
			return "", err
		}
	}
	return value, rows.Err()
}

func saveOpenAIOAuthOSProfilesLocked(ctx context.Context, client *dbent.Client, account *service.Account, previous, profiles *service.OpenAIOAuthOSProfiles) (bool, error) {
	changed := !reflect.DeepEqual(previous, profiles)
	if changed {
		// Clearing only the previous marker first avoids a transient unique-index
		// conflict when repairing a missing or invalid default designation.
		if previous != nil && previous.DefaultOS != profiles.DefaultOS {
			if _, err := client.ExecContext(ctx, `UPDATE account_openai_oauth_os_profiles
				SET is_default=false, updated_at=NOW() WHERE account_id=$1 AND is_default`, account.ID); err != nil {
				return false, err
			}
		}
		for _, os := range []string{"windows", "macos", "linux"} {
			profile := profiles.Profiles[os]
			if previous != nil && reflect.DeepEqual(previous.Profiles[os], profile) && (previous.DefaultOS == os) == (profiles.DefaultOS == os) {
				continue
			}
			_, err := client.ExecContext(ctx, `INSERT INTO account_openai_oauth_os_profiles
				(account_id, os_family, installation_id, user_agent, sync_session_id, is_default)
				VALUES ($1,$2,$3::uuid,$4,$5::uuid,$6)
				ON CONFLICT (account_id,os_family) DO UPDATE SET
				installation_id=EXCLUDED.installation_id, user_agent=EXCLUDED.user_agent,
				sync_session_id=EXCLUDED.sync_session_id, is_default=EXCLUDED.is_default, updated_at=NOW()`,
				account.ID, os, profile.InstallationID, profile.UserAgent, profile.SyncSessionID, profiles.DefaultOS == os)
			if err != nil {
				return false, err
			}
		}
	}
	profile, ok := profiles.Profiles[profiles.DefaultOS]
	if !ok {
		return false, fmt.Errorf("OpenAI OAuth OS profiles have no default installation")
	}
	result, err := client.ExecContext(ctx, `UPDATE accounts SET
		extra=jsonb_set(COALESCE(extra,'{}'::jsonb),'{openai_pinned_installation_id}',to_jsonb($2::text),true),
		credentials=jsonb_set(COALESCE(credentials,'{}'::jsonb),'{user_agent}',to_jsonb($3::text),true), updated_at=NOW()
		WHERE id=$1 AND (extra->>'openai_pinned_installation_id' IS DISTINCT FROM $2::text
		OR credentials->>'user_agent' IS DISTINCT FROM $3::text)`, account.ID, profile.InstallationID, profile.UserAgent)
	if err != nil {
		return false, err
	}
	if count, err := result.RowsAffected(); err != nil {
		return false, err
	} else if count > 0 {
		changed = true
	}
	result, err = client.ExecContext(ctx, `INSERT INTO openai_oauth_sync_sessions(account_id,session_id)
		VALUES ($1,$2) ON CONFLICT (account_id) DO UPDATE SET session_id=EXCLUDED.session_id,updated_at=NOW()
		WHERE openai_oauth_sync_sessions.session_id IS DISTINCT FROM EXCLUDED.session_id`, account.ID, profile.SyncSessionID)
	if err != nil {
		return false, err
	}
	if count, err := result.RowsAffected(); err != nil {
		return false, err
	} else if count > 0 {
		changed = true
	}
	service.ApplyOpenAIOAuthOSProfiles(account, profiles)
	return changed, nil
}

// The caller holds the account row lock, or has just inserted it in the same
// transaction. It may pass an account's new type/credentials for conversions.
func ensureOpenAIOAuthOSProfilesLocked(ctx context.Context, client *dbent.Client, account *service.Account) (*service.OpenAIOAuthOSProfiles, bool, error) {
	if !service.IsOpenAIOAuthOSProfileOwner(account) {
		return nil, false, infraerrors.BadRequest("OPENAI_OAUTH_OS_PROFILES_UNSUPPORTED", "OS installation profiles require a regular OpenAI OAuth parent account")
	}
	stored, err := loadOpenAIOAuthOSProfiles(ctx, client, []int64{account.ID})
	if err != nil {
		return nil, false, err
	}
	legacySync, err := loadLegacyOpenAIOAuthSyncSession(ctx, client, account.ID)
	if err != nil {
		return nil, false, err
	}
	profiles, err := service.BuildOpenAIOAuthOSProfiles(account, stored[account.ID], legacySync)
	if err != nil {
		return nil, false, err
	}
	changed, err := saveOpenAIOAuthOSProfilesLocked(ctx, client, account, stored[account.ID], profiles)
	if err != nil {
		return nil, false, err
	}
	migrated, err := migrateOpenAIOAuthOSCredentialsLocked(ctx, client, account, profiles)
	if err != nil {
		return nil, false, err
	}
	if migrated {
		loaded, loadErr := loadOpenAIOAuthOSProfiles(ctx, client, []int64{account.ID})
		if loadErr != nil {
			return nil, false, loadErr
		}
		profiles = loaded[account.ID]
		service.ApplyOpenAIOAuthOSProfiles(account, profiles)
	}
	return profiles, changed || migrated, nil
}

func (r *accountRepository) mutateOpenAIOAuthOSProfiles(ctx context.Context, accountID int64, mutate func(*service.Account, *service.OpenAIOAuthOSProfiles) error) (*service.OpenAIOAuthOSProfiles, error) {
	baseCtx := ctx
	client := clientFromContext(ctx, r.client)
	var tx *dbent.Tx
	if dbent.TxFromContext(ctx) == nil {
		var err error
		tx, err = r.client.Tx(ctx)
		if err != nil && !errors.Is(err, dbent.ErrTxStarted) {
			return nil, err
		}
		if tx != nil {
			defer func() { _ = tx.Rollback() }()
			ctx = dbent.NewTxContext(ctx, tx)
			client = tx.Client()
		}
	}
	account, err := lockAccountConfiguration(ctx, client, accountID)
	if err != nil {
		return nil, err
	}
	if mutate != nil && service.IsOpenAIOAuthOSProfileOwner(account) && !account.IsOpenAIInstallationPinEnabled() {
		return nil, infraerrors.BadRequest("OPENAI_INSTALLATION_REGENERATE_PIN_DISABLED", "enable fixed installation_id and save the account before regenerating")
	}
	profiles, changed, err := ensureOpenAIOAuthOSProfilesLocked(ctx, client, account)
	if err != nil {
		return nil, err
	}
	if mutate != nil {
		previous := service.CloneOpenAIOAuthOSProfiles(profiles)
		if err := mutate(account, profiles); err != nil {
			return nil, err
		}
		mutated, err := saveOpenAIOAuthOSProfilesLocked(ctx, client, account, previous, profiles)
		if err != nil {
			return nil, err
		}
		changed = changed || mutated
	}
	if changed {
		if err := enqueueSchedulerOutbox(ctx, client, service.SchedulerOutboxEventAccountChanged, &accountID, nil, nil); err != nil {
			return nil, err
		}
	}
	if tx != nil {
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		if changed {
			r.syncSchedulerAccountSnapshot(baseCtx, accountID)
		}
	}
	return service.CloneOpenAIOAuthOSProfiles(profiles), nil
}

func (r *accountRepository) EnsureOpenAIOAuthOSProfiles(ctx context.Context, accountID int64) (*service.OpenAIOAuthOSProfiles, error) {
	return r.mutateOpenAIOAuthOSProfiles(ctx, accountID, nil)
}

// Called only when a credentials write actually changes profile eligibility.
// Routine token refreshes do not query or write the profile table.
func reconcileOpenAIOAuthOSProfileEligibilityLocked(ctx context.Context, client *dbent.Client, accountID int64) error {
	account, err := lockAccountConfiguration(ctx, client, accountID)
	if err != nil {
		return err
	}
	if service.IsOpenAIOAuthOSProfileOwner(account) {
		_, _, err := ensureOpenAIOAuthOSProfilesLocked(ctx, client, account)
		return err
	}
	err = revokeSharedOpenAIOAuthCredentialsLocked(ctx, client, accountID)
	if err != nil {
		return err
	}
	_, err = client.ExecContext(ctx, `DELETE FROM account_openai_oauth_os_profiles WHERE account_id=$1`, accountID)
	return err
}

func (r *accountRepository) RegenerateOpenAIOAuthOSProfileInstallationID(ctx context.Context, accountID int64, os string) (*service.OpenAIOAuthOSProfiles, error) {
	os = service.NormalizeOpenAIOSFamily(os)
	if os == "" {
		return nil, infraerrors.BadRequest("OPENAI_OAUTH_OS_PROFILE_INVALID", "OS family must be windows, macos or linux")
	}
	return r.mutateOpenAIOAuthOSProfiles(ctx, accountID, func(account *service.Account, profiles *service.OpenAIOAuthOSProfiles) error {
		if !account.IsOpenAIInstallationPinEnabled() {
			return infraerrors.BadRequest("OPENAI_INSTALLATION_REGENERATE_PIN_DISABLED", "enable fixed installation_id and save the account before regenerating")
		}
		profile, ok := profiles.Profiles[os]
		if !ok {
			return infraerrors.BadRequest("OPENAI_OAUTH_OS_PROFILE_INVALID", "OS family must be windows, macos or linux")
		}
		profile.InstallationID = uuid.NewString()
		profiles.Profiles[os] = profile
		return nil
	})
}

// BackfillOpenAIOAuthOSProfiles bounds reads and transactions; each account is
// independently locked and safe to resume after an interrupted startup.
func (r *accountRepository) BackfillOpenAIOAuthOSProfiles(ctx context.Context) error {
	var cursor int64
	for {
		rows, err := r.client.QueryContext(ctx, `SELECT id FROM accounts WHERE deleted_at IS NULL AND id>$1 AND `+codexTurnStateOwnerExpression("credentials")+` ORDER BY id LIMIT 100`, cursor)
		if err != nil {
			return err
		}
		var ids []int64
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				_ = rows.Close()
				return err
			}
			ids = append(ids, id)
		}
		err = rows.Err()
		_ = rows.Close()
		if err != nil {
			return err
		}
		if len(ids) == 0 {
			return nil
		}
		for _, id := range ids {
			if _, err := r.EnsureOpenAIOAuthOSProfiles(ctx, id); err != nil {
				if errors.Is(err, service.ErrAccountNotFound) || infraerrors.Reason(err) == "OPENAI_OAUTH_OS_PROFILES_UNSUPPORTED" {
					continue // A concurrent account deletion or conversion wins.
				}
				return fmt.Errorf("backfill OpenAI OAuth OS profiles for account %d: %w", id, err)
			}
		}
		cursor = ids[len(ids)-1]
	}
}
