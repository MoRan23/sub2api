package apicompat

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLiftResponsesToolOutputMedia(t *testing.T) {
	var input any
	require.NoError(t, json.Unmarshal([]byte(`[
		{"type":"function_call","call_id":"call_image","name":"view_image","arguments":"{}"},
		{"type":"function_call_output","call_id":"call_image","output":[{"type":"input_image","image_url":"data:image/png;base64,AQID"}]}
	]`), &input))

	lifted, changed := LiftResponsesToolOutputMedia(input)
	require.True(t, changed)

	items, ok := lifted.([]any)
	require.True(t, ok)
	require.Len(t, items, 3)

	outputItem, ok := items[1].(map[string]any)
	require.True(t, ok)
	outputText, ok := outputItem["output"].(string)
	require.True(t, ok)
	require.Contains(t, outputText, toolOutputMediaMarker)
	require.NotContains(t, outputText, "data:image/png")

	mediaMessage, ok := items[2].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "message", mediaMessage["type"])
	require.Equal(t, "user", mediaMessage["role"])

	parts, ok := mediaMessage["content"].([]map[string]any)
	require.True(t, ok)
	require.Len(t, parts, 2)
	require.Equal(t, "input_text", parts[0]["type"])
	require.Equal(t, "[Tool output media for call call_image]", parts[0]["text"])
	require.Equal(t, "input_image", parts[1]["type"])
	require.Equal(t, "data:image/png;base64,AQID", parts[1]["image_url"])
}

func TestLiftResponsesToolOutputMediaLeavesPlainOutputUntouched(t *testing.T) {
	input := []any{
		map[string]any{
			"type":    "function_call_output",
			"call_id": "call_text",
			"output":  "plain output",
		},
	}

	lifted, changed := LiftResponsesToolOutputMedia(input)
	require.False(t, changed)
	require.Equal(t, input, lifted)
}

func TestLiftResponsesToolOutputMediaKeepsParallelBatchContiguous(t *testing.T) {
	var input any
	require.NoError(t, json.Unmarshal([]byte(`[
		{"type":"function_call","call_id":"call_A","name":"view_image","arguments":"{}"},
		{"type":"function_call","call_id":"call_B","name":"view_image","arguments":"{}"},
		{"type":"function_call","call_id":"call_C","name":"view_image","arguments":"{}"},
		{"type":"function_call_output","call_id":"call_A","output":[{"type":"input_image","image_url":"data:image/png;base64,QQ=="}]},
		{"type":"message","role":"developer","content":[{"type":"input_text","text":"resized A"}]},
		{"type":"function_call_output","call_id":"call_B","output":[{"type":"input_image","image_url":"data:image/png;base64,Qg=="}]},
		{"type":"message","role":"developer","content":[{"type":"input_text","text":"resized B"}]},
		{"type":"function_call_output","call_id":"call_C","output":[{"type":"input_image","image_url":"data:image/png;base64,Qw=="}]},
		{"type":"message","role":"developer","content":[{"type":"input_text","text":"resized C"}]}
	]`), &input))

	lifted, changed := LiftResponsesToolOutputMedia(input)
	require.True(t, changed)

	items, ok := lifted.([]any)
	require.True(t, ok)
	require.Len(t, items, 10)

	for index, callID := range []string{"call_A", "call_B", "call_C"} {
		item, ok := items[index+3].(map[string]any)
		require.True(t, ok)
		require.Equal(t, "function_call_output", item["type"])
		require.Equal(t, callID, item["call_id"])
		outputText, ok := item["output"].(string)
		require.True(t, ok)
		require.Contains(t, outputText, toolOutputMediaMarker)
		require.NotContains(t, outputText, "data:image/png")
	}

	mediaMessage, ok := items[6].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "message", mediaMessage["type"])
	require.Equal(t, "user", mediaMessage["role"])
	parts, ok := mediaMessage["content"].([]map[string]any)
	require.True(t, ok)
	require.Len(t, parts, 6)
	require.Equal(t, "[Tool output media for call call_A]", parts[0]["text"])
	require.Equal(t, "data:image/png;base64,QQ==", parts[1]["image_url"])
	require.Equal(t, "[Tool output media for call call_B]", parts[2]["text"])
	require.Equal(t, "data:image/png;base64,Qg==", parts[3]["image_url"])
	require.Equal(t, "[Tool output media for call call_C]", parts[4]["text"])
	require.Equal(t, "data:image/png;base64,Qw==", parts[5]["image_url"])

	for index, text := range []string{"resized A", "resized B", "resized C"} {
		notice, ok := items[index+7].(map[string]any)
		require.True(t, ok)
		require.Equal(t, "developer", notice["role"])
		content, ok := notice["content"].([]any)
		require.True(t, ok)
		require.Len(t, content, 1)
		contentPart, ok := content[0].(map[string]any)
		require.True(t, ok)
		require.Equal(t, text, contentPart["text"])
	}
}

func TestLiftResponsesToolOutputMediaPreservesMixedBatchContent(t *testing.T) {
	var input any
	decoder := json.NewDecoder(strings.NewReader(`[
		{"type":"custom_tool_call_output","call_id":"call_image","status":"completed","output":{"content":[{"type":"input_text","text":"keep this result"},{"type":"image_url","image_url":{"url":"https://example.test/image.png"}}],"opaque":9007199254740993}},
		{"type":"message","role":"system","content":"keep this instruction"},
		{"type":"function_call_output","call_id":"call_text","output":{"opaque":9007199254740993,"image_url":"not an image node"}}
	]`))
	decoder.UseNumber()
	require.NoError(t, decoder.Decode(&input))

	lifted, changed := LiftResponsesToolOutputMedia(input)
	require.True(t, changed)
	items, ok := lifted.([]any)
	require.True(t, ok)
	require.Len(t, items, 4)
	imageOutput, ok := items[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "completed", imageOutput["status"])
	outputJSON, ok := imageOutput["output"].(string)
	require.True(t, ok)
	var output map[string]json.RawMessage
	require.NoError(t, json.Unmarshal([]byte(outputJSON), &output))
	require.Equal(t, "9007199254740993", string(output["opaque"]))
	require.JSONEq(t, `[{"type":"input_text","text":"keep this result"},{"type":"input_text","text":"[Tool output media moved to the following user message]"}]`, string(output["content"]))
	textItem, ok := items[1].(map[string]any)
	require.True(t, ok)
	textOutput, ok := textItem["output"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, json.Number("9007199254740993"), textOutput["opaque"])
	require.Equal(t, "not an image node", textOutput["image_url"])
	mediaMessage, ok := items[2].(map[string]any)
	require.True(t, ok)
	parts, ok := mediaMessage["content"].([]map[string]any)
	require.True(t, ok)
	require.Len(t, parts, 2)
	require.Equal(t, "https://example.test/image.png", parts[1]["image_url"])
	notice, ok := items[3].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "keep this instruction", notice["content"])

	again, changed := LiftResponsesToolOutputMedia(lifted)
	require.False(t, changed, "lifted media must not be emitted twice on replay")
	require.Equal(t, lifted, again)
}

func TestLiftResponsesToolOutputMediaRespectsConversationBoundaries(t *testing.T) {
	var input any
	require.NoError(t, json.Unmarshal([]byte(`[
		{"type":"function_call_output","call_id":"call_A","output":"data:image/png;base64,QQ=="},
		{"type":"message","role":"developer","content":"notice A"},
		{"type":"message","role":"user","content":"next turn"},
		{"type":"tool_search_output","call_id":"call_B","output":[{"type":"input_image","image_url":"data:image/png;base64,Qg=="}]},
		{"type":"message","role":"system","content":"notice B"}
	]`), &input))

	lifted, changed := LiftResponsesToolOutputMedia(input)
	require.True(t, changed)
	items, ok := lifted.([]any)
	require.True(t, ok)
	require.Len(t, items, 7)
	messages := make([]map[string]any, len(items))
	for i, item := range items {
		message, ok := item.(map[string]any)
		require.True(t, ok)
		messages[i] = message
	}
	require.Equal(t, "call_A", messages[0]["call_id"])
	require.Equal(t, "user", messages[1]["role"])
	require.Equal(t, "notice A", messages[2]["content"])
	require.Equal(t, "next turn", messages[3]["content"])
	require.Equal(t, "call_B", messages[4]["call_id"])
	require.Equal(t, "user", messages[5]["role"])
	require.Equal(t, "notice B", messages[6]["content"])
}

func TestLiftResponsesToolOutputMediaLeavesMediaFreeInstructionOrderUntouched(t *testing.T) {
	var input any
	require.NoError(t, json.Unmarshal([]byte(`[
		{"type":"function_call_output","call_id":"call_A","output":"plain A"},
		{"type":"message","role":"developer","content":"notice"},
		{"type":"function_call_output","call_id":"call_B","output":"plain B"}
	]`), &input))
	original, err := json.Marshal(input)
	require.NoError(t, err)
	lifted, changed := LiftResponsesToolOutputMedia(input)
	require.False(t, changed)
	actual, err := json.Marshal(lifted)
	require.NoError(t, err)
	require.Equal(t, string(original), string(actual))
}
