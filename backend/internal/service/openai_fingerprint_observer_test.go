package service

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

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
	if page := SnapshotFingerprintObservationSessions(1, 20, 0); page.Total != 0 || len(page.Items) != 0 {
		t.Fatalf("disabled observer returned stale session tree: %+v", page)
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

func TestRecordFingerprintObservationCapturesFinalParentAndForkProjection(t *testing.T) {
	SetFingerprintObservationEnabled(true)
	defer SetFingerprintObservationEnabled(false)
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	forkedFrom := "018f5c3c-6e3a-7abe-8def-1234567890ad"
	identity := OpenAICodexTurnIdentity{
		SessionID:          fingerprintObserverSessionV7,
		ThreadID:           fingerprintObserverThreadV7,
		ParentThreadID:     fingerprintObserverSessionV7,
		ForkedFromThreadID: forkedFrom,
		Relation:           OpenAICodexTurnRelationDescendant,
	}
	setFingerprintObservationOutboundIdentity(c, identity)
	body := []byte(`{"client_metadata":{"session_id":"` + fingerprintObserverSessionV7 + `","thread_id":"` + fingerprintObserverThreadV7 + `","x-codex-parent-thread-id":"` + fingerprintObserverSessionV7 + `","x-codex-turn-metadata":"{\"session_id\":\"` + fingerprintObserverSessionV7 + `\",\"thread_id\":\"` + fingerprintObserverThreadV7 + `\",\"parent_thread_id\":\"` + fingerprintObserverSessionV7 + `\",\"forked_from_thread_id\":\"` + forkedFrom + `\"}"}}`)
	(&OpenAIGatewayService{}).recordFingerprintObservationWithBody(
		c,
		newOpenAIOAuthPinAccount(9209, nil),
		installationIDResolution{},
		http.Header{
			"session-id":               []string{fingerprintObserverSessionV7},
			"thread-id":                []string{fingerprintObserverThreadV7},
			"x-codex-parent-thread-id": []string{fingerprintObserverSessionV7},
		},
		body,
	)

	entry := SnapshotFingerprintObservations(1)[0]
	if entry.SessionID != identity.SessionID || entry.ThreadID != identity.ThreadID ||
		entry.ParentThreadID != identity.ParentThreadID || entry.ForkedFromThreadID != identity.ForkedFromThreadID {
		t.Fatalf("final hierarchical identity was not captured: %+v", entry)
	}
}

func TestRecordFingerprintObservationAcceptsRootIdentity(t *testing.T) {
	SetFingerprintObservationEnabled(true)
	defer SetFingerprintObservationEnabled(false)
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	setFingerprintObservationOutboundIdentity(c, OpenAICodexTurnIdentity{
		SessionID: fingerprintObserverSessionV7,
		ThreadID:  fingerprintObserverSessionV7,
		Relation:  OpenAICodexTurnRelationRoot,
	})
	(&OpenAIGatewayService{}).recordFingerprintObservation(
		c,
		newOpenAIOAuthPinAccount(9210, nil),
		installationIDResolution{},
		http.Header{
			"session-id": []string{fingerprintObserverSessionV7},
			"thread-id":  []string{fingerprintObserverSessionV7},
		},
	)

	entry := SnapshotFingerprintObservations(1)[0]
	if entry.SessionID != fingerprintObserverSessionV7 || entry.ThreadID != fingerprintObserverSessionV7 {
		t.Fatalf("root identity was not observed: %+v", entry)
	}
}

func TestFingerprintObservationTurnMetadataUUIDValidation(t *testing.T) {
	headers := http.Header{
		"x-codex-turn-metadata": []string{`{"forked_from_thread_id":"` + fingerprintObserverThreadV7 + `"}`},
	}
	if got, present := fingerprintObservationTurnMetadataHeaderUUID(headers, fingerprintObserverThreadV7, "forked_from_thread_id"); !present || got != fingerprintObserverThreadV7 {
		t.Fatalf("valid final turn metadata was rejected: got=%q present=%v", got, present)
	}
	headers.Set("x-codex-turn-metadata", `{"forked_from_thread_id":"018f5c3c-6e3a-4abc-8def-1234567890ab"}`)
	if got, present := fingerprintObservationTurnMetadataHeaderUUID(headers, fingerprintObserverThreadV7, "forked_from_thread_id"); !present || got != "" {
		t.Fatalf("non-UUIDv7 turn metadata was accepted: got=%q present=%v", got, present)
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
		{name: "passthrough", method: http.MethodPost, path: "/v1/responses", account: passthrough, want: true},
		{name: "alpha", method: http.MethodPost, path: "/v1/alpha/search", account: oauth, want: true},
		{name: "bare alpha", method: http.MethodPost, path: "/alpha/search", account: oauth, want: true},
		{name: "codex alpha", method: http.MethodPost, path: "/backend-api/codex/alpha/search", account: oauth, want: true},
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
				observer.record(FingerprintObservationEntry{
					AccountName: "must-be-scrubbed",
					Username:    "pii-user",
					Email:       "pii@example.com",
					APIKeyName:  "pii-key-name",
				})
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

func TestFingerprintObservationSessionPaginationUsesStableSnapshot(t *testing.T) {
	SetFingerprintObservationEnabled(false)
	SetFingerprintObservationEnabled(true)
	defer SetFingerprintObservationEnabled(false)

	base := time.Date(2026, time.August, 9, 12, 0, 0, 0, time.UTC)
	sessionA := fingerprintObserverSessionV7
	threadA1 := fingerprintObserverThreadV7
	threadA2 := "018f5c3c-6e3a-7abe-8def-1234567890ad"
	sessionB := "018f5c3c-6e3b-7abc-8def-1234567890ae"
	sessionC := "018f5c3c-6e3c-7abc-8def-1234567890af"
	actor := FingerprintObservationEntry{
		UserID: 41, Username: "operator", Email: "operator@example.com",
		APIKeyID: 73, APIKeyName: "desktop",
	}
	record := func(at time.Time, sessionID, threadID, parentID, forkID string) {
		entry := actor
		entry.Timestamp = at
		entry.SessionID = sessionID
		entry.ThreadID = threadID
		entry.ParentThreadID = parentID
		entry.ForkedFromThreadID = forkID
		globalFingerprintObserver.record(entry)
	}
	record(base, sessionA, sessionA, "", "")
	record(base.Add(time.Second), sessionA, threadA1, sessionA, "")
	record(base.Add(2*time.Second), sessionB, sessionB, "", "")
	record(base.Add(3*time.Second), sessionA, threadA2, sessionA, threadA1)

	first := SnapshotFingerprintObservationSessions(1, 1, 0)
	if first.Total != 2 || first.Pages != 2 || len(first.Items) != 1 {
		t.Fatalf("unexpected first page: %+v", first)
	}
	if first.Items[0].SessionID != sessionA || first.Items[0].ObservationCount != 3 {
		t.Fatalf("newest session was not first: %+v", first.Items[0])
	}
	if first.Items[0].RootThread == nil || first.Items[0].RootThread.ThreadID != sessionA {
		t.Fatalf("root thread was not grouped: %+v", first.Items[0].RootThread)
	}
	if len(first.Items[0].ChildThreads) != 2 || first.Items[0].ChildThreads[0].ThreadID != threadA2 {
		t.Fatalf("child threads are not unique/newest-first: %+v", first.Items[0].ChildThreads)
	}
	if first.Items[0].ChildThreads[0].ParentThreadID != sessionA || first.Items[0].ChildThreads[0].ForkedFromThreadID != threadA1 {
		t.Fatalf("child relationship was not retained: %+v", first.Items[0].ChildThreads[0])
	}
	if first.Items[0].ChildThreads[0].Relation != "descendant" || first.Items[0].RootThread.Relation != "root" {
		t.Fatalf("unexpected relation projection: root=%q child=%q", first.Items[0].RootThread.Relation, first.Items[0].ChildThreads[0].Relation)
	}

	record(base.Add(4*time.Second), sessionC, sessionC, "", "")
	second := SnapshotFingerprintObservationSessions(2, 1, first.SnapshotSeq)
	if second.SnapshotSeq != first.SnapshotSeq || second.Total != 2 || len(second.Items) != 1 || second.Items[0].SessionID != sessionB {
		t.Fatalf("new records shifted a bounded snapshot: first=%+v second=%+v", first, second)
	}
	fresh := SnapshotFingerprintObservationSessions(1, 20, 0)
	if fresh.Total != 3 || len(fresh.Items) != 3 || fresh.Items[0].SessionID != sessionC {
		t.Fatalf("fresh snapshot did not include the new session: %+v", fresh)
	}
}

func TestFingerprintObservationGroupingSeparatesActorsAndEmptySessions(t *testing.T) {
	observer := &fingerprintObserver{ring: make([]FingerprintObservationEntry, 16)}
	observer.setEnabled(true)
	base := time.Date(2026, time.August, 9, 13, 0, 0, 0, time.UTC)
	observer.record(FingerprintObservationEntry{Timestamp: base, UserID: 1, APIKeyID: 10})
	observer.record(FingerprintObservationEntry{Timestamp: base.Add(time.Second), UserID: 1, APIKeyID: 10})
	observer.record(FingerprintObservationEntry{
		Timestamp: base.Add(2 * time.Second), UserID: 1, APIKeyID: 10,
		SessionID: fingerprintObserverSessionV7,
	})
	observer.record(FingerprintObservationEntry{
		Timestamp: base.Add(3 * time.Second), UserID: 2, APIKeyID: 20,
		SessionID: fingerprintObserverSessionV7, ThreadID: fingerprintObserverSessionV7,
	})

	entries, highWater := observer.snapshotThrough(0)
	sessions := aggregateFingerprintObservationSessions(entries)
	if highWater != 4 || len(sessions) != 4 {
		t.Fatalf("empty sessions or actor boundary were merged: highWater=%d sessions=%+v", highWater, sessions)
	}
	var compact *FingerprintObservationSessionNode
	for i := range sessions {
		if sessions[i].UserID == 1 && sessions[i].SessionID == fingerprintObserverSessionV7 {
			compact = &sessions[i]
			break
		}
	}
	if compact == nil || len(compact.UnthreadedObservations) != 1 || compact.RootThread != nil {
		t.Fatalf("compact observation was not placed in the unthreaded branch: %+v", compact)
	}
}

func TestRecordFingerprintObservationCopiesActorWithoutRawAPIKey(t *testing.T) {
	SetFingerprintObservationEnabled(false)
	SetFingerprintObservationEnabled(true)
	defer SetFingerprintObservationEnabled(false)
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	c.Set("api_key", &APIKey{
		ID: 808, UserID: 707, Key: "sk-must-never-be-observed", Name: "workstation",
		User: &User{ID: 707, Username: "alice", Email: "alice@example.com"},
	})

	(&OpenAIGatewayService{}).recordFingerprintObservation(
		c,
		newOpenAIOAuthPinAccount(9401, nil),
		installationIDResolution{},
		http.Header{},
	)
	entry := SnapshotFingerprintObservations(1)[0]
	if entry.UserID != 707 || entry.Username != "alice" || entry.Email != "alice@example.com" ||
		entry.APIKeyID != 808 || entry.APIKeyName != "workstation" {
		t.Fatalf("actor snapshot mismatch: %+v", entry)
	}
	encoded, err := json.Marshal(entry)
	if err != nil {
		t.Fatalf("marshal observation: %v", err)
	}
	if bytes.Contains(encoded, []byte("sk-must-never-be-observed")) {
		t.Fatalf("observation leaked raw API key: %s", encoded)
	}
}

func TestFingerprintObservationPageNormalizesBounds(t *testing.T) {
	SetFingerprintObservationEnabled(false)
	page := SnapshotFingerprintObservationSessions(0, 1000, 0)
	if page.Page != 1 || page.PageSize != 100 || page.Pages != 1 || page.Items == nil {
		t.Fatalf("pagination bounds were not normalized: %+v", page)
	}
}
