package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestCyberReasoningEffortsAndMappingBoundaries(t *testing.T) {
	for _, model := range []string{"gpt-5.6-cyber", "gpt-6-cyber", "openai/GPT-6-CYBER", "gpt-6-cyber-max", "gpt-5.6-cyber-2026-11-11"} {
		for _, effort := range []string{"low", "medium", "high", "xhigh", "max"} {
			t.Run(model+"/"+effort, func(t *testing.T) {
				body := []byte(fmt.Sprintf(`{"model":"6 Cyber","reasoning":{"effort":%q}}`, effort))
				require.Equal(t, effort, normalizeOpenAIReasoningEffortForModel(effort, model))
				require.Equal(t, effort, *CanonicalRequestedReasoningEffort(body))
				require.Equal(t, effort, *extractOpenAIReasoningEffortFromBody(body, model, "6 Cyber"))
				require.Equal(t, effort, *ExtractResponsesReasoningEffortFromBody(body, model))
			})
		}
	}
	body := []byte(`{"model":"gpt-6-cyber","reasoning":{"effort":"max"}}`)
	// Actual upstream identity wins over the public name, in either direction.
	require.Equal(t, "max", *extractOpenAIReasoningEffortFromBody(body, "gpt-6-cyber", "public"))
	require.Equal(t, "xhigh", *extractOpenAIReasoningEffortFromBody(body, "gpt-5.4", "gpt-6-cyber"))
	for _, model := range []string{"gpt-6-cyber-preview", "gpt-5.6-cyber-custom", "gpt-6-cyberpunk"} {
		require.Equal(t, "xhigh", normalizeOpenAIReasoningEffortForModel("max", model))
	}
	// Reasoning capability must not extend the GPT-5.6 compact downgrade rule.
	for _, model := range []string{"gpt-5.6-cyber", "gpt-6-cyber"} {
		unchanged, changed, err := normalizeOpenAICodexCompactReasoningEffort(body, model)
		require.NoError(t, err)
		require.False(t, changed)
		require.Equal(t, body, unchanged)
	}

	var messages apicompat.AnthropicRequest
	require.NoError(t, json.Unmarshal([]byte(`{"output_config":{"effort":"max"}}`), &messages))
	require.Equal(t, "max", openAICompatAnthropicReasoningEffort(&messages, "gpt-6-cyber", "xhigh"))
	require.Equal(t, "xhigh", openAICompatAnthropicReasoningEffort(&messages, "gpt-5.4", "xhigh"))
}

func TestCyberClientUltraAliasDoesNotAddServerReasoningEffort(t *testing.T) {
	// Native Codex resolves the Ultra UI choice to a supported model effort and
	// proactive multi-agent mode before sending the request. Preserve that
	// distinction: the gateway records the wire effort, not the client alias.
	for _, model := range []string{"gpt-5.6-cyber", "gpt-6-cyber"} {
		body := []byte(fmt.Sprintf(`{"model":%q,"reasoning":{"effort":"ultra"}}`, model))
		require.Empty(t, normalizeOpenAIReasoningEffortForModel("ultra", model))
		require.Nil(t, CanonicalRequestedReasoningEffort(body))
		out, changed, err := ApplyReasoningEffortPolicy(body, "max", nil, ReasoningEffortOverLimitDowngrade)
		require.NoError(t, err)
		require.False(t, changed)
		require.Equal(t, body, out, "an unknown raw effort retains the existing passthrough behavior")
	}
	for _, platform := range []string{PlatformOpenAI, PlatformComposite} {
		_, err := normalizeMaxReasoningEffortForPlatform(platform, "ultra")
		require.Error(t, err)
		_, err = NormalizeReasoningEffortMappings(platform, []ReasoningEffortMapping{{From: "ultra", To: "max"}})
		require.Error(t, err)
	}
}

func TestCyberResponsesEffortMatchesPayloadAndUsage(t *testing.T) {
	for _, model := range []string{"gpt-5.6-cyber", "gpt-6-cyber"} {
		for _, requestedModel := range []string{model, "public-cyber"} {
			for _, oauth := range []bool{false, true} {
				for _, effort := range []string{"max", "xhigh"} {
					for _, stream := range []bool{false, true} {
						t.Run(fmt.Sprintf("%s/%s/oauth=%t/%s/stream=%t", model, requestedModel, oauth, effort, stream), func(t *testing.T) {
							response := fmt.Sprintf(`{"id":"resp_cyber","model":%q,"status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":1,"output_tokens":2}}`, model)
							contentType := "application/json"
							if stream || oauth {
								response = "data: {\"type\":\"response.completed\",\"response\":" + response + "}\n\ndata: [DONE]\n\n"
								contentType = "text/event-stream"
							}
							upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: http.StatusOK,
								Header: http.Header{"Content-Type": []string{contentType}}, Body: io.NopCloser(strings.NewReader(response))}}
							svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
							account := rawGPT56ResponsesAPIKeyAccount(requestedModel, model)
							if oauth {
								account = rawGPT56ResponsesOAuthAccount(requestedModel, model)
								svc.accountRepo = newAuthorizedOpenAIOAuthTestRepo(account)
							}
							body := []byte(fmt.Sprintf(`{"model":%q,"stream":%t,"input":"hello","instructions":"test","reasoning":{"effort":%q}}`, requestedModel, stream, effort))
							c, _ := gin.CreateTestContext(httptest.NewRecorder())
							c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(string(body)))
							SetOpenAIClientTransport(c, OpenAIClientTransportHTTP)
							result, err := svc.Forward(context.Background(), c, account, body)
							require.NoError(t, err)
							require.NotNil(t, result)
							require.Equal(t, model, gjson.GetBytes(upstream.lastBody, "model").String())
							require.Equal(t, effort, gjson.GetBytes(upstream.lastBody, "reasoning.effort").String())
							require.Equal(t, effort, optionalStringValue(result.ReasoningEffort))
							usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
							billingRepo := &openAIRecordUsageBillingRepoStub{result: &UsageBillingApplyResult{Applied: true}}
							usageService := newOpenAIRecordUsageServiceWithBillingRepoForTest(usageRepo, billingRepo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
							require.NoError(t, usageService.RecordUsage(context.Background(), &OpenAIRecordUsageInput{
								Result: result, Account: account, User: &User{ID: 21}, APIKey: &APIKey{ID: 22, Group: &Group{RateMultiplier: 1}},
							}))
							require.NotNil(t, usageRepo.lastLog)
							require.Equal(t, effort, optionalStringValue(usageRepo.lastLog.ReasoningEffort))
							require.Equal(t, effort, optionalStringValue(usageRepo.lastLog.RequestedReasoningEffort))
						})
					}
				}
			}
		}
	}
}

func TestCyberWSReasoningEffortIsPerTurn(t *testing.T) {
	first := []byte(`{"model":"public-cyber","reasoning":{"effort":"max"}}`)
	meta := newOpenAIWSPassthroughUsageMeta("public-cyber", first)
	meta.captureRequestedReasoningEffort(first)
	meta.initFromFirstFrame(first, "gpt-6-cyber")
	require.Equal(t, "max", optionalStringValue(meta.reasoningEffort.Load()))
	require.Equal(t, "max", optionalStringValue(meta.requestedReasoningEffort.Load()))
	next := []byte(`{"model":"public-cyber","reasoning":{"effort":"high"}}`)
	meta.captureRequestedReasoningEffort(next)
	meta.updateFromResponseCreate(next, "gpt-5.6-cyber", "public-cyber")
	require.Equal(t, "high", optionalStringValue(meta.reasoningEffort.Load()))
	require.Equal(t, "high", optionalStringValue(meta.requestedReasoningEffort.Load()))
	meta.updateFromResponseCreate(first, "gpt-5.4", "public-cyber")
	require.Equal(t, "xhigh", optionalStringValue(meta.reasoningEffort.Load()))
	meta.updateFromResponseCreate([]byte(`{"model":"public-cyber"}`), "gpt-6-cyber", "public-cyber")
	require.Nil(t, meta.reasoningEffort.Load())
}
