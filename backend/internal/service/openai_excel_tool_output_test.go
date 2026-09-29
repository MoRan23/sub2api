package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func excelOutputHistoryState(t *testing.T, kind, carrier string) (*OpenAIExcelWireState, map[string]any, map[string]any) {
	t.Helper()
	clientType := "function_call"
	client := map[string]any{"name": "execute", "namespace": "client", "call_id": "output-call", "arguments": `{}`}
	if kind == "custom" {
		clientType = "custom_tool_call"
		delete(client, "arguments")
		client["input"] = "synthetic input"
	}
	client["type"] = clientType
	native := map[string]any{"type": clientType, "name": "execute", "namespace": "client", "call_id": "output-call"}
	if kind == "custom" {
		native["input"] = "synthetic input"
	} else {
		native["arguments"] = `{}`
	}
	if carrier != "" {
		native = map[string]any{"type": "function_call", "name": carrier, "call_id": "output-call", "arguments": `{"code":"synthetic cached native input"}`}
	}
	history := excelTestHistory()
	state := &OpenAIExcelWireState{options: OpenAIExcelWireOptions{History: history, HistoryScope: "output-test-scope"}}
	raw, err := json.Marshal(native)
	require.NoError(t, err)
	require.NoError(t, history.StoreExcelNativeCall(context.Background(), state.options.HistoryScope, "output-call", raw))
	return state, client, native
}

func TestExcelToolOutputPreservesExplicitValues(t *testing.T) {
	for _, kind := range []string{"function", "custom"} {
		for _, carrier := range []string{"", "run_officejs", "functions.run_officejs"} {
			for _, output := range []struct {
				name  string
				value any
			}{
				{"empty_text", ""},
				{"whitespace", " \t\r\n"},
				{"empty_array", []any{}},
				{"image_array", []any{map[string]any{"type": "input_image", "image_url": "data:image/png;base64,c3ludGhldGlj"}}},
			} {
				t.Run(kind+"/"+carrier+"/"+output.name, func(t *testing.T) {
					state, client, native := excelOutputHistoryState(t, kind, carrier)
					outputType := "function_call_output"
					if kind == "custom" {
						outputType = "custom_tool_call_output"
					}
					items, err := state.translateHistory(context.Background(), []any{client, map[string]any{
						"type": outputType, "call_id": "output-call", "name": "execute", "namespace": "client", "output": output.value,
					}})
					require.NoError(t, err)
					require.Len(t, items, 2)
					require.Equal(t, native, items[0])
					result := openAIExcelMap(items[1])
					require.Equal(t, output.value, result["output"])
					require.Equal(t, "output-call", result["call_id"])
					if carrier != "" {
						require.Equal(t, "function_call_output", result["type"])
						require.NotContains(t, result, "name")
						require.NotContains(t, result, "namespace")
					} else {
						require.Equal(t, outputType, result["type"])
						require.Equal(t, "execute", result["name"])
						require.Equal(t, "client", result["namespace"])
					}
				})
			}
		}
	}
}

func TestExcelToolOutputRejectsMissingOrNull(t *testing.T) {
	for _, kind := range []string{"function", "custom"} {
		for _, carrier := range []string{"", "run_officejs"} {
			for _, present := range []bool{false, true} {
				state, client, _ := excelOutputHistoryState(t, kind, carrier)
				outputType := "function_call_output"
				if kind == "custom" {
					outputType = "custom_tool_call_output"
				}
				output := map[string]any{"type": outputType, "call_id": "output-call"}
				if present {
					output["output"] = nil
				}
				items, err := state.translateHistory(context.Background(), []any{client, output})
				require.ErrorContains(t, err, "excel tool output is missing or null")
				require.Nil(t, items)
			}
		}
	}
}

func TestExcelToolOutputPlanRequiresExplicitResultBeforeNormalization(t *testing.T) {
	for _, output := range []struct {
		name    string
		present bool
		value   any
		valid   bool
	}{
		{"empty", true, "", true},
		{"success", true, "Plan updated", true},
		{"missing", false, nil, false},
		{"null", true, nil, false},
	} {
		t.Run(output.name, func(t *testing.T) {
			state := &OpenAIExcelWireState{}
			call := map[string]any{"type": "function_call", "call_id": "plan-call", "name": "update_plan", "arguments": `{"plan":[]}`}
			result := map[string]any{"type": "function_call_output", "call_id": "plan-call"}
			if output.present {
				result["output"] = output.value
			}
			items, err := state.translateHistory(context.Background(), []any{call, result})
			if !output.valid {
				require.ErrorContains(t, err, "excel tool output is missing or null")
				require.Nil(t, items)
				return
			}
			require.NoError(t, err)
			require.JSONEq(t, `{"status":"ok"}`, openAIExcelString(openAIExcelMap(items[1])["output"]))
		})
	}
}

func TestExcelToolOutputUncachedRelayPreservesEmptyResult(t *testing.T) {
	for _, kind := range []string{"function", "custom"} {
		state, client, _ := excelOutputHistoryState(t, kind, "")
		state.options.History = nil
		outputType := "function_call_output"
		if kind == "custom" {
			outputType = "custom_tool_call_output"
		}
		items, err := state.translateHistory(context.Background(), []any{client, map[string]any{
			"type": outputType, "call_id": "output-call", "name": "execute", "namespace": "client", "output": "",
		}})
		require.NoError(t, err)
		require.True(t, openAIExcelCallIsTransport(openAIExcelMap(items[0])))
		result := openAIExcelMap(items[1])
		require.Equal(t, "function_call_output", result["type"])
		require.Equal(t, "", result["output"])
		require.NotContains(t, result, "name")
		require.NotContains(t, result, "namespace")
	}
}
