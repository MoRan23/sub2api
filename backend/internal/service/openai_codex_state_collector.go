package service

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

type CodexTurnStateHTTPCollector struct{ Do CodexTurnStateCollectorHTTPDo }

var ErrCodexTurnStateCollectorProxyUnavailable = errors.New("collector_proxy_unavailable")

// These fixed errors may be exposed as diagnostic categories. Never retain an
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
func (c *CodexTurnStateHTTPCollector) Collect(ctx context.Context, input CodexTurnStateCollectRequest) (CodexTurnStateCollectResult, error) {
	var result CodexTurnStateCollectResult
	if c == nil || c.Do == nil || input.ProxyID <= 0 || !codexTurnStateEligible(input.Account) || strings.TrimSpace(input.Model) == "" {
		return result, errors.New("collector_not_configured")
	}
	body, _ := json.Marshal(map[string]any{
		"model": input.Model, "stream": true, "store": false, "instructions": "Reply with OK.",
		"input":               []any{map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": "Reply with OK."}}}},
		"parallel_tool_calls": true,
		"include":             []string{"reasoning.encrypted_content"},
	})
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
	request.Header.Set("x-client-request-id", uuid.NewString())
	response, err := c.Do(ctx, input, request)
	if response != nil {
		result.StatusCode = response.StatusCode
		result.RetryAfter = codexTurnStateRetryAfter(response.Header.Get("Retry-After"), time.Now())
	}
	if err != nil {
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
		if errors.Is(err, ErrCodexTurnStateCollectorProxyUnavailable) {
			return result, ErrCodexTurnStateCollectorProxyUnavailable
		}
		if codexTurnStateCollectorTimedOut(err) {
			return result, context.DeadlineExceeded
		}
		return result, errCodexTurnStateCollectorTransportFailed
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
		if codexTurnStateCollectorTimedOut(err) {
			return result, context.DeadlineExceeded
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
