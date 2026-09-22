package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openaicookies"
	"github.com/tidwall/gjson"
)

// A candidate belongs to one physical response. It is encrypted and published
// with the ticket by the state repository, never committed to a separate jar.
type codexTurnStateCookieAttempt interface {
	Snapshot(time.Time) (openaicookies.Bundle, error)
	MarkPublished()
	Discard()
	Diagnostic() openaicookies.Diagnostic
}

type codexTurnStateCookiePublication struct {
	BundleBinding           CodexTurnStateBundleBinding
	SourceOS                string
	EncryptedCookieBundle   string
	CookieBundleExpiresAt   *time.Time
	AuthorizationGeneration string
}

type codexTurnStateCookieEnvelope struct {
	Binding                 CodexTurnStateBundleBinding `json:"binding"`
	Version                 int                         `json:"version"`
	OwnerAccountID          int64                       `json:"owner_account_id"`
	Model                   string                      `json:"model"`
	AuthorizationGeneration string                      `json:"authorization_generation"`
	Bundle                  openaicookies.Bundle        `json:"bundle"`
	ResponseEvidence        CodexModelEvidence          `json:"response_evidence"`
}

func applyCodexTurnStateCookiePublication(record *CodexTurnStateRecord, publication codexTurnStateCookiePublication) {
	record.EncryptedCookieBundle = publication.EncryptedCookieBundle
	record.CookieBundleExpiresAt = publication.CookieBundleExpiresAt
	record.AuthorizationGeneration = publication.AuthorizationGeneration
	record.BundleBinding = publication.BundleBinding
}

var errCodexCookieAdmission = errors.New("cookie_target_rejected")

func (s *CodexTurnStateService) encryptCodexCookiePublication(key CodexTurnStateKey, authorization string, bundle openaicookies.Bundle, binding CodexTurnStateBundleBinding, evidence ...CodexModelEvidence) (codexTurnStateCookiePublication, error) {
	if s == nil || s.encryptor == nil || !binding.Valid() || !bundle.ValidAt(s.now()) {
		return codexTurnStateCookiePublication{}, errCodexCookieAdmission
	}
	envelope := codexTurnStateCookieEnvelope{Version: 2, OwnerAccountID: key.OwnerAccountID, Model: key.Model, AuthorizationGeneration: authorization, Bundle: bundle, Binding: binding}
	if len(evidence) > 0 {
		envelope.ResponseEvidence = evidence[0]
	}
	value, err := json.Marshal(envelope)
	if err != nil {
		return codexTurnStateCookiePublication{}, errCodexCookieAdmission
	}
	encrypted, err := s.encryptor.Encrypt(string(value))
	if err != nil {
		return codexTurnStateCookiePublication{}, openaicookies.ErrStoreUnavailable
	}
	expiresAt := bundle.ExpiresAt
	return codexTurnStateCookiePublication{EncryptedCookieBundle: encrypted, CookieBundleExpiresAt: &expiresAt, AuthorizationGeneration: authorization, BundleBinding: binding}, nil
}

func (s *CodexTurnStateService) emptyCodexCookiePublication(key CodexTurnStateKey, authorization string, expiresAt time.Time, binding CodexTurnStateBundleBinding) (codexTurnStateCookiePublication, error) {
	return s.encryptCodexCookiePublication(key, authorization, openaicookies.Bundle{Entries: []openaicookies.Entry{}, ExpiresAt: expiresAt}, binding)
}

func (s *CodexTurnStateService) prepareBusinessCookiePublication(a *CodexTurnStateAttempt, expiresAt time.Time) (codexTurnStateCookiePublication, error) {
	if a == nil || !a.Enabled {
		return codexTurnStateCookiePublication{}, errCodexCookieAdmission
	}
	a.mu.Lock()
	staged := a.cookieAttempt
	evidence := a.modelEvidence.snapshot(a.Model)
	qualified := a.historyDelivered && a.cookieResponseComplete && !a.cookieResponseFailed && a.cookieCredentialsBound
	a.mu.Unlock()
	// A source without Cookie candidates can only contribute an empty bundle.
	// Native WS is bypassed at its entry point and never reaches publication.
	if staged == nil {
		publication, err := s.encryptCodexCookiePublication(a.key, a.AuthorizationGeneration, openaicookies.Bundle{ExpiresAt: expiresAt}, a.OutboundBinding, evidence)
		publication.SourceOS = a.OSFamily
		return publication, err
	}
	if !qualified {
		return codexTurnStateCookiePublication{}, errCodexCookieAdmission
	}
	bundle, err := staged.Snapshot(expiresAt)
	if err != nil {
		return codexTurnStateCookiePublication{}, err
	}
	publication, err := s.encryptCodexCookiePublication(a.key, a.AuthorizationGeneration, bundle, a.OutboundBinding, evidence)
	publication.SourceOS = a.OSFamily
	return publication, err
}

func (s *CodexTurnStateService) prepareCollectorCookiePublication(key CodexTurnStateKey, authorization string, result *CodexTurnStateCollectResult, expiresAt time.Time) (codexTurnStateCookiePublication, error) {
	if result == nil || !result.completed || result.StatusCode < 200 || result.StatusCode >= 300 || result.ModelMismatch() {
		return codexTurnStateCookiePublication{}, errCodexCookieAdmission
	}
	if result.cookieAttempt == nil {
		return s.encryptCodexCookiePublication(key, authorization, openaicookies.Bundle{ExpiresAt: expiresAt}, result.BundleBinding, result.ModelEvidence)
	}
	bundle, err := result.cookieAttempt.Snapshot(expiresAt)
	if err != nil {
		return codexTurnStateCookiePublication{}, err
	}
	return s.encryptCodexCookiePublication(key, authorization, bundle, result.BundleBinding, result.ModelEvidence)
}

func (s *CodexTurnStateService) codexCookieBundleForSnapshot(a *CodexTurnStateAttempt) (openaicookies.Bundle, error) {
	if a == nil || a.Snapshot.Token == "" {
		return openaicookies.Bundle{}, nil
	}
	if s == nil || s.encryptor == nil || a.Snapshot.EncryptedCookieBundle == "" {
		return openaicookies.Bundle{}, openaicookies.ErrBundleInvalid
	}
	plain, err := s.encryptor.Decrypt(a.Snapshot.EncryptedCookieBundle)
	if err != nil {
		return openaicookies.Bundle{}, openaicookies.ErrBundleInvalid
	}
	var envelope codexTurnStateCookieEnvelope
	if json.Unmarshal([]byte(plain), &envelope) != nil || envelope.Version != 2 || !envelope.Binding.Valid() || envelope.Binding != a.Snapshot.BundleBinding || envelope.Binding != a.OutboundBinding || envelope.Binding.WireMode != a.WireMode || envelope.OwnerAccountID != a.OwnerAccountID || envelope.Model != a.Model || envelope.AuthorizationGeneration != a.AuthorizationGeneration {
		return openaicookies.Bundle{}, openaicookies.ErrBundleInvalid
	}
	if !envelope.Bundle.ValidAt(s.now()) || envelope.Bundle.ExpiresAt.After(a.Snapshot.ExpiresAt) {
		return openaicookies.Bundle{}, openaicookies.ErrBundleExpired
	}
	return envelope.Bundle.Clone(), nil
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

// Only diagnostic bookkeeping remains after the atomic ticket/bundle CAS.
func (a *CodexTurnStateAttempt) finishCookies(_ context.Context, delivered, cacheAccepted bool, now time.Time) {
	if a == nil {
		return
	}
	a.mu.Lock()
	staged := a.cookieAttempt
	qualified := delivered && a.cookieResponseComplete && !a.cookieResponseFailed &&
		a.cookieCredentialsBound && a.cookieTarget.Shape == CodexTurnStateShapeTarget && a.cookieTarget.ExpiresAt.After(now) &&
		a.Enabled && cacheAccepted
	a.mu.Unlock()
	if staged == nil {
		return
	}
	if qualified {
		staged.MarkPublished()
	}
	staged.Discard()
	diagnostic := staged.Diagnostic()
	a.mu.Lock()
	a.modelEvidence.headers.CookieDiagnostic = codexCookieDiagnostic(diagnostic)
	a.mu.Unlock()
}

func (r *CodexTurnStateCollectResult) commitCookies(_ context.Context) {
	if r == nil || r.cookieAttempt == nil || !r.completed || r.StatusCode < 200 || r.StatusCode >= 300 || r.ModelMismatch() {
		return
	}
	r.cookieAttempt.MarkPublished()
	diagnostic := r.cookieAttempt.Diagnostic()
	r.ModelEvidence.CookieDiagnostic = codexCookieDiagnostic(diagnostic)
}

func (r *CodexTurnStateCollectResult) discardCookies() {
	if r != nil && r.cookieAttempt != nil {
		r.cookieAttempt.Discard()
		if diagnostic := r.cookieAttempt.Diagnostic(); diagnostic.Reason != "" {
			r.ModelEvidence.CookieDiagnostic = codexCookieDiagnostic(diagnostic)
		}
	}
}
