package apicompat

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestChatCompletionsToResponsesPathMappingDeletedSearchAndParts(t *testing.T) {
	var request ChatCompletionsRequest
	require.NoError(t, json.Unmarshal([]byte(`{
		"model":"gpt-5","messages":[
			{"role":"assistant","content":"same","tool_calls":[{"id":"call_1","type":"function","function":{"name":"test","arguments":"{}"}}]},
			{"role":"user","content":[
				{"type":"input_text","text":"same"},
				{"type":"image_url","image_url":{"url":"data:image/png;base64,"}},
				{"type":"text","text":"same"},
				{"type":"image_url","image_url":{"url":"https://example.invalid/image.png"}},
				{"type":"text","text":"same"}
			]}
		],"tools":[
			{"type":"web_search_preview","user_location":{"timezone":"Asia/Shanghai"}},
			{"type":"function"},
			{"type":"web_search","user_location":{"timezone":"Asia/Shanghai"}}
		]
	}`), &request))
	out, paths, err := ChatCompletionsToResponsesWithPathMapping(&request)
	require.NoError(t, err)
	plain, err := ChatCompletionsToResponses(&request)
	require.NoError(t, err)
	require.Equal(t, plain, out)
	require.Equal(t, "", paths["tools.0.user_location.timezone"])
	require.Equal(t, "tools.0.user_location.timezone", paths["tools.2.user_location.timezone"])
	require.Equal(t, "input.0.content.0.text", paths["messages.0.content"])
	require.Equal(t, "", paths["messages.1.content.0.text"])
	require.Equal(t, "input.2.content.0.text", paths["messages.1.content.2.text"])
	require.Equal(t, "input.2.content.2.text", paths["messages.1.content.4.text"])
}

func TestAnthropicToResponsesPathMappingSystemExtractionAndTextParts(t *testing.T) {
	var request AnthropicRequest
	require.NoError(t, json.Unmarshal([]byte(`{
		"model":"gpt-5","system":[
			{"type":"text","text":"x-anthropic-billing-header: unused"},
			{"type":"text","text":"same"}
		],"messages":[
			{"role":"assistant","content":[{"type":"thinking","thinking":"private","signature":"opaque"},{"type":"text","text":"same"}]},
			{"role":"user","content":[
				{"type":"text","text":"same"},
				{"type":"tool_result","tool_use_id":"call_1","content":"done"},
				{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AA=="}},
				{"type":"input_text","text":"same"},
				{"type":"text","text":"same"}
			]}
		],"tools":[{"name":"read_file","input_schema":{"type":"object"}},{"type":"web_search_20250305","name":"web_search","user_location":{"timezone":"Asia/Shanghai"}}]
	}`), &request))
	out, paths, err := AnthropicToResponsesWithPathMapping(&request)
	require.NoError(t, err)
	plain, err := AnthropicToResponses(&request)
	require.NoError(t, err)
	require.Equal(t, plain, out)
	require.Equal(t, "", paths["system.0.text"])
	require.Equal(t, "input.0.content.0.text", paths["system.1.text"])
	require.Equal(t, "input.2.content.0.text", paths["messages.0.content.1.text"])
	require.Equal(t, "input.4.content.0.text", paths["messages.1.content.0.text"])
	require.Equal(t, "", paths["messages.1.content.3.text"])
	require.Equal(t, "input.4.content.2.text", paths["messages.1.content.4.text"])
	require.Equal(t, "tools.1.user_location.timezone", paths["tools.1.user_location.timezone"])
}

func TestRequestPathMappingComposesProtocolConversions(t *testing.T) {
	chat := &ChatCompletionsRequest{
		Model:    "gpt-5",
		Messages: []ChatMessage{{Role: "user", Content: json.RawMessage(`"same"`)}, {Role: "user", Content: json.RawMessage(`"same"`)}},
		Tools:    []ChatTool{{Type: "web_search_preview"}, {Type: "web_search", UserLocation: json.RawMessage(`{"timezone":"Asia/Shanghai"}`)}},
	}
	responses, first, err := ChatCompletionsToResponsesWithPathMapping(chat)
	require.NoError(t, err)
	anthropic, second, err := ResponsesToAnthropicRequestWithPathMapping(responses)
	require.NoError(t, err)
	_, third, err := AnthropicToResponsesWithPathMapping(anthropic)
	require.NoError(t, err)
	composed := ComposeRequestPathMappings(ComposeRequestPathMappings(first, second), third)
	require.Equal(t, "", composed["tools.0.user_location.timezone"])
	require.Equal(t, "tools.0.user_location.timezone", composed["tools.1.user_location.timezone"])
	require.Equal(t, "input.0.content.0.text", composed["messages.0.content"])
	require.Equal(t, "input.0.content.1.text", composed["messages.1.content"])
}

func TestComposeRequestPathMappingsDoesNotAssumeIdentity(t *testing.T) {
	first := map[string]string{"removed": "", "known": "intermediate", "unknown": "other"}
	next := map[string]string{"intermediate": "final", "unrelated": "unknown"}
	composed := ComposeRequestPathMappings(first, next)
	require.Equal(t, map[string]string{"removed": "", "known": "final"}, composed)
	composed["known"] = "changed"
	require.Equal(t, "intermediate", first["known"])
	require.Equal(t, "final", next["intermediate"])
}

func TestRequestPathMappingsDoNotMutateRequests(t *testing.T) {
	chat := &ChatCompletionsRequest{Model: "gpt-5", Messages: []ChatMessage{{Role: "user", Content: json.RawMessage(`"<environment_context><timezone>Asia/Shanghai</timezone></environment_context>"`)}}}
	before, err := json.Marshal(chat)
	require.NoError(t, err)
	_, paths, err := ChatCompletionsToResponsesWithPathMapping(chat)
	require.NoError(t, err)
	require.Equal(t, "input.0.content", paths["messages.0.content"])
	after, err := json.Marshal(chat)
	require.NoError(t, err)
	require.Equal(t, before, after)
}
