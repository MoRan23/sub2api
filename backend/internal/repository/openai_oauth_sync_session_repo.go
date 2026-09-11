package repository

import (
	"context"
	"fmt"

	"github.com/Wei-Shaw/sub2api/ent"
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
	candidate, err := uuid.NewV7()
	if err != nil {
		return "", fmt.Errorf("generate OAuth sync session id: %w", err)
	}
	if err := r.client.OpenAIOAuthSyncSession.Create().
		SetAccountID(accountID).
		SetSessionID(candidate.String()).
		OnConflictColumns(openaioauthsyncsession.FieldAccountID).
		DoNothing().
		Exec(ctx); err != nil {
		return "", fmt.Errorf("create OAuth sync session for account %d: %w", accountID, err)
	}
	return r.GetOAuthSyncSession(ctx, accountID)
}

func (r *openAIOAuthSyncSessionRepository) GetOAuthSyncSession(ctx context.Context, accountID int64) (string, error) {
	if r == nil || r.client == nil {
		return "", fmt.Errorf("nil OpenAI OAuth sync-session repository")
	}
	row, err := r.client.OpenAIOAuthSyncSession.Query().
		Where(openaioauthsyncsession.AccountIDEQ(accountID)).
		Only(ctx)
	if err != nil {
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
