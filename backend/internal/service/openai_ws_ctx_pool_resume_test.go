package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

const ctxPoolResumeTurnID = "018f5c3c-6e3a-7ac1-8def-1234567890b0"

type ctxPoolResumeOutcome struct {
	firstErr, finalErr error
	retry              []byte
	currentTurn        bool
	capture            OpenAIOAuthIdentityCapture
	hasCapture         bool
}

type ctxPoolResumeHarness struct {
	client      *coderws.Conn
	first, next *openAIWSCaptureConn
	dialer      *openAIWSQueueDialer
	done        <-chan ctxPoolResumeOutcome
}

func newCtxPoolResumeHarness(t *testing.T, events [][]byte, replace bool, configureDialer ...func(*openAIWSQueueDialer) openAIWSClientDialer) *ctxPoolResumeHarness {
	t.Helper()
	return newCtxPoolResumeHarnessForAccount(t, events, replace, false, configureDialer...)
}

func newCtxPoolResumeHarnessForAccount(t *testing.T, events [][]byte, replace, oauth bool, configureDialer ...func(*openAIWSQueueDialer) openAIWSClientDialer) *ctxPoolResumeHarness {
	t.Helper()
	gin.SetMode(gin.TestMode)
	cfg := newOpenAIWSV2TestConfig()
	cfg.JWT.Secret = "ctx-pool-resume-test-secret"
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	cfg.Gateway.OpenAIWS.ModeRouterV2Enabled = true
	cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 1
	cfg.Gateway.OpenAIWS.MinIdlePerAccount = 0
	cfg.Gateway.OpenAIWS.MaxIdlePerAccount = 1
	cfg.Gateway.OpenAIWS.QueueLimitPerConn = 8
	cfg.Gateway.OpenAIWS.DialTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.WriteTimeoutSeconds = 3
	first := &openAIWSCaptureConn{events: events}
	next := &openAIWSCaptureConn{events: [][]byte{[]byte(`{"type":"response.completed","response":{"id":"resp_replacement","output":[],"usage":{"input_tokens":4,"output_tokens":1}}}`)}}
	dialer := &openAIWSQueueDialer{conns: []openAIWSClientConn{first, next}}
	pool := newOpenAIWSConnPool(cfg)
	var upstreamDialer openAIWSClientDialer = dialer
	for _, configure := range configureDialer {
		upstreamDialer = configure(dialer)
	}
	pool.setClientDialerForTest(upstreamDialer)
	svc := &OpenAIGatewayService{
		cfg: cfg, cache: &stubGatewayCache{}, httpUpstream: &httpUpstreamRecorder{},
		openaiWSResolver: NewOpenAIWSProtocolResolver(cfg), toolCorrector: NewCodexToolCorrector(), openaiWSPool: pool,
	}
	account := &Account{
		ID: 88701, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1,
		Credentials: map[string]any{"api_key": "key-a", "model_mapping": map[string]any{"gpt-6-astra": "gpt-5.1", "gpt-6.1-sol": "gpt-5.5"}},
		Extra:       map[string]any{"openai_apikey_responses_websockets_v2_mode": OpenAIWSIngressModeCtxPool},
	}
	replacement := *account
	replacement.ID = 88702
	replacement.Credentials = map[string]any{"api_key": "key-b", "model_mapping": map[string]any{"gpt-6.1-sol": "gpt-6-sol", "gpt-6-sol": "must-not-map-twice"}}
	token, replacementToken := "key-a", "key-b"
	if oauth {
		account.Type, replacement.Type = AccountTypeOAuth, AccountTypeOAuth
		account.Extra = map[string]any{"openai_oauth_responses_websockets_v2_mode": OpenAIWSIngressModeCtxPool}
		replacement.Extra = map[string]any{"openai_oauth_responses_websockets_v2_mode": OpenAIWSIngressModeCtxPool}
		account.Credentials["access_token"] = "access-token-a"
		account.Credentials["chatgpt_account_id"] = "chatgpt-account-a"
		account.Credentials["chatgpt_user_id"] = "chatgpt-user-a"
		replacement.Credentials["access_token"] = "access-token-b"
		replacement.Credentials["chatgpt_account_id"] = "chatgpt-account-b"
		replacement.Credentials["chatgpt_user_id"] = "chatgpt-user-b"
		token, replacementToken = "access-token-a", "access-token-b"
	}
	svc.accountRepo = newAuthorizedOpenAIOAuthTestRepo(account, &replacement)
	hooks := &OpenAIWSIngressHooks{ReasoningEffortMappings: []ReasoningEffortMapping{
		{From: "high", To: "medium"}, {From: "medium", To: "low"},
	}}
	done := make(chan ctxPoolResumeOutcome, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := coderws.Accept(w, r, nil)
		if err != nil {
			done <- ctxPoolResumeOutcome{firstErr: err, finalErr: err}
			return
		}
		defer conn.CloseNow()
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = r.Clone(r.Context())
		c.Set("api_key", &APIKey{ID: 88703})
		readCtx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		_, firstMessage, err := conn.Read(readCtx)
		cancel()
		if err != nil {
			done <- ctxPoolResumeOutcome{firstErr: err, finalErr: err}
			return
		}
		SetOpenAIOAuthIdentityCapture(c, CaptureOpenAIOAuthIdentity(c, firstMessage, "ctx-pool-resume"))
		firstErr := svc.ProxyResponsesWebSocketFromClient(r.Context(), c, conn, account, token, firstMessage, hooks)
		retry, currentTurn := OpenAIWSCurrentTurnRetryPayload(firstErr)
		capture, hasCapture := OpenAIWSCurrentTurnRetryIdentityCapture(firstErr)
		outcome := ctxPoolResumeOutcome{firstErr: firstErr, finalErr: firstErr, retry: retry, currentTurn: currentTurn, capture: capture, hasCapture: hasCapture}
		if replace && currentTurn && len(retry) > 0 {
			if hasCapture {
				SetOpenAIOAuthIdentityCapture(c, capture)
			}
			outcome.finalErr = svc.ProxyResponsesWebSocketFromClient(r.Context(), c, conn, &replacement, replacementToken, retry, hooks)
		}
		done <- outcome
	}))
	t.Cleanup(server.Close)
	dialCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	client, _, err := coderws.Dial(dialCtx, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
	cancel()
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.CloseNow() })
	return &ctxPoolResumeHarness{client: client, first: first, next: next, dialer: dialer, done: done}
}

func (h *ctxPoolResumeHarness) write(t *testing.T, payload []byte) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	require.NoError(t, h.client.Write(ctx, coderws.MessageText, payload))
}

func (h *ctxPoolResumeHarness) read(t *testing.T) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, payload, err := h.client.Read(ctx)
	require.NoError(t, err)
	return payload
}

func (h *ctxPoolResumeHarness) outcome(t *testing.T) ctxPoolResumeOutcome {
	t.Helper()
	select {
	case outcome := <-h.done:
		return outcome
	case <-time.After(5 * time.Second):
		t.Fatal("ctx_pool resume did not finish")
		return ctxPoolResumeOutcome{}
	}
}

func ctxPoolResumeCompleted(turn int) []byte {
	return []byte(fmt.Sprintf(`{"type":"response.completed","response":{"id":"resp_turn_%d","output":[{"id":"msg_%d","type":"message","role":"assistant","content":[{"type":"output_text","text":"assistant-turn-%d"}]},{"id":"fc_%d","type":"function_call","call_id":"call_%d","name":"inspect","arguments":"{}"}],"usage":{"input_tokens":1,"output_tokens":1}}}`, turn, turn, turn, turn, turn))
}

func ctxPoolResumeCreate(turn int, final bool) []byte {
	model := "gpt-6-astra"
	if final {
		model = "gpt-6.1-sol"
	}
	request := map[string]any{"type": "response.create", "model": model, "store": false,
		"client_metadata": map[string]any{"session_id": "client-session", "thread_id": "client-thread"},
	}
	if final {
		request["client_metadata"].(map[string]any)["x-codex-turn-metadata"] = `{"turn_id":"` + ctxPoolResumeTurnID + `"}`
		request["prompt_cache_key"] = "current-turn-cache"
		request["reasoning"] = map[string]any{"effort": "high"}
	}
	if turn == 1 {
		request["input"] = []map[string]any{{"role": "user", "content": "first-user-input"}}
	} else {
		request["previous_response_id"] = fmt.Sprintf("resp_turn_%d", turn-1)
		request["input"] = []map[string]any{{"type": "function_call_output", "call_id": fmt.Sprintf("call_%d", turn-1), "output": fmt.Sprintf("tool-result-turn-%d", turn)}}
	}
	encoded, _ := json.Marshal(request)
	return encoded
}

func ctxPoolResumeRateLimit() []byte {
	return []byte(`{"type":"error","error":{"code":"usage_limit_reached","type":"usage_limit_reached","message":"quota exhausted"}}`)
}

func TestOpenAIWSCtxPoolLaterTurn429ResumesCurrentTurn(t *testing.T) {
	for _, failTurn := range []int{2, 3} {
		t.Run(fmt.Sprintf("turn_%d", failTurn), func(t *testing.T) {
			var events [][]byte
			for turn := 1; turn < failTurn; turn++ {
				events = append(events, ctxPoolResumeCompleted(turn))
			}
			events = append(events, ctxPoolResumeRateLimit())
			h := newCtxPoolResumeHarness(t, events, true)
			for turn := 1; turn < failTurn; turn++ {
				h.write(t, ctxPoolResumeCreate(turn, false))
				require.Equal(t, fmt.Sprintf("resp_turn_%d", turn), gjson.GetBytes(h.read(t), "response.id").String())
			}
			h.write(t, ctxPoolResumeCreate(failTurn, true))
			require.Equal(t, "resp_replacement", gjson.GetBytes(h.read(t), "response.id").String())
			_ = h.client.Close(coderws.StatusNormalClosure, "done")
			outcome := h.outcome(t)
			var failover *UpstreamFailoverError
			require.ErrorAs(t, outcome.firstErr, &failover)
			require.Equal(t, http.StatusTooManyRequests, failover.StatusCode)
			require.NoError(t, outcome.finalErr)
			require.True(t, outcome.currentTurn)
			require.NotEmpty(t, outcome.retry)
			require.False(t, gjson.GetBytes(outcome.retry, "previous_response_id").Exists())
			require.Equal(t, "gpt-6.1-sol", gjson.GetBytes(outcome.retry, "model").String())
			require.Equal(t, "high", gjson.GetBytes(outcome.retry, "reasoning.effort").String(), "retry snapshot must retain the client's pre-policy effort")
			require.True(t, outcome.hasCapture)
			require.Equal(t, "client-session", outcome.capture.Logical.SessionKey)
			require.Equal(t, "client-thread", outcome.capture.Logical.ThreadKey)
			require.Equal(t, ctxPoolResumeTurnID, outcome.capture.RequestTurn.ID)
			require.Equal(t, "current-turn-cache", outcome.capture.PromptCacheKey.Value)
			input := gjson.GetBytes(outcome.retry, "input").Raw
			require.Equal(t, 1, strings.Count(input, "first-user-input"))
			for turn := 1; turn < failTurn; turn++ {
				require.Equal(t, 1, strings.Count(input, fmt.Sprintf("assistant-turn-%d", turn)))
				require.Equal(t, 1, strings.Count(input, fmt.Sprintf(`"id":"fc_%d"`, turn)))
				require.Equal(t, 1, strings.Count(input, fmt.Sprintf("tool-result-turn-%d", turn+1)))
				require.Less(t, strings.Index(input, fmt.Sprintf("assistant-turn-%d", turn)), strings.Index(input, fmt.Sprintf("tool-result-turn-%d", turn+1)))
			}
			require.Len(t, h.first.writes, failTurn)
			require.Equal(t, "medium", gjson.Get(requestToJSONString(h.first.writes[failTurn-1]), "reasoning.effort").String())
			require.Len(t, h.next.writes, 1, "only the failed turn executes on the replacement account")
			replacementBody := requestToJSONString(h.next.writes[0])
			require.Equal(t, "gpt-6-sol", gjson.Get(replacementBody, "model").String(), "replacement account maps the original model once")
			require.Equal(t, "medium", gjson.Get(replacementBody, "reasoning.effort").String(), "non-idempotent effort policy must apply once per attempt, not twice after failover")
			require.Contains(t, replacementBody, fmt.Sprintf("tool-result-turn-%d", failTurn))
			require.Equal(t, 2, h.dialer.DialCount())
			require.Equal(t, "Bearer key-b", h.dialer.DialHeaders(1).Get("Authorization"))
		})
	}
}

func TestOpenAIWSCtxPoolLaterTurn429AfterOutputDoesNotReplay(t *testing.T) {
	for _, prefix := range []string{
		`{"type":"response.created","response":{"id":"resp_partial"}}`,
		`{"type":"response.output_text.delta","delta":"partial"}`,
	} {
		t.Run(gjson.Get(prefix, "type").String(), func(t *testing.T) {
			events := [][]byte{ctxPoolResumeCompleted(1), []byte(prefix), ctxPoolResumeRateLimit(), []byte(`{"type":"response.failed","response":{"id":"resp_partial","error":{"code":"usage_limit_reached","message":"quota exhausted"}}}`)}
			h := newCtxPoolResumeHarness(t, events, true)
			h.write(t, ctxPoolResumeCreate(1, false))
			h.read(t)
			h.write(t, ctxPoolResumeCreate(2, true))
			require.Equal(t, gjson.Get(prefix, "type").String(), gjson.GetBytes(h.read(t), "type").String())
			require.Equal(t, "error", gjson.GetBytes(h.read(t), "type").String())
			require.Equal(t, "response.failed", gjson.GetBytes(h.read(t), "type").String())
			_ = h.client.Close(coderws.StatusNormalClosure, "done")
			outcome := h.outcome(t)
			var failover *UpstreamFailoverError
			require.False(t, errors.As(outcome.firstErr, &failover))
			require.False(t, outcome.currentTurn)
			require.Empty(t, outcome.retry)
			require.Empty(t, h.next.writes)
			require.Equal(t, 1, h.dialer.DialCount())
		})
	}
}

func TestOpenAIWSCtxPoolLaterTurn429WithoutReplayHistoryNeverReturnsFirstTurn(t *testing.T) {
	for _, tc := range []struct{ name, request string }{
		{"orphan_tool_output", `{"type":"response.create","model":"gpt-6.1-sol","previous_response_id":"resp_turn_1","input":[{"type":"function_call_output","call_id":"never-issued","output":"current-orphan"}]}`},
		{"external_response_chain", `{"type":"response.create","model":"gpt-6.1-sol","previous_response_id":"resp_external","input":[{"type":"function_call_output","call_id":"call_1","output":"external-chain-delta"}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newCtxPoolResumeHarness(t, [][]byte{ctxPoolResumeCompleted(1), ctxPoolResumeRateLimit()}, true)
			h.write(t, ctxPoolResumeCreate(1, false))
			h.read(t)
			h.write(t, []byte(tc.request))
			outcome := h.outcome(t)
			var failover *UpstreamFailoverError
			require.ErrorAs(t, outcome.firstErr, &failover)
			require.True(t, outcome.currentTurn, "unsafe continuation must remain marked as a later-turn failure")
			require.Empty(t, outcome.retry, "do not invent complete history or replay the first frame")
			require.Empty(t, h.next.writes)
			require.Equal(t, 1, h.dialer.DialCount())
		})
	}
}

// The first turn succeeds, but the leased socket is stale when the next frame
// is written. Its existing retry path must preserve the current frame if the
// replacement socket is rejected during the HTTP handshake.
type ctxPoolResumeStaleConn struct {
	*openAIWSCaptureConn
}

func (c *ctxPoolResumeStaleConn) WriteJSON(ctx context.Context, value any) error {
	c.mu.Lock()
	used := len(c.writes) > 0
	c.mu.Unlock()
	if used {
		return errors.New("stale upstream socket write failed")
	}
	return c.openAIWSCaptureConn.WriteJSON(ctx, value)
}

type ctxPoolResumeHandshake429Dialer struct {
	mu    sync.Mutex
	calls int
	base  *openAIWSQueueDialer
}

func (d *ctxPoolResumeHandshake429Dialer) Dial(ctx context.Context, address string, headers http.Header, proxy string) (openAIWSClientConn, int, http.Header, error) {
	d.mu.Lock()
	d.calls++
	call := d.calls
	d.mu.Unlock()
	if call > 1 {
		return nil, http.StatusTooManyRequests, http.Header{"Retry-After": []string{"60"}}, errors.New("upstream websocket handshake rate limited")
	}
	return d.base.Dial(ctx, address, headers, proxy)
}

func TestOpenAIWSCtxPoolLaterTurn429DuringReconnectCarriesCurrentFrame(t *testing.T) {
	var reconnect *ctxPoolResumeHandshake429Dialer
	completed := []byte(`{"type":"response.completed","response":{"id":"resp_turn_1","output":[{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"output_text","text":"first-answer"}]}],"usage":{"input_tokens":1,"output_tokens":1}}}`)
	h := newCtxPoolResumeHarness(t, [][]byte{completed}, false, func(base *openAIWSQueueDialer) openAIWSClientDialer {
		base.conns[0] = &ctxPoolResumeStaleConn{openAIWSCaptureConn: base.conns[0].(*openAIWSCaptureConn)}
		reconnect = &ctxPoolResumeHandshake429Dialer{base: base}
		return reconnect
	})
	h.write(t, ctxPoolResumeCreate(1, false))
	require.Equal(t, "resp_turn_1", gjson.GetBytes(h.read(t), "response.id").String())
	var second map[string]any
	require.NoError(t, json.Unmarshal(ctxPoolResumeCreate(2, true), &second))
	delete(second, "previous_response_id")
	second["input"] = []map[string]any{
		{"role": "user", "content": "first-user-input"},
		{"type": "message", "role": "assistant", "content": []map[string]any{{"type": "output_text", "text": "first-answer"}}},
		{"role": "user", "content": "current-after-reconnect"},
	}
	payload, err := json.Marshal(second)
	require.NoError(t, err)
	h.write(t, payload)
	outcome := h.outcome(t)
	var failover *UpstreamFailoverError
	require.ErrorAs(t, outcome.firstErr, &failover)
	require.Equal(t, http.StatusTooManyRequests, failover.StatusCode)
	require.Equal(t, "60", failover.ResponseHeaders.Get("Retry-After"))
	require.True(t, outcome.currentTurn, "handshake errors after earlier completed turns must never be treated as first-turn errors")
	require.NotEmpty(t, outcome.retry)
	require.Contains(t, string(outcome.retry), "current-after-reconnect")
	require.Contains(t, string(outcome.retry), "first-answer")
	require.Equal(t, "gpt-6.1-sol", gjson.GetBytes(outcome.retry, "model").String())
	require.True(t, outcome.hasCapture)
	require.Equal(t, ctxPoolResumeTurnID, outcome.capture.RequestTurn.ID)
	require.Len(t, h.first.writes, 1, "only the completed first turn reached the old socket")
	require.Empty(t, h.next.writes)
	reconnect.mu.Lock()
	calls := reconnect.calls
	reconnect.mu.Unlock()
	require.Equal(t, 2, calls, "the second physical dial must exercise HTTP 429 handling")
}

type ctxPoolResumeTurnStateDialer struct {
	base *openAIWSQueueDialer
}

func (d *ctxPoolResumeTurnStateDialer) Dial(ctx context.Context, address string, headers http.Header, proxy string) (openAIWSClientConn, int, http.Header, error) {
	conn, status, responseHeaders, err := d.base.Dial(ctx, address, headers, proxy)
	if headers.Get("Authorization") == "Bearer access-token-a" {
		responseHeaders = make(http.Header)
		responseHeaders.Set(openAIWSTurnStateHeader, "old-account-turn-state")
	}
	return conn, status, responseHeaders, err
}

func TestOpenAIWSCtxPoolLaterTurn429OAuthRematerializesCurrentIdentity(t *testing.T) {
	h := newCtxPoolResumeHarnessForAccount(t, [][]byte{ctxPoolResumeCompleted(1), ctxPoolResumeRateLimit()}, true, true,
		func(base *openAIWSQueueDialer) openAIWSClientDialer { return &ctxPoolResumeTurnStateDialer{base: base} })
	h.write(t, ctxPoolResumeCreate(1, false))
	require.Equal(t, "resp_turn_1", gjson.GetBytes(h.read(t), "response.id").String())
	h.write(t, ctxPoolResumeCreate(2, true))
	require.Equal(t, "resp_replacement", gjson.GetBytes(h.read(t), "response.id").String())
	_ = h.client.Close(coderws.StatusNormalClosure, "done")
	outcome := h.outcome(t)
	require.NoError(t, outcome.finalErr)
	var failover *UpstreamFailoverError
	require.ErrorAs(t, outcome.firstErr, &failover)
	require.Equal(t, "old-account-turn-state", failover.ResponseHeaders.Get(openAIWSTurnStateHeader), "fixture must supply account A's actual handshake state")
	require.True(t, outcome.currentTurn)
	require.True(t, outcome.hasCapture)
	require.Equal(t, ctxPoolResumeTurnID, outcome.capture.RequestTurn.ID)
	require.Equal(t, "client-session", outcome.capture.Logical.SessionKey)
	require.Equal(t, "client-thread", outcome.capture.Logical.ThreadKey)
	require.Equal(t, "current-turn-cache", outcome.capture.PromptCacheKey.Value)
	require.Equal(t, "high", gjson.GetBytes(outcome.retry, "reasoning.effort").String())
	require.Len(t, h.first.writes, 2)
	require.Len(t, h.next.writes, 1)
	firstBody := requestToJSONString(h.first.writes[1])
	nextBody := requestToJSONString(h.next.writes[0])
	for _, field := range []string{"session_id", "thread_id"} {
		firstID := gjson.Get(firstBody, "client_metadata."+field).String()
		nextID := gjson.Get(nextBody, "client_metadata."+field).String()
		for _, value := range []string{firstID, nextID} {
			parsed, err := uuid.Parse(value)
			require.NoError(t, err)
			require.Equal(t, uuid.Version(7), parsed.Version())
		}
		require.NotEqual(t, firstID, nextID, "replacement OAuth owner must get its own projected "+field)
	}
	require.NotEqual(t, gjson.Get(firstBody, "prompt_cache_key").String(), gjson.Get(nextBody, "prompt_cache_key").String())
	require.Equal(t, "medium", gjson.Get(firstBody, "reasoning.effort").String())
	require.Equal(t, "medium", gjson.Get(nextBody, "reasoning.effort").String())
	require.Equal(t, "gpt-6-sol", gjson.Get(nextBody, "model").String())
	require.Contains(t, nextBody, "assistant-turn-1")
	require.Contains(t, nextBody, "tool-result-turn-2")
	require.NotContains(t, nextBody, "old-account-turn-state")
	require.False(t, gjson.Get(nextBody, "previous_response_id").Exists())
	nextHeaders := h.dialer.DialHeaders(1)
	require.Equal(t, "Bearer access-token-b", nextHeaders.Get("Authorization"))
	require.Equal(t, "chatgpt-account-b", nextHeaders.Get("ChatGPT-Account-Id"))
	require.Empty(t, nextHeaders.Get(openAIWSTurnStateHeader), "old credential owner's turn-state must not reach the replacement handshake")
}
