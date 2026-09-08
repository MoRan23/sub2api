package admin

import (
	"context"
	"log/slog"
	"maps"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

func snapshotAdminOpenAIRefreshAccount(account *service.Account) *service.Account {
	snapshot := *account
	snapshot.Credentials = maps.Clone(account.Credentials)
	if account.ProxyID != nil {
		proxyID := *account.ProxyID
		snapshot.ProxyID = &proxyID
	}
	return &snapshot
}

func invalidateAdminOpenAIRefreshToken(ctx context.Context, invalidator service.TokenCacheInvalidator, account *service.Account) {
	if invalidator == nil {
		return
	}
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	if err := invalidator.InvalidateToken(cleanupCtx, account); err != nil {
		slog.Warn("admin_openai_refresh.invalidate_token_failed", "account_id", account.ID, "error", err)
	}
}
