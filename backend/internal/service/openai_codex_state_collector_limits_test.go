package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"testing/iotest"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestCodexTurnStateCollectorAlignedRequestContract(t *testing.T) {
	_, _, account := newCodexStateTestService(t)
	account.Credentials["chatgpt_account_id"] = "collector-owner"
	var sessions, requestIDs []string
	collector := NewCodexTurnStateHTTPCollector(func(_ context.Context, input CodexTurnStateCollectRequest, request *http.Request) (*http.Response, error) {
		require.Equal(t, int64(99), input.ProxyID)
		require.Same(t, account, input.Account)
		require.Equal(t, "final-model", input.Model)
		require.Equal(t, http.MethodPost, request.Method)
		require.Equal(t, "https://chatgpt.com/backend-api/codex/responses", request.URL.String())
		require.Equal(t, "Bearer test-token", request.Header.Get("Authorization"))
		require.Equal(t, "collector-owner", request.Header.Get("ChatGPT-Account-Id"))
		require.Equal(t, "application/json", request.Header.Get("Content-Type"))
		require.Equal(t, "text/event-stream", request.Header.Get("Accept"))
		require.Empty(t, request.Header.Get("x-codex-turn-state"))
		body, err := io.ReadAll(request.Body)
		require.NoError(t, err)
		require.JSONEq(t, `{"model":"final-model","instructions":"Reply with OK.","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"Reply with OK."}]}],"stream":true,"store":false,"parallel_tool_calls":true,"include":["reasoning.encrypted_content"]}`, string(body))
		require.EqualValues(t, len(body), request.ContentLength)
		for _, header := range []string{"session_id", "x-client-request-id"} {
			_, err = uuid.Parse(request.Header.Get(header))
			require.NoError(t, err, header)
		}
		sessions = append(sessions, request.Header.Get("session_id"))
		requestIDs = append(requestIDs, request.Header.Get("x-client-request-id"))
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("data: {\"type\":\"response.completed\"}\n\n"))}, nil
	})
	for range 2 {
		_, err := collector.Collect(context.Background(), CodexTurnStateCollectRequest{Account: account, Model: "final-model", ProxyID: 99})
		require.NoError(t, err)
	}
	for _, ids := range [][]string{sessions, requestIDs} {
		require.NotEqual(t, ids[0], ids[1], "each physical collection gets a new identity")
	}
	require.NotEqual(t, sessions[0], requestIDs[0])
}

func TestCodexTurnStateCollectorSSELimitCodesAndLocations(t *testing.T) {
	for _, code := range []string{"rate_limit_exceeded", "insufficient_quota"} {
		for _, eventType := range []string{"error", "response.failed", "response.incomplete"} {
			for _, location := range []string{"response", "error", "top"} {
				t.Run(code+"/"+eventType+"/"+location, func(t *testing.T) {
					event := map[string]any{"type": eventType}
					switch location {
					case "response":
						event["response"] = map[string]any{"error": map[string]any{"code": code}}
					case "error":
						event["error"] = map[string]any{"code": code}
					case "top":
						event["code"] = code
					}
					data, err := json.Marshal(event)
					require.NoError(t, err)
					result, err, body := codexStateCollectSSETest(t, "data: "+string(data)+"\n\n", "")
					require.ErrorIs(t, err, errCodexTurnStateCollectorRateLimited)
					require.Equal(t, http.StatusOK, result.StatusCode, "semantic limits must retain the real HTTP status")
					require.Empty(t, result.Tokens)
					require.True(t, body.closed)
				})
			}
		}
	}
}

func TestCodexTurnStateCollectorSSELimitClassification(t *testing.T) {
	for _, tc := range []struct {
		name, frame string
		limited     bool
		failed      bool
	}{
		{"response_code_first", `data: {"type":"error","response":{"error":{"code":"invalid_request"}},"error":{"code":"rate_limit_exceeded"},"code":"insufficient_quota"}`, false, true},
		{"error_code_before_top", `data: {"type":"error","error":{"code":"invalid_request"},"code":"insufficient_quota"}`, false, true},
		{"response_limit_before_other_codes", `data: {"type":"error","response":{"error":{"code":"rate_limit_exceeded"}},"error":{"code":"invalid_request"},"code":"invalid_request"}`, true, true},
		{"empty_response_code_falls_through", `data: {"type":"error","response":{"error":{"code":""}},"error":{"code":"insufficient_quota"},"code":"invalid_request"}`, true, true},
		{"empty_nested_codes_fall_through", `data: {"type":"error","response":{"error":{"code":""}},"error":{"code":""},"code":"rate_limit_exceeded"}`, true, true},
		{"message_text_is_not_a_code", `data: {"type":"error","error":{"message":"rate_limit_exceeded insufficient_quota secret-body"}}`, false, true},
		{"malformed_error_still_fails", `data: {"type":"error","error":"bad"}`, false, true},
		{"nonstring_error_code_still_fails", `data: {"type":"response.failed","error":{"code":123}}`, false, true},
		{"code_is_case_sensitive", `data: {"type":"error","code":"RATE_LIMIT_EXCEEDED"}`, false, true},
		{"code_must_match_exactly", `data: {"type":"error","code":" rate_limit_exceeded "}`, false, true},
		{"output_delta_is_not_a_failure", `data: {"type":"response.output_text.delta","delta":"insufficient_quota","code":"rate_limit_exceeded"}`, false, false},
		{"json_type_wins_over_sse_event", "event: error\ndata: {\"type\":\"response.output_text.delta\",\"code\":\"rate_limit_exceeded\"}", false, false},
		{"json_failure_wins_over_sse_event", "event: response.output_text.delta\ndata: {\"type\":\"error\",\"code\":\"rate_limit_exceeded\"}", true, true},
		{"absent_json_type_uses_sse_event", "event: response.failed\ndata: {\"error\":{\"code\":\"insufficient_quota\"}}", true, true},
		{"empty_json_type_uses_sse_event", "event: response.incomplete\ndata: {\"type\":\"\",\"code\":\"rate_limit_exceeded\"}", true, true},
		{"multiline_sse_data", "event: error\ndata: {\ndata: \"error\": {\"code\":\"insufficient_quota\"}\ndata: }", true, true},
		{"event_name_does_not_leak_to_next_event", "event: error\ndata: {}\n\ndata: {\"type\":\"response.completed\"}", false, true},
		{"event_only_frame_does_not_leak_to_next_frame", "event: error\n\ndata: {\"code\":\"rate_limit_exceeded\"}", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err, body := codexStateCollectSSETest(t, tc.frame+"\n\ndata: {\"type\":\"response.completed\"}\n\n", "")
			if tc.limited {
				require.ErrorIs(t, err, errCodexTurnStateCollectorRateLimited)
			} else {
				require.NotErrorIs(t, err, errCodexTurnStateCollectorRateLimited)
			}
			if tc.failed {
				require.Error(t, err)
				require.NotContains(t, err.Error(), "secret-body")
				require.Empty(t, result.Tokens)
			} else {
				require.NoError(t, err)
			}
			require.True(t, body.closed)
		})
	}
}

func TestCodexTurnStateCollectorSSELimitStopsBeforeLaterCompletion(t *testing.T) {
	token := codexStateTestToken(10, time.Now().Add(-time.Minute))
	metadata := fmt.Sprintf("data: {\"type\":\"response.metadata\",\"headers\":{\"x-codex-turn-state\":%q}}\n\n", token)
	failure := "event: response.failed\ndata: {\"error\":{\"code\":\"rate_limit_exceeded\",\"message\":\"secret-body\"}}\n\n"
	stream := metadata + failure + "data: {\"type\":\"response.completed\"}\n\n"
	result, err, body := codexStateCollectSSETest(t, stream, token)
	require.ErrorIs(t, err, errCodexTurnStateCollectorRateLimited)
	require.NotContains(t, err.Error(), "secret-body")
	require.Empty(t, result.Tokens, "neither response headers nor prior metadata may publish after failure")
	require.Equal(t, len(metadata)+len(failure), body.read, "one-byte input must stop immediately at the failed event boundary")
	require.NotNil(t, result.Observation)
	require.Equal(t, "metadata", result.Observation.ResponseSource)
	require.Equal(t, CodexTurnStateShapeTarget, result.Observation.Shape)
	require.Equal(t, len(token), result.Observation.TokenLength)
	safeJSON, err := json.Marshal(result.Observation)
	require.NoError(t, err)
	require.NotContains(t, string(safeJSON), token)
	require.NotContains(t, string(safeJSON), "secret-body")
}

func TestCodexTurnStateCollectorRetryAfterParsing(t *testing.T) {
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name, header string
		want         time.Duration
	}{
		{"seconds", "120", 2 * time.Minute},
		{"date", now.Add(3 * time.Minute).Format(http.TimeFormat), 3 * time.Minute},
		{"past", now.Add(-time.Minute).Format(http.TimeFormat), 0},
		{"invalid", "private invalid header", 0},
		{"negative", "-1", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, codexTurnStateRetryAfter(tc.header, now))
		})
	}
}

func TestCodexTurnStateCollectorSSEAndHTTPRateLimitOutcomes(t *testing.T) {
	for _, wire := range []string{"http_429", "sse_rate_limit_exceeded", "sse_insufficient_quota"} {
		for _, pacing := range []string{"no_header", "retry_after", "longer_account_cooldown"} {
			t.Run(wire+"/"+pacing, func(t *testing.T) {
				isolateCodexTurnStateSummaryStore(t)
				s, repo, account := newCodexStateTestService(t)
				clock := time.Now().UTC().Truncate(time.Second)
				s.now = func() time.Time { return clock }
				ctx := context.Background()
				seed, err := s.Prepare(ctx, account, "gpt-5")
				require.NoError(t, err)
				markCodexStateTestBusinessSent(t, s, seed)
				oldToken := codexStateTestToken(10, clock.Add(-CodexTurnStateLifetime+10*time.Second))
				s.Observe(seed, oldToken)
				require.NoError(t, s.Finish(ctx, seed, true))
				other := seedCodexStateTestDemand(t, s, account, "gpt-5-mini")
				before, err := repo.Get(ctx, seed.key)
				require.NoError(t, err)
				newToken := codexStateTestToken(10, clock)
				var calls atomic.Int64
				wait := time.Duration(0)
				if pacing == "retry_after" {
					wait = 2 * time.Minute
				} else if pacing == "longer_account_cooldown" {
					wait = 5 * time.Minute
				}
				s.collector = NewCodexTurnStateHTTPCollector(func(_ context.Context, input CodexTurnStateCollectRequest, _ *http.Request) (*http.Response, error) {
					calls.Add(1)
					require.EqualValues(t, 2, input.ProxyID)
					header := http.Header{"X-Codex-Turn-State": {newToken}}
					if pacing != "no_header" {
						header.Set("Retry-After", "120")
					}
					if pacing == "longer_account_cooldown" {
						// An account cooldown may be updated while the network request is in flight.
						until := clock.Add(wait)
						updated := *account
						updated.RateLimitResetAt = &until
						accounts := s.accounts.(*codexStateTestAccounts)
						accounts.mu.Lock()
						accounts.account = &updated
						accounts.mu.Unlock()
					}
					status, body := http.StatusTooManyRequests, "{\"error\":\"secret-body\"}"
					if wire != "http_429" {
						status = http.StatusOK
						body = fmt.Sprintf("data: {\"type\":\"response.failed\",\"response\":{\"error\":{\"code\":%q,\"message\":\"secret-body\"}}}\n\ndata: {\"type\":\"response.completed\"}\n\n", strings.TrimPrefix(wire, "sse_"))
					}
					return &http.Response{StatusCode: status, Header: header, Body: io.NopCloser(strings.NewReader(body))}, nil
				})
				s.collect(ctx, seed.key)
				after, err := repo.Get(ctx, seed.key)
				require.NoError(t, err)
				require.EqualValues(t, 1, calls.Load())
				require.Equal(t, "collector_rate_limited", after.LastError)
				if wait > 0 {
					require.Equal(t, "backoff", after.CollectionStatus)
				} else {
					require.Equal(t, "pending", after.CollectionStatus)
				}
				require.Equal(t, "collector_rate_limited", after.CollectionReason)
				require.Equal(t, "expiring", after.DemandReason)
				require.Equal(t, clock.Add(wait), after.NextCollectAt)
				require.Equal(t, before.EncryptedToken, after.EncryptedToken, "the failure must not replace an existing valid token")
				require.Equal(t, before.ExpiresAt, after.ExpiresAt)
				require.Equal(t, before.LastBusinessAt, after.LastBusinessAt)
				requireCodexEnabledSummary(t, s, account.ID, seed.Model, newToken, "header", "collector", 0)
				// A new service instance must observe persisted owner pacing, not just a local timer.
				restarted := NewCodexTurnStateService(repo, s.accounts, s.encryptor, s.collector)
				restarted.now, restarted.modelPolicy = s.now, s.modelPolicy
				restarted.collect(ctx, other.key)
				if wait == 0 {
					require.EqualValues(t, 2, calls.Load(), "429 without Retry-After or account cooldown does not invent a fixed wait")
					return
				}
				require.EqualValues(t, 1, calls.Load())
				natural, err := s.Prepare(ctx, account, seed.Model)
				require.NoError(t, err)
				markCodexStateTestBusinessSent(t, s, natural)
				s.Observe(natural, newToken)
				require.NoError(t, s.Finish(ctx, natural, true))
				accepted, err := repo.Get(ctx, seed.key)
				require.NoError(t, err)
				plain, err := s.encryptor.Decrypt(accepted.EncryptedToken)
				require.NoError(t, err)
				require.Equal(t, newToken, plain)
				require.Equal(t, "refresh", accepted.DemandReason)
				require.Equal(t, after.NextCollectAt, accepted.NextCollectAt)
				require.Equal(t, "collector_rate_limited", accepted.LastError)
				restarted.collect(ctx, other.key)
				require.EqualValues(t, 1, calls.Load(), "successful business must not erase the owner-wide collection cooldown")
			})
		}
	}
}

type codexStateCollectorReadCloser struct {
	io.Reader
	read   int
	closed bool
}

func (r *codexStateCollectorReadCloser) Read(buffer []byte) (int, error) {
	n, err := r.Reader.Read(buffer)
	r.read += n
	return n, err
}

func (r *codexStateCollectorReadCloser) Close() error {
	r.closed = true
	return nil
}

func codexStateCollectSSETest(t *testing.T, stream, token string) (CodexTurnStateCollectResult, error, *codexStateCollectorReadCloser) {
	t.Helper()
	_, _, account := newCodexStateTestService(t)
	body := &codexStateCollectorReadCloser{Reader: iotest.OneByteReader(strings.NewReader(stream))}
	collector := NewCodexTurnStateHTTPCollector(func(context.Context, CodexTurnStateCollectRequest, *http.Request) (*http.Response, error) {
		header := make(http.Header)
		if token != "" {
			header.Set("X-Codex-Turn-State", token)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: header, Body: body}, nil
	})
	result, err := collector.Collect(context.Background(), CodexTurnStateCollectRequest{Account: account, Model: "gpt-5", ProxyID: 2})
	return result, err, body
}
