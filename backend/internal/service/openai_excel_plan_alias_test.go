package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestExcelPlanAliasesRetainNativeHistory(t *testing.T) {
	for _, tc := range []struct {
		input, expected string
	}{
		{"pending", "pending"},
		{"not_started", "pending"},
		{"todo", "pending"},
		{"planned", "pending"},
		{"queued", "pending"},
		{"blocked", "pending"},
		{"in_progress", "in_progress"},
		{"active", "in_progress"},
		{"started", "in_progress"},
		{"doing", "in_progress"},
		{"current", "in_progress"},
		{"completed", "completed"},
		{"complete", "completed"},
		{"done", "completed"},
		{"finished", "completed"},
		{" \tNOT STARTED\n", "pending"},
		{" In-Progress ", "in_progress"},
		{" FINISHED\t", "completed"},
	} {
		t.Run(tc.input, func(t *testing.T) {
			history := excelTestHistory()
			_, state := excelTestPrepare(t, excelPlanAliasRequest, history)
			arguments, err := json.Marshal(map[string]any{
				"summary": "Inspect the repository", "plan": []any{
					map[string]any{"description": "Read files", "status": tc.input},
				},
			})
			require.NoError(t, err)
			native := map[string]any{
				"type": "function_call", "id": "native-plan-item", "call_id": "native-plan-call",
				"name": "update_plan", "arguments": string(arguments),
			}
			response := map[string]any{"output": []any{native}}
			require.NoError(t, state.translateResponse(context.Background(), response))
			output, ok := response["output"].([]any)
			require.True(t, ok)
			require.Len(t, output, 1)
			client := openAIExcelMap(output[0])
			require.Equal(t, "native-plan-item", client["id"])
			require.Equal(t, "native-plan-call", client["call_id"])
			expected, err := json.Marshal(map[string]any{
				"explanation": "Inspect the repository", "plan": []any{
					map[string]any{"step": "Read files", "status": tc.expected},
				},
			})
			require.NoError(t, err)
			require.JSONEq(t, string(expected), openAIExcelString(client["arguments"]))
			// Persist and replay the native call exactly; normalized client plan
			// arguments must not overwrite the upstream identity or its payload.
			nativeBytes, err := json.Marshal(native)
			require.NoError(t, err)
			stored, err := history.LoadExcelNativeCall(context.Background(), state.options.HistoryScope, "native-plan-call")
			require.NoError(t, err)
			require.JSONEq(t, string(nativeBytes), string(stored))
			replayed, err := state.translateHistory(context.Background(), []any{
				client, map[string]any{"type": "function_call_output", "call_id": "native-plan-call", "output": "Plan updated"},
			})
			require.NoError(t, err)
			require.Len(t, replayed, 2)
			replayedBytes, err := json.Marshal(replayed[0])
			require.NoError(t, err)
			require.JSONEq(t, string(nativeBytes), string(replayedBytes))
		})
	}
}

func TestExcelPlanAliasUnknownStatusStillRejected(t *testing.T) {
	history := excelTestHistory()
	_, state := excelTestPrepare(t, excelPlanAliasRequest, history)
	response := map[string]any{"output": []any{map[string]any{
		"type": "function_call", "id": "native-item", "call_id": "native-call", "name": "update_plan",
		"arguments": `{"plan":[{"description":"Read files","status":"mysterious"}]}`,
	}}}
	require.Error(t, state.translateResponse(context.Background(), response))
	require.Empty(t, history.values)
}

const excelPlanAliasRequest = `{"model":"gpt-6-astra","input":"inspect","tools":[{"type":"function","name":"update_plan","parameters":{"type":"object","required":["plan"],"additionalProperties":false,"properties":{"explanation":{"type":"string"},"plan":{"type":"array","items":{"type":"object","required":["step","status"],"additionalProperties":false,"properties":{"step":{"type":"string"},"status":{"type":"string","enum":["pending","in_progress","completed"]}}}}}}}]}`
