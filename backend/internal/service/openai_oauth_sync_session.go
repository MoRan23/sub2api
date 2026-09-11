package service

import "context"

// OAuthSyncSessionRepository stores the one stable synchronous root session
// owned by an OpenAI OAuth account. The operation must be atomic across
// processes and return the winner when multiple callers race to initialize it.
type OAuthSyncSessionRepository interface {
	GetOrCreateOAuthSyncSession(ctx context.Context, accountID int64) (string, error)
	GetOAuthSyncSession(ctx context.Context, accountID int64) (string, error)
	DeleteOAuthSyncSession(ctx context.Context, accountID int64) error
}
