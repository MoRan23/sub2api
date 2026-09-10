package apicompat

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestResponsesToChatCompletionsRequestWithPathMapping(t *testing.T) {
	tests := []struct {
		name         string
		instructions string
		input        string
		want         map[string]string
		unmatched    []string
	}{
		{
			name:         "bare input after instructions",
			instructions: "Follow the user request.",
			input:        `"current environment"`,
			want:         map[string]string{"input": "messages.1.content"},
		},
		{
			name: "unsupported input items do not consume message indices",
			input: `[
				{"type":"web_search_call","content":"discarded"},
				{"type":"local_shell_call","content":"discarded"},
				{"type":"message","role":"user","content":"current environment"}
			]`,
			want: map[string]string{"input.2.content": "messages.0.content"},
		},
		{
			name: "unanswered call and orphan reply do not consume message indices",
			input: `[
				{"type":"function_call","call_id":"unanswered","name":"inspect","arguments":"{}"},
				{"type":"function_call_output","call_id":"orphan","output":"orphan result"},
				{"role":"user","content":"current environment"}
			]`,
			want: map[string]string{"input.2.content": "messages.0.content"},
		},
		{
			name: "tool reply normalization and injected image shift following user",
			input: `[
				{"type":"function_call","call_id":"image_call","name":"inspect","arguments":"{}"},
				{"role":"user","content":"current environment"},
				{"type":"function_call_output","call_id":"image_call","output":"data:image/png;base64,aGVsbG8="}
			]`,
			want: map[string]string{"input.1.content": "messages.3.content"},
		},
		{
			name: "identical multimodal texts retain distinct structural positions",
			input: `[{"role":"user","content":[
				{"type":"unsupported","text":"discarded"},
				{"type":"input_text","text":"same environment"},
				{"type":"input_image","image_url":"https://example.invalid/image.png"},
				{"type":"input_text","text":"same environment"}
			]}]`,
			want: map[string]string{
				"input.0.content.1.text": "messages.0.content.0.text",
				"input.0.content.3.text": "messages.0.content.2.text",
			},
		},
		{
			name: "multiple text blocks merged into one string are ambiguous",
			input: `[{"role":"user","content":[
				{"type":"input_text","text":"same environment"},
				{"type":"input_text","text":"same environment"}
			]}]`,
			unmatched: []string{"input.0.content.0.text", "input.0.content.1.text"},
		},
		{
			name: "only surviving array text maps to scalar content",
			input: `[{"role":"user","content":[
				{"type":"unsupported","text":"discarded"},
				{"type":"input_text","text":""},
				{"type":"input_text","text":"current environment"}
			]}]`,
			want: map[string]string{"input.0.content.2.text": "messages.0.content"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := &ResponsesRequest{
				Model:        "model",
				Instructions: tc.instructions,
				Input:        json.RawMessage(tc.input),
			}
			original, err := json.Marshal(req)
			require.NoError(t, err)
			wantOutput, err := ResponsesToChatCompletionsRequestWithOptions(req, nil)
			require.NoError(t, err)
			gotOutput, mapping, err := ResponsesToChatCompletionsRequestWithPathMapping(req, nil)
			require.NoError(t, err)
			require.Equal(t, wantOutput, gotOutput, "recording provenance must not change the converted request")
			for source, destination := range tc.want {
				actual, exists := mapping[source]
				require.True(t, exists, "source path %s must be traced", source)
				require.Equal(t, destination, actual, "source path %s", source)
			}
			for _, source := range tc.unmatched {
				require.NotContains(t, mapping, source, "ambiguous many-to-one text cannot be reported as matched or dropped")
			}
			after, err := json.Marshal(req)
			require.NoError(t, err)
			require.JSONEq(t, string(original), string(after), "mapping must not mutate the source request")
		})
	}
}

func TestResponsesToChatCompletionsRequestWithPathMappingDroppedSearchLocation(t *testing.T) {
	req := &ResponsesRequest{
		Model: "model",
		Input: json.RawMessage(`"hello"`),
		Tools: []ResponsesTool{
			{Type: "web_search", UserLocation: json.RawMessage(`{"timezone":"America/Los_Angeles","city":"Seattle"}`)},
			{Type: "web_search_preview", UserLocation: json.RawMessage(`{"timezone":"Asia/Shanghai"}`)},
			{Type: "function", Name: "inspect", Parameters: json.RawMessage(`{"type":"object"}`)},
		},
	}
	wantOutput, err := ResponsesToChatCompletionsRequestWithOptions(req, nil)
	require.NoError(t, err)
	gotOutput, mapping, err := ResponsesToChatCompletionsRequestWithPathMapping(req, nil)
	require.NoError(t, err)
	require.Equal(t, wantOutput, gotOutput)
	require.Len(t, gotOutput.Tools, 1)
	for _, source := range []string{"tools.0.user_location.timezone", "tools.1.user_location.timezone"} {
		destination, exists := mapping[source]
		require.True(t, exists, "removed search location must be explicitly recorded: %s", source)
		require.Empty(t, destination)
	}
}

func TestResponsesToChatCompletionsRequestWithPathMappingReasoningCallbackOnce(t *testing.T) {
	req := &ResponsesRequest{
		Model: "model",
		Input: json.RawMessage(`[
			{"type":"reasoning","id":"reasoning_one","summary":[],"encrypted_content":"opaque"},
			{"role":"assistant","content":"answer one"},
			{"role":"user","content":"another question"},
			{"type":"reasoning","id":"reasoning_two","summary":[],"encrypted_content":"opaque"},
			{"role":"assistant","content":"answer two"}
		]`),
	}
	calls := make(map[string]int)
	opts := &ResponsesToChatOptions{ReasoningContentByID: func(id string) string {
		calls[id]++
		return "cached " + id
	}}
	gotOutput, mapping, err := ResponsesToChatCompletionsRequestWithPathMapping(req, opts)
	require.NoError(t, err)
	require.Equal(t, map[string]int{"reasoning_one": 1, "reasoning_two": 1}, calls,
		"tracing must not execute the stateful reasoning lookup a second time")
	require.Equal(t, "messages.1.content", mapping["input.2.content"])
	wantOutput, err := ResponsesToChatCompletionsRequestWithOptions(req, &ResponsesToChatOptions{
		ReasoningContentByID: func(id string) string { return "cached " + id },
	})
	require.NoError(t, err)
	require.Equal(t, wantOutput, gotOutput)
}

func TestResponsesToChatCompletionsRequestWithPathMappingNilRequest(t *testing.T) {
	out, _, err := ResponsesToChatCompletionsRequestWithPathMapping(nil, nil)
	require.Error(t, err)
	require.Nil(t, out)
}
