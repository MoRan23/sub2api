package service

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"time"
)

// codexTelemetryWSTurn retains only bounded response metadata, never a request
// or response frame. The passthrough reader and writer may call it concurrently.
type codexTelemetryWSTurn struct {
	mu       sync.Mutex
	attempt  *CodexTelemetryAttempt
	result   CodexTelemetryResult
	terminal bool
	done     bool
}

func (s *OpenAIGatewayService) beginCodexTelemetryWS(ctx context.Context, account *Account, physicalHeaders, authHeaders http.Header, body []byte) *codexTelemetryWSTurn {
	headers := physicalHeaders.Clone()
	if headers == nil {
		headers = make(http.Header)
	}
	// The pool intentionally retains only safe identity headers. Credentials
	// come from this account attempt, not the fingerprint observation store.
	for _, name := range []string{"Authorization", "Chatgpt-Account-Id"} {
		if value := codexTelemetryHeader(authHeaders, name); value != "" {
			headers.Set(name, value)
		}
	}
	proxyURL := ""
	if account != nil && account.ProxyID != nil && account.Proxy != nil {
		proxyURL = account.Proxy.URL()
	}
	attempt := s.beginCodexTelemetryFromWire(ctx, account, headers, body, proxyURL, true)
	if attempt == nil {
		return nil
	}
	return &codexTelemetryWSTurn{attempt: attempt}
}

func (t *codexTelemetryWSTurn) observe(message []byte, eventType string) {
	if t == nil || eventType == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.done {
		return
	}
	now := time.Now()
	if t.result.FirstEventAt.IsZero() {
		t.result.FirstEventAt = now
	}
	if t.result.FirstTokenAt.IsZero() && isOpenAIWSTokenEvent(eventType) {
		t.result.FirstTokenAt = now
	}
	_, responseID, _ := parseOpenAIWSEventEnvelope(message)
	if responseID != "" {
		t.result.ResponseID = responseID
	}
	if !isOpenAIWSTerminalEvent(eventType) && eventType != "error" {
		return
	}
	status := "failed"
	switch eventType {
	case "response.completed", "response.done":
		status = "completed"
	case "response.cancelled", "response.canceled":
		status = "cancelled"
	}
	statusCode := http.StatusOK
	if eventType == "error" {
		code, typ, _ := parseOpenAIWSErrorEventFields(message)
		statusCode = openAIWSErrorHTTPStatusFromRaw(code, typ)
	}
	result := codexTelemetryResultFromResponse(message, status, statusCode, t.result.FirstEventAt, t.result.FirstTokenAt)
	result.ExplicitClientInterrupt = t.result.ExplicitClientInterrupt
	if result.ResponseID == "" {
		result.ResponseID = t.result.ResponseID
	}
	t.result = result
	t.terminal = true
}

func (t *codexTelemetryWSTurn) requestCancel() {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.done {
		t.result.ExplicitClientInterrupt = true
	}
}

func (t *codexTelemetryWSTurn) writeFailed() {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.done && !t.terminal {
		t.result.Status = "failed"
	}
}

// finish is invoked at the retry decision, not upon seeing an error frame:
// recoverable pre-output failures must not prematurely end a logical turn.
func (t *codexTelemetryWSTurn) finish(retry bool) {
	if t == nil {
		return
	}
	t.mu.Lock()
	if t.done {
		t.mu.Unlock()
		return
	}
	t.done = true
	result := t.result
	if strings.TrimSpace(result.Status) == "" {
		// A broken socket or a cancelled server context is not evidence of an
		// explicit user interruption, even when no first token was received.
		result.Status = "interrupted"
	}
	if result.FinishedAt.IsZero() {
		result.FinishedAt = time.Now()
	}
	t.mu.Unlock()
	if retry {
		t.attempt.Retry(result)
	} else {
		t.attempt.Finish(result)
	}
}
