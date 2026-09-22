package openaicookies

import (
	"context"
	"sync"
)

type attemptKey struct{}

// Attempt stages one HTTP response's cookies until its caller accepts the target
// ticket. It must not be shared by separate physical retries. It contains secret
// values in memory only and must never be serialized or logged.
type Attempt struct {
	mu         sync.Mutex
	manager    *Manager
	scope      Scope
	sequence   uint64
	closed     bool
	result     error
	pending    *candidate
	diagnostic Diagnostic
}

type candidate struct {
	changes    []Mutation
	diagnostic Diagnostic
	observer   context.Context
}

// WithAttempt creates a fresh acceptance boundary, overriding any prior attempt.
// Without an attempt, bound auxiliary requests can send saved cookies but cannot
// change the cookie pool. Unbound OAuth flows remain isolated in temporary memory.
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

func (a *Attempt) begin(manager *Manager, scope Scope) uint64 {
	if a == nil {
		return 0
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return 0
	}
	a.sequence++
	a.pending = nil
	a.diagnostic = Diagnostic{}
	if a.manager != nil && (a.manager != manager || a.scope != scope) {
		a.closed, a.result = true, ErrInvalidScope
		return 0
	}
	a.manager, a.scope = manager, scope
	return a.sequence
}

func (a *Attempt) stage(sequence uint64, pending candidate) {
	if a == nil || sequence == 0 {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.closed && a.sequence == sequence {
		a.pending = &pending
		a.diagnostic = cloneDiagnostic(pending.diagnostic)
		a.diagnostic.Reason = "cookie_staged"
	}
}

// Diagnostic returns metadata for this response and its commit result, never
// cookie values. Callers may attach it after their completion callback returns.
func (a *Attempt) Diagnostic() Diagnostic {
	if a == nil {
		return Diagnostic{}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return cloneDiagnostic(a.diagnostic)
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

// Commit is called only after a valid target ticket has been accepted. With a
// state cache, its publication CAS must have succeeded first. The cookie store
// independently fences authorization and every identity revision read before the
// request; a late response cannot overwrite a newer accepted response's cookies.
// Repeated calls return the original result and never repeat a write.
func (a *Attempt) Commit(ctx context.Context) error {
	if a == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	a.mu.Lock()
	if a.closed {
		err := a.result
		a.mu.Unlock()
		return err
	}
	a.closed = true
	pending := a.pending
	a.pending = nil
	if pending == nil {
		a.mu.Unlock()
		return nil
	}
	saved, deleted, err := a.manager.merge(ctx, a.scope, pending.changes)
	a.result = err
	diagnostic := pending.diagnostic
	if err != nil {
		diagnostic.Reason = safeReason(err)
	} else {
		diagnostic.Reason = "cookie_updated"
		diagnostic.SavedCount, diagnostic.DeletedCount = saved, deleted
	}
	a.diagnostic = cloneDiagnostic(diagnostic)
	a.mu.Unlock()
	report(pending.observer, diagnostic)
	return err
}

// Discard releases staged secret values without changing any saved cookie.
func (a *Attempt) Discard() {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.closed {
		a.closed, a.result = true, ErrAttemptClosed
		if a.pending != nil {
			a.diagnostic.Reason = "cookie_target_rejected"
		}
	}
	a.pending = nil
}
