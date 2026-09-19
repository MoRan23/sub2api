package service

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Reuse the caller's body across the real dispatch paths. Strict raw Chat
// adaptation must not leak into the OAuth Responses conversion or later raw
// attempts, and must retain the locally normalized outbound user agent.
func TestOpenAIChatRolePathsPreserveAccountBoundaries(t *testing.T) {
	SetCodexCanonicalUserAgentResolver(func() string {
		return "codex-tui/0.146.0 (Ubuntu 22.4.0; x86_64) xterm-256color"
	})
	t.Cleanup(func() { SetCodexCanonicalUserAgentResolver(nil) })
	const storedUA = "codex_cli_rs/0.120.0 (Windows 11.0.26100; x86_64) WindowsTerminal"
	const outboundUA = "codex_cli_rs/0.146.0 (Windows 11.0.26100; x86_64) WindowsTerminal"

	for _, stream := range []bool{false, true} {
		t.Run("stream="+strconv.FormatBool(stream), func(t *testing.T) {
			body := []byte(`{"model":"gpt-5.4","stream":` + strconv.FormatBool(stream) + `,"messages":[{"role":"system","content":"initial policy"},{"role":"user","content":"first"},{"role":"system","content":"later policy"},{"role":"assistant","content":"answer"},{"role":"developer","content":"developer policy"},{"role":"user","content":"next"}],"tools":[{"type":"function","function":{"name":"python","parameters":{"type":"object"}}},{"type":"function","function":{"name":"other","strict":true,"parameters":{"type":"object"}}}],"tool_choice":{"type":"allowed_tools","allowed_tools":{"mode":"required","tools":[{"type":"function","function":{"name":"python"}}]}}}`)
			original := bytes.Clone(body)
			upstream := &httpUpstreamRecorder{}
			svc, _ := newOpenAIIdentityPathService(t, true, upstream)
			for _, path := range []string{"strict", "oauth", "compatible"} {
				t.Run(path, func(t *testing.T) {
					account := newOpenAIIdentityPathAPIKeyAccount(9911)
					account.Credentials["user_agent"] = storedUA
					account.Credentials["api_protocol"] = APIProtocolChatCompletions
					account.Extra["openai_responses_supported"] = false
					account.Credentials["base_url"] = "https://api.deepseek.com"
					upstream.resp = openAIChatRolePathResponse(stream)
					switch path {
					case "oauth":
						account = newOpenAIIdentityPathOAuthAccount(9912)
						upstream.resp = openAICompatSSECompletedResponse("resp_chat_roles", "gpt-5.4")
					case "compatible":
						account.Credentials["base_url"] = "https://compatible.example.test/v1"
					}
					c, recorder := newOpenAIIdentityPathContext(t, "/v1/chat/completions", body, 99)
					result, err := svc.ForwardAsChatCompletions(context.Background(), c, account, body, "chat-role-path", "gpt-5.4")
					require.NoError(t, err, recorder.Body.String())
					require.NotNil(t, result)
					require.Equal(t, original, body)
					require.Contains(t, recorder.Body.String(), "ok")
					out := upstream.lastBody
					if path == "oauth" {
						require.Contains(t, upstream.lastReq.URL.Path, "/responses")
						require.Equal(t, "initial policy", gjson.GetBytes(out, "instructions").String())
						require.Equal(t, `["user","developer","assistant","developer","user"]`, gjson.GetBytes(out, "input.#.role").Raw)
						require.Equal(t, "later policy", gjson.GetBytes(out, "input.1.content").String())
						require.Equal(t, "developer policy", gjson.GetBytes(out, "input.3.content").String())
						require.Equal(t, int64(2), gjson.GetBytes(out, "tools.#").Int())
						require.Equal(t, "other", gjson.GetBytes(out, "tools.1.name").String())
						require.True(t, gjson.GetBytes(out, "tools.1.strict").Bool())
						require.Equal(t, "allowed_tools", gjson.GetBytes(out, "tool_choice.type").String())
						require.Equal(t, "required", gjson.GetBytes(out, "tool_choice.mode").String())
						require.False(t, gjson.GetBytes(out, "tool_choice.allowed_tools").Exists())
						alias := gjson.GetBytes(out, "tools.0.name").String()
						require.NotEmpty(t, alias)
						require.NotEqual(t, "python", alias)
						require.Equal(t, alias, gjson.GetBytes(out, "tool_choice.tools.0.name").String())
						return
					}
					require.Equal(t, "/v1/chat/completions", upstream.lastReq.URL.Path)
					require.Equal(t, outboundUA, upstream.lastReq.Header.Get("User-Agent"))
					require.Nil(t, GetOpenAIChatConversionCheck(c))
					wantMessages := gjson.GetBytes(body, "messages").Raw
					if path == "strict" {
						wantMessages = strings.ReplaceAll(wantMessages, `"role":"developer"`, `"role":"system"`)
						wantMessages, err = sjson.Set(wantMessages, "3.reasoning_content", deepSeekChatReasoningPlaceholderText)
						require.NoError(t, err)
					}
					require.JSONEq(t, wantMessages, gjson.GetBytes(out, "messages").Raw)
					for _, field := range []string{"tools", "tool_choice"} {
						require.JSONEq(t, gjson.GetBytes(body, field).Raw, gjson.GetBytes(out, field).Raw)
					}
				})
			}
			require.Len(t, upstream.requests, 3)
		})
	}
}

func openAIChatRolePathResponse(stream bool) *http.Response {
	contentType := "application/json"
	body := `{"id":"chatcmpl_roles","object":"chat.completion","model":"gpt-5.4","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":2}}`
	if stream {
		contentType = "text/event-stream"
		body = "data: {\"id\":\"chatcmpl_roles\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: {\"choices\":[],\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":2}}\n\ndata: [DONE]\n\n"
	}
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {contentType}}, Body: io.NopCloser(strings.NewReader(body))}
}
