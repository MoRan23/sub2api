package service

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestGrokSafetyTextRoutesPreserveRejectionAndFinalTokens(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, route := range []string{"responses", "chat-raw", "chat-bridge", "messages"} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/%t", route, stream), func(t *testing.T) {
				ctx := WithGrokSafetyFee(context.Background(), 0.2)
				body := []byte(fmt.Sprintf(`{"model":"grok-4.7","stream":%t,"messages":[{"role":"user","content":"hi"}],"max_tokens":256}`, stream))
				if route == "responses" {
					body = []byte(fmt.Sprintf(`{"model":"grok-4.7","stream":%t,"input":"hi"}`, stream))
				}
				recorder := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(recorder)
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/"+route, bytes.NewReader(body)).WithContext(ctx)
				c.Set("api_key", &APIKey{ID: 1})
				account := &Account{ID: 1, Platform: PlatformGrok, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "test", "base_url": "https://api.x.ai/v1"}}
				payload := `{"error":{"code":"safety_review_rejected","message":"Blocked"},"usage":{"input_tokens":7,"output_tokens":2}}`
				contentType := "application/json"
				if stream || route == "chat-bridge" || route == "messages" {
					contentType = "text/event-stream"
					// Deliberately split one JSON value over data lines and omit type; the
					// authoritative exact code must not rely on a Responses event name.
					payload = "event: error\ndata: {\"error\":{\"code\":\"safety_review_rejected\",\"message\":\"Blocked\"},\n" +
						"data: \"usage\":{\"input_tokens\":7,\"output_tokens\":2}}\n\n"
				}
				upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{contentType}}, Body: io.NopCloser(strings.NewReader(payload))}}
				svc := &OpenAIGatewayService{httpUpstream: upstream}
				var result *OpenAIForwardResult
				var err error
				switch route {
				case "responses":
					result, err = svc.forwardGrokResponses(ctx, c, account, body, "grok-4.7", stream, time.Now())
				case "chat-raw":
					result, err = svc.forwardAsRawChatCompletions(ctx, c, account, body, "")
				case "chat-bridge":
					result, err = svc.forwardGrokChatCompletionsViaResponses(ctx, c, account, body, "test-cache", "")
				case "messages":
					result, err = svc.forwardAsAnthropic(ctx, c, account, body, "", "")
				}
				require.ErrorIs(t, err, ErrGrokSafetyRejected)
				require.NotNil(t, result)
				require.NotNil(t, result.GrokSafetyRejection)
				require.Equal(t, 7, result.Usage.InputTokens)
				require.Equal(t, 2, result.Usage.OutputTokens)
				require.Contains(t, recorder.Body.String(), GrokSafetyRejectionCode)
				require.NotContains(t, recorder.Body.String(), "response.completed")
				if !stream {
					require.GreaterOrEqual(t, recorder.Code, 400)
				}
			})
		}
	}
}

func TestGrokSafetyWSBridgeMultilineAndPerTurnIsolation(t *testing.T) {
	fee := 0.3
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
	c.Set("api_key", &APIKey{ID: 1, Group: &Group{Platform: PlatformGrok, GrokSafetyFeeEnabled: true, GrokSafetyFee: &fee}})
	account := &Account{ID: 1, Platform: PlatformGrok, Type: AccountTypeAPIKey, Credentials: map[string]any{"base_url": "https://api.x.ai/v1"}}
	upstream := &httpUpstreamRecorder{}
	svc := &OpenAIGatewayService{httpUpstream: upstream}
	request := []byte(`{"type":"response.create","model":"grok-4.7","input":"hi"}`)
	previousCall := ""
	for turn := 1; turn <= 2; turn++ {
		upstream.resp = &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(
			"event: response.failed\ndata: {\"response\":{\"error\":{\"code\":\"safety_review_rejected\",\"message\":\"blocked\"},\n" +
				"data: \"usage\":{\"input_tokens\":11,\"output_tokens\":3}}}\n\n"))}
		var messages [][]byte
		result, err := svc.proxyOpenAIWSHTTPBridgeTurn(context.Background(), c, account, "fixture-token", request, len(request), "grok-4.7", "", "", "", "", turn,
			func(message []byte) error { messages = append(messages, bytes.Clone(message)); return nil })
		require.ErrorIs(t, err, ErrGrokSafetyRejected)
		require.NotNil(t, result.GrokSafetyRejection)
		require.Equal(t, fee, result.GrokSafetyRejection.Fee)
		require.NotEqual(t, previousCall, result.GrokSafetyRejection.CallID)
		previousCall = result.GrokSafetyRejection.CallID
		require.Equal(t, 11, result.Usage.InputTokens)
		require.Equal(t, 3, result.Usage.OutputTokens)
		require.Len(t, messages, 1)
		require.Contains(t, string(messages[0]), GrokSafetyRejectionCode)
	}
}

func TestGrokSafetyMessagesNestedErrorEnvelope(t *testing.T) {
	payload := grokSafetyMessagesErrorPayload([]byte(`{"type":"response.failed","response":{"error":{"code":"safety_review_rejected","message":"blocked","param":"input","extra":true}}}`))
	require.JSONEq(t, `{"type":"error","error":{"code":"safety_review_rejected","message":"blocked","param":"input","extra":true}}`, string(payload))
}
