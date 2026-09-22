package openaicookies

import (
	"context"
	"net/http"
	"sort"
	"sync"
	"time"
)

type attemptKey struct{}
type bundleKey struct{}
type guardKey struct{}
type fallbackKey struct{}
type rejectedSendKey struct{}
type rejectedSendPolicy struct{ cause error }
type bundlePolicy struct {
	bundle Bundle
	bypass bool
}

// WithBundle explicitly enables replacement from this snapshot for one HTTP
// attempt. Bundle{} starts without saved cookies, as required for collectors.
// No account-wide jar or caller Cookie header is imported into this snapshot.
func WithBundle(ctx context.Context, bundle Bundle) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx = context.WithValue(ctx, bundleKey{}, bundlePolicy{bundle: bundle.Clone()})
	// Selecting a new bundle (especially an independent collector's empty base)
	// must not inherit another operation's authorization guard or restoration.
	ctx = context.WithValue(ctx, guardKey{}, struct{}{})
	ctx = context.WithValue(ctx, rejectedSendKey{}, struct{}{})
	return context.WithValue(ctx, fallbackKey{}, struct{}{})
}

// Bypass preserves the request exactly, including headers. Gateway header
// allowlists remain responsible for excluding untrusted client cookies.
func Bypass(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, bundleKey{}, bundlePolicy{bypass: true})
}

// EnabledForRequest lets a client preserve its original policy when cookie
// handling is bypassed. Static wrappers use the same decision in RoundTrip.
func EnabledForRequest(request *http.Request) bool {
	if request == nil || websocketUpgrade(request) {
		return false
	}
	policy, enabled := request.Context().Value(bundleKey{}).(bundlePolicy)
	if policy.bypass {
		return false
	}
	scope, _ := ScopeFromContext(request.Context())
	return enabled || scope.Valid() && scope.EphemeralID != ""
}

// WithSendGuard runs on a request clone just before an enabled physical send,
// before cookie replacement. The service checks authoritative account state;
// false leaves no response candidate for publication. WithRejectedSendError
// aborts the send; otherwise WithFallback restores the legacy request.
func WithSendGuard(ctx context.Context, guard func(*http.Request) bool) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, guardKey{}, guard)
}

// WithRejectedSendError requires an enabled bundle to pass every send-time check
// before calling the underlying transport. Apply it after WithBundle. A rejected
// send never invokes WithFallback: callers must rebuild the complete baseline
// request themselves. Passing nil clears the strict policy.
//
// Rejections match ErrBundleSendRejected and the supplied error with errors.Is;
// their public error text is fixed and never includes request or cookie values.
func WithRejectedSendError(ctx context.Context, cause error) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, rejectedSendKey{}, rejectedSendPolicy{cause: cause})
}

type bundleSendRejectedError struct{ cause error }

func (e bundleSendRejectedError) Error() string        { return ErrBundleSendRejected.Error() }
func (e bundleSendRejectedError) Is(target error) bool { return target == ErrBundleSendRejected }
func (e bundleSendRejectedError) Unwrap() error        { return e.cause }

func rejectedBundleSend(ctx context.Context) error {
	policy, ok := ctx.Value(rejectedSendKey{}).(rejectedSendPolicy)
	if !ok || policy.cause == nil {
		return nil
	}
	return bundleSendRejectedError{cause: policy.cause}
}

// WithFallback restores only the feature-owned ticket changes when the guarded
// bundle cannot be sent. It runs on the current physical request clone so later
// authorization and unrelated body/header normalization remain untouched.
func WithFallback(ctx context.Context, restore func(*http.Request)) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, fallbackKey{}, restore)
}

// Attempt holds one response's candidate only in request-local memory. Snapshot
// never writes a cookie store: the state repository publishes ticket and cookies
// atomically after validating the successful target response.
type Attempt struct {
	mu         sync.Mutex
	scope      Scope
	bound      bool
	sequence   uint64
	closed     bool
	published  bool
	captured   bool
	entries    []Entry
	receivedAt time.Time
	now        func() time.Time
	diagnostic Diagnostic
}

func WithAttempt(ctx context.Context) (context.Context, *Attempt) {
	if ctx == nil {
		ctx = context.Background()
	}
	attempt := &Attempt{}
	return context.WithValue(ctx, attemptKey{}, attempt), attempt
}

func attemptFromContext(ctx context.Context) *Attempt {
	attempt, _ := ctx.Value(attemptKey{}).(*Attempt)
	return attempt
}

func (a *Attempt) invalidate() {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.sequence++
	a.captured, a.entries, a.diagnostic = false, nil, Diagnostic{}
}

func (a *Attempt) begin(scope Scope, now func() time.Time) uint64 {
	if a == nil {
		return 0
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return 0
	}
	a.sequence++
	a.captured, a.entries, a.diagnostic = false, nil, Diagnostic{}
	if a.bound && a.scope != scope {
		a.closed = true
		a.diagnostic.Reason = ErrInvalidScope.Error()
		return 0
	}
	a.scope, a.bound, a.now = scope, true, now
	return a.sequence
}

func (a *Attempt) capture(sequence uint64, entries []Entry, receivedAt time.Time, diagnostic Diagnostic) {
	if a == nil || sequence == 0 {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.closed && a.sequence == sequence {
		a.captured = true
		a.entries = append([]Entry(nil), entries...)
		a.receivedAt = receivedAt
		a.diagnostic = cloneDiagnostic(diagnostic)
		a.diagnostic.Reason = "cookie_staged"
	}
}

// Snapshot returns an independent candidate, including a valid empty cookie set.
// A caller must still validate the target and commit this with its token in one
// state CAS. Session cookies gain an absolute ticket-bounded deadline; inherited
// cookie deadlines are never extended by a later accepted target ticket.
func (a *Attempt) Snapshot(ticketExpiresAt time.Time) (Bundle, error) {
	if a == nil {
		return Bundle{}, ErrNoSnapshot
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return Bundle{}, ErrAttemptClosed
	}
	if !a.captured || a.now == nil {
		return Bundle{}, ErrNoSnapshot
	}
	now := a.now()
	deadline := ticketExpiresAt
	if limit := a.receivedAt.Add(BundleLifetime); deadline.After(limit) {
		deadline = limit
	}
	if deadline.IsZero() || !deadline.After(now) {
		return Bundle{}, ErrBundleExpired
	}
	result := Bundle{Entries: append([]Entry(nil), a.entries...), ExpiresAt: deadline}
	for index := range result.Entries {
		entry := &result.Entries[index]
		if !entry.Valid() {
			return Bundle{}, ErrBundleInvalid
		}
		if entry.ExpiresAt.IsZero() || entry.ExpiresAt.After(deadline) {
			entry.ExpiresAt = deadline
		}
		if !entry.ExpiresAt.After(now) {
			return Bundle{}, ErrBundleExpired
		}
		if entry.ExpiresAt.Before(result.ExpiresAt) {
			result.ExpiresAt = entry.ExpiresAt
		}
	}
	sort.Slice(result.Entries, func(i, j int) bool { return result.Entries[i].Key < result.Entries[j].Key })
	if !result.ValidAt(now) {
		return Bundle{}, ErrBundleInvalid
	}
	return result, nil
}

func (a *Attempt) Diagnostic() Diagnostic {
	if a == nil {
		return Diagnostic{}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return cloneDiagnostic(a.diagnostic)
}

// MarkPublished records only a successful atomic ticket-state CAS. It does not
// persist anything and cannot turn an uncaptured or discarded attempt into a
// successful cookie publication.
func (a *Attempt) MarkPublished() {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.closed && a.captured {
		a.published = true
		a.diagnostic.Reason = "cookie_bundle_saved"
	}
}

func cloneDiagnostic(diagnostic Diagnostic) Diagnostic {
	diagnostic.Names = append([]string(nil), diagnostic.Names...)
	diagnostic.Cookies = append([]DiagnosticCookie(nil), diagnostic.Cookies...)
	for index := range diagnostic.Cookies {
		if diagnostic.Cookies[index].ExpiresAt != nil {
			expires := *diagnostic.Cookies[index].ExpiresAt
			diagnostic.Cookies[index].ExpiresAt = &expires
		}
	}
	return diagnostic
}

// Discard releases all request-local secret values without changing stored state.
func (a *Attempt) Discard() {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.closed && a.captured && !a.published {
		a.diagnostic.Reason = "cookie_target_rejected"
	}
	a.closed, a.captured, a.entries = true, false, nil
}
