package apicompat

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestChatConversionAllowedToolsPreservesDeclarationsAndReferences(t *testing.T) {
	for _, mode := range []string{"auto", "required"} {
		t.Run(mode, func(t *testing.T) {
			body := []byte(fmt.Sprintf(`{"model":"gpt-5.5","messages":[{"role":"user","content":"hello"}],"tools":[
				{"type":"function","function":{"name":"unselected","description":"keep me","parameters":{"type":"object","properties":{"x":{"type":"number"}}},"strict":false}},
				{"type":"web_search","user_location":{"city":"Paris"}},
				{"type":"function","function":{"name":"python","parameters":{"type":"object"},"strict":true}}
			],"tool_choice":{"type":"allowed_tools","allowed_tools":{"mode":%q,"tools":[{"type":"function","function":{"name":"python"}}]}},"parallel_tool_calls":false}`, mode))
			before := bytes.Clone(body)
			check, err := CheckOpenAIOAuthChatConversion(body)
			require.NoError(t, err)
			require.Equal(t, "checked", check.Status)
			require.Equal(t, before, body)
			var request ChatCompletionsRequest
			require.NoError(t, json.Unmarshal(body, &request))
			original, err := json.Marshal(request)
			require.NoError(t, err)
			response, err := ChatCompletionsToResponses(&request)
			require.NoError(t, err)
			require.Len(t, response.Tools, 3)
			require.Equal(t, "unselected", response.Tools[0].Name)
			require.Equal(t, "keep me", response.Tools[0].Description)
			require.JSONEq(t, `{"type":"object","properties":{"x":{"type":"number"}}}`, string(response.Tools[0].Parameters))
			require.NotNil(t, response.Tools[0].Strict)
			require.False(t, *response.Tools[0].Strict)
			require.Equal(t, "web_search", response.Tools[1].Type)
			require.JSONEq(t, `{"city":"Paris"}`, string(response.Tools[1].UserLocation))
			require.True(t, *response.Tools[2].Strict)
			require.JSONEq(t, fmt.Sprintf(`{"type":"allowed_tools","mode":%q,"tools":[{"type":"function","name":"python"}]}`, mode), string(response.ToolChoice))
			require.NotNil(t, response.ParallelToolCalls)
			require.False(t, *response.ParallelToolCalls)
			after, err := json.Marshal(request)
			require.NoError(t, err)
			require.Equal(t, original, after)
		})
	}
}

func TestChatConversionMapsFunctionChoiceDeveloperAndImageDetail(t *testing.T) {
	body := []byte(`{"model":"gpt-5.5","messages":[{"role":"developer","content":"Keep this instruction"},{"role":"user","content":[{"type":"image_url","image_url":{"url":"https://example.com/image.png","detail":"high"}}]},{"role":"assistant","reasoning":"deliberation","content":"answer"}],"tools":[{"type":"function","function":{"name":"lookup"}}],"tool_choice":{"type":"function","function":{"name":"lookup"}}}`)
	check, err := CheckOpenAIOAuthChatConversion(body)
	require.NoError(t, err)
	require.Equal(t, []ChatConversionIssue{{Path: "messages.2.reasoning", Reason: "assistant_reasoning_wrapped"}}, check.Issues)
	var request ChatCompletionsRequest
	require.NoError(t, json.Unmarshal(body, &request))
	response, err := ChatCompletionsToResponses(&request)
	require.NoError(t, err)
	require.JSONEq(t, `{"type":"function","name":"lookup"}`, string(response.ToolChoice))
	var input []ResponsesInputItem
	require.NoError(t, json.Unmarshal(response.Input, &input))
	require.Len(t, input, 3)
	require.Equal(t, "developer", input[0].Role)
	require.JSONEq(t, `"Keep this instruction"`, string(input[0].Content))
	require.JSONEq(t, `[{"type":"input_image","image_url":"https://example.com/image.png","detail":"high"}]`, string(input[1].Content))
	require.JSONEq(t, `[{"type":"output_text","text":"<thinking>deliberation</thinking>\nanswer"}]`, string(input[2].Content))
}

func TestChatConversionCheckAllowsExistingMappedSemantics(t *testing.T) {
	requests := []string{
		`{"messages":[{"role":"system","content":[{"type":"text","text":"guide"},{"type":"image_url","image_url":{"url":"https://example.com/system.png","detail":"low"}}]},{"role":"user","content":[{"type":"file","file":{"filename":"report.pdf","file_id":"file_1"}}]}],"response_format":{"type":"json_schema","json_schema":{"name":"report","description":"result","schema":{"type":"object","properties":{"yes":{"type":"boolean"}}},"strict":false}},"n":1,"modalities":["text"],"logprobs":false,"top_logprobs":0}`,
		`{"messages":[{"role":"user","content":null}],"functions":[{"name":"legacy","description":"supported","parameters":{"type":"object"},"strict":true}],"function_call":{"name":"legacy"}}`,
		`{"messages":[{"role":"user","content":"hi"}],"tools":[{"type":"code_execution"},{"type":"x_search","allowed_x_handles":["example"],"excluded_x_handles":[],"from_date":"2026-01-01","to_date":"2026-01-31","enable_image_understanding":false,"enable_video_understanding":true}],"tool_choice":{"type":"code_execution"}}`,
		`{"messages":[{"role":"user","content":[]}],"response_format":{"type":"text"},"tool_choice":"none","seed":4,"logit_bias":{"1":-20},"provider_extension":{"anything":true}}`,
	}
	for index, raw := range requests {
		t.Run(fmt.Sprint(index), func(t *testing.T) {
			check, err := CheckOpenAIOAuthChatConversion([]byte(raw))
			require.NoError(t, err)
			require.Equal(t, "checked", check.Status)
		})
	}
}

func TestChatConversionResponseFormatNormalizesSupportedType(t *testing.T) {
	for _, test := range []struct{ input, expected string }{
		{`{"type":" JSON_OBJECT "}`, `{"type":"json_object"}`},
		{`{"type":" JSON_SCHEMA ","json_schema":{"name":"out","schema":{"type":"object"},"strict":false}}`, `{"type":"json_schema","name":"out","schema":{"type":"object"},"strict":false}`},
	} {
		body := []byte(`{"messages":[{"role":"user","content":"hello"}],"response_format":` + test.input + `}`)
		_, err := CheckOpenAIOAuthChatConversion(body)
		require.NoError(t, err)
		var request ChatCompletionsRequest
		require.NoError(t, json.Unmarshal(body, &request))
		response, err := ChatCompletionsToResponses(&request)
		require.NoError(t, err)
		require.JSONEq(t, test.expected, string(response.Text.Format))
	}
}

func TestChatConversionNullToolChoiceDoesNotShadowLegacyChoice(t *testing.T) {
	for _, test := range []struct{ fields, expected string }{
		{`"function_call":null`, ""},
		{`"tool_choice":null,"function_call":{"name":"lookup"}`, `{"type":"function","name":"lookup"}`},
	} {
		body := []byte(`{"messages":[{"role":"user","content":"hello"}],"functions":[{"name":"lookup"}],` + test.fields + `}`)
		_, err := CheckOpenAIOAuthChatConversion(body)
		require.NoError(t, err)
		var request ChatCompletionsRequest
		require.NoError(t, json.Unmarshal(body, &request))
		response, err := ChatCompletionsToResponses(&request)
		require.NoError(t, err)
		if test.expected == "" {
			require.Empty(t, response.ToolChoice)
		} else {
			require.JSONEq(t, test.expected, string(response.ToolChoice))
		}
	}
}

func TestChatConversionCheckPreservesHistoricalCallStrings(t *testing.T) {
	// Historical functions need not still be declared as callable tools. Even a
	// model-produced invalid JSON argument string is history, not a new schema.
	body := []byte(`{"messages":[{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"retired","arguments":"not valid JSON"}},{"id":"call_2","type":"function","function":{"name":"other","arguments":"{}"}}]},{"role":"tool","tool_call_id":"call_2","content":"second"},{"role":"tool","tool_call_id":"call_1","content":[{"type":"text","text":"first"},{"type":"text","text":" result"}]}]}`)
	check, err := CheckOpenAIOAuthChatConversion(body)
	require.NoError(t, err)
	require.Equal(t, "checked", check.Status)
	var request ChatCompletionsRequest
	require.NoError(t, json.Unmarshal(body, &request))
	response, err := ChatCompletionsToResponses(&request)
	require.NoError(t, err)
	var input []ResponsesInputItem
	require.NoError(t, json.Unmarshal(response.Input, &input))
	require.Len(t, input, 4)
	require.Equal(t, "not valid JSON", input[0].Arguments)
	require.Equal(t, "call_2", input[2].CallID)
	require.Equal(t, "call_1", input[3].CallID)
	require.Equal(t, "first result", input[3].Output)
}

func TestChatConversionCheckKnownLossAndExistingEmptyFallbacks(t *testing.T) {
	body := []byte(`{"max_tokens":12,"max_completion_tokens":20,"temperature":0.7,"top_p":0.8,"frequency_penalty":0.2,"presence_penalty":0,"stop":["END"],"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64, "}}]},{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"lookup","arguments":""}}]},{"role":"tool","tool_call_id":"call_1","content":""}]}`)
	check, err := CheckOpenAIOAuthChatConversion(body)
	require.NoError(t, err)
	require.Equal(t, "known_loss", check.Status)
	require.Len(t, check.Issues, 10)
	require.Contains(t, check.Issues, ChatConversionIssue{Path: "max_tokens", Reason: "oauth_output_limit_ignored"})
	require.Contains(t, check.Issues, ChatConversionIssue{Path: "stop", Reason: "oauth_stop_ignored"})
	require.Contains(t, check.Issues, ChatConversionIssue{Path: "messages.0.content.0", Reason: "empty_media_placeholder_omitted"})
	require.Contains(t, check.Issues, ChatConversionIssue{Path: "messages.1.tool_calls.0.function.arguments", Reason: "empty_tool_arguments_defaulted"})
	require.Contains(t, check.Issues, ChatConversionIssue{Path: "messages.2.content", Reason: "empty_tool_output_placeholder"})
	var request ChatCompletionsRequest
	require.NoError(t, json.Unmarshal(body, &request))
	response, err := ChatCompletionsToResponses(&request)
	require.NoError(t, err)
	var input []ResponsesInputItem
	require.NoError(t, json.Unmarshal(response.Input, &input))
	require.Equal(t, "{}", input[1].Arguments)
	require.Equal(t, "(empty)", input[2].Output)
}

func TestChatConversionCheckRejectsSemanticLossBeforeTypedDecode(t *testing.T) {
	base := `{"messages":[{"role":"user","content":"hello"}]}`
	cases := []struct {
		name, patch, path, reason string
	}{
		{"multiple choices", `{"n":2}`, "n", "multiple_choices_unsupported"},
		{"audio output", `{"audio":{}}`, "audio", "audio_output_unsupported"},
		{"audio modality", `{"modalities":["text","audio"]}`, "modalities", "audio_output_unsupported"},
		{"logprobs", `{"logprobs":true}`, "logprobs", "logprobs_unsupported"},
		{"top logprobs", `{"top_logprobs":4}`, "top_logprobs", "logprobs_unsupported"},
		{"bad max tokens", `{"max_tokens":"5"}`, "max_tokens", "invalid_field_type"},
		{"bad format", `{"response_format":{"type":"json_schema","json_schema":{"name":"result"}}}`, "response_format.json_schema.schema", "invalid_response_format"},
		{"format extension", `{"response_format":{"type":"json_object","SECRET_FIELD":"SECRET_VALUE"}}`, "response_format", "invalid_response_format"},
		{"unknown format", `{"response_format":{"type":"SECRET_VALUE"}}`, "response_format.type", "unsupported_response_format"},
		{"unsupported top reasoning", `{"reasoning":{"effort":"high"}}`, "reasoning", "unsupported_reasoning_field"},
		{"unknown role", `{"messages":[{"role":"SECRET_VALUE","content":"hello"}]}`, "messages.0.role", "unsupported_role"},
		{"name", `{"messages":[{"role":"user","name":"SECRET_VALUE","content":"hello"}]}`, "messages.0.name", "unsupported_message_name"},
		{"assistant object", `{"messages":[{"role":"assistant","content":{"SECRET_FIELD":"SECRET_VALUE"}}]}`, "messages.0.content", "unsupported_content"},
		{"refusal", `{"messages":[{"role":"assistant","refusal":"SECRET_VALUE"}]}`, "messages.0.refusal", "unsupported_message_field"},
		{"assistant audio", `{"messages":[{"role":"assistant","audio":{"id":"SECRET_VALUE"}}]}`, "messages.0.audio", "unsupported_message_field"},
		{"assistant image", `{"messages":[{"role":"assistant","content":[{"type":"image_url","image_url":{"url":"SECRET_VALUE"}}]}]}`, "messages.0.content.0.type", "unsupported_content_part"},
		{"unknown content", `{"messages":[{"role":"user","content":[{"type":"SECRET_VALUE","SECRET_FIELD":"SECRET_VALUE"}]}]}`, "messages.0.content.0.type", "unsupported_content_part"},
		{"bad image detail", `{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"https://example.com/a","detail":"SECRET_VALUE"}}]}]}`, "messages.0.content.0.image_url.detail", "invalid_image_detail"},
		{"conflicting reasoning", `{"messages":[{"role":"assistant","reasoning":"a","reasoning_content":"b"}]}`, "messages.0.reasoning", "conflicting_reasoning_aliases"},
		{"legacy history", `{"messages":[{"role":"assistant","function_call":{"name":"SECRET_VALUE","arguments":"{}"}}]}`, "messages.0.function_call", "legacy_function_history_unsupported"},
		{"legacy result", `{"messages":[{"role":"function","name":"SECRET_VALUE","content":"done"}]}`, "messages.0.role", "legacy_function_history_unsupported"},
		{"custom declaration", `{"tools":[{"type":"custom","custom":{"name":"SECRET_VALUE"}}]}`, "tools.0.type", "unsupported_tool_type"},
		{"unknown web options", `{"tools":[{"type":"web_search","SECRET_FIELD":"SECRET_VALUE"}]}`, "tools.0", "unsupported_tool_field"},
		{"duplicate declaration", `{"tools":[{"type":"function","function":{"name":"same"}},{"type":"function","function":{"name":"same"}}]}`, "tools.1.function.name", "duplicate_tool_name"},
		{"trimmed function would lose selection", `{"tools":[{"type":"function","function":{"name":" lookup "}}],"tool_choice":{"type":"function","function":{"name":" lookup "}}}`, "tools.0.function.name", "invalid_tool_definition"},
		{"missing selected function", `{"tool_choice":{"type":"function","function":{"name":"SECRET_VALUE"}}}`, "tool_choice", "unknown_tool_reference"},
		{"custom history", `{"messages":[{"role":"assistant","tool_calls":[{"id":"call_1","type":"custom","custom":{"name":"SECRET_VALUE","input":"SECRET_VALUE"}}]}]}`, "messages.0.tool_calls.0", "unsupported_tool_call_field"},
		{"unknown call", `{"messages":[{"role":"tool","tool_call_id":"SECRET_VALUE","content":"done"}]}`, "messages.0.tool_call_id", "unknown_tool_call_id"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			var root, patch map[string]json.RawMessage
			require.NoError(t, json.Unmarshal([]byte(base), &root))
			require.NoError(t, json.Unmarshal([]byte(test.patch), &patch))
			for key, value := range patch {
				root[key] = value
			}
			body, err := json.Marshal(root)
			require.NoError(t, err)
			before := bytes.Clone(body)
			check, err := CheckOpenAIOAuthChatConversion(body)
			require.Nil(t, check)
			var semantic *ChatConversionError
			require.True(t, errors.As(err, &semantic), "error=%v", err)
			require.Equal(t, test.path, semantic.Path)
			require.Equal(t, test.reason, semantic.Reason)
			require.NotContains(t, err.Error(), "SECRET")
			require.Equal(t, before, body)
		})
	}
}

func TestChatConversionAllowedToolsRejectsInvalidRestrictions(t *testing.T) {
	choices := []string{
		`{"type":"allowed_tools","allowed_tools":{"mode":"none","tools":[{"type":"function","function":{"name":"lookup"}}]}}`,
		`{"type":"allowed_tools","allowed_tools":{"mode":"auto","tools":[]}}`,
		`{"type":"allowed_tools","allowed_tools":{"mode":"required","tools":[{"type":"function","function":{"name":"missing"}}]}}`,
		`{"type":"allowed_tools","allowed_tools":{"mode":"required","tools":[{"type":"function","function":{"name":"lookup"}},{"type":"function","function":{"name":"lookup"}}]}}`,
		`{"type":"allowed_tools","allowed_tools":{"mode":"required","tools":[{"type":"custom","custom":{"name":"lookup"}}]}}`,
		`{"type":"allowed_tools","allowed_tools":{"mode":"required","tools":"lookup"}}`,
	}
	for _, choice := range choices {
		body := []byte(fmt.Sprintf(`{"messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"lookup"}}],"tool_choice":%s}`, choice))
		_, err := CheckOpenAIOAuthChatConversion(body)
		var semantic *ChatConversionError
		require.ErrorAs(t, err, &semantic)
		require.True(t, strings.HasPrefix(semantic.Path, "tool_choice"))
	}
}

func TestChatConversionCheckHistoryValidation(t *testing.T) {
	call := `{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{}"}}]}`
	result := `{"role":"tool","tool_call_id":"call_1","content":"done"}`
	for _, messages := range []string{
		call + "," + call,
		call + "," + result + "," + result,
		result + "," + call,
		call + `,{"role":"tool","tool_call_id":"call_1","content":[{"type":"image_url","image_url":{"url":"https://example.com/image"}}]}`,
		`{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"lookup","arguments":{}}}]}`,
		`{"role":"assistant","tool_calls":[{"id":"call_1","index":"0","type":"function","function":{"name":"lookup","arguments":"{}"}}]}`,
	} {
		_, err := CheckOpenAIOAuthChatConversion([]byte(`{"messages":[` + messages + `]}`))
		var semantic *ChatConversionError
		require.ErrorAs(t, err, &semantic)
	}
}

func TestChatConversionCheckBoundsDiagnosticIssues(t *testing.T) {
	messages := make([]string, 50)
	for index := range messages {
		messages[index] = `{"role":"assistant","reasoning":"private analysis"}`
	}
	check, err := CheckOpenAIOAuthChatConversion([]byte(`{"messages":[` + strings.Join(messages, ",") + `]}`))
	require.NoError(t, err)
	require.Equal(t, "known_loss", check.Status)
	require.Len(t, check.Issues, 32)
	serialized, err := json.Marshal(check)
	require.NoError(t, err)
	require.NotContains(t, string(serialized), "private analysis")
}

func TestChatConversionSharedConverterDoesNotApplyOAuthGuard(t *testing.T) {
	request := &ChatCompletionsRequest{
		Messages:   []ChatMessage{{Role: "user", Content: json.RawMessage(`"hello"`)}},
		ToolChoice: json.RawMessage(`{"type":"provider_specific","name":"custom"}`),
	}
	response, err := ChatCompletionsToResponses(request)
	require.NoError(t, err)
	require.JSONEq(t, string(request.ToolChoice), string(response.ToolChoice))
}
