package apicompat

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestResponsesToAnthropicRequestWithPathMapping_TextProvenance(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  map[string]string
	}{
		{
			name:  "plain input",
			input: `"same"`,
			want:  map[string]string{"input": "messages.0.content"},
		},
		{
			name:  "identical strings retain separate source positions after merge",
			input: `[{"role":"user","content":"same"},{"role":"user","content":"same"}]`,
			want: map[string]string{
				"input.0.content": "messages.0.content.0.text",
				"input.1.content": "messages.0.content.1.text",
			},
		},
		{
			name:  "unsupported parts removed and images occupy destination indices",
			input: `[{"role":"user","content":[{"type":"input_file","text":"same"},{"type":"input_text","text":"same"},{"type":"input_image","image_url":"data:image/png;base64,YQ=="},{"type":"text","text":"same"},{"type":"output_text","text":"same"}]}]`,
			want: map[string]string{
				"input.0.content.0.text": "",
				"input.0.content.1.text": "messages.0.content.0.text",
				"input.0.content.3.text": "messages.0.content.2.text",
				"input.0.content.4.text": "",
			},
		},
		{
			name:  "reasoning and blank assistant discarded without matching repeated text",
			input: `[{"type":"reasoning","content":[{"type":"reasoning_text","text":"same"}]},{"role":"assistant","content":" "},{"role":"user","content":"same"},{"role":"assistant","content":[{"type":"input_text","text":"same"},{"type":"output_text","text":"same"}]}]`,
			want: map[string]string{
				"input.0.content.0.text": "",
				"input.1.content":        "",
				"input.2.content":        "messages.0.content",
				"input.3.content.0.text": "",
				"input.3.content.1.text": "messages.1.content.0.text",
			},
		},
		{
			name:  "system joins remain unmatched rather than falsely deleted",
			input: `[{"role":"system","content":"same"},{"role":"developer","content":[{"type":"input_text","text":"same"}]},{"role":"user","content":"same"}]`,
			want:  map[string]string{"input.2.content": "messages.0.content"},
		},
		{
			name:  "opaque malformed content remains unmatched",
			input: `[{"role":"user","content":{"text":"same"}},{"role":"assistant","content":[{"type":"output_text","text":"same"},{"type":"output_text","text":42}]},{"role":"user","content":"same"}]`,
			want:  map[string]string{"input.2.content": "messages.2.content"},
		},
		{
			name:  "unknown role uses supported user conversion",
			input: `[{"type":"future_item","content":[{"type":"input_text","text":"same"},{"type":"reasoning_text","text":"same"}]}]`,
			want: map[string]string{
				"input.0.content.0.text": "messages.0.content.0.text",
				"input.0.content.1.text": "",
			},
		},
		{
			name:  "tool pairing moves assistant text before calls and preserves user order",
			input: `[{"role":"user","content":"same"},{"type":"function_call","call_id":"call_a","name":"run","arguments":"{}"},{"role":"assistant","content":"same"},{"role":"user","content":"same"},{"type":"function_call_output","call_id":"call_a","output":"same"},{"role":"user","content":"same"},{"role":"assistant","content":"same"}]`,
			want: map[string]string{
				"input.0.content": "messages.0.content",
				"input.2.content": "messages.1.content.0.text",
				"input.3.content": "messages.2.content.1.text",
				"input.5.content": "messages.2.content.2.text",
				"input.6.content": "messages.3.content.0.text",
			},
		},
		{
			name:  "dangling call removal requires second merge",
			input: `[{"role":"user","content":"same"},{"type":"function_call","call_id":"missing","name":"run","arguments":"{}"},{"role":"user","content":"same"}]`,
			want: map[string]string{
				"input.0.content": "messages.0.content.0.text",
				"input.2.content": "messages.0.content.1.text",
			},
		},
		{
			name:  "orphan tool result removal preserves neighboring text identity",
			input: `[{"role":"user","content":"same"},{"type":"function_call_output","call_id":"orphan","output":"same"},{"role":"user","content":"same"}]`,
			want: map[string]string{
				"input.0.content": "messages.0.content.0.text",
				"input.2.content": "messages.0.content.1.text",
			},
		},
		{
			name:  "empty source strings are explicitly discarded",
			input: `[{"role":"user","content":""},{"role":"assistant","content":[{"type":"text","text":""}]},{"role":"user","content":"same"}]`,
			want: map[string]string{
				"input.0.content":        "",
				"input.1.content.0.text": "",
				"input.2.content":        "messages.0.content",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := &ResponsesRequest{Model: "model", Instructions: "system", Input: json.RawMessage(tt.input)}
			original, err := ResponsesToAnthropicRequest(req)
			require.NoError(t, err)
			got, paths, err := ResponsesToAnthropicRequestWithPathMapping(req)
			require.NoError(t, err)
			require.Equal(t, original, got, "provenance must not change conversion output")
			require.Equal(t, tt.want, paths)
			require.JSONEq(t, tt.input, string(req.Input), "source input is immutable")
		})
	}
}

func TestResponsesToAnthropicRequestWithPathMapping_Tools(t *testing.T) {
	req := &ResponsesRequest{Input: json.RawMessage(`"q"`), Tools: []ResponsesTool{
		{Type: "function", Name: "fn", UserLocation: json.RawMessage(`{"timezone":"Asia/Shanghai"}`)},
		{Type: "web_search", UserLocation: json.RawMessage(`{"timezone":"Asia/Shanghai","unknown":{"large":9007199254740993}}`)},
		{Type: "google_search", UserLocation: json.RawMessage(`{"timezone":null}`)},
		{Type: "web_search_20250305", UserLocation: json.RawMessage(`{"timezone":42}`)},
		{Type: "web_search_preview", UserLocation: json.RawMessage(`{"timezone":"Asia/Shanghai"}`)},
		{Type: "web_search", UserLocation: json.RawMessage(`{"city":"Seattle"}`)},
		{Type: "web_search", UserLocation: json.RawMessage(`"opaque"`)},
		{Type: "web_search"},
		{Type: "web_search", UserLocation: json.RawMessage(`null`)},
	}}
	original, err := ResponsesToAnthropicRequest(req)
	require.NoError(t, err)
	out, paths, err := ResponsesToAnthropicRequestWithPathMapping(req)
	require.NoError(t, err)
	require.Equal(t, original, out)
	require.Equal(t, map[string]string{
		"input":                          "messages.0.content",
		"tools.0.user_location.timezone": "",
		"tools.1.user_location.timezone": "tools.1.user_location.timezone",
		"tools.2.user_location.timezone": "tools.2.user_location.timezone",
		"tools.3.user_location.timezone": "tools.3.user_location.timezone",
		"tools.4.user_location.timezone": "",
		"tools.5.user_location.timezone": "tools.5.user_location.timezone",
		"tools.6.user_location.timezone": "tools.6.user_location.timezone",
		"tools.7.user_location.timezone": "tools.7.user_location.timezone",
		"tools.8.user_location.timezone": "tools.8.user_location.timezone",
	}, paths)
}

func TestResponsesToAnthropicRequestWithPathMapping_PreservesError(t *testing.T) {
	req := &ResponsesRequest{Input: json.RawMessage(`42`)}
	_, original := ResponsesToAnthropicRequest(req)
	out, paths, err := ResponsesToAnthropicRequestWithPathMapping(req)
	require.Error(t, original)
	require.EqualError(t, err, original.Error())
	require.Nil(t, out)
	require.Nil(t, paths)
}

func TestResponsesToAnthropicRequestWithPathMapping_NilRequest(t *testing.T) {
	out, paths, err := ResponsesToAnthropicRequestWithPathMapping(nil)
	require.EqualError(t, err, "responses request is nil")
	require.Nil(t, out)
	require.Nil(t, paths)
}
