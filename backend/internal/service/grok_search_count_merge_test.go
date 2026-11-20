//go:build unit

package service

import (
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

func TestGrokSearchCounterTerminalUsageSealsCount(t *testing.T) {
	for _, eventType := range []string{"response.completed", "response.incomplete", "response.done"} {
		for _, count := range []int{0, 2} {
			t.Run(fmt.Sprintf("%s/%d", eventType, count), func(t *testing.T) {
				var counter grokSearchCounter
				counter.ObserveResponsesData([]byte(`{"type":"response.output_item.done","item":{"type":"web_search_call","id":"before"}}`))
				require.Equal(t, 1, counter.total)
				counter.ObserveResponsesData([]byte(fmt.Sprintf(`{"type":%q,"response":{"usage":{"server_side_tool_usage_details":{"web_search_calls":%d}}}}`, eventType, count)))
				counter.ObserveResponsesData([]byte(`{"type":"response.output_item.done","item":{"type":"web_search_call","id":"after"}}`))
				counter.ObserveResponsesData([]byte(`{"type":"response.done","response":{"usage":{"server_side_tool_usage_details":{"web_search_calls":99}}}}`))
				require.Equal(t, count, counter.total)
				require.Nil(t, counter.seen)
			})
		}
	}
}

func TestGrokSearchCounterChatOnlyAcceptsFinalUsage(t *testing.T) {
	var counter grokSearchCounter
	counter.ObserveChatData(`{"choices":[{"delta":{"content":"partial"},"finish_reason":null}],"usage":{"server_side_tool_usage_details":{"web_search_calls":20}}}`)
	require.Zero(t, counter.total)
	require.False(t, counter.reported)
	counter.ObserveChatData(`{"choices":[],"usage":{"server_side_tool_usage_details":{"web_search_calls":2}}`)
	require.False(t, counter.reported, "truncated JSON is not authoritative usage")
	counter.ObserveChatData(`{"choices":[],"usage":{"server_side_tool_usage_details":{"web_search_calls":0}}}`)
	counter.ObserveChatData(`{"choices":[],"usage":{"server_side_tool_usage_details":{"web_search_calls":20}}}`)
	require.True(t, counter.reported)
	require.Zero(t, counter.total)
}

func TestRawChatGrokSearchUsage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, streaming := range []bool{false, true} {
		for _, details := range []string{"", `,"server_side_tool_usage_details":{"web_search_calls":0}`, `,"server_side_tool_usage_details":{"web_search_calls":2,"x_search_calls":1}`} {
			t.Run(fmt.Sprintf("stream=%t/details=%s", streaming, details), func(t *testing.T) {
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
				usage := `{"prompt_tokens":5,"completion_tokens":2` + details + `}`
				body := `{"model":"grok-4.7","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":` + usage + `}`
				if streaming {
					body = "data: {\"model\":\"grok-4.7\",\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n" +
						"data: {\"choices\":[],\n" + "data: \"usage\":" + usage + "}\n\n" + "data: [DONE]\n\n"
				}
				resp := &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
				svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig()}
				account := &Account{Platform: PlatformGrok, Type: AccountTypeAPIKey}
				var result *OpenAIForwardResult
				var err error
				if streaming {
					result, err = svc.streamRawChatCompletions(c, resp, account, "grok-4.7", "grok-4.7", "grok-4.7", nil, nil, time.Now(), 0)
				}
				if !streaming {
					result, err = svc.bufferRawChatCompletions(c, resp, account, "grok-4.7", "grok-4.7", "grok-4.7", nil, nil, time.Now())
				}
				require.NoError(t, err)
				require.NotNil(t, result)
				want := 0
				if strings.Contains(details, `"web_search_calls":2`) {
					want = 3
				}
				require.Equal(t, want, result.SearchCount)
			})
		}
	}
}
