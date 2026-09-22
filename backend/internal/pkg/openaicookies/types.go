// Package openaicookies manages infrastructure cookies for scoped OpenAI HTTP requests.
package openaicookies

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"
)

var (
	ErrInvalidScope       = errors.New("cookie_invalid_scope")
	ErrStaleScope         = errors.New("cookie_stale_scope")
	ErrStoreUnavailable   = errors.New("cookie_store_unavailable")
	ErrStoreCorrupt       = errors.New("cookie_store_corrupt")
	ErrConflict           = errors.New("cookie_commit_conflict")
	ErrAttemptClosed      = errors.New("cookie_attempt_closed")
	ErrNoSnapshot         = errors.New("cookie_snapshot_unavailable")
	ErrBundleExpired      = errors.New("cookie_bundle_expired")
	ErrBundleInvalid      = errors.New("cookie_bundle_invalid")
	ErrBundleSendRejected = errors.New("cookie_bundle_send_rejected")
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
	ctx = context.WithValue(ctx, scopeKey{}, scope)
	if scope.EphemeralID != "" {
		// A new unbound authorization flow cannot inherit a prior account's
		// bundle, feature-disable marker, or request-specific restoration.
		ctx = context.WithValue(ctx, bundleKey{}, struct{}{})
		ctx = context.WithValue(ctx, guardKey{}, struct{}{})
		ctx = context.WithValue(ctx, fallbackKey{}, struct{}{})
		ctx = context.WithValue(ctx, rejectedSendKey{}, struct{}{})
	}
	return ctx
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

// Bundle is a frozen cookie snapshot attached to one accepted turn-state ticket.
// It is persisted only inside that ticket's encrypted, atomic state record. Its
// lifetime never exceeds the ticket's local lifetime, including session cookies.
type Bundle struct {
	Entries   []Entry   `json:"entries"`
	ExpiresAt time.Time `json:"expires_at"`
}

const BundleLifetime = 240 * time.Second

func (b Bundle) Clone() Bundle {
	b.Entries = append([]Entry(nil), b.Entries...)
	return b
}

// Fresh identifies an explicit empty starting snapshot, not a saved ticket bundle.
func (b Bundle) Fresh() bool { return len(b.Entries) == 0 && b.ExpiresAt.IsZero() }

// ValidAt rejects the entire bundle if any cookie is expired or malformed. A
// valid accepted ticket may carry an empty cookie set, with a nonzero deadline.
func (b Bundle) ValidAt(now time.Time) bool {
	if b.ExpiresAt.IsZero() || !b.ExpiresAt.After(now) {
		return false
	}
	seen := make(map[string]bool, len(b.Entries))
	for _, entry := range b.Entries {
		if !entry.Valid() || (!AllowedURL(&url.URL{Scheme: "https", Host: entry.Domain}) && entry.Domain != "openai.com") ||
			entry.ExpiresAt.IsZero() || entry.ExpiresAt.Before(b.ExpiresAt) || !entry.ExpiresAt.After(now) || seen[entry.Key] {
			return false
		}
		seen[entry.Key] = true
	}
	return true
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

// mutation applies one response cookie to a request-local candidate. There is no
// independent cookie-store API: persistence belongs to the atomic ticket bundle.
type mutation struct {
	Key   string
	Entry *Entry
}

type DiagnosticCookie struct {
	Name      string
	ExpiresAt *time.Time
}
type Diagnostic struct {
	Reason string
	// SendState describes reaching the HTTP RoundTrip boundary, not network success.
	// Empty means unknown; rejected preflight sends report "not_sent".
	SendState    string
	Sent         bool
	Source       string
	Names        []string
	Cookies      []DiagnosticCookie
	SentCount    int
	SavedCount   int
	DeletedCount int
}

type observerKey struct{}
type sendObserverKey struct{}

// WithObserver adds a safe request-local callback. Diagnostics never contain values.
func WithObserver(ctx context.Context, observer func(Diagnostic)) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, observerKey{}, observer)
}

// WithSendObserver observes the final request after bundle validation and cookie
// application, immediately before RoundTrip. It is never called for a rejected
// send or a websocket upgrade. The callback must not retain or mutate the request.
func WithSendObserver(ctx context.Context, observer func(*http.Request)) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, sendObserverKey{}, observer)
}

// NeedsTransportObserver keeps disabled cookie flows observable without enabling
// cookie replacement or response capture.
func NeedsTransportObserver(request *http.Request) bool {
	if request == nil || websocketUpgrade(request) {
		return false
	}
	observer, _ := request.Context().Value(observerKey{}).(func(Diagnostic))
	sendObserver, _ := request.Context().Value(sendObserverKey{}).(func(*http.Request))
	return observer != nil || sendObserver != nil
}

func report(ctx context.Context, diagnostic Diagnostic) {
	if observer, ok := ctx.Value(observerKey{}).(func(Diagnostic)); ok && observer != nil {
		observer(diagnostic)
	}
}
