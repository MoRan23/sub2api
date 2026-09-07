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

func TestDaybreakMaxReasoningEffort(t *testing.T) {
	for _, model := range []string{
		"gpt-daybreak-blue-latest",
		"gpt-daybreak-red-latest",
		"openai/GPT-DAYBREAK-BLUE-LATEST",
		"openai/GPT-DAYBREAK-RED-LATEST",
	} {
		t.Run(model, func(t *testing.T) {
			require.Equal(t, "max", normalizeOpenAIReasoningEffortForModel(" MAX ", model))
			body := []byte(`{"reasoning":{"effort":"max"}}`)
			effort := extractOpenAIReasoningEffortFromBody(body, model)
			require.NotNil(t, effort)
			require.Equal(t, "max", *effort)

			meta := newOpenAIWSPassthroughUsageMeta(model, body)
			meta.initFromFirstFrame(body, model)
			require.NotNil(t, meta.reasoningEffort.Load())
			require.Equal(t, "max", *meta.reasoningEffort.Load())
			meta.updateFromResponseCreate(body, model, model)
			require.NotNil(t, meta.reasoningEffort.Load())
			require.Equal(t, "max", *meta.reasoningEffort.Load())
		})
	}

	for _, model := range []string{"gpt-5.5", "gpt-daybreak-green-latest", "gpt-daybreak-blue-latest-other", "gpt-daybreak-red-latest-other"} {
		t.Run(model, func(t *testing.T) {
			require.Equal(t, "xhigh", normalizeOpenAIReasoningEffortForModel("max", model))
		})
	}
}

func TestDaybreakForwardPreservesMaxInPayloadAndUsage(t *testing.T) {
	for _, model := range []string{"gpt-daybreak-blue-latest", "gpt-daybreak-red-latest"} {
		for _, passthrough := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/passthrough=%t", model, passthrough), func(t *testing.T) {
				upstream := &httpUpstreamRecorder{resp: &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
					Body:       io.NopCloser(strings.NewReader("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_daybreak\",\"model\":\"gpt-5.6-sol\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":2}}}\n\ndata: [DONE]\n\n")),
				}}
				svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
				account := &Account{
					ID: 11, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Concurrency: 1,
					Credentials: map[string]any{"access_token": "oauth-token", "chatgpt_account_id": "chatgpt-acc"},
					Extra:       map[string]any{"openai_passthrough": passthrough},
				}
				body := []byte(fmt.Sprintf(`{"model":%q,"instructions":"test","input":"hello","stream":true,"reasoning":{"effort":"max"}}`, model))
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(string(body)))
				SetOpenAIClientTransport(c, OpenAIClientTransportHTTP)

				result, err := svc.Forward(context.Background(), c, account, body)
				require.NoError(t, err)
				require.NotNil(t, result)
				require.Equal(t, model, gjson.GetBytes(upstream.lastBody, "model").String())
				require.Equal(t, "max", gjson.GetBytes(upstream.lastBody, "reasoning.effort").String())
				require.NotNil(t, result.ReasoningEffort)
				require.Equal(t, "max", *result.ReasoningEffort)
			})
		}
	}
}
