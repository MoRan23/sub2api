package service

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
)

const (
	fingerprintObserverSessionV7 = "018f5c3c-6e3a-7abc-8def-1234567890ab"
	fingerprintObserverThreadV7  = "018f5c3c-6e3a-7abd-8def-1234567890ac"
)

func TestNormalizeFingerprintObservationUUIDv7(t *testing.T) {
	if got := NormalizeFingerprintObservationUUIDv7(fingerprintObserverSessionV7); got != fingerprintObserverSessionV7 {
		t.Fatalf("valid UUIDv7 was rejected: %q", got)
	}
	for _, raw := range []string{
		"",
		"not-a-uuid",
		"  " + fingerprintObserverSessionV7,
		"{" + fingerprintObserverSessionV7 + "}",
		"018f5c3c-6e3a-4abc-8def-1234567890ab", // v4
		"018F5C3C-6E3A-7ABC-8DEF-1234567890AB", // non-canonical case
	} {
		if got := NormalizeFingerprintObservationUUIDv7(raw); got != "" {
			t.Errorf("invalid UUIDv7 %q normalized to %q", raw, got)
		}
	}
}

func TestFingerprintObserverScrubsRingOnDisable(t *testing.T) {
	SetFingerprintObservationEnabled(false)
	SetFingerprintObservationEnabled(true)
	acct := newOpenAIOAuthPinAccount(9201, nil)
	(&OpenAIGatewayService{}).recordFingerprintObservation(nil, acct, installationIDResolution{
		Enabled:    true,
		ClientID:   "client-installation",
		OutboundID: "outbound-installation",
	}, http.Header{
		"Session-Id": []string{fingerprintObserverSessionV7},
	})
	if got := SnapshotFingerprintObservations(1); len(got) != 1 {
		t.Fatalf("expected one observation before disable, got %d", len(got))
	}
	SetFingerprintObservationEnabled(false)
	if got := SnapshotFingerprintObservations(1); len(got) != 0 {
		t.Fatalf("disabled observer returned %d stale entries", len(got))
	}
	globalFingerprintObserver.mu.Lock()
	defer globalFingerprintObserver.mu.Unlock()
	for i, entry := range globalFingerprintObserver.ring {
		if entry != (FingerprintObservationEntry{}) {
			t.Fatalf("ring slot %d retained data after disable: %+v", i, entry)
		}
	}
}

func TestRecordFingerprintObservationValidatesSessionAndThreadIndependently(t *testing.T) {
	SetFingerprintObservationEnabled(true)
	defer SetFingerprintObservationEnabled(false)
	acct := newOpenAIOAuthPinAccount(9202, nil)
	svc := &OpenAIGatewayService{}
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	setFingerprintObservationOutboundIdentity(c, OpenAIOutboundSessionIdentity{
		SessionID: fingerprintObserverSessionV7,
		ThreadID:  fingerprintObserverThreadV7,
	})
	svc.recordFingerprintObservation(c, acct, installationIDResolution{}, http.Header{
		"session-id": []string{fingerprintObserverSessionV7},
		"thread-id":  []string{"legacy-hash"},
	})
	entries := SnapshotFingerprintObservations(1)
	if len(entries) != 1 {
		t.Fatalf("expected one observation, got %d", len(entries))
	}
	if entries[0].SessionID != fingerprintObserverSessionV7 || entries[0].ThreadID != "" {
		t.Fatalf("unexpected independently validated IDs: %+v", entries[0])
	}
}

func TestRecordFingerprintObservationUnpinnedUsesClientInstallationID(t *testing.T) {
	SetFingerprintObservationEnabled(true)
	defer SetFingerprintObservationEnabled(false)

	(&OpenAIGatewayService{}).recordFingerprintObservation(
		nil,
		newOpenAIOAuthPinAccount(9208, nil),
		installationIDResolution{ClientID: "body-only-installation"},
		http.Header{},
	)

	entries := SnapshotFingerprintObservations(1)
	if len(entries) != 1 {
		t.Fatalf("expected one observation, got %d", len(entries))
	}
	if entries[0].OutboundInstallationID != "body-only-installation" {
		t.Fatalf("unpinned outbound installation ID = %q", entries[0].OutboundInstallationID)
	}
}

func TestRecordFingerprintObservationUsesFinalHeaderAliases(t *testing.T) {
	SetFingerprintObservationEnabled(true)
	defer SetFingerprintObservationEnabled(false)
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	acct := newOpenAIOAuthPinAccount(9203, nil)
	setFingerprintObservationOutboundIdentity(c, OpenAIOutboundSessionIdentity{
		SessionID: fingerprintObserverSessionV7,
		ThreadID:  fingerprintObserverThreadV7,
	})
	(&OpenAIGatewayService{}).recordFingerprintObservation(c, acct, installationIDResolution{}, http.Header{
		"session_id":          []string{fingerprintObserverSessionV7},
		"conversation_id":     []string{fingerprintObserverThreadV7},
		"x-client-request-id": []string{fingerprintObserverThreadV7},
		"user-agent":          []string{"codex_cli_rs/1.0"},
	})
	entry := SnapshotFingerprintObservations(1)[0]
	if entry.SessionID != fingerprintObserverSessionV7 || entry.ThreadID != fingerprintObserverThreadV7 {
		t.Fatalf("header aliases were not captured: %+v", entry)
	}
	if entry.InboundEndpoint != "POST /v1/responses" {
		t.Fatalf("unexpected endpoint for context without registered route: %q", entry.InboundEndpoint)
	}
}

func TestRecordFingerprintObservationBodyFallbackKeepsHeadersAuthoritative(t *testing.T) {
	SetFingerprintObservationEnabled(true)
	defer SetFingerprintObservationEnabled(false)
	acct := newOpenAIOAuthPinAccount(9204, nil)
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	setFingerprintObservationOutboundIdentity(c, OpenAIOutboundSessionIdentity{
		SessionID: fingerprintObserverSessionV7,
		ThreadID:  fingerprintObserverThreadV7,
	})
	body := []byte(`{"client_metadata":{"session_id":"018f5c3c-6e3a-7abe-8def-1234567890ad","thread_id":"` + fingerprintObserverThreadV7 + `"}}`)
	(&OpenAIGatewayService{}).recordFingerprintObservationWithBody(
		c,
		acct,
		installationIDResolution{},
		http.Header{"session-id": []string{fingerprintObserverSessionV7}},
		body,
	)
	entry := SnapshotFingerprintObservations(1)[0]
	if entry.SessionID != fingerprintObserverSessionV7 {
		t.Fatalf("body value replaced authoritative header: %+v", entry)
	}
	if entry.ThreadID != fingerprintObserverThreadV7 {
		t.Fatalf("valid body fallback was not captured: %+v", entry)
	}
}

func TestRecordFingerprintObservationRejectsUntrustedClientUUIDv7(t *testing.T) {
	SetFingerprintObservationEnabled(true)
	defer SetFingerprintObservationEnabled(false)
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

	body := []byte(`{"client_metadata":{"session_id":"` + fingerprintObserverSessionV7 + `","thread_id":"` + fingerprintObserverThreadV7 + `"}}`)
	(&OpenAIGatewayService{}).recordFingerprintObservationWithBody(
		c,
		newOpenAIOAuthPinAccount(9205, nil),
		installationIDResolution{},
		http.Header{
			"session-id": []string{fingerprintObserverSessionV7},
			"thread-id":  []string{fingerprintObserverThreadV7},
		},
		body,
	)

	entry := SnapshotFingerprintObservations(1)[0]
	if entry.SessionID != "" || entry.ThreadID != "" {
		t.Fatalf("client UUIDv7 values were retained without a server-owned pair: %+v", entry)
	}
}

func TestRecordFingerprintObservationDoesNotFallbackPastFinalInvalidHeader(t *testing.T) {
	SetFingerprintObservationEnabled(true)
	defer SetFingerprintObservationEnabled(false)
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	setFingerprintObservationOutboundIdentity(c, OpenAIOutboundSessionIdentity{
		SessionID: fingerprintObserverSessionV7,
		ThreadID:  fingerprintObserverThreadV7,
	})

	body := []byte(`{"client_metadata":{"session_id":"` + fingerprintObserverSessionV7 + `","thread_id":"` + fingerprintObserverThreadV7 + `"}}`)
	(&OpenAIGatewayService{}).recordFingerprintObservationWithBody(
		c,
		newOpenAIOAuthPinAccount(9206, nil),
		installationIDResolution{},
		http.Header{"session-id": []string{"legacy-final-value"}},
		body,
	)

	entry := SnapshotFingerprintObservations(1)[0]
	if entry.SessionID != "" {
		t.Fatalf("body value bypassed an authoritative final header: %+v", entry)
	}
	if entry.ThreadID != fingerprintObserverThreadV7 {
		t.Fatalf("missing thread header did not use the trusted body fallback: %+v", entry)
	}
}

func TestRecordFingerprintObservationRejectsConflictingFinalAliases(t *testing.T) {
	SetFingerprintObservationEnabled(true)
	defer SetFingerprintObservationEnabled(false)
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	setFingerprintObservationOutboundIdentity(c, OpenAIOutboundSessionIdentity{
		SessionID: fingerprintObserverSessionV7,
		ThreadID:  fingerprintObserverThreadV7,
	})

	body := []byte(`{"client_metadata":{"session_id":"` + fingerprintObserverSessionV7 + `","thread_id":"` + fingerprintObserverThreadV7 + `"}}`)
	(&OpenAIGatewayService{}).recordFingerprintObservationWithBody(
		c,
		newOpenAIOAuthPinAccount(9208, nil),
		installationIDResolution{},
		http.Header{
			"session-id":      []string{fingerprintObserverSessionV7},
			"session_id":      []string{"018f5c3c-6e3a-7abe-8def-1234567890ad"},
			"thread-id":       []string{fingerprintObserverThreadV7},
			"conversation_id": []string{"legacy-conflict"},
		},
		body,
	)

	entry := SnapshotFingerprintObservations(1)[0]
	if entry.SessionID != "" || entry.ThreadID != "" {
		t.Fatalf("conflicting final aliases were reported as trusted: %+v", entry)
	}
}

func TestSetFingerprintObservationOutboundIdentityInvalidReplacementClearsStalePair(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	setFingerprintObservationOutboundIdentity(c, OpenAIOutboundSessionIdentity{
		SessionID: fingerprintObserverSessionV7,
		ThreadID:  fingerprintObserverThreadV7,
	})
	setFingerprintObservationOutboundIdentity(c, OpenAIOutboundSessionIdentity{
		SessionID: "invalid-replacement",
		ThreadID:  fingerprintObserverThreadV7,
	})

	if identity, ok := fingerprintObservationOutboundIdentityFromContext(c); ok || identity != (OpenAIOutboundSessionIdentity{}) {
		t.Fatalf("invalid replacement retained stale provenance: %+v ok=%v", identity, ok)
	}
}

func TestFingerprintObservationClearsStaleTrustedPairBeforeFallback(t *testing.T) {
	SetFingerprintObservationEnabled(true)
	defer SetFingerprintObservationEnabled(false)
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	setFingerprintObservationOutboundIdentity(c, OpenAIOutboundSessionIdentity{
		SessionID: fingerprintObserverSessionV7,
		ThreadID:  fingerprintObserverThreadV7,
	})
	clearFingerprintObservationOutboundIdentity(c)

	body := []byte(`{"client_metadata":{"session_id":"` + fingerprintObserverSessionV7 + `","thread_id":"` + fingerprintObserverThreadV7 + `"}}`)
	(&OpenAIGatewayService{}).recordFingerprintObservationWithBody(
		c,
		newOpenAIOAuthPinAccount(9207, nil),
		installationIDResolution{},
		http.Header{"session-id": []string{"legacy-final-value"}},
		body,
	)

	entry := SnapshotFingerprintObservations(1)[0]
	if entry.SessionID != "" || entry.ThreadID != "" {
		t.Fatalf("stale trusted pair was reused after clear: %+v", entry)
	}
}

func TestShouldRecordFingerprintObservationRequestCoverage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	oauth := newOpenAIOAuthPinAccount(9301, nil)
	passthrough := newOpenAIOAuthPinAccount(9302, map[string]any{"openai_passthrough": true})
	apiKey := &Account{ID: 9303, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	grok := &Account{ID: 9304, Platform: PlatformGrok, Type: AccountTypeOAuth}

	tests := []struct {
		name    string
		method  string
		path    string
		account *Account
		want    bool
	}{
		{name: "responses", method: http.MethodPost, path: "/v1/responses", account: oauth, want: true},
		{name: "prefixed responses", method: http.MethodPost, path: "/openai/v1/responses", account: oauth, want: true},
		{name: "responses alias", method: http.MethodPost, path: "/responses", account: oauth, want: true},
		{name: "codex responses alias", method: http.MethodPost, path: "/backend-api/codex/responses", account: oauth, want: true},
		{name: "compact", method: http.MethodPost, path: "/v1/responses/compact", account: oauth, want: true},
		{name: "prefixed compact", method: http.MethodPost, path: "/openai/v1/responses/compact", account: oauth, want: true},
		{name: "prefixed compact subpath", method: http.MethodPost, path: "/openai/v1/responses/compact/detail", account: oauth, want: true},
		{name: "compact alias", method: http.MethodPost, path: "/responses/compact", account: oauth, want: true},
		{name: "codex compact alias subpath", method: http.MethodPost, path: "/backend-api/codex/responses/compact/detail", account: oauth, want: true},
		{name: "messages bridge", method: http.MethodPost, path: "/v1/messages", account: oauth, want: true},
		{name: "prefixed messages bridge", method: http.MethodPost, path: "/openai/v1/messages", account: oauth, want: true},
		{name: "chat bridge", method: http.MethodPost, path: "/v1/chat/completions", account: oauth, want: true},
		{name: "prefixed chat bridge", method: http.MethodPost, path: "/openai/v1/chat/completions", account: oauth, want: true},
		{name: "websocket handshake", method: http.MethodGet, path: "/v1/responses", account: oauth, want: true},
		{name: "prefixed websocket handshake", method: http.MethodGet, path: "/openai/v1/responses", account: oauth, want: true},
		{name: "api key", method: http.MethodPost, path: "/v1/responses", account: apiKey, want: false},
		{name: "passthrough", method: http.MethodPost, path: "/v1/responses", account: passthrough, want: false},
		{name: "alpha", method: http.MethodPost, path: "/v1/alpha/search", account: oauth, want: false},
		{name: "grok", method: http.MethodPost, path: "/v1/responses", account: grok, want: false},
		{name: "live", method: http.MethodGet, path: "/v1/realtime", account: oauth, want: false},
		{name: "probe", method: http.MethodPost, path: "/api/v1/admin/accounts/9301/test", account: oauth, want: false},
		{name: "cyber block", method: http.MethodPost, path: "/internal/cyber/session-block", account: oauth, want: false},
		{name: "foreign responses root", method: http.MethodPost, path: "/foo/responses/bar", account: oauth, want: false},
		{name: "internal responses probe", method: http.MethodPost, path: "/internal/responses/probe", account: oauth, want: false},
		{name: "other responses subpath", method: http.MethodPost, path: "/v1/responses/resp_123/cancel", account: oauth, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(tt.method, tt.path, nil)
			if got := shouldRecordFingerprintObservationRequest(c, tt.account); got != tt.want {
				t.Fatalf("coverage decision for %s %s = %v, want %v", tt.method, tt.path, got, tt.want)
			}
		})
	}
}

func TestFingerprintObserverConcurrentDisableCannotRetainEntries(t *testing.T) {
	observer := &fingerprintObserver{ring: make([]FingerprintObservationEntry, 32)}
	observer.setEnabled(true)

	var writers sync.WaitGroup
	for i := 0; i < 8; i++ {
		writers.Add(1)
		go func() {
			defer writers.Done()
			for j := 0; j < 200; j++ {
				observer.record(FingerprintObservationEntry{AccountName: "must-be-scrubbed"})
			}
		}()
	}
	observer.setEnabled(false)
	writers.Wait()

	if got := observer.snapshot(0); len(got) != 0 {
		t.Fatalf("disabled observer retained %d concurrent entries", len(got))
	}
	observer.mu.Lock()
	defer observer.mu.Unlock()
	for i, entry := range observer.ring {
		if entry != (FingerprintObservationEntry{}) {
			t.Fatalf("ring slot %d retained data after concurrent disable: %+v", i, entry)
		}
	}
}
