package service

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

type CodexTurnStateHTTPCollector struct{ Do CodexTurnStateCollectorHTTPDo }

var ErrCodexTurnStateCollectorProxyUnavailable = errors.New("collector_proxy_unavailable")

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
		"model": input.Model, "stream": true, "store": false, "instructions": "Reply briefly.",
		"input":     []any{map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_text", "text": "Reply with OK."}}}},
		"reasoning": map[string]any{"effort": "low"},
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
	if err != nil {
		if errors.Is(err, ErrCodexTurnStateCollectorProxyUnavailable) {
			return result, ErrCodexTurnStateCollectorProxyUnavailable
		}
		return result, errors.New("collector_transport_failed")
	}
	if response == nil || response.Body == nil {
		return result, errors.New("collector_empty_response")
	}
	defer response.Body.Close()
	result.StatusCode = response.StatusCode
	result.RetryAfter = codexTurnStateRetryAfter(response.Header.Get("Retry-After"), time.Now())
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return result, nil
	}
	result.Tokens = append(result.Tokens, response.Header.Values("x-codex-turn-state")...)
	// A header alone is not enough: collect only from a completed Responses
	// stream. Cap total input so malformed endpoints cannot allocate unboundedly.
	scanner := bufio.NewScanner(io.LimitReader(response.Body, 2<<20))
	scanner.Buffer(make([]byte, 4096), 256<<10)
	var eventData []byte
	completed := false
	consume := func() error {
		if len(eventData) == 0 {
			return nil
		}
		data := eventData
		eventData = nil
		if bytes.Equal(data, []byte("[DONE]")) {
			return nil
		}
		var event struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(data, &event) != nil {
			return nil
		}
		if event.Type == "error" || event.Type == "response.failed" || event.Type == "response.incomplete" {
			return errors.New("collector_response_failed")
		}
		if event.Type == "response.completed" {
			completed = true
		}
		for _, token := range CodexTurnStateTokensFromEvent(data) {
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
				return CodexTurnStateCollectResult{StatusCode: result.StatusCode}, err
			}
			if completed {
				break
			}
			continue
		}
		if bytes.HasPrefix(line, []byte("data:")) {
			if len(eventData) > 0 {
				eventData = append(eventData, '\n')
			}
			eventData = append(eventData, bytes.TrimSpace(line[5:])...)
			if len(eventData) > 256<<10 {
				return result, errors.New("collector_event_too_large")
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return CodexTurnStateCollectResult{StatusCode: result.StatusCode}, errors.New("collector_stream_failed")
	}
	if err := consume(); err != nil {
		return CodexTurnStateCollectResult{StatusCode: result.StatusCode}, err
	}
	if !completed {
		return CodexTurnStateCollectResult{StatusCode: result.StatusCode}, errors.New("collector_response_incomplete")
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
