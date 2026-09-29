package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestExcelToolRelayPromptPreservesInstructionsAndStablePrefix(t *testing.T) {
	instruction := "User-owned instructions: keep this exact text.\nDo not rewrite it."
	tools := []any{map[string]any{
		"type": "namespace", "name": "functions", "tools": []any{
			map[string]any{"type": "function", "name": "exec", "parameters": map[string]any{"type": "object"}},
		},
	}}
	prepare := func(history []any) ([]any, *OpenAIExcelWireState) {
		t.Helper()
		body, err := json.Marshal(map[string]any{
			"model": "gpt-6-astra", "instructions": instruction, "tools": tools, "input": history,
		})
		require.NoError(t, err)
		wire, state := excelTestPrepare(t, string(body), excelTestHistory())
		items, ok := wire["input"].([]any)
		require.True(t, ok)
		return items, state
	}
	first := openAIExcelMessage("user", "Inspect the repository")
	initial, state := prepare([]any{first})
	followup, _ := prepare([]any{first, openAIExcelMessage("assistant", "Checking the files"), openAIExcelMessage("user", "Continue")})
	require.Len(t, initial, 4)
	require.Equal(t, initial[:3], followup[:3], "catalog and reminder must precede the growing conversation")
	require.Equal(t, first, initial[3])
	require.Equal(t, instruction, openAIExcelPartsText(openAIExcelMap(initial[0])["content"]))
	protocol := openAIExcelPartsText(openAIExcelMap(initial[1])["content"])
	reminder := openAIExcelPartsText(openAIExcelMap(initial[2])["content"])
	require.Contains(t, protocol, "make the actual tool call in the same response")
	require.Contains(t, protocol, "do not remove functions. when it is part of a catalog name")
	require.Contains(t, protocol, `"name":"functions.exec"`)
	require.Contains(t, reminder, "Client tools: functions.exec.")
	require.Contains(t, reminder, "A request that needs no tool may be answered directly")
	// Tool availability alone must not make an ordinary final answer invalid.
	require.NoError(t, state.translateResponse(context.Background(), map[string]any{
		"output": []any{openAIExcelMessage("assistant", "The answer needs no tools.")},
	}))
}

func TestExcelToolRelayNoToolsAndNoneDoNotRequireTools(t *testing.T) {
	for _, body := range []string{
		`{"model":"gpt-6-astra","instructions":"Original","input":"hello"}`,
		`{"model":"gpt-6-astra","instructions":"Original","input":"hello","tool_choice":"none","tools":[{"type":"function","name":"exec"}]}`,
	} {
		wire, state := excelTestPrepare(t, body, nil)
		items, ok := wire["input"].([]any)
		require.True(t, ok)
		require.Len(t, items, 3, "no tool reminder should be emitted")
		require.Empty(t, state.tools)
		require.Empty(t, state.toolProtocolReminder())
		require.Contains(t, openAIExcelPartsText(openAIExcelMap(items[1])["content"]), "Return the answer as assistant text")
		require.Contains(t, openAIExcelPartsText(openAIExcelMap(items[1])["content"]), "mcp__fastctx.inspect_local_file")
		require.Equal(t, "none", openAIExcelString(wire["tool_choice"]))
		require.NoError(t, state.translateResponse(context.Background(), map[string]any{
			"output": []any{openAIExcelMessage("assistant", "hello")},
		}))
	}
}

func TestExcelToolRelayReminderKeepsPlanAndCustomToolGuidance(t *testing.T) {
	_, state := excelTestPrepare(t, `{"model":"gpt-6-astra","input":"work","tools":[{"type":"function","name":"update_plan"},{"type":"custom","name":"apply_patch","format":{"type":"text"}}]}`, excelTestHistory())
	reminder := state.toolProtocolReminder()
	require.Contains(t, reminder, "after its result, take the next substantive action through run_officejs")
	require.Contains(t, reminder, `{"name":"TOOL_NAME","input":"RAW_INPUT"}`)
	require.Contains(t, reminder, "never use arguments.patch")
	require.Contains(t, reminder, "Client tools: apply_patch, update_plan.")
}

func TestExcelToolRelayMissingCallIDDoesNotStoreOrInventIdentity(t *testing.T) {
	history := excelTestHistory()
	_, state := excelTestPrepare(t, `{"model":"gpt-6-astra","input":"inspect","tools":[{"type":"function","name":"exec","parameters":{"type":"object"}}]}`, history)
	native := map[string]any{
		"type": "function_call", "id": "native-item-id", "name": "run_officejs",
		"arguments": map[string]any{"code": map[string]any{"name": "exec", "arguments": map[string]any{"cmd": "pwd"}}},
	}
	client, err := state.translateNativeCall(context.Background(), native)
	require.Nil(t, client)
	require.Equal(t, "call_id_missing", openAIExcelToolFailureReason(err))
	require.NotContains(t, native, "call_id")
	require.Empty(t, history.values)
}

func TestExcelToolRelayObjectArgumentsRetainRealIDsAndNamespace(t *testing.T) {
	history := excelTestHistory()
	_, state := excelTestPrepare(t, `{"model":"gpt-6-astra","input":"inspect","tools":[{"type":"namespace","name":"functions","tools":[{"type":"function","name":"exec","parameters":{"type":"object","required":["cmd"],"properties":{"cmd":{"type":"string"}}}}]}]}`, history)
	native := map[string]any{
		"type": "function_call", "id": "native-real-item", "call_id": "native-real-call", "name": "functions.run_officejs",
		"arguments": map[string]any{"code": map[string]any{"name": "functions.exec", "arguments": map[string]any{"cmd": "pwd"}}},
	}
	client, err := state.translateNativeCall(context.Background(), native)
	require.NoError(t, err)
	require.Equal(t, "native-real-item", client["id"])
	require.Equal(t, "native-real-call", client["call_id"])
	require.Equal(t, "exec", client["name"])
	require.Equal(t, "functions", client["namespace"])
	require.JSONEq(t, `{"cmd":"pwd"}`, openAIExcelString(client["arguments"]))
	expected, err := json.Marshal(native)
	require.NoError(t, err)
	stored, err := history.LoadExcelNativeCall(context.Background(), state.options.HistoryScope, "native-real-call")
	require.NoError(t, err)
	require.JSONEq(t, string(expected), string(stored))
	items, err := state.translateHistory(context.Background(), []any{
		client, map[string]any{"type": "function_call_output", "call_id": "native-real-call", "output": "workspace"},
	})
	require.NoError(t, err)
	replayed, err := json.Marshal(items[0])
	require.NoError(t, err)
	require.JSONEq(t, string(expected), string(replayed))
}
