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
)

// Regression fixture from the compaction investigation: Engine performed three
// samples (360688 input / 356864 cached / 209 output in its internal ledger),
// but exposes only the last sample as standard Responses usage. Sub2 neither
// reconstructs that internal ledger nor adds cached input to total input.
const engineContextUsageFixture = `{"input_tokens":120331,"input_tokens_details":{"cached_tokens":120192},"output_tokens":83,"total_tokens":120414}`

func forwardEngineContextUsage(t *testing.T, endpoint string, stream, receipt bool) *OpenAIForwardResult {
	t.Helper()
	response := `{"id":"resp_context","object":"response","status":"completed","usage":` + engineContextUsageFixture + `,"exact":9007199254740993123,"output":[]}`
	wire := response
	contentType := "application/json"
	if stream {
		// Interim observations are not added to the authoritative final usage.
		wire = "event: response.in_progress\ndata: {\"type\":\"response.in_progress\",\"response\":{\"usage\":{\"input_tokens\":360688,\"input_tokens_details\":{\"cached_tokens\":356864},\"output_tokens\":209}}}\n\n" +
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
	require.Equal(t, OpenAIUsage{InputTokens: 120331, CacheReadInputTokens: 120192, OutputTokens: 83}, result.Usage)
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
			require.Equal(t, 1, billing.applied)
			require.Equal(t, 1, logs.calls)
			require.Equal(t, 139, billing.commands[0].InputTokens)
			require.Equal(t, 120192, billing.commands[0].CacheReadTokens)
			require.Equal(t, 83, billing.commands[0].OutputTokens)
		})
	}
}

func TestCodexEngineStandardContextUsageReplayDeduplicates(t *testing.T) {
	logs, billing := &openAIRecordUsageLogRepoStub{inserted: true}, &engineReceiptBillingRepo{}
	svc := newOpenAIRecordUsageServiceWithBillingRepoForTest(logs, billing, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
	for _, stream := range []bool{true, false, false} {
		input := engineReceiptTestInput(engineReceiptTestID("a"))
		input.Result = forwardEngineContextUsage(t, "/v1/responses", stream, true)
		require.True(t, input.Result.CodexEngineUsageFinalized)
		require.NoError(t, svc.RecordUsage(context.Background(), input))
	}
	require.Equal(t, 1, billing.applied)
	require.Equal(t, 1, logs.calls)
	require.Len(t, billing.commands, 3)
	for _, command := range billing.commands {
		require.Equal(t, billing.commands[0].RequestFingerprint, command.RequestFingerprint)
		require.Equal(t, 139, command.InputTokens)
		require.Equal(t, 120192, command.CacheReadTokens)
		require.Equal(t, 83, command.OutputTokens)
	}
}
