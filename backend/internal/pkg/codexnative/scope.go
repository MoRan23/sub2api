// Package codexnative implements the sampled native Codex HTTP transport.
// Callers explicitly opt OAuth requests in; host names alone never enable it.
package codexnative

import (
	"context"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
)

type Platform string

const (
	Windows Platform = "windows"
	Linux   Platform = "linux"
	MacOS   Platform = "macos"
)

// Scope contains only frozen scalar hints. It must not retain a mutable account
// or a request containing credentials. AccountID is the credential owner ID.
type Scope struct {
	AccountID          int64
	SourceUserAgent    string
	AccountUserAgent   string
	CanonicalUserAgent string
	Purpose            string
}

type scopeKey struct{}

func WithScope(ctx context.Context, scope Scope) context.Context {
	return context.WithValue(ctx, scopeKey{}, scope)
}

// WithoutScope explicitly disables an inherited OAuth scope, e.g. when a retry
// switches to an API-key account. It does not erase unrelated context values.
func WithoutScope(ctx context.Context) context.Context {
	return context.WithValue(ctx, scopeKey{}, struct{}{})
}

func ScopeFromContext(ctx context.Context) (Scope, bool) {
	if ctx == nil {
		return Scope{}, false
	}
	scope, ok := ctx.Value(scopeKey{}).(Scope)
	return scope, ok
}

// Selection is safe to use in a transport cache key. It contains no UA text,
// account metadata, request identity, or proxy credentials.
type Selection struct {
	Platform  Platform
	ProfileID string
	Digest    string
	MatchedBy string
}

func Resolve(finalUA string, scope Scope) Selection {
	candidates := []struct{ ua, source string }{
		{finalUA, "final_user_agent"},
		{scope.SourceUserAgent, "source_user_agent"},
		{scope.AccountUserAgent, "account_user_agent"},
		{scope.CanonicalUserAgent, "canonical_user_agent"},
	}
	for _, candidate := range candidates {
		if platform := platformFromUA(candidate.ua); platform != "" {
			return selection(platform, candidate.source)
		}
	}
	return selection(Linux, "builtin_linux")
}

func platformFromUA(ua string) Platform {
	return Platform(openai.DetectOSFamilyFromUserAgent(ua))
}
