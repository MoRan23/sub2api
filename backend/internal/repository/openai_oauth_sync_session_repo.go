package repository

import (
	"context"
	stdsql "database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/account"
	"github.com/Wei-Shaw/sub2api/ent/openaioauthsyncsession"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
)

type openAIOAuthSyncSessionRepository struct {
	client *ent.Client
}

// NewOpenAIOAuthSyncSessionRepository creates the account-scoped sync-session
// repository. The Ent upsert uses a unique account_id conflict target, so the
// first UUID wins under concurrent requests and every caller reads that UUID.
func NewOpenAIOAuthSyncSessionRepository(client *ent.Client) service.OAuthSyncSessionRepository {
	return &openAIOAuthSyncSessionRepository{client: client}
}

func (r *openAIOAuthSyncSessionRepository) GetOrCreateOAuthSyncSession(ctx context.Context, accountID int64) (string, error) {
	if r == nil || r.client == nil {
		return "", fmt.Errorf("nil OpenAI OAuth sync-session repository")
	}
	if accountID <= 0 {
		return "", fmt.Errorf("invalid OAuth account id %d", accountID)
	}
	if _, err := r.client.Account.Query().Where(account.IDEQ(accountID)).Only(ctx); err != nil {
		if ent.IsNotFound(err) {
			return "", fmt.Errorf("OAuth account %d does not exist", accountID)
		}
		return "", fmt.Errorf("validate OAuth account %d: %w", accountID, err)
	}

	// Read first so existing rows avoid an unnecessary write. This also makes
	// the common path independent of INSERT ... ON CONFLICT result semantics.
	if existing, err := r.GetOAuthSyncSession(ctx, accountID); err == nil {
		if strings.TrimSpace(existing) == "" {
			return "", fmt.Errorf("OAuth sync session row for account %d has empty session_id", accountID)
		}
		return existing, nil
	} else if !isOAuthSyncNotFound(err) {
		return "", diagnoseOAuthSyncSessionError(accountID, "read", err)
	}

	candidate, err := uuid.NewV7()
	if err != nil {
		return "", fmt.Errorf("generate OAuth sync session id: %w", err)
	}
	// Use a driver-level upsert so PostgreSQL always affects one row. Ent's
	// implicit RETURNING scan with DO NOTHING yielded sql.ErrNoRows on the
	// conflict path and caused the account-test failure; the driver Exec avoids
	// that scan while preserving the first session_id.
	if _, err := r.client.ExecContext(ctx, `
		INSERT INTO openai_oauth_sync_sessions (account_id, session_id, created_at, updated_at)
		VALUES ($1, $2, NOW(), NOW())
		ON CONFLICT (account_id) DO UPDATE SET updated_at = NOW()`, accountID, candidate.String()); err != nil {
		return "", diagnoseOAuthSyncSessionError(accountID, "create", err)
	}

	// The conflict target is account_id, therefore a concurrent initializer can
	// safely be followed by a read. If the row disappeared or the migration is
	// missing, return an actionable error instead of raw sql.ErrNoRows.
	root, err := r.GetOAuthSyncSession(ctx, accountID)
	if err != nil {
		if isOAuthSyncNotFound(err) {
			return "", fmt.Errorf("OAuth sync session row missing for account %d after initialization; verify migration 238_openai_oauth_sync_sessions.sql is applied: %w", accountID, err)
		}
		return "", diagnoseOAuthSyncSessionError(accountID, "verify", err)
	}
	if strings.TrimSpace(root) == "" {
		return "", fmt.Errorf("OAuth sync session row for account %d has empty session_id", accountID)
	}
	return root, nil
}

func isOAuthSyncNotFound(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, stdsql.ErrNoRows) || ent.IsNotFound(err)
}

func diagnoseOAuthSyncSessionError(accountID int64, operation string, err error) error {
	if err == nil {
		return nil
	}
	message := strings.ToLower(err.Error())
	if strings.Contains(message, "does not exist") || strings.Contains(message, "undefined table") || strings.Contains(message, "no such table") {
		return fmt.Errorf("OAuth sync session %s failed for account %d: migration 238_openai_oauth_sync_sessions.sql is missing: %w", operation, accountID, err)
	}
	if ent.IsConstraintError(err) {
		return fmt.Errorf("OAuth sync session %s violated database constraints for account %d (check account ownership and migration 238): %w", operation, accountID, err)
	}
	return fmt.Errorf("OAuth sync session %s failed for account %d: %w", operation, accountID, err)
}

func (r *openAIOAuthSyncSessionRepository) GetOAuthSyncSession(ctx context.Context, accountID int64) (string, error) {
	if r == nil || r.client == nil {
		return "", fmt.Errorf("nil OpenAI OAuth sync-session repository")
	}
	row, err := r.client.OpenAIOAuthSyncSession.Query().
		Where(openaioauthsyncsession.AccountIDEQ(accountID)).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return "", err
		}
		return "", err
	}
	return row.SessionID, nil
}

func (r *openAIOAuthSyncSessionRepository) DeleteOAuthSyncSession(ctx context.Context, accountID int64) error {
	if r == nil || r.client == nil {
		return fmt.Errorf("nil OpenAI OAuth sync-session repository")
	}
	_, err := r.client.OpenAIOAuthSyncSession.Delete().
		Where(openaioauthsyncsession.AccountIDEQ(accountID)).
		Exec(ctx)
	return err
}
