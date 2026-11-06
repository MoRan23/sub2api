package service

import (
	"context"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func engineReceiptTestAccount() *Account {
	return &Account{ID: 3001, Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
		Extra: map[string]any{OpenAIAPIKeyModeExtraKey: "codex_engine"}}
}

func engineReceiptTestID(char string) string { return "ceu_" + strings.Repeat(char, 43) }

func TestCodexEngineUsageReceiptID(t *testing.T) {
	id := engineReceiptTestID("a")
	for _, tc := range []struct {
		name    string
		headers http.Header
		valid   bool
	}{
		{"valid", http.Header{codexEngineUsageIDHeader: {id}}, true},
		{"lowercase", http.Header{strings.ToLower(codexEngineUsageIDHeader): {id}}, true},
		{"missing", nil, false},
		{"short", http.Header{codexEngineUsageIDHeader: {id[:46]}}, false},
		{"long", http.Header{codexEngineUsageIDHeader: {id + "a"}}, false},
		{"whitespace", http.Header{codexEngineUsageIDHeader: {" " + id}}, false},
		{"bad character", http.Header{codexEngineUsageIDHeader: {id[:46] + "/"}}, false},
		{"prefix", http.Header{codexEngineUsageIDHeader: {"bad_" + id[4:]}}, false},
		{"joined", http.Header{codexEngineUsageIDHeader: {id + "," + id}}, false},
		{"duplicate", http.Header{codexEngineUsageIDHeader: {id, id}}, false},
		{"duplicate case", http.Header{codexEngineUsageIDHeader: {id}, strings.ToLower(codexEngineUsageIDHeader): {id}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			account := engineReceiptTestAccount()
			got := codexEngineUsageReceiptID(account, tc.headers)
			if tc.valid {
				require.Regexp(t, `^ce:[A-Za-z0-9_-]{43}$`, got)
				require.Len(t, got, 46)
				require.Equal(t, got, codexEngineUsageReceiptID(account, tc.headers))
			} else {
				require.Empty(t, got)
			}
			account.Type = AccountTypeOAuth
			require.Empty(t, codexEngineUsageReceiptID(account, tc.headers))
			account.Type, account.Extra = AccountTypeAPIKey, nil
			require.Empty(t, codexEngineUsageReceiptID(account, tc.headers))
		})
	}
	account := engineReceiptTestAccount()
	original := codexEngineUsageReceiptID(account, http.Header{codexEngineUsageIDHeader: {id}})
	account.ID = math.MaxInt64
	scoped := codexEngineUsageReceiptID(account, http.Header{codexEngineUsageIDHeader: {id}})
	require.NotEqual(t, original, scoped)
	require.Len(t, scoped, 46, "even the longest account ID fits the real VARCHAR(64) column")
}

func TestCodexEngineUsageFinality(t *testing.T) {
	for _, tc := range []struct {
		name   string
		stream bool
		frames []string
		final  bool
	}{
		{"json", false, []string{`{"usage":{"input_tokens":5,"output_tokens":2}}`}, true},
		{"truncated json after usage", false, []string{`{"usage":{"input_tokens":5,"output_tokens":2},"result":`}, false},
		{"truncated SSE after usage", true, []string{`{"type":"response.completed","response":{"usage":{"input_tokens":5,"output_tokens":2},"result":`}, false},
		{"zero final", false, []string{`{"usage":{"input_tokens":0,"output_tokens":0}}`}, true},
		{"missing output", false, []string{`{"usage":{"input_tokens":5}}`}, false},
		{"invalid counter", false, []string{`{"usage":{"input_tokens":-5,"output_tokens":2}}`}, false},
		{"string counter", false, []string{`{"usage":{"input_tokens":"5","output_tokens":2}}`}, false},
		{"header alone", false, []string{`{"id":"resp_1"}`}, false},
		{"response complete", true, []string{`{"type":"response.completed","response":{"usage":{"input_tokens":5,"output_tokens":2}}}`}, true},
		{"response failed partial meter", true, []string{`{"type":"response.failed","response":{"usage":{"input_tokens":5,"output_tokens":2}}}`}, false},
		{"response failed JSON partial meter", false, []string{`{"status":"failed","usage":{"input_tokens":5,"output_tokens":2}}`}, false},
		{"response incomplete", true, []string{`{"type":"response.incomplete","response":{"usage":{"input_tokens":5,"output_tokens":2}}}`}, true},
		{"partial EOF", true, []string{`{"type":"response.in_progress","response":{"usage":{"input_tokens":5,"output_tokens":2}}}`}, false},
		{"terminal no usage", true, []string{`{"type":"response.in_progress","response":{"usage":{"input_tokens":5,"output_tokens":2}}}`, `{"type":"response.completed","response":{}}`}, false},
		{"response done cannot finalize partial", true, []string{`{"type":"response.in_progress","response":{"usage":{"input_tokens":5,"output_tokens":2}}}`, `{"type":"response.completed","response":{}}`, `[DONE]`}, false},
		{"chat", true, []string{`{"choices":[{"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":2}}`, `[DONE]`}, true},
		{"chat missing finish", true, []string{`{"usage":{"prompt_tokens":5,"completion_tokens":2}}`, `[DONE]`}, false},
		{"chat final missing counter", true, []string{`{"choices":[{"finish_reason":"stop"}],"usage":{"prompt_tokens":5}}`, `[DONE]`}, false},
		{"chat no done", true, []string{`{"usage":{"prompt_tokens":5,"completion_tokens":2}}`}, false},
		{"chat malformed final usage", true, []string{`{"choices":[{"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":2}}`, `{"usage":{"prompt_tokens":10,"completion_tokens":4},"choices":`, `[DONE]`}, false},
		{"messages", true, []string{`{"type":"message_start","message":{"usage":{"input_tokens":5}}}`, `{"type":"message_delta","usage":{"input_tokens":5,"output_tokens":2}}`, `{"type":"message_stop"}`}, true},
		{"messages output-only delta is partial", true, []string{`{"type":"message_start","message":{"usage":{"input_tokens":5}}}`, `{"type":"message_delta","usage":{"output_tokens":2}}`, `{"type":"message_stop"}`}, false},
		{"messages last incomplete delta invalidates prior complete", true, []string{`{"type":"message_delta","usage":{"input_tokens":5,"output_tokens":2}}`, `{"type":"message_delta","usage":{"output_tokens":3}}`, `{"type":"message_stop"}`}, false},
		{"messages last missing usage invalidates prior complete", true, []string{`{"type":"message_delta","usage":{"input_tokens":5,"output_tokens":2}}`, `{"type":"message_delta"}`, `{"type":"message_stop"}`}, false},
		{"messages missing output", true, []string{`{"type":"message_start","message":{"usage":{"input_tokens":5}}}`, `{"type":"message_stop"}`}, false},
		{"messages initial zero output is not final", true, []string{`{"type":"message_start","message":{"usage":{"input_tokens":5,"output_tokens":0}}}`, `{"type":"message_stop"}`}, false},
		{"messages malformed final delta", true, []string{`{"type":"message_start","message":{"usage":{"input_tokens":5}}}`, `{"type":"message_delta","usage":{"output_tokens":2}}`, `{"type":"message_delta","usage":{"output_tokens":4},"delta":`, `{"type":"message_stop"}`}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := &OpenAIForwardResult{Stream: tc.stream, UpstreamHeaders: http.Header{codexEngineUsageIDHeader: {engineReceiptTestID("a")}}}
			for _, data := range tc.frames {
				// The relay obtains event type from the payload when event: is absent.
				event := gjson.Get(data, "type").String()
				observeCodexEnginePayload(result, []byte(data), event)
			}
			require.Equal(t, tc.final, result.CodexEngineUsageFinalized)
		})
	}
}

type engineReceiptBillingRepo struct {
	UsageBillingRepository
	commands []*UsageBillingCommand
	claims   map[string]string
	applied  int
}

func (r *engineReceiptBillingRepo) Apply(_ context.Context, cmd *UsageBillingCommand) (*UsageBillingApplyResult, error) {
	copy := *cmd
	r.commands = append(r.commands, &copy)
	if r.claims == nil {
		r.claims = make(map[string]string)
	}
	key := fmt.Sprintf("%d:%s", cmd.APIKeyID, cmd.RequestID)
	if old, ok := r.claims[key]; ok {
		if old != cmd.RequestFingerprint {
			return nil, ErrUsageBillingRequestConflict
		}
		return &UsageBillingApplyResult{Applied: false}, nil
	}
	r.claims[key] = cmd.RequestFingerprint
	r.applied++
	return &UsageBillingApplyResult{Applied: true}, nil
}

func engineReceiptTestInput(id string) *OpenAIRecordUsageInput {
	return &OpenAIRecordUsageInput{
		Result: &OpenAIForwardResult{RequestID: "upstream-volatile", Model: "gpt-5.1", Duration: time.Second,
			Usage:           OpenAIUsage{InputTokens: 50, OutputTokens: 20, CacheReadInputTokens: 10},
			UpstreamHeaders: http.Header{codexEngineUsageIDHeader: {id}}, CodexEngineUsageFinalized: true},
		Account: engineReceiptTestAccount(), User: &User{ID: 2001}, APIKey: &APIKey{ID: 1001}, RequestPayloadHash: "first-body",
	}
}

func TestOpenAIRecordUsage_EngineReceiptStableAcrossReplay(t *testing.T) {
	logs, billing := &openAIRecordUsageLogRepoStub{inserted: true}, &engineReceiptBillingRepo{}
	svc := newOpenAIRecordUsageServiceWithBillingRepoForTest(logs, billing, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
	id := engineReceiptTestID("a")
	first := engineReceiptTestInput(id)
	require.NoError(t, svc.RecordUsage(context.WithValue(context.Background(), ctxkey.ClientRequestID, "first-client"), first))
	replay := engineReceiptTestInput(id)
	replay.Result.RequestID = "new-upstream-id"
	replay.RequestPayloadHash = "changed-body"
	svc.cfg.Default.RateMultiplier *= 4
	require.NoError(t, svc.RecordUsage(context.WithValue(context.Background(), ctxkey.ClientRequestID, "retry-client"), replay))
	require.Zero(t, billing.applied, "public context usage never creates consumption charges")
	require.Zero(t, logs.calls)
	require.Equal(t, first.Result.Usage, replay.Result.Usage)
	require.Equal(t, 50, replay.Result.Usage.InputTokens)

	conflict := engineReceiptTestInput(id)
	conflict.Result.Usage.OutputTokens++
	require.NoError(t, svc.RecordUsage(context.Background(), conflict))
	require.Zero(t, billing.applied)
	require.Zero(t, logs.calls)

	// A different receipt still settles exclusively through confirmed events.
	require.NoError(t, svc.RecordUsage(context.Background(), engineReceiptTestInput(engineReceiptTestID("b"))))
	require.Zero(t, billing.applied)
	require.Zero(t, logs.calls)

	// No downstream key can turn context usage into consumption usage.
	otherKey := engineReceiptTestInput(id)
	otherKey.APIKey.ID++
	require.NoError(t, svc.RecordUsage(context.Background(), otherKey))
	require.Zero(t, billing.applied)
}

func TestOpenAIRecordUsage_EngineReceiptNeedsFinalUsage(t *testing.T) {
	logs, billing := &openAIRecordUsageLogRepoStub{inserted: true}, &engineReceiptBillingRepo{}
	svc := newOpenAIRecordUsageServiceWithBillingRepoForTest(logs, billing, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
	partial := engineReceiptTestInput(engineReceiptTestID("a"))
	partial.Result.CodexEngineUsageFinalized = false
	require.NoError(t, svc.RecordUsage(context.Background(), partial))
	require.Empty(t, billing.commands)
	require.Zero(t, logs.calls)
	// Final public usage is context evidence, not a consumption event.
	require.NoError(t, svc.RecordUsage(context.Background(), engineReceiptTestInput(engineReceiptTestID("a"))))
	require.Zero(t, billing.applied)
	require.Zero(t, logs.calls)
}

func TestOpenAIRecordUsage_EngineFailedPartialDoesNotClaimReceipt(t *testing.T) {
	logs, billing := &openAIRecordUsageLogRepoStub{inserted: true}, &engineReceiptBillingRepo{}
	svc := newOpenAIRecordUsageServiceWithBillingRepoForTest(logs, billing, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
	partial := engineReceiptTestInput(engineReceiptTestID("a"))
	partial.Result.Stream = true
	partial.Result.CodexEngineUsageFinalized = false
	observeCodexEnginePayload(partial.Result, []byte(`{"type":"response.failed","response":{"usage":{"input_tokens":7,"output_tokens":2}}}`), "response.failed")
	require.False(t, partial.Result.CodexEngineUsageFinalized)
	require.Equal(t, OpenAIUsage{InputTokens: 7, OutputTokens: 2}, partial.Result.Usage)
	require.NoError(t, svc.RecordUsage(context.Background(), partial))
	require.Empty(t, billing.commands)
	require.Zero(t, logs.calls)
	complete := engineReceiptTestInput(engineReceiptTestID("a"))
	complete.Result.CodexEngineUsageFinalized = false
	observeCodexEnginePayload(complete.Result, []byte(`{"usage":{"input_tokens":50,"output_tokens":20}}`), "")
	require.True(t, complete.Result.CodexEngineUsageFinalized)
	require.NoError(t, svc.RecordUsage(context.Background(), complete))
	require.Zero(t, billing.applied)
	require.Zero(t, logs.calls)
}

func TestOpenAIRecordUsage_EngineReceiptFallbackAndTrustBoundary(t *testing.T) {
	for _, mode := range []string{"missing", "invalid", "nonengine", "response headers only"} {
		t.Run(mode, func(t *testing.T) {
			logs, billing := &openAIRecordUsageLogRepoStub{inserted: true}, &engineReceiptBillingRepo{}
			svc := newOpenAIRecordUsageServiceWithBillingRepoForTest(logs, billing, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
			input := engineReceiptTestInput(engineReceiptTestID("a"))
			input.Result.CodexEngineUsageFinalized = false
			switch mode {
			case "missing":
				input.Result.UpstreamHeaders = nil
			case "invalid":
				input.Result.UpstreamHeaders.Set(codexEngineUsageIDHeader, "invalid")
			case "nonengine":
				input.Account.Extra = nil
			case "response headers only":
				input.Result.ResponseHeaders, input.Result.UpstreamHeaders = input.Result.UpstreamHeaders, nil
			}
			require.NoError(t, svc.RecordUsage(context.WithValue(context.Background(), ctxkey.ClientRequestID, "legacy"), input))
			if mode == "nonengine" {
				require.Equal(t, 1, billing.applied)
				require.Equal(t, "client:legacy", billing.commands[0].RequestID)
			} else {
				require.Zero(t, billing.applied)
				require.Zero(t, logs.calls)
			}
		})
	}
}

func TestOpenAIRecordUsage_EngineReceiptRequiresRepository(t *testing.T) {
	logs, users := &openAIRecordUsageLogRepoStub{}, &openAIRecordUsageUserRepoStub{}
	svc := newOpenAIRecordUsageServiceWithBillingRepoForTest(logs, nil, users, &openAIRecordUsageSubRepoStub{}, nil)
	require.NoError(t, svc.RecordUsage(context.Background(), engineReceiptTestInput(engineReceiptTestID("a"))))
	require.Zero(t, users.deductCalls)
	require.Zero(t, logs.calls)
}

func TestCodexEngineUsageReceiptFinalityFromPhysicalResponse(t *testing.T) {
	for _, tc := range []struct {
		name, endpoint, contentType, wire string
		stream, final                     bool
	}{
		{"responses JSON", "/v1/responses", "application/json", `{"id":"resp_1","usage":{"input_tokens":5,"output_tokens":2}}`, false, true},
		{"chat JSON", "/v1/chat/completions", "application/json", `{"usage":{"prompt_tokens":5,"completion_tokens":2}}`, false, true},
		{"messages JSON", "/v1/messages", "application/json", `{"usage":{"input_tokens":5,"output_tokens":2}}`, false, true},
		{"messages cached JSON", "/v1/messages", "application/json", `{"usage":{"input_tokens":3,"cache_read_input_tokens":2,"output_tokens":2}}`, false, true},
		{"responses SSE", "/v1/responses", "text/event-stream", "event: response.completed\ndata: {\"response\":{\"usage\":{\"input_tokens\":5,\"output_tokens\":2}}}\n\n", true, true},
		{"chat SSE", "/v1/chat/completions", "text/event-stream", "data: {\"choices\":[{\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":2}}\n\ndata: [DONE]\n\n", true, true},
		{"messages SSE", "/v1/messages", "text/event-stream", "event: message_start\ndata: {\"message\":{\"usage\":{\"input_tokens\":5}}}\n\nevent: message_delta\ndata: {\"usage\":{\"input_tokens\":5,\"output_tokens\":2}}\n\nevent: message_stop\ndata: {}\n\n", true, true},
		{"messages cached SSE", "/v1/messages", "text/event-stream", "event: message_start\ndata: {\"message\":{\"usage\":{\"input_tokens\":99,\"cache_read_input_tokens\":99}}}\n\nevent: message_delta\ndata: {\"usage\":{\"input_tokens\":3,\"cache_read_input_tokens\":2,\"output_tokens\":2}}\n\nevent: message_stop\ndata: {}\n\n", true, true},
		{"partial SSE", "/v1/responses", "text/event-stream", "event: response.in_progress\ndata: {\"response\":{\"usage\":{\"input_tokens\":5,\"output_tokens\":2}}}\n\n", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, tc.endpoint, nil)
			c.Request.Header.Set(codexEngineUsageIDHeader, engineReceiptTestID("x"))
			headers := http.Header{"Content-Type": {tc.contentType}, codexEngineUsageIDHeader: {engineReceiptTestID("a")}}
			upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: headers, Body: io.NopCloser(strings.NewReader(tc.wire))}}
			svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
			account := engineAccount()
			account.ID = 3001
			body := []byte(fmt.Sprintf(`{"model":"alias","stream":%t}`, tc.stream))
			result, err := svc.forwardCodexEngine(context.Background(), c, account, body, tc.endpoint, "")
			if tc.final {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
			require.NotNil(t, result)
			require.Equal(t, tc.final, result.CodexEngineUsageFinalized)
			require.Equal(t, 5, result.Usage.InputTokens)
			require.Equal(t, 2, result.Usage.OutputTokens)
			require.Equal(t, codexEngineUsageReceiptID(account, headers), codexEngineUsageReceiptID(account, result.UpstreamHeaders))
			require.Empty(t, upstream.lastReq.Header.Get(codexEngineUsageIDHeader), "client-supplied receipts must not be forwarded")
			require.Equal(t, tc.wire, recorder.Body.String())
		})
	}
}

func TestCodexEngineReceiptFinalCountersMatchJSONReplay(t *testing.T) {
	for _, tc := range []struct {
		name   string
		frames []string
		replay string
		want   OpenAIUsage
	}{
		{"responses zero replaces progressive", []string{
			`{"type":"response.in_progress","response":{"usage":{"input_tokens":50,"output_tokens":20,"input_tokens_details":{"cached_tokens":10}}}}`,
			`{"type":"response.completed","response":{"usage":{"input_tokens":0,"output_tokens":0}}}`,
		}, `{"usage":{"input_tokens":0,"output_tokens":0}}`, OpenAIUsage{}},
		{"responses removes previous cache", []string{
			`{"type":"response.in_progress","response":{"usage":{"input_tokens":50,"output_tokens":20,"input_tokens_details":{"cached_tokens":10}}}}`,
			`{"type":"response.completed","response":{"usage":{"input_tokens":50,"output_tokens":20}}}`,
		}, `{"usage":{"input_tokens":50,"output_tokens":20}}`, OpenAIUsage{InputTokens: 50, OutputTokens: 20}},
		{"chat zero replaces previous chunk", []string{
			`{"usage":{"prompt_tokens":50,"completion_tokens":20,"prompt_tokens_details":{"cached_tokens":10}}}`,
			`{"choices":[{"finish_reason":"stop"}],"usage":{"prompt_tokens":0,"completion_tokens":0}}`, `[DONE]`,
		}, `{"usage":{"prompt_tokens":0,"completion_tokens":0}}`, OpenAIUsage{}},
		{"messages final zero output", []string{
			`{"type":"message_start","message":{"usage":{"input_tokens":50,"cache_read_input_tokens":10}}}`,
			`{"type":"message_delta","usage":{"output_tokens":20}}`,
			`{"type":"message_delta","usage":{"input_tokens":50,"output_tokens":0}}`, `{"type":"message_stop"}`,
		}, `{"usage":{"input_tokens":50,"output_tokens":0}}`, OpenAIUsage{InputTokens: 50}},
		{"messages final full counters replace start", []string{
			`{"type":"message_start","message":{"usage":{"input_tokens":50,"cache_read_input_tokens":10}}}`,
			`{"type":"message_delta","usage":{"input_tokens":0,"output_tokens":0}}`, `{"type":"message_stop"}`,
		}, `{"usage":{"input_tokens":0,"output_tokens":0}}`, OpenAIUsage{}},
		{"messages final cache counters", []string{
			`{"type":"message_start","message":{"usage":{"input_tokens":999,"cache_read_input_tokens":999}}}`,
			`{"type":"message_delta","usage":{"input_tokens":50,"cache_read_input_tokens":10,"cache_creation_input_tokens":2,"output_tokens":20}}`, `{"type":"message_stop"}`,
		}, `{"usage":{"input_tokens":50,"cache_read_input_tokens":10,"cache_creation_input_tokens":2,"output_tokens":20}}`, OpenAIUsage{InputTokens: 62, CacheReadInputTokens: 10, CacheCreationInputTokens: 2, OutputTokens: 20}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			original := engineReceiptTestInput(engineReceiptTestID("a"))
			original.Result.Stream = true
			original.Result.CodexEngineUsageFinalized = false
			if strings.HasPrefix(tc.name, "messages") {
				original.Result.UpstreamEndpoint = "/v1/messages"
			}
			for _, frame := range tc.frames {
				observeCodexEnginePayload(original.Result, []byte(frame), gjson.Get(frame, "type").String())
			}
			require.True(t, original.Result.CodexEngineUsageFinalized)
			require.Equal(t, tc.want, original.Result.Usage)
			replay := engineReceiptTestInput(engineReceiptTestID("a"))
			replay.Result.CodexEngineUsageFinalized = false
			replay.Result.UpstreamEndpoint = original.Result.UpstreamEndpoint
			observeCodexEnginePayload(replay.Result, []byte(tc.replay), "")
			require.True(t, replay.Result.CodexEngineUsageFinalized)
			require.Equal(t, tc.want, replay.Result.Usage)
			id := codexEngineUsageReceiptID(original.Account, original.Result.UpstreamHeaders)
			require.Equal(t, newCodexEngineUsageReceipt(id, original).fingerprint, newCodexEngineUsageReceipt(id, replay).fingerprint)
		})
	}
}

func TestCodexEngineWithoutValidReceiptRetainsProgressiveUsage(t *testing.T) {
	for _, receipt := range []string{"", "invalid"} {
		result := &OpenAIForwardResult{Stream: true, UpstreamHeaders: http.Header{codexEngineUsageIDHeader: {receipt}}}
		observeCodexEnginePayload(result, []byte(`{"response":{"usage":{"input_tokens":50,"output_tokens":20}}}`), "response.in_progress")
		observeCodexEnginePayload(result, []byte(`{"response":{"usage":{"input_tokens":0,"output_tokens":0}}}`), "response.completed")
		require.Equal(t, OpenAIUsage{InputTokens: 50, OutputTokens: 20}, result.Usage)
		require.False(t, result.CodexEngineUsageFinalized)
	}
}

func TestOpenAIRecordUsage_EngineMessagesJSONAndSSEReceiptChargeOnce(t *testing.T) {
	logs, billing := &openAIRecordUsageLogRepoStub{inserted: true}, &engineReceiptBillingRepo{}
	svc := newOpenAIRecordUsageServiceWithBillingRepoForTest(logs, billing, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
	const usage = `{"input_tokens":3,"cache_read_input_tokens":2,"cache_creation_input_tokens":1,"output_tokens":2}`
	original := engineReceiptTestInput(engineReceiptTestID("a"))
	original.Result.Stream, original.Result.CodexEngineUsageFinalized = true, false
	original.Result.UpstreamEndpoint = "/v1/messages"
	observeCodexEnginePayload(original.Result, []byte(`{"usage":`+usage+`}`), "message_delta")
	observeCodexEnginePayload(original.Result, []byte(`{}`), "message_stop")
	require.True(t, original.Result.CodexEngineUsageFinalized)
	require.Equal(t, 6, original.Result.Usage.InputTokens)
	require.NoError(t, svc.RecordUsage(context.Background(), original))
	replay := engineReceiptTestInput(engineReceiptTestID("a"))
	replay.Result.CodexEngineUsageFinalized = false
	replay.Result.UpstreamEndpoint = "/v1/messages"
	observeCodexEnginePayload(replay.Result, []byte(`{"usage":`+usage+`}`), "")
	require.True(t, replay.Result.CodexEngineUsageFinalized)
	require.Equal(t, original.Result.Usage, replay.Result.Usage)
	require.NoError(t, svc.RecordUsage(context.WithValue(context.Background(), ctxkey.ClientRequestID, "replay"), replay))
	require.Zero(t, billing.applied)
	require.Zero(t, logs.calls)
}
