package apicompat

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAnthropicToChatCompletionsRequestWithPathMapping(t *testing.T) {
	tests := []struct {
		name      string
		system    string
		messages  string
		want      map[string]string
		unmatched []string
	}{
		{
			name:     "string system shifts scalar user content",
			system:   `"instructions"`,
			messages: `[{"role":"user","content":"current environment"}]`,
			want: map[string]string{
				"system":             "messages.0.content",
				"messages.0.content": "messages.1.content",
			},
		},
		{
			name: "single surviving system text maps to scalar",
			system: `[
				{"type":"text","text":"x-anthropic-billing-header: cc_version=test"},
				{"type":"text","text":""},
				{"type":"text","text":"instructions"}
			]`,
			messages: `[{"role":"user","content":"current environment"}]`,
			want: map[string]string{
				"system.2.text":      "messages.0.content",
				"messages.0.content": "messages.1.content",
			},
		},
		{
			name: "multiple system texts collapsed into one string stay unmatched",
			system: `[
				{"type":"text","text":"same instructions"},
				{"type":"text","text":"same instructions"}
			]`,
			messages:  `[{"role":"user","content":"current environment"}]`,
			want:      map[string]string{"messages.0.content": "messages.1.content"},
			unmatched: []string{"system.0.text", "system.1.text"},
		},
		{
			name: "single surviving user text maps to scalar",
			messages: `[{"role":"user","content":[
				{"type":"unsupported","text":"ignored"},
				{"type":"text","text":""},
				{"type":"text","text":"current environment"}
			]}]`,
			want: map[string]string{"messages.0.content.2.text": "messages.0.content"},
		},
		{
			name: "multiple user texts collapsed into one string stay unmatched",
			messages: `[{"role":"user","content":[
				{"type":"text","text":"same environment"},
				{"type":"text","text":"same environment"}
			]}]`,
			unmatched: []string{"messages.0.content.0.text", "messages.0.content.1.text"},
		},
		{
			name: "multimodal duplicate texts retain structural positions",
			messages: `[{"role":"user","content":[
				{"type":"unsupported","text":"ignored"},
				{"type":"text","text":"same environment"},
				{"type":"image","source":{"type":"base64","media_type":"image/png","data":"aGVsbG8="}},
				{"type":"text","text":"same environment"}
			]}]`,
			want: map[string]string{
				"messages.0.content.1.text": "messages.0.content.0.text",
				"messages.0.content.3.text": "messages.0.content.2.text",
			},
		},
		{
			name: "tool reply normalization and lifted images preserve source positions",
			messages: `[
				{"role":"assistant","content":[{"type":"tool_use","id":"image_call","name":"inspect","input":{}}]},
				{"role":"user","content":"between call and reply"},
				{"role":"user","content":[
					{"type":"tool_result","tool_use_id":"image_call","content":[
						{"type":"text","text":"tool result"},
						{"type":"image","source":{"type":"base64","media_type":"image/png","data":"aGVsbG8="}}
					]},
					{"type":"text","text":"current environment"}
				]},
				{"role":"user","content":"following question"}
			]`,
			want: map[string]string{
				"messages.1.content":        "messages.2.content",
				"messages.2.content.1.text": "messages.3.content.0.text",
				"messages.3.content":        "messages.4.content",
			},
		},
		{
			name: "orphan reply and unanswered call are removed before mapping next user",
			messages: `[
				{"role":"assistant","content":[{"type":"tool_use","id":"unanswered","name":"inspect","input":{}}]},
				{"role":"user","content":[{"type":"tool_result","tool_use_id":"orphan","content":"ignored"}]},
				{"role":"user","content":"current environment"}
			]`,
			want: map[string]string{"messages.2.content": "messages.0.content"},
		},
		{
			name: "single assistant text remains scalar despite thinking block",
			messages: `[{"role":"assistant","content":[
				{"type":"thinking","thinking":"not user text"},
				{"type":"text","text":"answer"}
			]}]`,
			want: map[string]string{"messages.0.content.1.text": "messages.0.content"},
		},
		{
			name: "multiple assistant texts collapsed into one string stay unmatched",
			messages: `[{"role":"assistant","content":[
				{"type":"text","text":"answer part one"},
				{"type":"text","text":"answer part two"}
			]}]`,
			unmatched: []string{"messages.0.content.0.text", "messages.0.content.1.text"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := &AnthropicRequest{Model: "model", MaxTokens: 2048, System: json.RawMessage(tc.system)}
			require.NoError(t, json.Unmarshal([]byte(tc.messages), &req.Messages))
			original, err := json.Marshal(req)
			require.NoError(t, err)
			wantOutput, err := AnthropicToChatCompletionsRequest(req)
			require.NoError(t, err)
			gotOutput, mapping, err := AnthropicToChatCompletionsRequestWithPathMapping(req)
			require.NoError(t, err)
			require.Equal(t, wantOutput, gotOutput, "recording provenance must not change the converted request")
			for source, destination := range tc.want {
				actual, exists := mapping[source]
				require.True(t, exists, "source path %s must be traced", source)
				require.Equal(t, destination, actual, "source path %s", source)
			}
			for _, source := range tc.unmatched {
				require.NotContains(t, mapping, source, "ambiguous merged text cannot be reported as matched or dropped")
			}
			after, err := json.Marshal(req)
			require.NoError(t, err)
			require.JSONEq(t, string(original), string(after), "mapping must not mutate the source request")
		})
	}
}

func TestAnthropicToChatCompletionsRequestWithPathMappingDroppedSearchLocation(t *testing.T) {
	req := &AnthropicRequest{
		Model: "model",
		Messages: []AnthropicMessage{
			{Role: "user", Content: json.RawMessage(`"hello"`)},
		},
		Tools: []AnthropicTool{
			{Type: "web_search_20250305", Name: "web_search", UserLocation: json.RawMessage(`{"timezone":"America/Los_Angeles","city":"Seattle"}`)},
			{Type: "web_search", Name: "web_search", UserLocation: json.RawMessage(`{"timezone":"Asia/Shanghai"}`)},
			{Name: "inspect", InputSchema: json.RawMessage(`{"type":"object"}`)},
		},
	}
	wantOutput, err := AnthropicToChatCompletionsRequest(req)
	require.NoError(t, err)
	gotOutput, mapping, err := AnthropicToChatCompletionsRequestWithPathMapping(req)
	require.NoError(t, err)
	require.Equal(t, wantOutput, gotOutput)
	require.Len(t, gotOutput.Tools, 1)
	for _, source := range []string{"tools.0.user_location.timezone", "tools.1.user_location.timezone"} {
		destination, exists := mapping[source]
		require.True(t, exists, "removed search location must be explicitly recorded: %s", source)
		require.Empty(t, destination)
	}
}

func TestAnthropicToChatCompletionsRequestWithPathMappingNilRequest(t *testing.T) {
	out, _, err := AnthropicToChatCompletionsRequestWithPathMapping(nil)
	require.Error(t, err)
	require.Nil(t, out)
}

func TestAnthropicToChatCompletionsRequestWithPathMappingDroppedSearchLocationInvalidShapes(t *testing.T) {
	for _, raw := range []string{"", `null`, `"invalid"`, `{}`, `{"timezone":null}`, `{"timezone":7}`} {
		t.Run("location="+raw, func(t *testing.T) {
			req := &AnthropicRequest{
				Model: "model",
				Messages: []AnthropicMessage{
					{Role: "user", Content: json.RawMessage(`"hello"`)},
				},
				Tools: []AnthropicTool{
					{Type: "web_search_20250305", Name: "web_search", UserLocation: json.RawMessage(raw)},
					{Name: "inspect", InputSchema: json.RawMessage(`{"type":"object"}`), UserLocation: json.RawMessage(`{"timezone":"UTC"}`)},
				},
			}
			out, mapping, err := AnthropicToChatCompletionsRequestWithPathMapping(req)
			require.NoError(t, err)
			require.Len(t, out.Tools, 1)
			destination, exists := mapping["tools.0.user_location.timezone"]
			require.True(t, exists, "a discarded search tool is definitive even when its location was invalid or missing")
			require.Empty(t, destination)
			require.NotContains(t, mapping, "tools.1.user_location.timezone", "function metadata is outside the search-location mapping contract")
		})
	}
}
