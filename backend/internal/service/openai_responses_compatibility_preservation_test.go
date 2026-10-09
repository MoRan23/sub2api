package service

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestOAuthWebSearchHistoryRawInsertionPreservesSource(t *testing.T) {
	for _, tail := range []string{"", `, { "type": "compaction_trigger", "opaque": 18446744073709551615 }`} {
		body := []byte(" \n" + `{"model":"gpt-6-astra","input":[
 {"type":"reasoning","encrypted_content":"gAAAAopaque\\u003c","large":9007199254740993},
 {"type":"web_search_call","id":"ws_keep","action":{"query":"q","unknown":1.234567890123456789e+99}}` + tail + `
 ],"tool_choice":"required","access_programs":{"cyber":null,"unknown":true},"unknown":18446744073709551615}`)
		source := bytes.Clone(body)
		before := parseRawJSONView(body)
		out, changed, err := ensureOpenAIOAuthWebSearchToolForHistoryBody(body, true)
		require.NoError(t, err)
		require.True(t, changed)
		require.True(t, gjson.ValidBytes(out))
		require.Equal(t, source, body)
		after := parseRawJSONView(out)
		for _, path := range []string{"input.0", "input.1", "access_programs", "unknown", "tool_choice"} {
			require.Equal(t, before.Get(path).Raw, after.Get(path).Raw, path)
		}
		require.Equal(t, "additional_tools", after.Get("input.2.type").String())
		if tail != "" {
			require.Equal(t, before.Get("input.2").Raw, after.Get("input.3").Raw)
		}
	}
}

func TestOAuthWebSearchHistoryNoopRetainsOriginalBuffer(t *testing.T) {
	body := []byte(`{"input":[{"type":"message","content":"` + strings.Repeat("web_search_call ", 1000) + `"}],"tools":[]}`)
	out, changed, err := ensureOpenAIOAuthWebSearchToolForHistoryBody(body, true)
	require.NoError(t, err)
	require.False(t, changed)
	require.Same(t, &body[0], &out[0])
}

func TestResponsesCompatibilityOptionsPreserveFinalModelAndEngineBody(t *testing.T) {
	oauth := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{
		"model_mapping": map[string]any{"gpt-6-astra": "gpt-5.5"},
	}}
	body := []byte(`{"model":"gpt-6-astra","reasoning":{"mode":"pro","effort":"max"},"input":[{"type":"web_search_call","id":"ws_keep"}],"access_programs":{"cyber":"daybreak_blue"}}`)
	finalModel := "gpt-6-astra"
	out, _, err := normalizeOpenAIResponsesCompatibilityBodyWithOptions(body, oauth, openAIResponsesCompatibilityOptions{FinalModel: &finalModel, Compact: true})
	require.NoError(t, err)
	require.Equal(t, "pro", gjson.GetBytes(out, "reasoning.mode").String())
	require.False(t, gjsonToolsContainWebSearch(gjson.GetBytes(out, "tools")), "standalone compact must not get web_search declarations")
	require.Equal(t, "daybreak_blue", gjson.GetBytes(out, "access_programs.cyber").String())

	engine := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Extra: map[string]any{OpenAIAPIKeyModeExtraKey: "codex_engine"}}
	out, changed, err := normalizeOpenAIResponsesCompatibilityBodyWithOptions(body, engine, openAIResponsesCompatibilityOptions{})
	require.NoError(t, err)
	require.False(t, changed)
	require.Same(t, &body[0], &out[0], "Engine skips generic compatibility rewrites; its physical transport applies the Daybreak policy")
}
