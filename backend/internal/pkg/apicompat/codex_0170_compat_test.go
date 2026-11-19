package apicompat

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestReasoningEffortWireTypes(t *testing.T) {
	for _, value := range []string{`"disabled"`, `"custom"`, `"18446744073709551615"`, `0`, `9007199254740993`, `18446744073709551615`} {
		t.Run(value, func(t *testing.T) {
			var source ResponsesRequest
			require.NoError(t, json.Unmarshal([]byte(`{"model":"gpt-6-sol","input":"hello","reasoning":{"effort":`+value+`}}`), &source))
			chat, err := ResponsesToChatCompletionsRequest(&source)
			require.NoError(t, err)
			wire, err := json.Marshal(chat)
			require.NoError(t, err)
			var fields map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(wire, &fields))
			require.JSONEq(t, value, string(fields["reasoning_effort"]))
			var decoded ChatCompletionsRequest
			require.NoError(t, json.Unmarshal(wire, &decoded))
			back, err := ChatCompletionsToResponses(&decoded)
			require.NoError(t, err)
			reasoningJSON, err := json.Marshal(back.Reasoning)
			require.NoError(t, err)
			require.NoError(t, json.Unmarshal(reasoningJSON, &fields))
			require.JSONEq(t, value, string(fields["effort"]))
		})
	}
	for _, value := range []string{`-1`, `1.2`, `1e3`, `18446744073709551616`, `true`, `{}`, `[]`} {
		t.Run("invalid/"+value, func(t *testing.T) {
			var r ResponsesReasoning
			require.Error(t, json.Unmarshal([]byte(`{"effort":`+value+`}`), &r))
			var c ChatCompletionsRequest
			require.Error(t, json.Unmarshal([]byte(`{"reasoning_effort":`+value+`}`), &c))
		})
	}
}

func TestChatUnsetReasoningEffortIsOmitted(t *testing.T) {
	for _, input := range []string{`{"model":"test","messages":[]}`, `{"model":"test","messages":[],"reasoning_effort":null}`, `{"model":"test","messages":[],"reasoning_effort":""}`} {
		var req ChatCompletionsRequest
		require.NoError(t, json.Unmarshal([]byte(input), &req))
		wire, err := json.Marshal(req)
		require.NoError(t, err)
		require.NotContains(t, string(wire), "reasoning_effort")
	}
}

func TestResponsesBridgesRejectUnresolvedContinuation(t *testing.T) {
	for _, input := range []string{`"continue"`, `[{"role":"user","content":"continue"},{"type":"configuration_update","reasoning":{"effort":"high"}}]`} {
		req := &ResponsesRequest{Model: "test", PreviousResponseID: "resp_old", Reasoning: &ResponsesReasoning{Effort: "low"}, Input: json.RawMessage(input)}
		_, err := ResponsesToChatCompletionsRequest(req)
		require.ErrorContains(t, err, "resend the full conversation")
		_, err = ResponsesToAnthropicRequest(req)
		require.ErrorContains(t, err, "resend the full conversation")
		require.Equal(t, "resp_old", req.PreviousResponseID)
	}
}

func TestBridgeConfigurationUpdateEffort(t *testing.T) {
	request := &ResponsesRequest{
		Model: "gpt-6-sol", Reasoning: &ResponsesReasoning{Effort: "low", Summary: "auto"},
		Input: json.RawMessage(`[{"type":"message","role":"user","content":"hello"},{"type":"configuration_update","reasoning":{"effort":"high"}},{"type":"configuration_update","reasoning":{"effort":18446744073709551615}}]`),
	}
	chat, err := ResponsesToChatCompletionsRequest(request)
	require.NoError(t, err)
	require.Equal(t, "18446744073709551615", chat.ReasoningEffortText())
	require.True(t, chat.NumericReasoningEffort())
	require.Len(t, chat.Messages, 1)
	require.Equal(t, "low", request.Reasoning.Effort, "input request must stay immutable")
	_, err = ResponsesToAnthropicRequest(request)
	require.ErrorContains(t, err, "numeric reasoning effort")
	request.Model = "claude-sonnet-5-5"
	request.Input = json.RawMessage(`[{"role":"user","content":"hello"},{"type":"configuration_update","reasoning":{"effort":10000}},{"type":"configuration_update","reasoning":{"effort":"high"}}]`)
	anthropic, err := ResponsesToAnthropicRequest(request)
	require.NoError(t, err)
	require.Equal(t, "high", anthropic.OutputConfig.Effort)
	request.Reasoning.Effort = "custom"
	request.Input = json.RawMessage(`"hello"`)
	_, err = ResponsesToAnthropicRequest(request)
	require.ErrorContains(t, err, "does not support reasoning effort")
}

func TestResponsesInputAudioChatBridge(t *testing.T) {
	for _, format := range []string{"wav", "mpeg", "mp3"} {
		t.Run(format, func(t *testing.T) {
			input := fmt.Sprintf(`[{"role":"user","content":[{"type":"input_text","text":"before"},{"type":"input_audio","audio_url":"data:audio/%s;base64,AQIDBA=="},{"type":"input_text","text":"after"}]}]`, format)
			req := &ResponsesRequest{Model: "audio-provider", Input: json.RawMessage(input)}
			chat, paths, err := ResponsesToChatCompletionsRequestWithPathMapping(req, nil)
			require.NoError(t, err)
			require.Len(t, chat.Messages, 1)
			var parts []ChatContentPart
			require.NoError(t, json.Unmarshal(chat.Messages[0].Content, &parts))
			require.Len(t, parts, 3)
			require.Equal(t, "before", parts[0].Text)
			require.Equal(t, "input_audio", parts[1].Type)
			require.Equal(t, "AQIDBA==", parts[1].InputAudio.Data)
			expectedFormat := format
			if format == "mpeg" {
				expectedFormat = "mp3"
			}
			require.Equal(t, expectedFormat, parts[1].InputAudio.Format)
			require.Equal(t, "after", parts[2].Text)
			require.Equal(t, "messages.0.content.2.text", paths["input.0.content.2.text"])
			back, err := ChatCompletionsToResponses(chat)
			require.NoError(t, err)
			require.Contains(t, string(back.Input), `"type":"input_audio"`)
			require.Contains(t, string(back.Input), "AQIDBA==")
			require.Equal(t, input, string(req.Input))
		})
	}
}

func TestResponsesToolAudioPreservesToolSequence(t *testing.T) {
	for _, outputType := range []string{"function_call_output", "custom_tool_call_output"} {
		t.Run(outputType, func(t *testing.T) {
			input := `[{"type":"function_call","call_id":"call_a","name":"record","arguments":"{}"},{"type":"function_call","call_id":"call_b","name":"status","arguments":"{}"},{"type":"` + outputType + `","call_id":"call_a","output":{"content":[{"type":"input_text","text":"captured"},{"type":"input_audio","audio_url":"data:audio/wav;base64,AQID"}]}},{"type":"function_call_output","call_id":"call_b","output":"ready"}]`
			chat, err := ResponsesToChatCompletionsRequest(&ResponsesRequest{Model: "audio-provider", Input: json.RawMessage(input)})
			require.NoError(t, err)
			require.Len(t, chat.Messages, 4)
			require.Equal(t, []string{"assistant", "tool", "tool", "user"}, []string{chat.Messages[0].Role, chat.Messages[1].Role, chat.Messages[2].Role, chat.Messages[3].Role})
			require.Equal(t, "call_a", chat.Messages[1].ToolCallID)
			require.Equal(t, "call_b", chat.Messages[2].ToolCallID)
			require.Contains(t, string(chat.Messages[1].Content), "captured")
			require.NotContains(t, string(chat.Messages[1].Content), "AQID")
			var parts []ChatContentPart
			require.NoError(t, json.Unmarshal(chat.Messages[3].Content, &parts))
			require.Contains(t, parts[0].Text, "call_a")
			require.Equal(t, "AQID", parts[1].InputAudio.Data)
			_, err = ResponsesToAnthropicRequest(&ResponsesRequest{Model: "claude-sonnet-5-5", Input: json.RawMessage(input)})
			require.ErrorContains(t, err, "does not support input_audio")
		})
	}
}

func TestAudioBridgeRejectsUnsupportedInputs(t *testing.T) {
	for _, fields := range []string{`"audio_url":"https://example.test/audio.wav"`, `"file_id":"file-audio"`, `"audio_url":"data:audio/ogg;base64,AQID"`, `"audio_url":"data:audio/wav;base64,"`, `"audio_url":"data:audio/wav;base64,A!ID"`} {
		for _, tool := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/tool=%t", fields, tool), func(t *testing.T) {
				part := `{"type":"input_audio",` + fields + `}`
				input := `[{"role":"user","content":[` + part + `]}]`
				if tool {
					input = `[{"type":"function_call","call_id":"c","name":"audio","arguments":"{}"},{"type":"function_call_output","call_id":"c","output":[` + part + `]}]`
				}
				req := &ResponsesRequest{Model: "test", Input: json.RawMessage(input)}
				_, err := ResponsesToChatCompletionsRequest(req)
				require.ErrorContains(t, err, "audio")
				_, err = ResponsesToAnthropicRequest(req)
				require.ErrorContains(t, err, "does not support input_audio")
			})
		}
	}
	for _, role := range []string{"system", "developer", "assistant", "user"} {
		req := &ResponsesRequest{Model: "test", Input: json.RawMessage(`[{"role":"` + role + `","content":[{"type":"input_audio","audio_url":"data:audio/wav;base64,AQID"}]}]`)}
		_, err := ResponsesToAnthropicRequest(req)
		require.ErrorContains(t, err, "does not support input_audio")
	}
}

func TestNativeResponsesAudioNotLifted(t *testing.T) {
	var input any
	raw := `[{"type":"function_call_output","call_id":"a","output":[{"type":"input_audio","audio_url":"https://example.test/audio.wav","file_id":"audio-file"}]}]`
	require.NoError(t, json.Unmarshal([]byte(raw), &input))
	out, changed := LiftResponsesToolOutputMedia(input)
	require.False(t, changed)
	encoded, err := json.Marshal(out)
	require.NoError(t, err)
	require.JSONEq(t, raw, string(encoded))
}

func TestAnthropicAudioValidationDoesNotInterpretText(t *testing.T) {
	text := `{"type":"input_audio","audio_url":"data:audio/wav;base64,AQID"}`
	content, err := json.Marshal(text)
	require.NoError(t, err)
	out, err := convertResponsesUserToAnthropicContent(content)
	require.NoError(t, err)
	require.JSONEq(t, string(content), string(out))
	_, err = convertResponsesUserToAnthropicContent(json.RawMessage(`[{"type":"input\u005faudio","audio_url":"data:audio/wav;base64,AQID"}]`))
	require.ErrorContains(t, err, "does not support input_audio")
}

func BenchmarkResponsesInlineAudioPayload(b *testing.B) {
	uri := "data:audio/wav;base64," + strings.Repeat("AQID", 256*1024)
	b.ReportAllocs()
	b.SetBytes(int64(len(uri)))
	for b.Loop() {
		part, err := responsesAudioToChatPart(uri, "")
		if err != nil || len(part.InputAudio.Data) != 1024*1024 {
			b.Fatal(err)
		}
	}
}

func BenchmarkBridgeEffectiveReasoningLargeAudio(b *testing.B) {
	request := &ResponsesRequest{Input: json.RawMessage(`[{"role":"user","content":[{"type":"input_audio","audio_url":"data:audio/wav;base64,` + strings.Repeat("AQID", 256*1024) + `"}]},{"type":"configuration_update","reasoning":{"effort":9007199254740993}}]`)}
	b.ReportAllocs()
	for b.Loop() {
		effort, err := effectiveResponsesReasoning(request)
		if err != nil || effort.Effort != "9007199254740993" {
			b.Fatal(err)
		}
	}
}
