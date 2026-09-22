package service

import (
	"context"
	"net/http"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/codexnative"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openaicookies"
)

// WithOpenAINativeHTTPScope freezes platform hints for a known Codex OAuth HTTP
// operation. It never changes a request's actual User-Agent. Callers with a
// shadow account should pass its resolved credential owner. A nil account is
// reserved for explicit authentication operations before an account exists.
func WithOpenAINativeHTTPScope(ctx context.Context, account *Account, sourceUA string) context.Context {
	if account != nil && !account.UsesOpenAICodexProtocol() {
		return openaicookies.WithoutScope(codexnative.WithoutScope(ctx))
	}
	ctx = withOpenAIHTTPCookieAccountScope(ctx, account)
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
		return request.WithContext(openaicookies.WithoutScope(codexnative.WithoutScope(request.Context())))
	}
	return request.WithContext(withOpenAINativeHTTPAccountScope(request.Context(), account, repo, purpose))
}

// Cookie identity follows the credential snapshot that supplied the bearer, not
// a UA guess or the default slot of a later account reload. A nil account keeps
// an explicitly isolated authorization flow supplied by its caller.
func withOpenAIHTTPCookieAccountScope(ctx context.Context, account *Account) context.Context {
	if account == nil {
		return ctx
	}
	if !RequiresOpenAIOAuthOSAuthorization(account) || account.OpenAIOAuthCredentialOwnerID <= 0 ||
		NormalizeOpenAIOSFamily(account.OpenAIOAuthCredentialOS) == "" || account.OpenAIOAuthAuthorizationGeneration == "" {
		return openaicookies.WithoutScope(ctx)
	}
	return openaicookies.WithScope(ctx, openaicookies.Scope{
		OwnerAccountID:          account.OpenAIOAuthCredentialOwnerID,
		OSFamily:                account.OpenAIOAuthCredentialOS,
		AuthorizationGeneration: account.OpenAIOAuthAuthorizationGeneration,
	})
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
