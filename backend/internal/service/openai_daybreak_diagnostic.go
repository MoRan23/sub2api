package service

import (
	"context"
	"net/http"
	"strings"
	"sync"

	"github.com/tidwall/gjson"
)

// DiagnosticDaybreakPreferences is frozen for one attribution or Pelican task.
// It belongs to the business account, not its shared credential owner.
type DiagnosticDaybreakPreferences struct {
	Blue bool `json:"blue"`
	Red  bool `json:"red"`
}

// DiagnosticDaybreakEvidence records the actual inference send, never a catalog
// request or a guess derived from the account's current preferences.
type DiagnosticDaybreakEvidence struct {
	Cyber  string `json:"cyber"`
	Reason string `json:"reason"`
}

func ReadDiagnosticDaybreakPreferences(account *Account) DiagnosticDaybreakPreferences {
	blue := openAIDaybreakEnabled(account, OpenAIDaybreakBlueEnabledKey)
	return DiagnosticDaybreakPreferences{Blue: blue, Red: blue && openAIDaybreakEnabled(account, OpenAIDaybreakRedEnabledKey)}
}

type diagnosticDaybreakPreferencesKey struct{}
type diagnosticDaybreakAttemptKey struct{}

type diagnosticDaybreakAttempt struct {
	preferences DiagnosticDaybreakPreferences
	mu          sync.Mutex
	evidence    *DiagnosticDaybreakEvidence
}

func withDiagnosticDaybreakPreferences(ctx context.Context, preferences DiagnosticDaybreakPreferences) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	preferences.Red = preferences.Blue && preferences.Red
	return context.WithValue(ctx, diagnosticDaybreakPreferencesKey{}, preferences)
}

func diagnosticDaybreakPreferencesFromContext(ctx context.Context) (DiagnosticDaybreakPreferences, bool) {
	if ctx == nil {
		return DiagnosticDaybreakPreferences{}, false
	}
	preferences, ok := ctx.Value(diagnosticDaybreakPreferencesKey{}).(DiagnosticDaybreakPreferences)
	return preferences, ok
}

// Only the shared test transport activates this marker. A frozen preference on
// its own is insufficient to exempt an unrelated request from system policy.
func withDiagnosticDaybreakAttempt(ctx context.Context, preferences DiagnosticDaybreakPreferences) context.Context {
	return context.WithValue(ctx, diagnosticDaybreakAttemptKey{}, &diagnosticDaybreakAttempt{preferences: preferences})
}

func withoutDiagnosticDaybreakAttempt(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return context.WithValue(ctx, diagnosticDaybreakAttemptKey{}, (*diagnosticDaybreakAttempt)(nil))
}

func diagnosticDaybreakAttemptFromContext(ctx context.Context) *diagnosticDaybreakAttempt {
	if ctx == nil || !isOpenAICandyTest(ctx) {
		return nil
	}
	attempt, _ := ctx.Value(diagnosticDaybreakAttemptKey{}).(*diagnosticDaybreakAttempt)
	return attempt
}

func diagnosticDaybreakInferencePreferences(ctx context.Context, account *Account) (DiagnosticDaybreakPreferences, bool) {
	if !IsOpenAIDaybreakAccount(account) {
		return DiagnosticDaybreakPreferences{}, false
	}
	attempt := diagnosticDaybreakAttemptFromContext(ctx)
	if attempt == nil {
		return DiagnosticDaybreakPreferences{}, false
	}
	if disabled, _ := ctx.Value(openAIDaybreakInjectionDisabledKey{}).(bool); disabled {
		return DiagnosticDaybreakPreferences{}, false
	}
	return attempt.preferences, true
}

func diagnosticDaybreakHTTPRequest(request *http.Request) bool {
	return request != nil && request.Method == http.MethodPost && request.URL != nil &&
		strings.HasSuffix(strings.TrimRight(request.URL.Path, "/"), "/responses") &&
		diagnosticDaybreakAttemptFromContext(request.Context()) != nil
}

func diagnosticDaybreakInferenceBody(body []byte) bool {
	if !gjson.ValidBytes(body) {
		return false
	}
	root := gjson.ParseBytes(body)
	if !root.IsObject() || root.Get("generate").Type == gjson.False {
		return false
	}
	frameType := root.Get("type")
	return !frameType.Exists() || frameType.String() == "response.create"
}

func recordDiagnosticDaybreakHTTPRequest(request *http.Request, account *Account) {
	if request == nil || request.Method != http.MethodPost || request.URL == nil || diagnosticDaybreakAttemptFromContext(request.Context()) == nil {
		return
	}
	path := strings.TrimRight(request.URL.Path, "/")
	if !strings.HasSuffix(path, "/responses") && !strings.HasSuffix(path, "/chat/completions") {
		return
	}
	body, err := openAIDaybreakHTTPRequestBody(request)
	if err != nil {
		return
	}
	if strings.HasSuffix(path, "/responses") && !diagnosticDaybreakInferenceBody(body) {
		return
	}
	reason := openAIDaybreakDecisionFromRequest(request)
	if reason == "" {
		reason = "not_oauth"
		if IsOpenAIDaybreakAccount(account) {
			reason = "not_added"
		}
	}
	evidence := &DiagnosticDaybreakEvidence{Cyber: gjson.GetBytes(body, "access_programs.cyber").String(), Reason: reason}
	attempt := diagnosticDaybreakAttemptFromContext(request.Context())
	attempt.mu.Lock()
	attempt.evidence = evidence
	attempt.mu.Unlock()
}

func diagnosticDaybreakEvidenceFromContext(ctx context.Context) *DiagnosticDaybreakEvidence {
	attempt := diagnosticDaybreakAttemptFromContext(ctx)
	if attempt == nil {
		return nil
	}
	attempt.mu.Lock()
	defer attempt.mu.Unlock()
	if attempt.evidence == nil {
		return nil
	}
	copy := *attempt.evidence
	return &copy
}
