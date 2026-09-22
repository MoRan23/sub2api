package service

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func requestOSTestBody(t *testing.T, key string, messages ...map[string]any) []byte {
	t.Helper()
	body, err := json.Marshal(map[string]any{key: messages})
	require.NoError(t, err)
	return body
}

func requestOSTestMessage(role, text string) map[string]any {
	return map[string]any{"role": role, "content": []map[string]any{{"type": "input_text", "text": text}}}
}

func TestCaptureOpenAIRequestOSPreservesOriginalUAAndBody(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
	ua := "codex-tui/0.152.0 (Ubuntu 24.04.4; x86_64) WindowsTerminal"
	c.Request.Header.Set("User-Agent", ua)
	body := requestOSTestBody(t, "input", requestOSTestMessage("user", "<environment_context><os>Windows</os></environment_context>"))
	original := string(body)
	gotUA, family, source := captureOpenAIRequestOS(c, body)
	require.Equal(t, ua, gotUA)
	require.Equal(t, "linux", family)
	require.Equal(t, "user_agent", source)
	require.Equal(t, original, string(body))

	c.Request.Header.Set("User-Agent", "unknown-client/1.0")
	gotUA, family, source = captureOpenAIRequestOS(c, body)
	require.Equal(t, "unknown-client/1.0", gotUA)
	require.Equal(t, "linux", family)
	require.Equal(t, "user_agent", source)
	gotUA, family, source = captureOpenAIRequestOS(nil, nil)
	require.Empty(t, gotUA)
	require.Empty(t, family)
	require.Empty(t, source)
}

func TestOpenAIRequestOSFromEnvironment(t *testing.T) {
	for _, test := range []struct{ name, content, want string }{
		{"os without timezone or date", "<os>Windows 11</os>", "windows"},
		{"platform", "<platform>Darwin</platform>", "macos"},
		{"operating system", "<operating_system>Ubuntu Linux</operating_system>", "linux"},
		{"explicit beats contradictory paths", "<os>Linux</os><cwd>C:\\repo</cwd><workspace_root>/Users/alice/repo</workspace_root>", "linux"},
		{"explicit conflict", "<os>Linux</os><platform>Windows</platform>", ""},
		{"same explicit family", "<os>Mac OS X</os><platform>Darwin</platform>", "macos"},
		{"unknown explicit does not guess path", "<os>FreeBSD</os><cwd>C:\\repo</cwd>", ""},
		{"android explicit is unknown", "<os>Android Linux</os><cwd>/home/test/project</cwd>", ""},
		{"ios explicit is unknown", "<os>iOS like Mac OS X</os><cwd>/Users/test/project</cwd>", ""},
		{"windows phone explicit is unknown", "<os>Windows Phone 10</os><cwd>C:\\repo</cwd>", ""},
		{"empty explicit does not guess path", "<os></os><cwd>/home/alice/repo</cwd>", ""},
		{"conflict in single explicit field", "<os>Windows Linux</os>", ""},
		{"nested explicit field", "<os>Linux<example>Windows</example></os>", ""},
		{"windows cwd", "<cwd>D:\\Code\\project</cwd><shell>bash</shell>", "windows"},
		{"windows forward slash", "<cwd>c:/Code/project</cwd>", "windows"},
		{"unc workspace", `<filesystem><workspace_roots><root>\\server\share\repo</root></workspace_roots></filesystem>`, "windows"},
		{"mac cwd", "<cwd>/Users/alice/project</cwd>", "macos"},
		{"linux workspace", "<workspace_roots><root>/home/alice/project</root></workspace_roots>", "linux"},
		{"path conflicts", "<cwd>D:\\Code\\project</cwd><filesystem><workspace_roots><root>/home/alice/project</root></workspace_roots></filesystem>", ""},
		{"ambiguous path does not conflict", "<cwd>/workspace/project</cwd><workspace_root>/Users/alice/project</workspace_root>", "macos"},
		{"shell is not OS", "<shell>powershell</shell><shell_version>7.6</shell_version>", ""},
		{"ambiguous workspace", "<cwd>/workspace/project</cwd><shell>bash</shell>", ""},
		{"permissions do not supply OS", `<filesystem><permission_profile><file_system><root>C:\repo</root></file_system></permission_profile></filesystem>`, ""},
		{"arbitrary prose ignored", "The user's other computer runs Windows", ""},
		{"arbitrary nested OS ignored", "<example><os>Windows</os></example>", ""},
		{"xml comment rejected", "<!-- example --><os>Windows</os>", ""},
		{"CDATA rejected", "<os><![CDATA[Windows]]></os>", ""},
		{"codeblock rejected", "<os>Windows</os>\n```text\nexample\n```", ""},
		{"blockquote rejected", "<os>Windows</os>\n> example", ""},
		{"malformed XML", "<os>Windows</platform>", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			text := "<environment_context>" + test.content + "</environment_context>"
			require.Equal(t, test.want, openAIRequestOSFromEnvironment(text))
		})
	}
}

func TestCaptureOpenAIRequestOSMobileUserAgentUsesEnvironmentFallback(t *testing.T) {
	for _, ua := range []string{
		"Mozilla/5.0 (Linux; Android 15) AppleWebKit/537.36",
		"Mozilla/5.0 (iPhone; CPU iPhone OS 18_5 like Mac OS X) AppleWebKit/605.1.15",
		"Mozilla/5.0 (Windows Phone 10.0) IEMobile/11.0",
	} {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
		c.Request.Header.Set("User-Agent", ua)
		gotUA, family, source := captureOpenAIRequestOS(c, nil)
		require.Equal(t, ua, gotUA)
		require.Empty(t, family)
		require.Empty(t, source)
		body := requestOSTestBody(t, "input", requestOSTestMessage("user", "<environment_context><os>Windows</os></environment_context>"))
		_, family, source = captureOpenAIRequestOS(c, body)
		require.Empty(t, family, "a captured unknown OS cannot be replaced by a later body")
		require.Empty(t, source)
		fresh := osIdentityTestContext(t, ua)
		_, family, source = captureOpenAIRequestOS(fresh, body)
		require.Equal(t, "windows", family)
		require.Equal(t, "environment_context", source)
	}
}

func TestOpenAIRequestOSContextPreservesUnknownAcrossIdentityRecapture(t *testing.T) {
	c := osIdentityTestContext(t, "unknown-client/1.0")
	first := CaptureOpenAIRequestOS(c, []byte(`{"input":"hello"}`))
	require.True(t, first.Captured)
	require.Empty(t, first.Family)
	c.Request.Header.Set("User-Agent", "codex_cli_rs/1.0 (Windows 11; x86_64)")
	SetOpenAIOAuthIdentityCapture(c, CaptureOpenAIOAuthIdentity(c, []byte(`{"input":"later"}`), ""))
	require.Equal(t, first, OpenAIRequestOSFromContext(c.Request.Context()))
	ctx := ContextWithOpenAIRequestOS(c.Request.Context(), OpenAIRequestOS{Family: OpenAIOSLinux, Source: "later"})
	require.Equal(t, first, OpenAIRequestOSFromContext(ctx))
	require.False(t, OpenAIRequestOSFromContext(context.Background()).Captured)
}

func TestOpenAIRequestOSContextBridgePreservesUnknownBeforeWSCapture(t *testing.T) {
	ctx := ContextWithOpenAIRequestOS(context.Background(), OpenAIRequestOS{Source: "original_unknown"})
	c := osIdentityTestContext(t, "codex_cli_rs/1.0 (Linux 6.8; x86_64)")
	body := []byte(`{"type":"response.create","input":"later frame"}`)
	ctx = captureOpenAIRequestOSContext(ctx, c, body)
	SetOpenAIOAuthIdentityCapture(c, CaptureOpenAIOAuthIdentity(c, body, ""))
	require.Equal(t, OpenAIRequestOSFromContext(ctx), OpenAIRequestOSFromContext(c.Request.Context()))
	require.Empty(t, OpenAIRequestOSFromContext(ctx).Family)
	capture, ok := OpenAIOAuthIdentityCaptureFromContext(c)
	require.True(t, ok)
	require.Empty(t, capture.OSFamily)
	require.Equal(t, "original_unknown", capture.OSSource)
}

func TestOpenAIRequestOSUsesOnlyLatestCurrentStandaloneEnvironment(t *testing.T) {
	const windows = "<environment_context><os>Windows</os></environment_context>"
	const linux = "<environment_context><os>Linux</os></environment_context>"
	const unknown = "<environment_context><cwd>/workspace/project</cwd></environment_context>"
	for _, test := range []struct {
		name     string
		messages []map[string]any
		want     string
	}{
		{"latest user streak", []map[string]any{requestOSTestMessage("user", windows), requestOSTestMessage("assistant", "done"), requestOSTestMessage("user", linux)}, "linux"},
		{"historical environment", []map[string]any{requestOSTestMessage("user", windows), requestOSTestMessage("assistant", "done"), requestOSTestMessage("user", "next question")}, ""},
		{"tool retires environment", []map[string]any{requestOSTestMessage("user", windows), requestOSTestMessage("tool", linux)}, ""},
		{"system ignored", []map[string]any{requestOSTestMessage("system", windows), requestOSTestMessage("user", "next")}, ""},
		{"developer ignored", []map[string]any{requestOSTestMessage("developer", windows), requestOSTestMessage("user", "next")}, ""},
		{"latest environment wins", []map[string]any{requestOSTestMessage("user", windows), requestOSTestMessage("user", linux), requestOSTestMessage("user", "next")}, "linux"},
		{"latest unknown does not revive old", []map[string]any{requestOSTestMessage("user", windows), requestOSTestMessage("user", unknown)}, ""},
		{"latest malformed does not revive old", []map[string]any{requestOSTestMessage("user", windows), requestOSTestMessage("user", "<environment_context><os>Linux</os>")}, ""},
		{"quoted environment ignored", []map[string]any{requestOSTestMessage("user", "> "+windows)}, ""},
		{"fenced environment ignored", []map[string]any{requestOSTestMessage("user", "```xml\n"+windows+"\n```")}, ""},
		{"embedded environment ignored", []map[string]any{requestOSTestMessage("user", "Please explain "+windows)}, ""},
		{"string content", []map[string]any{{"role": "user", "content": windows}}, "windows"},
		{"chat text content", []map[string]any{{"role": "user", "content": []map[string]any{{"type": "text", "text": linux}}}}, "linux"},
		{"tool result content ignored", []map[string]any{{"role": "user", "content": []map[string]any{{"type": "tool_result", "text": windows}}}}, ""},
		{"metadata environment", []map[string]any{{"role": "user", "content": []map[string]any{{"type": "input_text", "text": windows}}, "internal_chat_message_metadata_passthrough": map[string]any{"content_item_kinds": []string{"environments.environment_context"}}}}, "windows"},
		{"metadata reference ignored", []map[string]any{{"role": "user", "content": []map[string]any{{"type": "input_text", "text": windows}}, "internal_chat_message_metadata_passthrough": map[string]any{"content_item_kinds": []string{"text"}}}}, ""},
	} {
		for _, key := range []string{"input", "messages"} {
			t.Run(test.name+"/"+key, func(t *testing.T) {
				require.Equal(t, test.want, openAIRequestOSFromBody(requestOSTestBody(t, key, test.messages...)))
			})
		}
	}
}

func TestOpenAIRequestOSRejectsOversizedOrAmbiguousInput(t *testing.T) {
	const windows = "<environment_context><os>Windows</os></environment_context>"
	require.Empty(t, openAIRequestOSFromBody([]byte(strings.Repeat(" ", openAIRequestOSBodyLimit+1))))
	require.Empty(t, openAIRequestOSFromBody([]byte(`{"input":`)))
	require.Empty(t, openAIRequestOSFromBody([]byte(`{"input":[],"messages":[]}`)))
	require.Empty(t, openAIRequestOSFromBody(requestOSTestBody(t, "input", requestOSTestMessage("user", windows+strings.Repeat(" ", openAIRequestOSTextLimit)))))
	messages := make([]map[string]any, openAIRequestOSMessageLimit+1)
	for i := range messages {
		messages[i] = requestOSTestMessage("user", "next")
	}
	messages[0] = requestOSTestMessage("user", windows)
	require.Empty(t, openAIRequestOSFromBody(requestOSTestBody(t, "input", messages...)))
	for i := range messages[:openAIRequestOSEnvironmentLimit+1] {
		messages[i] = requestOSTestMessage("user", windows)
	}
	require.Empty(t, openAIRequestOSFromBody(requestOSTestBody(t, "input", messages[:openAIRequestOSEnvironmentLimit+1]...)))
	require.Empty(t, openAIRequestOSFromEnvironment("<environment_context><os>Windows</os>"+strings.Repeat("<x></x>", openAIRequestOSXMLNodeLimit)+"</environment_context>"))
	require.Empty(t, openAIRequestOSFromEnvironment("<environment_context>"+strings.Repeat("<x>", openAIRequestOSXMLDepthLimit)+"<os>Windows</os>"+strings.Repeat("</x>", openAIRequestOSXMLDepthLimit)+"</environment_context>"))
}
