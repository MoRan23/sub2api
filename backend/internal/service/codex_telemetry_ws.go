package service

import (
	"context"
	"net/http"
	"sync"
	"time"
)

// codexTelemetryWSTurn retains only bounded response metadata, never a request
// or response frame. The passthrough reader and writer may call it concurrently.
type codexTelemetryWSTurn struct {
	mu               sync.Mutex
	attempt          *CodexTelemetryAttempt
	result           CodexTelemetryResult
	terminal         bool
	done             bool
	stream           codexTelemetryStream
	cancelRequested  bool
	lastReadFinished time.Time
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
	t.observeRead(message, eventType, time.Time{}, time.Now(), nil)
}

// observeRead receives the physical read boundaries, excluding time spent
// rewriting or delivering a previous frame. Protocol Ping/Pong is consumed by
// the WS implementation and never passed here as a model event.
func (t *codexTelemetryWSTurn) observeRead(message []byte, eventType string, started, finished time.Time, readErr error) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.done {
		return
	}
	wait := time.Duration(-1)
	if !started.IsZero() && !finished.Before(started) {
		wait = finished.Sub(started)
		t.lastReadFinished = finished
	}
	if readErr != nil {
		t.stream.readFailed(finished, wait)
	} else {
		t.stream.observe(message, eventType, finished, wait)
	}
	t.stream.result.HTTPStatus = http.StatusSwitchingProtocols
	if t.cancelRequested && t.stream.result.Status == "cancelled" {
		t.stream.result.ExplicitClientInterrupt = true
	}
	t.result = t.stream.result
	t.terminal = t.result.Status != ""
}

// A gateway compatibility repair can extract multiple JSON documents from a
// single WS frame. They still count as only one physical event/read wait.
func (t *codexTelemetryWSTurn) observeBuffered(message []byte) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.done {
		return
	}
	at := t.lastReadFinished
	if at.IsZero() {
		at = time.Now()
	}
	t.stream.observeMetadata(message, at)
	if t.cancelRequested && t.stream.result.Status == "cancelled" {
		t.stream.result.ExplicitClientInterrupt = true
	}
	t.result = t.stream.result
	t.terminal = t.result.Status != ""
}

func (t *codexTelemetryWSTurn) sent(started, finished time.Time, sendErr error) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.done {
		return
	}
	t.stream.result.RequestSentAt = started
	succeeded := sendErr == nil
	t.stream.result.SendSucceeded = &succeeded
	if !started.IsZero() && !finished.Before(started) {
		t.stream.result.SendDurationMS = float64(finished.Sub(started)) / float64(time.Millisecond)
	}
	if sendErr != nil {
		if t.stream.result.Status == "" {
			t.stream.result.Status, t.stream.result.FinishedAt = "failed", finished
		}
		t.stream.result.DeliveryStatus = "incomplete"
	}
	t.result = t.stream.result
}

func (t *codexTelemetryWSTurn) requestCancel() {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.done {
		t.cancelRequested = true
	}
}

func (t *codexTelemetryWSTurn) writeFailed() {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.done && !t.terminal {
		t.stream.result.Status, t.stream.result.DeliveryStatus = "failed", "incomplete"
		t.result = t.stream.result
	}
}

func (t *codexTelemetryWSTurn) markDelivery(delivered bool) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.done || t.result.DeliveryStatus == "rejected" {
		return
	}
	status := "rejected"
	if delivered {
		status = "delivered"
	}
	t.stream.result.DeliveryStatus, t.result.DeliveryStatus = status, status
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
	if result.Status == "" {
		// A broken socket or a cancelled server context is not evidence of an
		// explicit user interruption, even when no first token was received.
		result.Status = "incomplete"
	}
	if result.DeliveryStatus == "" {
		result.DeliveryStatus = "incomplete"
		if retry {
			result.DeliveryStatus = "rejected"
		}
	}
	if result.FinishedAt.IsZero() {
		result.FinishedAt = time.Now()
	}
	t.result = result
	t.mu.Unlock()
	if retry {
		t.attempt.Retry(result)
	} else {
		t.attempt.Finish(result)
	}
}
