// Package openaicookies manages infrastructure cookies for scoped OpenAI HTTP requests.
package openaicookies

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"time"
)

var (
	ErrInvalidScope     = errors.New("cookie_invalid_scope")
	ErrStaleScope       = errors.New("cookie_stale_scope")
	ErrStoreUnavailable = errors.New("cookie_store_unavailable")
	ErrStoreCorrupt     = errors.New("cookie_store_corrupt")
	ErrConflict         = errors.New("cookie_commit_conflict")
	ErrAttemptClosed    = errors.New("cookie_attempt_closed")
)

// Scope follows the credential authorization, independently of model, proxy or purpose.
// EphemeralID identifies one unbound authorization flow and is never persisted.
type Scope struct {
	OwnerAccountID          int64
	OSFamily                string
	AuthorizationGeneration string
	EphemeralID             string
}

func (s Scope) Persistent() bool {
	return s.OwnerAccountID > 0 && s.EphemeralID == "" &&
		(s.OSFamily == "windows" || s.OSFamily == "linux" || s.OSFamily == "macos") &&
		strings.TrimSpace(s.AuthorizationGeneration) != ""
}

func (s Scope) Valid() bool {
	return s.Persistent() || (s.OwnerAccountID == 0 && s.OSFamily == "" && s.AuthorizationGeneration == "" && strings.TrimSpace(s.EphemeralID) != "")
}

type scopeKey struct{}

func WithScope(ctx context.Context, scope Scope) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, scopeKey{}, scope)
}

// WithoutScope clears a previous account projection without inheriting its cookies.
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

// Entry stores an already normalized cookie. ExpiresAt is absolute: persisted
// cookies are never reconstructed from Max-Age and cannot renew merely on load.
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

// CookieKey is the RFC cookie identity; HostOnly is deliberately not part of it.
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

// Mutation changes one cookie identity. A nil Entry deletes that identity.
type Mutation struct {
	Key             string
	Entry           *Entry
	ExpectedVersion *int64
}

// Snapshot includes value-free mutation versions, so another node's deletion or
// session replacement invalidates a process-local session without sharing its value.
type Snapshot struct {
	Entries  []Entry
	Versions map[string]int64
}

// Store is authoritative for persistent cookies and must fence every operation
// against the current authorization generation. It must return sanitized errors.
type Store interface {
	Load(context.Context, Scope) (Snapshot, error)
	Merge(context.Context, Scope, []Mutation) (map[string]int64, error)
}

type DiagnosticCookie struct {
	Name      string
	ExpiresAt *time.Time
}
type Diagnostic struct {
	Reason       string
	Sent         bool
	Source       string
	Names        []string
	Cookies      []DiagnosticCookie
	SentCount    int
	SavedCount   int
	DeletedCount int
}

type observerKey struct{}

// WithObserver adds a safe request-local callback. Diagnostics never contain values.
func WithObserver(ctx context.Context, observer func(Diagnostic)) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, observerKey{}, observer)
}

func report(ctx context.Context, diagnostic Diagnostic) {
	if observer, ok := ctx.Value(observerKey{}).(func(Diagnostic)); ok && observer != nil {
		observer(diagnostic)
	}
}
