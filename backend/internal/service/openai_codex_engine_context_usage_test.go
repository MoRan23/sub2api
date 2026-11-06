package service

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// Standard terminal Responses usage is authoritative for the client's context.
// Cached input is included in input_tokens, rather than added to it.
const engineContextUsageFixture = `{"input_tokens":1200,"input_tokens_details":{"cached_tokens":960},"output_tokens":83,"total_tokens":1283}`

func forwardEngineContextUsage(t *testing.T, endpoint string, stream, receipt bool) *OpenAIForwardResult {
	t.Helper()
	response := `{"id":"resp_context","object":"response","status":"completed","usage":` + engineContextUsageFixture + `,"exact":9007199254740993123,"output":[]}`
	wire := response
	contentType := "application/json"
	if stream {
		// Interim observations are not added to the authoritative final usage.
		wire = "event: response.in_progress\ndata: {\"type\":\"response.in_progress\",\"response\":{\"usage\":{\"input_tokens\":300,\"input_tokens_details\":{\"cached_tokens\":200},\"output_tokens\":15}}}\n\n" +
			"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":" + response + "}\n\n"
		contentType = "text/event-stream"
	}
	headers := http.Header{"Content-Type": {contentType}}
	if receipt {
		headers.Set(codexEngineUsageIDHeader, engineReceiptTestID("a"))
	}
	upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: http.StatusOK, Header: headers, Body: io.NopCloser(strings.NewReader(wire))}}
	svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, endpoint, nil)
	account := engineAccount()
	account.ID = 3001
	result, err := svc.forwardCodexEngine(context.Background(), c, account, []byte(fmt.Sprintf(`{"model":"alias","stream":%t}`, stream)), endpoint, "")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, wire, rec.Body.String(), "standard usage and all response bytes must reach Codex unchanged")
	require.Equal(t, OpenAIUsage{InputTokens: 1200, CacheReadInputTokens: 960, OutputTokens: 83}, result.Usage)
	return result
}

func TestCodexEngineStandardContextUsageForwardingAndBilling(t *testing.T) {
	for _, tc := range []struct {
		name, endpoint  string
		stream, receipt bool
	}{
		{"responses JSON", "/v1/responses", false, true},
		{"responses SSE", "/v1/responses", true, true},
		{"compact JSON", "/v1/responses/compact", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := forwardEngineContextUsage(t, tc.endpoint, tc.stream, tc.receipt)
			logs, billing := &openAIRecordUsageLogRepoStub{inserted: true}, &engineReceiptBillingRepo{}
			svc := newOpenAIRecordUsageServiceWithBillingRepoForTest(logs, billing, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
			input := engineReceiptTestInput(engineReceiptTestID("a"))
			input.Result = result
			require.NoError(t, svc.RecordUsage(context.Background(), input))
			require.Zero(t, billing.applied)
			require.Zero(t, logs.calls)
			job, _ := engineFrozenTestSnapshot(t)
			event := engineFrozenTestModelEvent()
			event.Usage = OpenAIUsage{InputTokens: 2400, CacheReadInputTokens: 1920, OutputTokens: 166}
			store := &engineFrozenStoreStub{}
			require.NoError(t, svc.SettleCodexEngineBillingEvent(context.Background(), store, job, event))
			require.Equal(t, 1, store.applied)
			require.Equal(t, 480, store.received.Command.InputTokens)
			require.Equal(t, 1920, store.received.Command.CacheReadTokens)
			require.Equal(t, 166, store.received.Command.OutputTokens)
			require.Equal(t, OpenAIUsage{InputTokens: 1200, CacheReadInputTokens: 960, OutputTokens: 83}, result.Usage)
		})
	}
}

func TestCodexEngineStandardContextUsageReplayDeduplicates(t *testing.T) {
	logs, billing := &openAIRecordUsageLogRepoStub{inserted: true}, &engineReceiptBillingRepo{}
	svc := newOpenAIRecordUsageServiceWithBillingRepoForTest(logs, billing, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
	job, _ := engineFrozenTestSnapshot(t)
	event, store := engineFrozenTestModelEvent(), &engineFrozenStoreStub{}
	for _, stream := range []bool{true, false, false} {
		input := engineReceiptTestInput(engineReceiptTestID("a"))
		input.Result = forwardEngineContextUsage(t, "/v1/responses", stream, true)
		require.True(t, input.Result.CodexEngineUsageFinalized)
		require.NoError(t, svc.RecordUsage(context.Background(), input))
		require.NoError(t, svc.SettleCodexEngineBillingEvent(context.Background(), store, job, event))
	}
	require.Zero(t, billing.applied)
	require.Zero(t, logs.calls)
	require.Empty(t, billing.commands)
	require.Equal(t, 1, store.applied, "confirmed event replay does not create another settlement")
	require.Equal(t, 80, store.received.Command.InputTokens)
	require.Equal(t, 20, store.received.Command.CacheReadTokens)
	require.Equal(t, 10, store.received.Command.OutputTokens)
}

func TestCodexEngineClientToolTurnsForwardAndBillIndependently(t *testing.T) {
	// exec belongs to the client. The gateway forwards the function call and
	// waits for a new external request containing the client's tool output.
	const toolsJSON = `[{"type":"function","name":"exec","parameters":{"type":"object","properties":{"command":{"type":"string"}},"required":["command"]}},{"type":"namespace","name":"client_workspace","tools":[{"type":"function","name":"read_file","parameters":{"type":"object","properties":{"path":{"type":"string"}}}}]}]`
	const callJSON = `{"type":"function_call","id":"fc_client_exec","call_id":"call_client_exec","name":"exec","arguments":"{\"command\":\"echo client tool output\"}","status":"completed"}`
	const outputJSON = `[{"type":"function_call_output","call_id":"call_client_exec","output":"client tool output"}]`
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%t", stream), func(t *testing.T) {
			upstream := &httpUpstreamRecorder{}
			logs, billing := &openAIRecordUsageLogRepoStub{inserted: true}, &engineReceiptBillingRepo{}
			svc := newOpenAIRecordUsageServiceWithBillingRepoForTest(logs, billing, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
			svc.httpUpstream = upstream
			store := &engineFrozenStoreStub{}
			account := engineAccount()
			account.ID = 3001
			turns := []struct {
				request, response, receipt string
				usage                      OpenAIUsage
			}{
				{
					request:  fmt.Sprintf(`{"model":"alias","stream":%t,"tools":%s,"input":[{"role":"user","content":"Run exec on the client."}]}`, stream, toolsJSON),
					response: `{"id":"resp_tool_1","object":"response","status":"completed","output":[` + callJSON + `],"usage":` + engineContextUsageFixture + `}`,
					receipt:  engineReceiptTestID("a"),
					usage:    OpenAIUsage{InputTokens: 1200, CacheReadInputTokens: 960, OutputTokens: 83},
				},
				{
					request:  fmt.Sprintf(`{"model":"alias","stream":%t,"tools":%s,"previous_response_id":"resp_tool_1","input":%s}`, stream, toolsJSON, outputJSON),
					response: `{"id":"resp_tool_2","object":"response","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Client tool output received."}]}],"usage":{"input_tokens":230,"input_tokens_details":{"cached_tokens":80},"output_tokens":17,"total_tokens":247}}`,
					receipt:  engineReceiptTestID("b"),
					usage:    OpenAIUsage{InputTokens: 230, CacheReadInputTokens: 80, OutputTokens: 17},
				},
			}
			var results []*OpenAIForwardResult
			// Both new rounds are billed, then replay each receipt after the
			// conversation has advanced to catch cross-round state leakage.
			for attempt, turnIndex := range []int{0, 1, 0, 1} {
				turn := turns[turnIndex]
				wire, contentType := turn.response, "application/json"
				if stream {
					wire = "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":" + turn.response + "}\n\n"
					contentType = "text/event-stream"
				}
				upstream.resp = &http.Response{StatusCode: http.StatusOK, Header: http.Header{
					"Content-Type": {contentType}, codexEngineUsageIDHeader: {turn.receipt},
					"X-Request-Id": {fmt.Sprintf("transport-attempt-%d", attempt)},
				}, Body: io.NopCloser(strings.NewReader(wire))}
				recorder := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(recorder)
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(turn.request))
				result, err := svc.forwardCodexEngine(context.Background(), c, account, []byte(turn.request), "/v1/responses", "")
				require.NoError(t, err)
				require.NotNil(t, result)
				require.Equal(t, wire, recorder.Body.String(), "client function calls and responses must be forwarded verbatim")
				require.Len(t, upstream.requests, attempt+1, "each external request must make one Engine request, including a client tool call response")
				require.Equal(t, "/prefix/v1/responses", upstream.lastReq.URL.Path)
				require.Equal(t, strings.Replace(turn.request, `"model":"alias"`, `"model":"native"`, 1), string(upstream.lastBody), "only the configured model mapping changes the request")
				require.Equal(t, toolsJSON, gjson.GetBytes(upstream.lastBody, "tools").Raw)
				if turnIndex == 0 {
					require.False(t, gjson.GetBytes(upstream.lastBody, "previous_response_id").Exists())
				} else {
					require.Equal(t, "resp_tool_1", gjson.GetBytes(upstream.lastBody, "previous_response_id").String())
					require.Equal(t, outputJSON, gjson.GetBytes(upstream.lastBody, "input").Raw)
				}
				require.True(t, result.CodexEngineUsageFinalized)
				require.Equal(t, turn.usage, result.Usage, "usage must describe this round without historical totals")
				input := engineReceiptTestInput(turn.receipt)
				input.Account, input.Result = account, result
				input.RequestPayloadHash = fmt.Sprintf("external-request-%d", attempt)
				require.NoError(t, svc.RecordUsage(context.Background(), input))
				require.Zero(t, billing.applied)
				require.Zero(t, logs.calls)
				require.Empty(t, billing.commands)
				job, _ := engineFrozenTestSnapshot(t)
				job.ID += int64(turnIndex)
				event := engineFrozenTestModelEvent()
				event.EventID, event.Usage = turn.receipt, turn.usage
				event.Usage.InputTokens += 500
				require.NoError(t, svc.SettleCodexEngineBillingEvent(context.Background(), store, job, event))
				require.Equal(t, min(attempt+1, 2), store.applied)
				command := store.received.Command
				require.Equal(t, turn.usage.InputTokens+500-turn.usage.CacheReadInputTokens, command.InputTokens)
				require.Equal(t, turn.usage.CacheReadInputTokens, command.CacheReadTokens)
				require.Equal(t, turn.usage.OutputTokens, command.OutputTokens)
				results = append(results, result)
			}
			require.Len(t, store.seen, 2)
			for index, result := range results {
				require.Equal(t, turns[index%2].usage, result.Usage, "later rounds and billing must not mutate earlier usage")
			}
		})
	}
}
