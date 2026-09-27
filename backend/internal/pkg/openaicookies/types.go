// Package openaicookies provides private, temporary cookie containers for
// unbound OAuth authorization flows. It does not pool account cookies.
package openaicookies

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"
	"time"
)

// Scope marks the request's authorization context. Bound scopes prevent a
// refresh or auxiliary request from inheriting an unbound flow's cookie jar.
// Only EphemeralID scopes are eligible for cookie handling.
type Scope struct {
	OwnerAccountID          int64
	OSFamily                string
	AuthorizationGeneration string
	EphemeralID             string
}

func (s Scope) Valid() bool {
	if s.EphemeralID != "" {
		return s.OwnerAccountID == 0 && s.OSFamily == "" && s.AuthorizationGeneration == "" && strings.TrimSpace(s.EphemeralID) != ""
	}
	return s.OwnerAccountID > 0 &&
		(s.OSFamily == "windows" || s.OSFamily == "linux" || s.OSFamily == "macos") &&
		strings.TrimSpace(s.AuthorizationGeneration) != ""
}

type scopeKey struct{}

func WithScope(ctx context.Context, scope Scope) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, scopeKey{}, scope)
}

// WithoutScope prevents a request from inheriting another authorization flow.
func WithoutScope(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, scopeKey{}, struct{}{})
}

func ScopeFromContext(ctx context.Context) (Scope, bool) {
	if ctx == nil {
		return Scope{}, false
	}
	scope, ok := ctx.Value(scopeKey{}).(Scope)
	return scope, ok
}

// Entry is a normalized, in-memory cookie with an absolute expiration.
type Entry struct {
	Key       string
	Name      string
	Value     string
	Domain    string
	Path      string
	HostOnly  bool
	Secure    bool
	HTTPOnly  bool
	Quoted    bool
	SameSite  http.SameSite
	ExpiresAt time.Time
	CreatedAt time.Time
	UpdatedAt time.Time
}

func CookieKey(name, domain, path string) string {
	digest := sha256.Sum256([]byte(name + "\x00" + domain + "\x00" + path))
	return hex.EncodeToString(digest[:])
}

func (e Entry) Valid() bool {
	if !AllowedName(e.Name) || e.Domain == "" || e.Domain != strings.ToLower(e.Domain) || strings.ContainsAny(e.Domain, " /:;\r\n\t") || strings.HasPrefix(e.Domain, ".") || strings.HasSuffix(e.Domain, ".") || !strings.HasPrefix(e.Path, "/") || e.Key != CookieKey(e.Name, e.Domain, e.Path) {
		return false
	}
	return e.cookie().Valid() == nil
}

func (e Entry) cookie() *http.Cookie {
	domain := e.Domain
	if e.HostOnly {
		domain = ""
	}
	return &http.Cookie{Name: e.Name, Value: e.Value, Domain: domain, Path: e.Path, Secure: e.Secure, HttpOnly: e.HTTPOnly, Quoted: e.Quoted, SameSite: e.SameSite, Expires: e.ExpiresAt}
}

type mutation struct {
	Key   string
	Entry *Entry
}
