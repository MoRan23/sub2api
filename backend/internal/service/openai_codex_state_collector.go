package service

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptrace"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openaicookies"
	"github.com/google/uuid"
)

type CodexTurnStateHTTPCollector struct{ Do CodexTurnStateCollectorHTTPDo }

var ErrCodexTurnStateCollectorProxyUnavailable = errors.New("collector_proxy_unavailable")

// These fixed errors may be exposed as diagnostic categories. Never expose an
// upstream error body or transport error text in a collection outcome.
var (
	errCodexTurnStateCollectorRateLimited        = errors.New("collector_rate_limited")
	errCodexTurnStateCollectorTransportFailed    = errors.New("collector_transport_failed")
	errCodexTurnStateCollectorEmptyResponse      = errors.New("collector_empty_response")
	errCodexTurnStateCollectorStreamFailed       = errors.New("collector_stream_failed")
	errCodexTurnStateCollectorResponseFailed     = errors.New("collector_response_failed")
	errCodexTurnStateCollectorResponseIncomplete = errors.New("collector_response_incomplete")
	errCodexTurnStateCollectorEventTooLarge      = errors.New("collector_event_too_large")
)

func codexTurnStateCollectorTimedOut(err error) bool {
	var networkError net.Error
	return errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &networkError) && networkError.Timeout())
}

func NewCodexTurnStateHTTPCollector(do CodexTurnStateCollectorHTTPDo) *CodexTurnStateHTTPCollector {
	return &CodexTurnStateHTTPCollector{Do: do}
}

// Collect creates its own identity and short, constant body. It never receives
// user messages, continuation state, a business proxy, or a daily root identity.
func (c *CodexTurnStateHTTPCollector) Collect(ctx context.Context, input CodexTurnStateCollectRequest) (result CodexTurnStateCollectResult, collectErr error) {
	var evidence codexModelEvidenceObserver
	var cookieMu sync.Mutex
	var cookieDiagnostic *CodexCookieDiagnostic
	defer func() {
		cookieMu.Lock()
		evidence.headers.CookieDiagnostic = cookieDiagnostic
		result.ModelEvidence = evidence.snapshot(input.Model)
		cookieMu.Unlock()
	}()
	ctx = openaicookies.WithObserver(ctx, func(diagnostic openaicookies.Diagnostic) {
		cookieMu.Lock()
		cookieDiagnostic = codexCookieDiagnostic(diagnostic)
		cookieMu.Unlock()
	})
	ctx, cookieAttempt := openaicookies.WithAttempt(ctx)
	result.cookieAttempt = cookieAttempt
	if c == nil || c.Do == nil || input.ProxyID <= 0 || !codexTurnStateEligible(input.Account) || strings.TrimSpace(input.Model) == "" {
		return result, errors.New("collector_not_configured")
	}
	body, _ := json.Marshal(map[string]any{
		"model": input.Model, "stream": true, "store": false, "instructions": "Reply with OK.",
		"input":               []any{map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": "Reply with OK."}}}},
		"parallel_tool_calls": true,
		"include":             []string{"reasoning.encrypted_content"},
	})
	trace := &codexTurnStateCollectorTrace{stage: "unknown"}
	ctx = httptrace.WithClientTrace(ctx, trace.hooks())
	onSend := input.onSend
	input.onSend = func(at time.Time) {
		trace.markSent(at)
		if onSend != nil {
			onSend(at)
		}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, chatgptCodexURL, bytes.NewReader(body))
	if err != nil {
		return result, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "text/event-stream")
	request.Header.Set("Authorization", "Bearer "+input.Account.GetCredential("access_token"))
	if accountID := input.Account.GetCredential("chatgpt_account_id"); accountID != "" {
		request.Header.Set("ChatGPT-Account-Id", accountID)
	}
	request.Header.Set("session_id", uuid.NewString())
	result.observationID = uuid.NewString()
	request.Header.Set("x-client-request-id", result.observationID)
	started := time.Now()
	response, err := c.Do(ctx, input, request)
	stage, sentAt := trace.snapshot()
	result.requestSentAt = sentAt
	if response != nil {
		if result.requestSentAt.IsZero() {
			// Test/custom adapters may omit the send hook. A real response is
			// evidence of sending, while a preflight error alone is not.
			result.requestSentAt = started
		}
		result.StatusCode = response.StatusCode
		evidence.observeHeaders(response.Header, "response")
		result.RetryAfter = codexTurnStateRetryAfter(response.Header.Get("Retry-After"), time.Now())
	}
	if err != nil {
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
		failure := newCodexTurnStateCollectorTransportError(err, stage)
		if !sentAt.IsZero() && !codexTurnStateCollectorCanceled(err) {
			slog.Warn("openai.codex_turn_state_collector_transport_failed", "account_id", input.Account.ID,
				"model", input.Model, "proxy_id", input.ProxyID, "observation_id", result.observationID, "elapsed_ms", time.Since(started).Milliseconds(),
				"stage", stage, "reason", failure.Error())
		}
		return result, failure
	}
	if response == nil || response.Body == nil {
		return result, errCodexTurnStateCollectorEmptyResponse
	}
	defer response.Body.Close()
	observe := func(token, source string) {
		if token == "" || len(token) > 4096 {
			return
		}
		now := time.Now()
		envelope, envelopeErr := InspectCodexTurnStateEnvelope(token, now)
		shape, shapeErr := ParseCodexTurnState(token, CodexTurnStateAccountTypeForAccount(input.Account), now)
		safe := &CodexTurnStateSafeObservation{ObservedAt: now, TokenLength: len(token), CipherBlocks: envelope.CipherBlocks,
			Shape: shape.Shape, ResponseSource: source, ObservedShape: envelope.ObservedShape, ValidationReason: envelope.ValidationReason,
			IssuedAt: envelope.IssuedAt, ExpiresAt: envelope.ExpiresAt, EnvelopeValid: envelopeErr == nil}
		if shapeErr != nil && safe.ValidationReason == "" {
			safe.ValidationReason = shapeErr.Error()
		}
		if safe.ValidationReason == "expired" {
			safe.Shape = "expired"
		}
		result.Observation = safe
	}
	for key, values := range response.Header {
		if strings.EqualFold(key, "x-codex-turn-state") {
			for _, token := range values {
				observe(token, "header")
				if len(result.Tokens) < 16 && len(token) <= 4096 {
					result.Tokens = append(result.Tokens, token)
				}
			}
		}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		result.Tokens = nil
		return result, nil
	}
	// A header alone is not enough: collect only from a completed Responses
	// stream. Cap total input so malformed endpoints cannot allocate unboundedly.
	scanner := bufio.NewScanner(io.LimitReader(response.Body, 2<<20))
	scanner.Buffer(make([]byte, 4096), 256<<10)
	var eventData []byte
	var eventName string
	completed := false
	consume := func() error {
		data, eventType := eventData, eventName
		eventData, eventName = nil, ""
		if len(data) == 0 {
			return nil
		}
		if bytes.Equal(data, []byte("[DONE]")) {
			return nil
		}
		var event struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(data, &event) != nil {
			return nil
		}
		if event.Type != "" {
			eventType = event.Type
		}
		// Preserve the actual upstream declaration, before any conversion. A
		// header routing hint is not evidence of the model in this response.
		evidence.observePayload([]byte(openAICompatPayloadWithEventType(string(data), eventType)))
		if eventType == "error" || eventType == "response.failed" || eventType == "response.incomplete" {
			failureErr := errCodexTurnStateCollectorResponseFailed
			if eventType == "response.incomplete" {
				failureErr = errCodexTurnStateCollectorResponseIncomplete
			}
			var failure struct {
				Code  string `json:"code"`
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
				Response struct {
					Error struct {
						Code string `json:"code"`
					} `json:"error"`
				} `json:"response"`
			}
			// Malformed error details cannot turn a recognized failure into a
			// successful response or provide a reliable rate-limit code.
			if json.Unmarshal(data, &failure) != nil {
				return failureErr
			}
			code := failure.Response.Error.Code
			if code == "" {
				code = failure.Error.Code
			}
			if code == "" {
				code = failure.Code
			}
			// Only structured upstream codes classify a limit; error prose is
			// neither trusted as a signal nor retained in the collection result.
			if code == "rate_limit_exceeded" || code == "insufficient_quota" {
				return errCodexTurnStateCollectorRateLimited
			}
			return failureErr
		}
		if eventType == "response.completed" {
			completed = true
		}
		for _, token := range CodexTurnStateTokensFromEvent(data) {
			observe(token, "metadata")
			if len(result.Tokens) < 16 && len(token) <= 4096 {
				result.Tokens = append(result.Tokens, token)
			}
		}
		return nil
	}
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			if err := consume(); err != nil {
				result.Tokens = nil
				return result, err
			}
			if completed {
				break
			}
			continue
		}
		if bytes.HasPrefix(line, []byte("event:")) {
			eventName = string(bytes.TrimSpace(line[len("event:"):]))
			continue
		}
		if bytes.HasPrefix(line, []byte("data:")) {
			if len(eventData) > 0 {
				eventData = append(eventData, '\n')
			}
			eventData = append(eventData, bytes.TrimSpace(line[5:])...)
			if len(eventData) > 256<<10 {
				result.Tokens = nil
				return result, errCodexTurnStateCollectorEventTooLarge
			}
		}
	}
	if err := scanner.Err(); err != nil {
		result.Tokens = nil
		if errors.Is(err, bufio.ErrTooLong) {
			return result, errCodexTurnStateCollectorEventTooLarge
		}
		failure := newCodexTurnStateCollectorTransportError(err, "response_body")
		if transport, ok := failure.(*codexTurnStateCollectorTransportError); ok && transport.code != "collector_transport_failed" {
			return result, failure
		}
		return result, errCodexTurnStateCollectorStreamFailed
	}
	if err := consume(); err != nil {
		result.Tokens = nil
		return result, err
	}
	if !completed {
		result.Tokens = nil
		return result, errCodexTurnStateCollectorResponseIncomplete
	}
	result.completed = true
	return result, nil
}

func codexTurnStateRetryAfter(raw string, now time.Time) time.Duration {
	if seconds, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64); err == nil && seconds > 0 && seconds <= int64((365*24*time.Hour)/time.Second) {
		return time.Duration(seconds) * time.Second
	}
	if stamp, err := http.ParseTime(raw); err == nil && stamp.After(now) {
		return stamp.Sub(now)
	}
	return 0
}
