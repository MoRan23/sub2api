package apicompat

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestCodex0172StandaloneOutputClassification(t *testing.T) {
	for _, tc := range []struct {
		name       string
		fields     string
		standalone bool
	}{
		{"omitted ID", `"name":"proposed_plan","namespace":"agent"`, true},
		{"null ID", `"name":"thread_hint","namespace":"agent","call_id":null`, true},
		{"omitted namespace", `"name":"context"`, true},
		{"null namespace", `"name":"context","namespace":null`, true},
		{"empty namespace", `"name":"context","namespace":""`, true},
		{"context cannot elevate role", `"name":"context","role":"developer"`, true},
		{"escaped provenance", `"name":"a\n\"b","namespace":"ns]\\x"`, true},
		{"empty ID", `"name":"context","call_id":""`, false},
		{"malformed ID", `"name":"context","call_id":42`, false},
		{"blank name", `"name":"  "`, false},
		{"missing name", `"namespace":"agent"`, false},
		{"invalid namespace", `"name":"context","namespace":42`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := &ResponsesRequest{Model: "test", Input: json.RawMessage(`[{"type":"function_call_output",` + tc.fields + `,"output":"line one\nline two"}]`)}
			chat, err := ResponsesToChatCompletionsRequest(req)
			require.NoError(t, err)
			require.Len(t, chat.Messages, 1)
			if !tc.standalone {
				require.Equal(t, "tool", chat.Messages[0].Role)
				anthropic, err := ResponsesToAnthropicRequest(req)
				if err == nil {
					require.Empty(t, anthropic.Messages)
				}
				return
			}
			require.Equal(t, "user", chat.Messages[0].Role)
			require.Empty(t, chat.Messages[0].ToolCallID)
			require.Equal(t, "line one\nline two", gjson.GetBytes(chat.Messages[0].Content, "1.text").String())
			label := gjson.GetBytes(chat.Messages[0].Content, "0.text").String()
			provenance := strings.TrimSuffix(strings.TrimPrefix(label, "[Context from "), "]\n")
			require.True(t, json.Valid([]byte(provenance)))
			require.Equal(t, gjson.GetBytes(req.Input, "0.name").String(), gjson.Get(provenance, "name").String())
			require.Equal(t, gjson.GetBytes(req.Input, "0.namespace").Raw, gjson.Get(provenance, "namespace").Raw)
			anthropic, err := ResponsesToAnthropicRequest(req)
			require.NoError(t, err)
			require.Len(t, anthropic.Messages, 1)
			require.Equal(t, "user", anthropic.Messages[0].Role)
			require.JSONEq(t, string(chat.Messages[0].Content), string(anthropic.Messages[0].Content))
		})
	}
}

func TestCodex0172StandaloneOutputShape(t *testing.T) {
	for _, output := range []string{`""`, `[]`, `[{"type":"input_text","text":""}]`} {
		req := &ResponsesRequest{Model: "test", Input: json.RawMessage(`[{"type":"function_call_output","name":"context","output":` + output + `}]`)}
		chat, err := ResponsesToChatCompletionsRequest(req)
		require.NoError(t, err)
		require.Equal(t, "user", chat.Messages[0].Role)
		require.Len(t, gjson.ParseBytes(chat.Messages[0].Content).Array(), 1)
		anthropic, err := ResponsesToAnthropicRequest(req)
		require.NoError(t, err)
		require.JSONEq(t, string(chat.Messages[0].Content), string(anthropic.Messages[0].Content))
	}
	for _, output := range []string{``, `,"output":null`, `,"output":{"text":"context"}`} {
		req := &ResponsesRequest{Model: "test", Input: json.RawMessage(`[{"type":"function_call_output","name":"context"` + output + `}]`)}
		chat, err := ResponsesToChatCompletionsRequest(req)
		require.NoError(t, err)
		require.Equal(t, "tool", chat.Messages[0].Role)
		anthropic, err := ResponsesToAnthropicRequest(req)
		require.NoError(t, err)
		require.Empty(t, anthropic.Messages)
	}
}

func TestCodex0172StandaloneOutputKeepsRealToolPairing(t *testing.T) {
	req := &ResponsesRequest{Model: "test", Input: json.RawMessage(`[
		{"type":"function_call","call_id":"call_real","name":"lookup","arguments":"{}"},
		{"type":"function_call_output","namespace":"agent","name":"proposed_plan","output":"plan"},
		{"type":"function_call_output","call_id":"call_real","output":"result"},
		{"type":"function_call_output","namespace":"agent","name":"thread_hint","output":"hint"},
		{"type":"message","role":"user","content":[{"type":"input_text","text":"continue"}]}
	]`)}
	chat, paths, err := ResponsesToChatCompletionsRequestWithPathMapping(req, nil)
	require.NoError(t, err)
	require.Len(t, chat.Messages, 5)
	require.Equal(t, "assistant", chat.Messages[0].Role)
	require.Equal(t, "call_real", chat.Messages[0].ToolCalls[0].ID)
	require.Equal(t, "tool", chat.Messages[1].Role)
	require.Equal(t, "call_real", chat.Messages[1].ToolCallID)
	require.Equal(t, `"result"`, string(chat.Messages[1].Content))
	require.Equal(t, "plan", gjson.GetBytes(chat.Messages[2].Content, "1.text").String())
	require.Equal(t, "hint", gjson.GetBytes(chat.Messages[3].Content, "1.text").String())
	require.Equal(t, "messages.2.content.1.text", paths["input.1.output"])
	require.Equal(t, "messages.3.content.1.text", paths["input.3.output"])
	require.Equal(t, "messages.4.content", paths["input.4.content.0.text"])

	anthropic, paths, err := ResponsesToAnthropicRequestWithPathMapping(req)
	require.NoError(t, err)
	require.Len(t, anthropic.Messages, 2)
	require.Equal(t, "tool_use", gjson.GetBytes(anthropic.Messages[0].Content, "0.type").String())
	require.Equal(t, "tool_result", gjson.GetBytes(anthropic.Messages[1].Content, "0.type").String())
	require.Equal(t, "call_real", gjson.GetBytes(anthropic.Messages[1].Content, "0.tool_use_id").String())
	require.Equal(t, "plan", gjson.GetBytes(anthropic.Messages[1].Content, "2.text").String())
	require.Equal(t, "hint", gjson.GetBytes(anthropic.Messages[1].Content, "4.text").String())
	require.Equal(t, "continue", gjson.GetBytes(anthropic.Messages[1].Content, "5.text").String())
	require.Equal(t, "messages.1.content.2.text", paths["input.1.output"])
	require.Equal(t, "messages.1.content.4.text", paths["input.3.output"])
	require.Equal(t, "messages.1.content.5.text", paths["input.4.content.0.text"])
}

func TestCodex0172StandaloneOutputPreservesOrderedMedia(t *testing.T) {
	for _, imagePart := range []string{"", `,{"type":"input_image","image_url":"data:image/png;base64,AQID"}`} {
		req := &ResponsesRequest{Model: "test", Input: json.RawMessage(`[{"type":"function_call_output","namespace":"codex_app","name":"send_message_to_thread","output":[{"type":"input_text","text":"before"}` + imagePart + `,{"type":"output_text","text":"after"}]}]`)}
		chat, paths, err := ResponsesToChatCompletionsRequestWithPathMapping(req, nil)
		require.NoError(t, err)
		last := 2
		if imagePart != "" {
			last = 3
			require.Equal(t, "data:image/png;base64,AQID", gjson.GetBytes(chat.Messages[0].Content, "2.image_url.url").String())
		}
		require.Len(t, gjson.ParseBytes(chat.Messages[0].Content).Array(), last+1)
		require.Equal(t, "messages.0.content.1.text", paths["input.0.output.0.text"])
		encoded, err := json.Marshal(chat)
		require.NoError(t, err)
		for source, target := range paths {
			if strings.HasPrefix(source, "input.0.output.") {
				require.Equal(t, gjson.GetBytes(req.Input, strings.TrimPrefix(source, "input.")).String(), gjson.GetBytes(encoded, target).String())
			}
		}
		anthropic, paths, err := ResponsesToAnthropicRequestWithPathMapping(req)
		require.NoError(t, err)
		require.Len(t, gjson.ParseBytes(anthropic.Messages[0].Content).Array(), last+1)
		require.Equal(t, "messages.0.content.1.text", paths["input.0.output.0.text"])
		encoded, err = json.Marshal(anthropic)
		require.NoError(t, err)
		for source, target := range paths {
			if strings.HasPrefix(source, "input.0.output.") {
				require.Equal(t, gjson.GetBytes(req.Input, strings.TrimPrefix(source, "input.")).String(), gjson.GetBytes(encoded, target).String())
			}
		}
	}
}

func TestCodex0172StandaloneAudio(t *testing.T) {
	req := &ResponsesRequest{Model: "test", Input: json.RawMessage(`[{"type":"function_call_output","name":"context","output":[{"type":"input_text","text":"before"},{"type":"input_audio","audio_url":"data:audio/wav;base64,AQID"},{"type":"input_text","text":"after"}]}]`)}
	chat, paths, err := ResponsesToChatCompletionsRequestWithPathMapping(req, nil)
	require.NoError(t, err)
	require.Equal(t, "input_audio", gjson.GetBytes(chat.Messages[0].Content, "2.type").String())
	require.Equal(t, "AQID", gjson.GetBytes(chat.Messages[0].Content, "2.input_audio.data").String())
	require.Equal(t, "messages.0.content.3.text", paths["input.0.output.2.text"])
	_, err = ResponsesToAnthropicRequest(req)
	require.ErrorContains(t, err, "does not support input_audio")
}

func TestCodex0172FileImageRejectedByBridges(t *testing.T) {
	for _, input := range []string{
		`[{"type":"message","role":"user","content":[{"type":"input_text","text":"describe"},{"type":"input_image","file_id":"file_image"}]}]`,
		`[{"type":"message","role":"system","content":[{"type":"input_text","text":"describe"},{"type":"input_image","file_id":"file_image"}]}]`,
		`[{"type":"message","role":"developer","content":[{"type":"input_text","text":"describe"},{"type":"input_image","file_id":"file_image"}]}]`,
		`[{"type":"message","role":"assistant","content":[{"type":"input_image","file_id":"file_image"}]}]`,
		`[{"type":"function_call","call_id":"call_image","name":"image","arguments":"{}"},{"type":"function_call_output","call_id":"call_image","output":[{"type":"input_image","file_id":"file_image"}]}]`,
		`[{"type":"function_call_output","namespace":"agent","name":"context","output":[{"type":"input_image","file_id":"file_image"}]}]`,
	} {
		req := &ResponsesRequest{Model: "test", Input: json.RawMessage(input)}
		_, err := ResponsesToChatCompletionsRequest(req)
		require.ErrorContains(t, err, "input_image file_id")
		_, err = ResponsesToAnthropicRequest(req)
		require.ErrorContains(t, err, "input_image file_id")
	}
	req := &ResponsesRequest{Model: "test", Input: json.RawMessage(`[{"role":"user","content":[{"type":"input_image","image_url":"data:image/png;base64,AQID","file_id":"file_image"}]}]`)}
	_, err := ResponsesToChatCompletionsRequest(req)
	require.NoError(t, err)
	_, err = ResponsesToAnthropicRequest(req)
	require.NoError(t, err)
}

func TestCodex0172StandaloneUnsupportedPartsRejected(t *testing.T) {
	for _, part := range []string{
		`{"type":"input_text"}`,
		`{"type":"output_text","text":null}`,
		`{"type":"text","text":null}`,
		`{"type":"input_text","text":42}`,
		`{"type":"encrypted_content","encrypted_content":"opaque"}`,
		`{"type":"input_file","file_id":"file_doc"}`,
		`{"type":"new_content","text":"must survive"}`,
		`42`,
	} {
		req := &ResponsesRequest{Model: "test", Input: json.RawMessage(`[{"type":"function_call_output","name":"context","output":[` + part + `]}]`)}
		_, err := ResponsesToChatCompletionsRequest(req)
		require.Error(t, err, part)
		_, err = ResponsesToAnthropicRequest(req)
		require.Error(t, err, part)
	}
	req := &ResponsesRequest{Model: "test", Input: json.RawMessage(`[{"type":"function_call_output","name":"context","output":[{"type":"input_image","image_url":"https://example.test/image.png"}]}]`)}
	_, err := ResponsesToAnthropicRequest(req)
	require.ErrorContains(t, err, "Base64 data URI")
}

func BenchmarkCodex0172StandaloneBridge(b *testing.B) {
	for _, content := range []struct{ name, output string }{
		{"text64KiB", `"` + strings.Repeat("x", 64*1024) + `"`},
		{"image256KiB", `[{"type":"input_text","text":"describe"},{"type":"input_image","image_url":"data:image/png;base64,` + strings.Repeat("AQID", 64*1024) + `"}]`},
	} {
		req := &ResponsesRequest{Model: "test", Input: json.RawMessage(`[{"type":"function_call_output","namespace":"agent","name":"context","output":` + content.output + `}]`)}
		b.Run(content.name+"/Chat", func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if _, err := ResponsesToChatCompletionsRequest(req); err != nil {
					b.Fatal(err)
				}
			}
		})
		b.Run(content.name+"/Messages", func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if _, err := ResponsesToAnthropicRequest(req); err != nil {
					b.Fatal(err)
				}
			}
		})
		b.Run(content.name+"/MessagesWithPathMapping", func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if _, _, err := ResponsesToAnthropicRequestWithPathMapping(req); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
