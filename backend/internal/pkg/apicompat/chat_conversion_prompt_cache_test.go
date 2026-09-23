package apicompat

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestChatConversionPreservesInputBreakpointsAndImageDetail(t *testing.T) {
	for _, role := range []string{"system", "developer", "user"} {
		t.Run(role, func(t *testing.T) {
			parts := json.RawMessage(`[{"type":"text","text":"hello","prompt_cache_breakpoint":{"mode":"explicit"}},{"type":"image_url","image_url":{"url":"https://example.com/image.png","detail":"high"},"prompt_cache_breakpoint":{"mode":"explicit"}},{"type":"file","file":{"file_id":"file-1"},"prompt_cache_breakpoint":{"mode":"explicit"}}]`)
			request := ChatCompletionsRequest{Model: "gpt-6-sol", PromptCacheOptions: json.RawMessage(`{"mode":"explicit","ttl":"30m"}`), Messages: []ChatMessage{{Role: role, Content: parts}}}
			body, err := json.Marshal(request)
			require.NoError(t, err)
			check, err := CheckOpenAIOAuthChatConversion(body)
			require.NoError(t, err)
			require.Equal(t, "checked", check.Status)
			converted, err := ChatCompletionsToResponses(&request)
			require.NoError(t, err)
			require.JSONEq(t, string(request.PromptCacheOptions), string(converted.PromptCacheOptions))
			var input []ResponsesInputItem
			require.NoError(t, json.Unmarshal(converted.Input, &input))
			require.Len(t, input, 1)
			require.Equal(t, role, input[0].Role)
			require.JSONEq(t, `[{"type":"input_text","text":"hello","prompt_cache_breakpoint":{"mode":"explicit"}},{"type":"input_image","image_url":"https://example.com/image.png","detail":"high","prompt_cache_breakpoint":{"mode":"explicit"}},{"type":"input_file","file_id":"file-1","prompt_cache_breakpoint":{"mode":"explicit"}}]`, string(input[0].Content))
		})
	}
}

func TestChatConversionDoesNotClaimFlattenedBreakpointsArePreserved(t *testing.T) {
	for _, body := range []string{
		`{"messages":[{"role":"assistant","content":[{"type":"text","text":"answer","prompt_cache_breakpoint":{"mode":"explicit"}}]}]}`,
		`{"messages":[{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{}"}}]},{"role":"tool","tool_call_id":"call_1","content":[{"type":"text","text":"answer","prompt_cache_breakpoint":{"mode":"explicit"}}]}],"tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":"object"}}}]}`,
	} {
		_, err := CheckOpenAIOAuthChatConversion([]byte(body))
		require.Error(t, err)
		require.Contains(t, err.Error(), "unsupported_content_part_field")
	}
}
