package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func excelCompatJSON(t *testing.T, value any) string {
	t.Helper()
	raw, err := json.Marshal(value)
	require.NoError(t, err)
	return string(raw)
}

func excelCompatState(t *testing.T, tools []any) (*OpenAIExcelWireState, *excelMemoryBackend) {
	t.Helper()
	history, backend := excelStateFixture()
	_, state := excelTestPrepare(t, excelCompatJSON(t, map[string]any{
		"model": "gpt-6-astra", "input": "Use the declared client tool.", "tools": tools,
	}), history)
	return state, backend
}

func excelCompatTool(kind, name string) map[string]any {
	tool := map[string]any{"type": kind, "name": name}
	if kind == "function" {
		tool["parameters"] = map[string]any{
			"type": "object", "required": []any{"text"}, "additionalProperties": false,
			"properties": map[string]any{"text": map[string]any{"type": "string"}},
		}
	}
	return tool
}

func excelCompatAssertNativeHistory(t *testing.T, state *OpenAIExcelWireState, backend *excelMemoryBackend, native, client map[string]any, outputType string) {
	t.Helper()
	callID := openAIExcelString(native["call_id"])
	restarted := NewOpenAIExcelStateStore(backend, excelTestCipher{})
	stored, err := restarted.LoadExcelNativeCall(context.Background(), state.options.HistoryScope, callID)
	require.NoError(t, err)
	require.JSONEq(t, excelCompatJSON(t, native), string(stored), "history must keep the exact native identity and transport envelope")
	state.options.History = restarted
	restored, err := state.translateHistory(context.Background(), []any{
		map[string]any{"type": "reasoning", "encrypted_content": "synthetic-encrypted-history"},
		client,
		map[string]any{"type": openAIExcelString(client["type"]) + "_output", "call_id": callID, "output": "synthetic tool result"},
	})
	require.NoError(t, err)
	require.Len(t, restored, 3)
	require.JSONEq(t, excelCompatJSON(t, native), excelCompatJSON(t, restored[1]))
	require.Equal(t, outputType, openAIExcelMap(restored[2])["type"])
	require.Equal(t, "synthetic tool result", openAIExcelMap(restored[2])["output"])
}

func TestExcelToolCompatExactNamesAndNativeHistory(t *testing.T) {
	for _, kind := range []string{"function", "custom"} {
		for _, identity := range []struct {
			name, namespace, declaredName, declaredNamespace string
		}{
			{"execute", "", "execute", ""},
			{"execute", "functions", "execute", "functions"},
			{"functions.execute", "", "execute", "functions"},
			{"functions.execute", "functions", "execute", "functions"},
			{"tools.execute", "", "tools.execute", ""},
		} {
			for _, carrier := range []struct{ name, namespace string }{
				{"", ""}, {"run_officejs", ""}, {"functions.run_officejs", ""},
				{"run_officejs", "functions"}, {"functions.run_officejs", "functions"},
			} {
				label := kind + "/" + identity.name + "/ns=" + identity.namespace + "/carrier=" + carrier.namespace + ":" + carrier.name
				t.Run(label, func(t *testing.T) {
					tool := excelCompatTool(kind, identity.declaredName)
					tools := []any{tool}
					if identity.declaredNamespace != "" {
						tools = []any{map[string]any{"type": "namespace", "name": identity.declaredNamespace, "tools": []any{tool}}}
					}
					state, history := excelCompatState(t, tools)
					call := map[string]any{"name": identity.name}
					if identity.namespace != "" {
						call["namespace"] = identity.namespace
					}
					if kind == "function" {
						call["arguments"] = `{"text":"synthetic payload"}`
					} else {
						call["input"] = "*** Begin Patch\nsynthetic payload\n*** End Patch"
					}
					native := call
					outputType := kind + "_call_output"
					if kind == "custom" {
						outputType = "custom_tool_call_output"
					}
					if carrier.name != "" {
						native = map[string]any{"name": carrier.name, "arguments": excelCompatJSON(t, map[string]any{"code": excelCompatJSON(t, call), "summary": "synthetic relay"})}
						if carrier.namespace != "" {
							native["namespace"] = carrier.namespace
						}
						outputType = "function_call_output"
					}
					native["type"] = "function_call"
					if carrier.name == "" && kind == "custom" {
						native["type"] = "custom_tool_call"
					}
					native["id"], native["call_id"], native["status"] = "native-item", "native-call", "completed"
					client, err := state.translateNativeCall(context.Background(), native)
					require.NoError(t, err)
					require.Equal(t, identity.declaredName, client["name"])
					require.Equal(t, identity.declaredNamespace, openAIExcelString(client["namespace"]))
					require.Equal(t, "native-call", client["call_id"])
					if kind == "function" {
						require.Equal(t, "function_call", client["type"])
						require.Equal(t, "native-item", client["id"])
						require.JSONEq(t, `{"text":"synthetic payload"}`, openAIExcelString(client["arguments"]))
					} else {
						require.Equal(t, "custom_tool_call", client["type"])
						require.Equal(t, call["input"], client["input"])
					}
					excelCompatAssertNativeHistory(t, state, history, native, client, outputType)
				})
			}
		}
	}
}

func TestExcelToolCompatDoesNotGuessToolNamespaces(t *testing.T) {
	for _, tc := range []struct {
		name, declaredNamespace, callName, callNamespace string
	}{
		{"no-host-prefix-added", "", "functions.execute", ""},
		{"no-basename-guess", "functions", "execute", ""},
		{"wrong-explicit-namespace", "functions", "functions.execute", "unrelated"},
		{"wrong-leaf-namespace", "functions", "execute", "unrelated"},
		{"namespace-does-not-wrap-top-level", "", "execute", "functions"},
		{"unrelated-native-tool", "functions", "list_skills", ""},
		{"unrelated-native-namespace", "functions", "run_officejs", "unrelated"},
	} {
		for _, relay := range []bool{false, true} {
			t.Run(tc.name+map[bool]string{false: "/direct", true: "/relay"}[relay], func(t *testing.T) {
				tools := []any{excelCompatTool("function", "execute")}
				if tc.declaredNamespace != "" {
					tools = []any{map[string]any{"type": "namespace", "name": tc.declaredNamespace, "tools": tools}}
				}
				state, history := excelCompatState(t, tools)
				native := map[string]any{"name": tc.callName, "namespace": tc.callNamespace, "arguments": map[string]any{"text": "synthetic"}}
				reason := "native_tool_undeclared"
				if relay {
					native = map[string]any{"name": "run_officejs", "arguments": map[string]any{"code": native}}
					reason = "relay_tool_undeclared"
				}
				native["type"], native["call_id"] = "function_call", "untrusted-call"
				client, err := state.translateNativeCall(context.Background(), native)
				require.Nil(t, client)
				require.Equal(t, reason, openAIExcelToolFailureReason(err))
				require.Empty(t, history.values, "invalid calls must not poison native history")
			})
		}
	}
}

func TestExcelToolCompatNamespacedReservedNamesRemainClientTools(t *testing.T) {
	for _, name := range []string{"update_plan", "run_officejs"} {
		t.Run(name, func(t *testing.T) {
			state, backend := excelCompatState(t, []any{map[string]any{
				"type": "namespace", "name": "client", "tools": []any{excelCompatTool("custom", name)},
			}})
			native := map[string]any{"type": "custom_tool_call", "id": "native-custom", "call_id": "custom-client-call", "name": name, "namespace": "client", "input": "synthetic client input"}
			client, err := state.translateNativeCall(context.Background(), native)
			require.NoError(t, err)
			require.Equal(t, "custom_tool_call", client["type"])
			require.Equal(t, "client", client["namespace"])
			excelCompatAssertNativeHistory(t, state, backend, native, client, "custom_tool_call_output")
		})
	}
}

func TestExcelToolCompatSafeDiagnostics(t *testing.T) {
	const (
		functionName = "private_function_name"
		customName   = "private_custom_name"
		propertyName = "private_schema_property"
		argument     = "PRIVATE_ARGUMENT_VALUE"
		schemaValue  = "PRIVATE_SCHEMA_VALUE"
	)
	function := map[string]any{"type": "function", "name": functionName, "parameters": map[string]any{
		"type": "object", "required": []any{propertyName}, "additionalProperties": false,
		"properties": map[string]any{propertyName: map[string]any{"type": "string", "const": schemaValue}},
	}}
	validArgs := map[string]any{propertyName: schemaValue}
	relay := func(code any) map[string]any {
		return map[string]any{"name": "run_officejs", "arguments": map[string]any{"code": code}}
	}
	for _, tc := range []struct {
		reason string
		call   map[string]any
	}{
		{"native_arguments_invalid", map[string]any{"name": "run_officejs", "arguments": "{" + argument}},
		{"relay_envelope_invalid", relay(argument)},
		{"nested_envelope_invalid", relay(map[string]any{"name": "run_officejs", "arguments": "{" + argument})},
		{"relay_tool_undeclared", relay(map[string]any{"name": "private_undeclared_tool", "arguments": validArgs})},
		{"native_tool_undeclared", map[string]any{"name": "private_undeclared_tool", "arguments": validArgs}},
		{"custom_input_not_text", map[string]any{"name": customName, "type": "custom_tool_call", "input": map[string]any{propertyName: argument}}},
		{"function_arguments_invalid", map[string]any{"name": functionName, "arguments": "{" + argument}},
		{"function_arguments_not_object", map[string]any{"name": functionName, "arguments": []any{argument}}},
		{"function_schema_mismatch", map[string]any{"name": functionName, "arguments": map[string]any{propertyName: argument}}},
		{"call_id_missing", map[string]any{"name": functionName, "arguments": validArgs}},
	} {
		t.Run(tc.reason, func(t *testing.T) {
			state, history := excelCompatState(t, []any{function, excelCompatTool("custom", customName)})
			if tc.call["type"] == nil {
				tc.call["type"] = "function_call"
			}
			if tc.reason != "call_id_missing" {
				tc.call["call_id"] = "private_call_id"
			}
			core, logs := observer.New(zap.WarnLevel)
			ctx := logger.IntoContext(context.Background(), zap.New(core))
			response, err := WrapOpenAIExcelResponse(ctx, &http.Response{
				StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}},
				Body: io.NopCloser(strings.NewReader(excelCompatJSON(t, map[string]any{"status": "completed", "output": []any{tc.call}}))),
			}, state)
			require.Nil(t, response)
			require.Error(t, err)
			require.Equal(t, "invalid_tool_call", openAIExcelProtocolReason(err))
			require.Equal(t, tc.reason, openAIExcelToolFailureReason(err))
			safe := openAIExcelSafeProtocolError(err)
			require.Equal(t, "Excel upstream response could not be translated safely (invalid_tool_call: "+tc.reason+")", safe)
			entries := logs.All()
			require.Len(t, entries, 1)
			require.Equal(t, "openai.excel_response_translation_failed", entries[0].Message)
			require.Equal(t, map[string]any{"reason": "invalid_tool_call", "tool_reason": tc.reason}, entries[0].ContextMap())
			logJSON := excelCompatJSON(t, entries[0].ContextMap())
			for _, secret := range []string{functionName, customName, propertyName, argument, schemaValue, "private_undeclared_tool", "private_call_id"} {
				require.NotContains(t, safe, secret)
				require.NotContains(t, err.Error(), secret)
				require.NotContains(t, logJSON+entries[0].Message, secret)
			}
			require.Empty(t, history.values)
		})
	}
}

func excelCompatSSE(t *testing.T, events ...map[string]any) string {
	t.Helper()
	var stream strings.Builder
	for _, event := range events {
		_, _ = stream.WriteString("data: " + excelCompatJSON(t, event) + "\n\n")
	}
	return stream.String()
}

func excelCompatReadEvents(t *testing.T, raw []byte) []map[string]any {
	t.Helper()
	events := make([]map[string]any, 0)
	for _, line := range strings.Split(string(raw), "\n") {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var event map[string]any
		require.NoError(t, json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event))
		events = append(events, event)
	}
	return events
}

func TestExcelToolCompatSSEKeepsNamespacesAndHistory(t *testing.T) {
	for _, kind := range []string{"function", "custom"} {
		t.Run(kind, func(t *testing.T) {
			state, history := excelCompatState(t, []any{map[string]any{"type": "namespace", "name": "functions", "tools": []any{excelCompatTool(kind, "execute")}}})
			native := map[string]any{"type": "function_call", "id": "native-item", "call_id": "native-sse-call", "name": "execute", "namespace": "functions", "arguments": `{"text":"synthetic stream payload"}`}
			if kind == "custom" {
				native["name"] = "functions.run_officejs"
				native["arguments"] = excelCompatJSON(t, map[string]any{"code": excelCompatJSON(t, map[string]any{"name": "execute", "namespace": "functions", "input": "synthetic custom input"})})
			}
			stream := excelCompatSSE(t,
				map[string]any{"type": "response.output_item.added", "output_index": 0, "item": native},
				map[string]any{"type": "response.output_item.done", "output_index": 0, "item": native},
				map[string]any{"type": "response.completed", "response": map[string]any{"status": "completed", "output": []any{native}}},
			)
			response, err := WrapOpenAIExcelResponse(context.Background(), &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(stream))}, state)
			require.NoError(t, err)
			defer func() { _ = response.Body.Close() }()
			raw, err := io.ReadAll(response.Body)
			require.NoError(t, err)
			events := excelCompatReadEvents(t, raw)
			require.Len(t, events, 5, "native events must become exactly one complete client call sequence")
			prefix := "response.function_call_arguments"
			if kind == "custom" {
				prefix = "response.custom_tool_call_input"
			}
			require.Equal(t, []any{"response.output_item.added", prefix + ".delta", prefix + ".done", "response.output_item.done", "response.completed"}, []any{events[0]["type"], events[1]["type"], events[2]["type"], events[3]["type"], events[4]["type"]})
			client := openAIExcelMap(events[3]["item"])
			require.Equal(t, "execute", client["name"])
			require.Equal(t, "functions", client["namespace"])
			require.Equal(t, "native-sse-call", client["call_id"])
			terminal, ok := openAIExcelMap(events[4]["response"])["output"].([]any)
			require.True(t, ok)
			require.Equal(t, client, terminal[0])
			require.NotContains(t, string(raw), "run_officejs")
			excelCompatAssertNativeHistory(t, state, history, native, client, "function_call_output")
		})
	}
}

func TestExcelToolCompatSSERejectsNamespaceChange(t *testing.T) {
	state, history := excelCompatState(t, []any{
		map[string]any{"type": "namespace", "name": "one", "tools": []any{excelCompatTool("function", "execute")}},
		map[string]any{"type": "namespace", "name": "two", "tools": []any{excelCompatTool("function", "execute")}},
	})
	initial := map[string]any{"type": "function_call", "id": "native-item", "call_id": "native-call", "name": "execute", "namespace": "one", "arguments": ""}
	terminal := map[string]any{"type": "function_call", "id": "native-item", "call_id": "native-call", "name": "execute", "namespace": "two", "arguments": `{"text":"synthetic"}`}
	stream := excelCompatSSE(t,
		map[string]any{"type": "response.output_item.added", "output_index": 0, "item": initial},
		map[string]any{"type": "response.completed", "response": map[string]any{"status": "completed", "output": []any{terminal}}},
	)
	response, err := WrapOpenAIExcelResponse(context.Background(), &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(stream))}, state)
	require.NoError(t, err)
	defer func() { _ = response.Body.Close() }()
	raw, err := io.ReadAll(response.Body)
	require.Error(t, err)
	require.Equal(t, "conflicting_output_items", openAIExcelProtocolReason(err))
	events := excelCompatReadEvents(t, raw)
	require.Len(t, events, 1)
	require.Equal(t, "error", events[0]["type"])
	require.Equal(t, "excel_protocol_error", events[0]["code"])
	require.NotContains(t, string(raw), "response.completed")
	require.NotContains(t, string(raw), "response.output_item")
	require.Empty(t, history.values)
}
