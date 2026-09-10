package apicompat

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

const webSearchLocationFixture = `{"type":"approximate","timezone":"Asia/Shanghai","country":"CN","region":"Shanghai","city":"Shanghai","future":{"id":9007199254740993,"values":[null,true,"unchanged"]}}`

func TestWebSearchUserLocationProtocolRoundTrip(t *testing.T) {
	var chat ChatCompletionsRequest
	require.NoError(t, json.Unmarshal([]byte(`{"model":"gpt-5","messages":[{"role":"user","content":"hello"}],"tools":[{"type":"web_search","user_location":`+webSearchLocationFixture+`}]}`), &chat))

	responses, err := ChatCompletionsToResponses(&chat)
	require.NoError(t, err)
	require.Len(t, responses.Tools, 1)
	require.Equal(t, webSearchLocationFixture, string(responses.Tools[0].UserLocation))
	responsesWire, err := json.Marshal(responses)
	require.NoError(t, err)
	var decodedResponses ResponsesRequest
	require.NoError(t, json.Unmarshal(responsesWire, &decodedResponses))

	anthropic, err := ResponsesToAnthropicRequest(&decodedResponses)
	require.NoError(t, err)
	require.Len(t, anthropic.Tools, 1)
	require.Equal(t, "web_search_20250305", anthropic.Tools[0].Type)
	require.Equal(t, webSearchLocationFixture, string(anthropic.Tools[0].UserLocation))
	anthropicWire, err := json.Marshal(anthropic)
	require.NoError(t, err)
	var decodedAnthropic AnthropicRequest
	require.NoError(t, json.Unmarshal(anthropicWire, &decodedAnthropic))

	roundTrip, err := AnthropicToResponses(&decodedAnthropic)
	require.NoError(t, err)
	require.Len(t, roundTrip.Tools, 1)
	require.Equal(t, "web_search", roundTrip.Tools[0].Type)
	require.Equal(t, webSearchLocationFixture, string(roundTrip.Tools[0].UserLocation))

	// Converted requests must own their location bytes: later conversion or
	// timezone edits must not rewrite the original audit/ingress view.
	responses.Tools[0].UserLocation[0] = '['
	require.Equal(t, webSearchLocationFixture, string(chat.Tools[0].UserLocation))
	anthropic.Tools[0].UserLocation[0] = '['
	require.Equal(t, webSearchLocationFixture, string(decodedResponses.Tools[0].UserLocation))
	roundTrip.Tools[0].UserLocation[0] = '['
	require.Equal(t, webSearchLocationFixture, string(decodedAnthropic.Tools[0].UserLocation))
}

func TestWebSearchUserLocationPreservesAbsentNullAndInvalidValues(t *testing.T) {
	for _, raw := range []string{"", "null", `"Asia/Shanghai"`, `7`, `{"timezone":null}`} {
		t.Run(raw, func(t *testing.T) {
			field := ""
			if raw != "" {
				field = `,"user_location":` + raw
			}
			var chat ChatCompletionsRequest
			require.NoError(t, json.Unmarshal([]byte(`{"model":"gpt-5","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"web_search"`+field+`}]}`), &chat))
			responses, err := ChatCompletionsToResponses(&chat)
			require.NoError(t, err)
			anthropic, err := ResponsesToAnthropicRequest(responses)
			require.NoError(t, err)
			roundTrip, err := AnthropicToResponses(anthropic)
			require.NoError(t, err)
			for _, tool := range []any{chat.Tools[0], responses.Tools[0], anthropic.Tools[0], roundTrip.Tools[0]} {
				wire, err := json.Marshal(tool)
				require.NoError(t, err)
				var fields map[string]json.RawMessage
				require.NoError(t, json.Unmarshal(wire, &fields))
				if raw == "" {
					require.NotContains(t, fields, "user_location")
				} else {
					require.Equal(t, raw, string(fields["user_location"]))
				}
			}
		})
	}
}

func TestWebSearchUserLocationOnlyMapsExistingServerSearchTools(t *testing.T) {
	for _, typ := range []string{"web_search", "google_search", "web_search_20250305"} {
		t.Run(typ, func(t *testing.T) {
			tools := convertResponsesToAnthropicTools([]ResponsesTool{{Type: typ, UserLocation: json.RawMessage(webSearchLocationFixture)}})
			require.Len(t, tools, 1)
			require.Equal(t, webSearchLocationFixture, string(tools[0].UserLocation))
		})
	}
	for _, typ := range []string{"web_search_20250305", "web_search_20260209"} {
		t.Run("anthropic_"+typ, func(t *testing.T) {
			tools := convertAnthropicToolsToResponses([]AnthropicTool{{Type: typ, Name: "web_search", UserLocation: json.RawMessage(webSearchLocationFixture)}})
			require.Len(t, tools, 1)
			require.Equal(t, webSearchLocationFixture, string(tools[0].UserLocation))
		})
	}

	// Chat's server search support is intentionally one-way. Location metadata
	// must not make the reverse adapter emit a previously unsupported tool.
	chat, err := ResponsesToChatCompletionsRequest(&ResponsesRequest{
		Model: "gpt-5", Input: json.RawMessage(`"hi"`),
		Tools: []ResponsesTool{{Type: "web_search", UserLocation: json.RawMessage(webSearchLocationFixture)}},
	})
	require.NoError(t, err)
	require.Empty(t, chat.Tools)

	codeTools := convertChatToolsToResponses([]ChatTool{{Type: "code_execution", UserLocation: json.RawMessage(webSearchLocationFixture)}}, nil)
	require.Len(t, codeTools, 1)
	require.Empty(t, codeTools[0].UserLocation)
}

func TestWebSearchUserLocationDoesNotInterpretFunctionSchema(t *testing.T) {
	const schema = `{"type":"object","properties":{"user_location":{"type":"object","properties":{"timezone":{"const":"Asia/Shanghai"}}}},"required":["user_location"]}`
	chatTools := convertChatToolsToResponses([]ChatTool{{
		Type: "function", UserLocation: json.RawMessage(webSearchLocationFixture),
		Function: &ChatFunction{Name: "web_search", Parameters: json.RawMessage(schema)},
	}}, nil)
	require.Len(t, chatTools, 1)
	require.Empty(t, chatTools[0].UserLocation)
	require.Equal(t, schema, string(chatTools[0].Parameters))

	anthropicTools := convertResponsesToAnthropicTools([]ResponsesTool{{
		Type: "function", Name: "web_search", Parameters: json.RawMessage(schema), UserLocation: json.RawMessage(webSearchLocationFixture),
	}})
	require.Len(t, anthropicTools, 1)
	require.Empty(t, anthropicTools[0].UserLocation)
	require.JSONEq(t, schema, string(anthropicTools[0].InputSchema))
	anthropicTools[0].UserLocation = json.RawMessage(webSearchLocationFixture)
	responseTools := convertAnthropicToolsToResponses(anthropicTools)
	require.Len(t, responseTools, 1)
	require.Empty(t, responseTools[0].UserLocation)
	require.JSONEq(t, schema, string(responseTools[0].Parameters))
}

func TestResponsesToolShorthandClearsPreviousUserLocation(t *testing.T) {
	tool := ResponsesTool{Type: "web_search", UserLocation: json.RawMessage(webSearchLocationFixture)}
	require.NoError(t, json.Unmarshal([]byte(`"exec"`), &tool))
	require.Equal(t, "custom", tool.Type)
	require.Equal(t, "exec", tool.Name)
	require.Nil(t, tool.UserLocation)
}
