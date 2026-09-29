package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestExcelDeclaredCodingToolsPrepareRelayAndHistory(t *testing.T) {
	const instruction = "Use the declared tools to inspect, modify, and verify the synthetic workspace."
	const patch = "*** Begin Patch\n*** Add File: synthetic.go\n+package synthetic\n*** End Patch"
	tools := []any{
		map[string]any{"type": "namespace", "name": "mcp__fastctx", "tools": []any{
			map[string]any{"type": "function", "name": "inspect_local_file", "description": "Read a workspace file through the client MCP server.", "parameters": map[string]any{
				"type": "object", "required": []any{"file_path"}, "additionalProperties": false,
				"properties": map[string]any{"file_path": map[string]any{"type": "string"}},
			}},
		}},
		map[string]any{"type": "custom", "name": "apply_patch", "description": "Apply a patch in the client workspace.", "format": map[string]any{"type": "text"}},
		map[string]any{"type": "namespace", "name": "functions", "tools": []any{
			map[string]any{"type": "function", "name": "exec", "description": "Run a command through the client tool.", "parameters": map[string]any{
				"type": "object", "required": []any{"cmd"}, "additionalProperties": false,
				"properties": map[string]any{"cmd": map[string]any{"type": "string"}},
			}},
		}},
	}
	// Commands, paths and patches below are only protocol fixtures. This test
	// transforms local HTTP bodies; there is no executor or upstream request.
	calls := []struct {
		name, namespace, kind string
		arguments             map[string]any
		input, result         string
	}{
		{"inspect_local_file", "mcp__fastctx", "function", map[string]any{"file_path": `D:\synthetic\main.go`}, "", "package synthetic"},
		{"apply_patch", "", "custom", nil, patch, "patch accepted by synthetic client"},
		{"exec", "functions", "function", map[string]any{"cmd": "go test ./synthetic/..."}, "", "synthetic test result: passed"},
	}
	for _, choice := range []string{"", "auto", "required"} {
		for _, relay := range []bool{false, true} {
			mode := "direct"
			if relay {
				mode = "run_officejs"
			}
			t.Run("choice="+choice+"/"+mode, func(t *testing.T) {
				history, backend := excelStateFixture()
				request := map[string]any{"model": "gpt-6-astra", "instructions": instruction, "input": "Implement the synthetic change.", "tools": tools}
				if choice != "" {
					request["tool_choice"] = choice
				}
				wire, state := excelTestPrepare(t, excelCompatJSON(t, request), history)
				require.NotEqual(t, "none", wire["tool_choice"], "a declared coding catalog must not be disabled")
				require.Len(t, state.tools, 3)
				items, ok := wire["input"].([]any)
				require.True(t, ok)
				require.Equal(t, instruction, openAIExcelPartsText(openAIExcelMap(items[0])["content"]))
				protocol := openAIExcelPartsText(openAIExcelMap(items[1])["content"])
				for _, name := range []string{"mcp__fastctx.inspect_local_file", "apply_patch", "functions.exec"} {
					require.Contains(t, protocol, `"name":"`+name+`"`)
				}
				require.NotContains(t, protocol, "No client tools are declared")
				require.NotContains(t, protocol, "Do not call or imitate host, MCP, skill, connector, file-system, visualization", "declared MCP and workspace tools must not receive the no-tools prohibition")
				require.Contains(t, protocol, "no Office code is executed")
				output := make([]any, 0, len(calls))
				for _, call := range calls {
					native := map[string]any{"name": call.name}
					if call.namespace != "" {
						native["namespace"] = call.namespace
					}
					if call.kind == "custom" {
						native["input"] = call.input
					} else {
						native["arguments"] = excelCompatJSON(t, call.arguments)
					}
					if relay {
						if call.namespace != "" {
							native["name"] = call.namespace + "." + call.name
							delete(native, "namespace")
						}
						native = map[string]any{"name": "run_officejs", "namespace": "functions", "arguments": excelCompatJSON(t, map[string]any{
							"summary": "Forward the declared client tool", "extended_summary": "Synthetic protocol fixture only",
							"destructive": false, "references": []any{}, "code": excelCompatJSON(t, native),
						})}
					}
					native["type"], native["id"], native["call_id"], native["status"] = "function_call", "native-"+call.name, "call-"+call.name, "completed"
					if !relay && call.kind == "custom" {
						native["type"] = "custom_tool_call"
					}
					output = append(output, native)
				}
				response, err := WrapOpenAIExcelResponse(context.Background(), &http.Response{
					StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}},
					Body: io.NopCloser(strings.NewReader(excelCompatJSON(t, map[string]any{"status": "completed", "output": output}))),
				}, state)
				require.NoError(t, err)
				defer func() { _ = response.Body.Close() }()
				raw, err := io.ReadAll(response.Body)
				require.NoError(t, err)
				var payload map[string]any
				require.NoError(t, json.Unmarshal(raw, &payload))
				clientCalls, ok := payload["output"].([]any)
				require.True(t, ok)
				require.Len(t, clientCalls, len(calls))
				followup := []any{map[string]any{"type": "reasoning", "encrypted_content": "synthetic-history-requires-native-restoration"}}
				for index, call := range calls {
					client := openAIExcelMap(clientCalls[index])
					require.Equal(t, call.name, client["name"])
					require.Equal(t, call.namespace, openAIExcelString(client["namespace"]))
					require.Equal(t, "call-"+call.name, client["call_id"])
					kind := "function_call"
					if call.kind == "custom" {
						kind = "custom_tool_call"
						require.Equal(t, call.input, client["input"])
						require.NotContains(t, client, "arguments")
					} else {
						require.JSONEq(t, excelCompatJSON(t, call.arguments), openAIExcelString(client["arguments"]))
					}
					require.Equal(t, kind, client["type"])
					followup = append(followup, client, map[string]any{"type": kind + "_output", "call_id": client["call_id"], "output": call.result})
				}
				request["input"] = followup
				// Recreate the real history store to exercise persisted native fields,
				// rather than replaying the already translated in-memory items.
				restarted := NewOpenAIExcelStateStore(backend, excelTestCipher{})
				continued, continuedState := excelTestPrepare(t, excelCompatJSON(t, request), restarted)
				require.NotEqual(t, "none", continued["tool_choice"])
				require.Len(t, continuedState.tools, 3)
				restored, ok := continued["input"].([]any)
				require.True(t, ok)
				require.GreaterOrEqual(t, len(restored), len(followup))
				restored = restored[len(restored)-len(followup):]
				for index, call := range calls {
					require.JSONEq(t, excelCompatJSON(t, output[index]), excelCompatJSON(t, restored[1+2*index]), "native tool identity and its relay envelope must round-trip unchanged")
					result := openAIExcelMap(restored[2+2*index])
					require.Equal(t, call.result, result["output"])
					require.Equal(t, "call-"+call.name, result["call_id"])
					outputType := "function_call_output"
					if call.kind == "custom" && !relay {
						outputType = "custom_tool_call_output"
					}
					require.Equal(t, outputType, result["type"])
				}
			})
		}
	}
}
