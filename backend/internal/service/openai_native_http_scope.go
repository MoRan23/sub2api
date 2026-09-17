package service

import (
	"context"
	"net/http"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/codexnative"
)

// WithOpenAINativeHTTPScope freezes platform hints for a known Codex OAuth HTTP
// operation. It never changes a request's actual User-Agent. Callers with a
// shadow account should pass its resolved credential owner. A nil account is
// reserved for explicit authentication operations before an account exists.
func WithOpenAINativeHTTPScope(ctx context.Context, account *Account, sourceUA string) context.Context {
	if account != nil && !account.UsesOpenAICodexProtocol() {
		return codexnative.WithoutScope(ctx)
	}
	scope, _ := codexnative.ScopeFromContext(ctx)
	if sourceUA = strings.TrimSpace(sourceUA); sourceUA != "" {
		scope.SourceUserAgent = sourceUA
	}
	if account != nil {
		scope.AccountID = account.ID
		if account.IsShadow() {
			// Never use a shadow-local identity when the owner wasn't supplied.
			scope.AccountID = *account.ParentAccountID
			scope.AccountUserAgent = ""
		} else {
			scope.AccountUserAgent = strings.TrimSpace(account.GetOpenAIUserAgent())
		}
	}
	if scope.CanonicalUserAgent == "" {
		scope.CanonicalUserAgent = CodexCanonicalUserAgent()
	}
	if scope.Purpose == "" {
		scope.Purpose = "oauth"
		if account == nil {
			scope.Purpose = "auth"
		}
	}
	return codexnative.WithScope(ctx, scope)
}

func withOpenAINativeHTTPRequestScope(request *http.Request, account *Account, repo AccountRepository, purpose string) *http.Request {
	if request == nil {
		return request
	}
	if account == nil || !account.UsesOpenAICodexProtocol() {
		return request.WithContext(codexnative.WithoutScope(request.Context()))
	}
	return request.WithContext(withOpenAINativeHTTPAccountScope(request.Context(), account, repo, purpose))
}

func withOpenAINativeHTTPAccountScope(ctx context.Context, account *Account, repo AccountRepository, purpose string) context.Context {
	credentialAccount := account
	if account.IsShadow() && repo != nil {
		if owner, err := resolveCredentialAccount(ctx, repo, account); err == nil && owner != nil {
			credentialAccount = owner
		}
	}
	ctx = WithOpenAINativeHTTPScope(ctx, credentialAccount, "")
	scope, _ := codexnative.ScopeFromContext(ctx)
	scope.Purpose = purpose
	return codexnative.WithScope(ctx, scope)
}
