package service

import (
	"context"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openaicookies"
	"github.com/tidwall/gjson"
)

// A candidate belongs to one physical response. The cookie manager performs its
// own authorization/revision CAS; services decide whether this response qualifies.
type codexTurnStateCookieAttempt interface {
	Commit(context.Context) error
	Discard()
	Diagnostic() openaicookies.Diagnostic
}

func observeCodexCookieResponseEvent(a *CodexTurnStateAttempt, payload []byte) {
	if a == nil || !gjson.ValidBytes(payload) {
		return
	}
	root := gjson.ParseBytes(payload)
	kind := strings.TrimSpace(root.Get("type").String())
	status := strings.TrimSpace(root.Get("response.status").String())
	if status == "" && root.Get("object").String() == "response" {
		status = strings.TrimSpace(root.Get("status").String())
	}
	failed := kind == "error" || kind == "response.failed" || kind == "response.incomplete" ||
		status == "failed" || status == "incomplete" || status == "cancelled"
	complete := kind == "response.completed" || (kind == "" && status == "completed")
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.finished {
		a.cookieResponseFailed = a.cookieResponseFailed || failed
		a.cookieResponseComplete = a.cookieResponseComplete || complete
	}
}

func failCodexCookieResponse(a *CodexTurnStateAttempt) {
	if a == nil {
		return
	}
	a.mu.Lock()
	if !a.finished {
		a.cookieResponseFailed = true
	}
	a.mu.Unlock()
}

// finishCookies is deliberately best effort: cookie storage never changes the
// already delivered business response or causes it to be retried.
func (a *CodexTurnStateAttempt) finishCookies(ctx context.Context, delivered, cacheAccepted bool, now time.Time) {
	if a == nil {
		return
	}
	a.mu.Lock()
	staged := a.cookieAttempt
	qualified := delivered && a.cookieResponseComplete && !a.cookieResponseFailed &&
		a.cookieCredentialsBound && a.cookieTarget.Shape == CodexTurnStateShapeTarget && a.cookieTarget.ExpiresAt.After(now) &&
		(!a.Enabled || cacheAccepted)
	a.mu.Unlock()
	if staged == nil {
		return
	}
	defer func() {
		staged.Discard()
		// The generic observer rejects callbacks after Finish. Read the terminal
		// diagnostic directly so only this attempt's metadata can be updated.
		if diagnostic := staged.Diagnostic(); diagnostic.Reason != "" {
			a.mu.Lock()
			a.modelEvidence.headers.CookieDiagnostic = codexCookieDiagnostic(diagnostic)
			a.mu.Unlock()
		}
	}()
	if qualified {
		_ = staged.Commit(ctx)
	}
}

func (r *CodexTurnStateCollectResult) commitCookies(ctx context.Context) {
	if r == nil || r.cookieAttempt == nil || !r.completed || r.StatusCode < 200 || r.StatusCode >= 300 || r.ModelMismatch() {
		return
	}
	_ = r.cookieAttempt.Commit(ctx)
	if diagnostic := r.cookieAttempt.Diagnostic(); diagnostic.Reason != "" {
		r.ModelEvidence.CookieDiagnostic = codexCookieDiagnostic(diagnostic)
	}
}

func (r *CodexTurnStateCollectResult) discardCookies() {
	if r != nil && r.cookieAttempt != nil {
		r.cookieAttempt.Discard()
		if diagnostic := r.cookieAttempt.Diagnostic(); diagnostic.Reason != "" {
			r.ModelEvidence.CookieDiagnostic = codexCookieDiagnostic(diagnostic)
		}
	}
}
