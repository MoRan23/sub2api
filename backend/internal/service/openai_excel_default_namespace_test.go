package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestExcelDefaultNamespaceFunctionAndCustomRoundTrip(t *testing.T) {
	for _, kind := range []string{"function", "custom"} {
		for _, declaredNamespace := range []string{"", "functions"} {
			for _, callNamespace := range []string{"", "functions"} {
				for _, relay := range []bool{false, true} {
					label := kind + "/declared=" + declaredNamespace + "/call=" + callNamespace + map[bool]string{false: "/direct", true: "/relay"}[relay]
					t.Run(label, func(t *testing.T) {
						tools := []any{excelCompatTool(kind, "execute")}
						if declaredNamespace != "" {
							tools = []any{map[string]any{"type": "namespace", "name": declaredNamespace, "tools": tools}}
						}
						state, backend := excelCompatState(t, tools)
						call := map[string]any{"name": "execute", "namespace": callNamespace}
						if kind == "function" {
							call["arguments"] = map[string]any{"text": "synthetic"}
						} else {
							call["input"] = "synthetic custom input"
						}
						native := call
						nativeType, outputType := "function_call", "function_call_output"
						if kind == "custom" {
							nativeType, outputType = "custom_tool_call", "custom_tool_call_output"
						}
						if relay {
							native = map[string]any{"name": "run_officejs", "namespace": "functions", "arguments": map[string]any{"code": call}}
							nativeType, outputType = "function_call", "function_call_output"
						}
						native["type"], native["id"], native["call_id"] = nativeType, "native-item", "native-call"
						client, err := state.translateNativeCall(context.Background(), native)
						require.NoError(t, err)
						require.Equal(t, "execute", client["name"])
						require.Equal(t, declaredNamespace, openAIExcelString(client["namespace"]))
						excelCompatAssertNativeHistory(t, state, backend, native, client, outputType)
					})
				}
			}
		}
	}
}

func TestExcelDefaultNamespaceAmbiguousDeclarationsRejected(t *testing.T) {
	for _, functionFirst := range []bool{false, true} {
		for _, callNamespace := range []string{"", "functions"} {
			for _, relay := range []bool{false, true} {
				t.Run(map[bool]string{false: "custom-top", true: "function-top"}[functionFirst]+"/ns="+callNamespace+map[bool]string{false: "/direct", true: "/relay"}[relay], func(t *testing.T) {
					plainKind, namespacedKind := "custom", "function"
					if functionFirst {
						plainKind, namespacedKind = namespacedKind, plainKind
					}
					state, backend := excelCompatState(t, []any{
						excelCompatTool(plainKind, "execute"),
						map[string]any{"type": "namespace", "name": "functions", "tools": []any{excelCompatTool(namespacedKind, "execute")}},
					})
					native := map[string]any{"name": "execute", "namespace": callNamespace, "arguments": map[string]any{"text": "synthetic"}, "input": "synthetic"}
					if relay {
						native = map[string]any{"name": "run_officejs", "arguments": map[string]any{"code": native}}
					}
					native["type"], native["id"], native["call_id"] = "function_call", "native-item", "native-call"
					client, err := state.translateNativeCall(context.Background(), native)
					require.Nil(t, client)
					require.Error(t, err)
					require.Empty(t, backend.values)
				})
			}
		}
	}
}

func TestExcelDefaultNamespaceExactLiteralNamePrecedesLegacyAlias(t *testing.T) {
	state, _ := excelCompatState(t, []any{excelCompatTool("custom", "codex_client__execute"), excelCompatTool("function", "execute")})
	for _, namespace := range []string{"", "functions"} {
		tool, ok := state.resolveClientTool("codex_client__execute", namespace)
		require.True(t, ok)
		require.Equal(t, "custom", tool.Kind)
		require.Equal(t, "codex_client__execute", tool.Name)
	}
}

func TestExcelDefaultNamespaceLiteralLegacyPlanNameIsNotNativePlan(t *testing.T) {
	for _, declaredNamespace := range []string{"", "functions"} {
		for _, callNamespace := range []string{"", "functions"} {
			for _, relay := range []bool{false, true} {
				t.Run("declared="+declaredNamespace+"/call="+callNamespace+map[bool]string{false: "/direct", true: "/relay"}[relay], func(t *testing.T) {
					literal := excelCompatTool("function", "codex_client__update_plan")
					tools := []any{literal}
					if declaredNamespace != "" {
						tools = []any{map[string]any{"type": "namespace", "name": declaredNamespace, "tools": tools}}
					}
					tools = append(tools, excelCompatTool("function", "update_plan"))
					state, backend := excelCompatState(t, tools)
					native := map[string]any{"name": "codex_client__update_plan", "namespace": callNamespace, "arguments": map[string]any{"text": "literal"}}
					if relay {
						native = map[string]any{"name": "run_officejs", "arguments": map[string]any{"code": native}}
					}
					native["type"], native["id"], native["call_id"] = "function_call", "literal-plan-item", "literal-plan-call"
					client, err := state.translateNativeCall(context.Background(), native)
					require.NoError(t, err)
					require.Equal(t, "codex_client__update_plan", openAIExcelString(client["name"]))
					require.JSONEq(t, `{"text":"literal"}`, openAIExcelString(client["arguments"]))
					excelCompatAssertNativeHistory(t, state, backend, native, client, "function_call_output")
				})
			}
		}
	}
}

func TestExcelDefaultNamespacePlanNormalizationAndResult(t *testing.T) {
	for _, declaredNamespace := range []string{"", "functions"} {
		for _, callNamespace := range []string{"", "functions"} {
			nativeNames := []string{"update_plan", "codex_client__update_plan"}
			if declaredNamespace == "functions" {
				nativeNames = append(nativeNames, "functions.update_plan")
				if callNamespace == "" {
					nativeNames = append(nativeNames, "codex_client__functions.update_plan")
				}
			}
			for _, nativeName := range nativeNames {
				t.Run("declared="+declaredNamespace+"/native="+callNamespace+"/name="+nativeName, func(t *testing.T) {
					tool := map[string]any{
						"type": "function", "name": "update_plan", "parameters": map[string]any{
							"type": "object", "required": []any{"plan"}, "additionalProperties": false,
							"properties": map[string]any{
								"plan": map[string]any{"type": "array", "items": map[string]any{
									"type": "object", "required": []any{"step", "status"}, "additionalProperties": false,
									"properties": map[string]any{"step": map[string]any{"type": "string"}, "status": map[string]any{"enum": []any{"pending", "in_progress", "completed"}}},
								}},
							},
						},
					}
					tools := []any{tool}
					if declaredNamespace != "" {
						tools = []any{map[string]any{"type": "namespace", "name": declaredNamespace, "tools": tools}}
					}
					state, backend := excelCompatState(t, tools)
					native := map[string]any{
						"type": "function_call", "id": "native-plan-item", "call_id": "native-plan-call", "name": nativeName, "namespace": callNamespace,
						"arguments": `{"plan":[{"description":"inspect","status":"active"}]}`,
					}
					client, err := state.translateNativeCall(context.Background(), native)
					require.NoError(t, err)
					require.Equal(t, "update_plan", openAIExcelString(client["name"]))
					require.Equal(t, declaredNamespace, openAIExcelString(client["namespace"]))
					require.JSONEq(t, `{"plan":[{"step":"inspect","status":"in_progress"}]}`, openAIExcelString(client["arguments"]))
					state.options.History = NewOpenAIExcelStateStore(backend, excelTestCipher{})
					replayed, err := state.translateHistory(context.Background(), []any{client, map[string]any{"type": "function_call_output", "call_id": "native-plan-call", "output": "Plan updated"}})
					require.NoError(t, err)
					require.JSONEq(t, excelCompatJSON(t, native), excelCompatJSON(t, replayed[0]))
					require.JSONEq(t, `{"status":"ok"}`, openAIExcelString(openAIExcelMap(replayed[1])["output"]))
					guidance := state.nativePlanGuidance()
					require.Contains(t, guidance, "Native update_plan is permitted")
					require.Contains(t, state.toolProtocolReminder(), guidance)
				})
			}
		}
	}
}

func TestExcelDefaultNamespaceDoesNotGrantUnrelatedPlanPermission(t *testing.T) {
	for _, tools := range [][]any{
		{map[string]any{"type": "namespace", "name": "planner", "tools": []any{excelCompatTool("function", "update_plan")}}},
		{excelCompatTool("custom", "update_plan")},
		{excelCompatTool("function", "update_plan"), map[string]any{"type": "namespace", "name": "functions", "tools": []any{excelCompatTool("function", "update_plan")}}},
	} {
		state, _ := excelCompatState(t, tools)
		require.Contains(t, state.nativePlanGuidance(), "Native update_plan is unavailable")
	}
	require.False(t, openAIExcelIsNativePlanCandidate(map[string]any{"type": "function_call", "name": "update_plan", "namespace": "planner"}))
	require.False(t, openAIExcelIsNativePlanCandidate(map[string]any{"type": "custom_tool_call", "name": "update_plan"}))
}
