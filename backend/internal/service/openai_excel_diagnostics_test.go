package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func excelDiagnosticFailure(t *testing.T, state *OpenAIExcelWireState, native map[string]any, stream bool) (map[string]any, string, error) {
	t.Helper()
	core, logs := observer.New(zap.WarnLevel)
	ctx := logger.IntoContext(context.Background(), zap.New(core))
	payload := map[string]any{"status": "completed", "output": []any{native}}
	body, contentType := excelCompatJSON(t, payload), "application/json"
	if stream {
		body = excelCompatSSE(t, map[string]any{"type": "response.completed", "response": payload})
		contentType = "text/event-stream"
	}
	response, err := WrapOpenAIExcelResponse(ctx, &http.Response{
		StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {contentType}}, Body: io.NopCloser(strings.NewReader(body)),
	}, state)
	var output []byte
	if stream {
		require.NoError(t, err)
		defer func() { _ = response.Body.Close() }()
		output, err = io.ReadAll(response.Body)
	} else {
		require.Nil(t, response)
	}
	require.Error(t, err)
	entries := logs.All()
	require.Len(t, entries, 1)
	require.Equal(t, "openai.excel_response_translation_failed", entries[0].Message)
	fields := entries[0].ContextMap()
	diagnostic := openAIExcelToolDiagnostic(err)
	require.NotNil(t, diagnostic)
	require.JSONEq(t, excelCompatJSON(t, diagnostic), excelCompatJSON(t, fields["tool_diagnostic"]))
	if stream {
		events := excelCompatReadEvents(t, output)
		require.Len(t, events, 1)
		require.Equal(t, "error", events[0]["type"])
		require.Equal(t, "excel_protocol_error", events[0]["code"])
		require.Equal(t, openAIExcelSafeProtocolError(err), events[0]["message"])
		require.NotContains(t, string(output), "tool_diagnostic")
		require.NotContains(t, string(output), "response.completed")
	}
	return fields, string(output), err
}

func TestExcelDiagnosticLogsNamesAndShapesWithoutPayloads(t *testing.T) {
	const (
		argument = "Bearer SYNTHETIC_AUTHORIZATION_SECRET"
		image    = "data:image/png;base64,SYNTHETIC_PRIVATE_IMAGE"
		schema   = "SYNTHETIC_SCHEMA_CONST"
	)
	for _, stream := range []bool{false, true} {
		for _, transport := range []bool{false, true} {
			t.Run(fmt.Sprintf("stream=%t/relay=%t", stream, transport), func(t *testing.T) {
				tool := excelCompatTool("function", "declared_client_tool")
				tool["description"] = "PRIVATE_TOOL_DESCRIPTION"
				tool["parameters"] = map[string]any{"type": "object", "properties": map[string]any{"PRIVATE_SCHEMA_PROPERTY": map[string]any{"const": schema}}}
				state, backend := excelCompatState(t, []any{map[string]any{"type": "namespace", "name": "client", "tools": []any{tool}}})
				candidate := map[string]any{"name": "missing_client_tool", "namespace": "client", "arguments": map[string]any{"PRIVATE_ARGUMENT_KEY": argument, "PRIVATE_IMAGE_KEY": image}}
				native := candidate
				reason := "native_tool_undeclared"
				code := ""
				if transport {
					code = excelCompatJSON(t, candidate)
					native = map[string]any{"name": "run_officejs", "namespace": "functions", "arguments": excelCompatJSON(t, map[string]any{"code": code, "summary": "PRIVATE_OUTER_SUMMARY"})}
					reason = "relay_tool_undeclared"
				}
				native["type"], native["call_id"] = "function_call", "PRIVATE_CALL_ID"
				fields, output, err := excelDiagnosticFailure(t, state, native, stream)
				require.Equal(t, "invalid_tool_call", fields["reason"])
				require.Equal(t, reason, fields["tool_reason"])
				diagnostic := openAIExcelToolDiagnostic(err)
				require.Equal(t, 1, diagnostic["version"])
				require.Equal(t, "resolve_client_tool", diagnostic["stage"])
				require.Equal(t, transport, diagnostic["transport"])
				require.Equal(t, 0, diagnostic["relay_depth"])
				require.Equal(t, true, diagnostic["call_id_present"])
				require.Equal(t, "missing_client_tool", openAIExcelMap(diagnostic["candidate_name"])["value"])
				require.Equal(t, "client", openAIExcelMap(diagnostic["candidate_namespace"])["value"])
				require.Equal(t, map[string]any{"type": "object", "count": 2}, diagnostic["candidate_arguments"])
				require.Equal(t, map[string]any{"type": "null"}, diagnostic["candidate_input"])
				if transport {
					require.Equal(t, "run_officejs", openAIExcelMap(diagnostic["native_name"])["value"])
					require.Equal(t, "functions", openAIExcelMap(diagnostic["native_namespace"])["value"])
					require.Equal(t, map[string]any{"type": "string", "bytes": len(openAIExcelString(native["arguments"]))}, diagnostic["arguments"])
					require.Equal(t, map[string]any{"type": "string", "bytes": len(code)}, diagnostic["code"])
				} else {
					require.Equal(t, "missing_client_tool", openAIExcelMap(diagnostic["native_name"])["value"])
					require.Equal(t, map[string]any{"type": "object", "count": 2}, diagnostic["arguments"])
					require.NotContains(t, diagnostic, "code")
				}
				logged := excelCompatJSON(t, fields)
				for _, private := range []string{argument, image, schema, "PRIVATE_TOOL_DESCRIPTION", "PRIVATE_SCHEMA_PROPERTY", "PRIVATE_ARGUMENT_KEY", "PRIVATE_IMAGE_KEY", "PRIVATE_CALL_ID", "PRIVATE_OUTER_SUMMARY"} {
					require.NotContains(t, logged+output+err.Error(), private)
				}
				require.Equal(t, "Excel upstream response could not be translated safely (invalid_tool_call: "+reason+")", openAIExcelSafeProtocolError(err))
				require.Empty(t, backend.values)
			})
		}
	}
}

func TestExcelDiagnosticSuspiciousIdentifiersAreWhollyRedacted(t *testing.T) {
	for _, value := range []string{
		"tool\r\nAuthorization: private", "https://private.invalid/tool?token=secret", `{"secret":"private"}`,
		"sk-synthetic-private-key", "sk_synthetic_private_key", "eyJhbGciOiJIUzI1NiJ9.private.signature", strings.Repeat("a", 129),
	} {
		for _, field := range []string{"name", "namespace"} {
			t.Run(fmt.Sprintf("%s/%d/%x", field, len(value), value[:1]), func(t *testing.T) {
				state, _ := excelCompatState(t, []any{excelCompatTool("function", "declared")})
				native := map[string]any{"type": "function_call", "name": "unknown", "namespace": "unmatched", "call_id": "synthetic", "arguments": map[string]any{}}
				native[field] = value
				fields, _, err := excelDiagnosticFailure(t, state, native, false)
				identifier := openAIExcelMap(openAIExcelToolDiagnostic(err)["native_"+field])
				require.Equal(t, map[string]any{"type": "string", "bytes": len(value), "redacted": true}, identifier)
				require.NotContains(t, identifier, "value", "do not log a possibly sensitive prefix")
				require.NotContains(t, excelCompatJSON(t, fields), value)
			})
		}
	}
	for _, value := range []any{map[string]any{"private": "value"}, []any{"private"}, true, json.Number("123456789")} {
		state, _ := excelCompatState(t, []any{excelCompatTool("function", "declared")})
		fields, _, err := excelDiagnosticFailure(t, state, map[string]any{"type": "function_call", "name": "unknown", "namespace": value}, false)
		require.Equal(t, "native_identity", openAIExcelToolDiagnostic(err)["stage"])
		identifier := openAIExcelMap(openAIExcelToolDiagnostic(err)["native_namespace"])
		require.Equal(t, true, identifier["redacted"])
		require.NotContains(t, identifier, "value")
		require.NotContains(t, excelCompatJSON(t, fields), "private")
		require.NotContains(t, excelCompatJSON(t, fields), "123456789")
	}
}

func TestExcelDiagnosticCatalogIsBoundedSortedAndSchemaFree(t *testing.T) {
	tools := make([]any, 0, 40)
	for i := 39; i >= 0; i-- {
		tool := excelCompatTool("function", fmt.Sprintf("tool_%02d", i))
		tool["description"] = "PRIVATE_DESCRIPTION"
		tool["parameters"] = map[string]any{"type": "object", "properties": map[string]any{"PRIVATE_PROPERTY": map[string]any{"const": "PRIVATE_CONST"}}}
		tools = append(tools, tool)
	}
	state, _ := excelCompatState(t, []any{map[string]any{"type": "namespace", "name": "client", "tools": tools}})
	fields, _, err := excelDiagnosticFailure(t, state, map[string]any{"type": "function_call", "name": "undeclared", "arguments": `{}`, "call_id": "synthetic"}, false)
	diagnostic := openAIExcelToolDiagnostic(err)
	require.Equal(t, 40, diagnostic["catalog_total"])
	require.Equal(t, true, diagnostic["catalog_truncated"])
	catalog, ok := diagnostic["catalog"].([]any)
	require.True(t, ok)
	require.Len(t, catalog, 32)
	for i, raw := range catalog {
		entry := openAIExcelMap(raw)
		require.Len(t, entry, 3)
		require.Equal(t, fmt.Sprintf("tool_%02d", i), openAIExcelMap(entry["name"])["value"])
		require.Equal(t, "client", openAIExcelMap(entry["namespace"])["value"])
		require.Equal(t, "function", entry["kind"])
	}
	for _, private := range []string{"PRIVATE_DESCRIPTION", "PRIVATE_PROPERTY", "PRIVATE_CONST", "tool_32", "tool_39"} {
		require.NotContains(t, excelCompatJSON(t, fields), private)
	}
}

func TestExcelDiagnosticStagesAndShapeOnlySnapshot(t *testing.T) {
	const private = "PRIVATE_MUTABLE_PAYLOAD"
	for _, tc := range []struct {
		stage string
		call  map[string]any
	}{
		{"outer_arguments", map[string]any{"name": "run_officejs", "arguments": "{" + private}},
		{"relay_envelope", map[string]any{"name": "run_officejs", "arguments": map[string]any{"code": []any{private}}}},
		{"nested_envelope", map[string]any{"name": "run_officejs", "arguments": map[string]any{"code": map[string]any{"name": "run_officejs", "arguments": "{" + private}}}},
		{"custom_input", map[string]any{"name": "custom", "input": map[string]any{private: private}}},
		{"function_schema", map[string]any{"name": "execute", "arguments": map[string]any{"text": []any{private}}}},
	} {
		t.Run(tc.stage, func(t *testing.T) {
			state, _ := excelCompatState(t, []any{excelCompatTool("function", "execute"), excelCompatTool("custom", "custom")})
			tc.call["type"], tc.call["call_id"] = "function_call", "synthetic"
			_, err := state.translateNativeCall(context.Background(), tc.call)
			require.Error(t, err)
			diagnostic := openAIExcelToolDiagnostic(err)
			require.Equal(t, tc.stage, diagnostic["stage"])
			switch tc.stage {
			case "outer_arguments":
				require.Equal(t, map[string]any{"type": "string", "bytes": len(private) + 1}, diagnostic["arguments"])
			case "relay_envelope":
				require.Equal(t, map[string]any{"type": "array", "count": 1}, diagnostic["code"])
			case "nested_envelope":
				require.Equal(t, 1, diagnostic["relay_depth"])
				require.Equal(t, map[string]any{"type": "string", "bytes": len(private) + 1}, diagnostic["candidate_arguments"])
			case "custom_input":
				require.Equal(t, map[string]any{"type": "object", "count": 1}, diagnostic["input"])
				require.Equal(t, map[string]any{"type": "object", "count": 1}, diagnostic["candidate_input"])
			case "function_schema":
				require.Equal(t, map[string]any{"type": "object", "count": 1}, diagnostic["candidate_arguments"])
			}
			before := excelCompatJSON(t, diagnostic)
			require.NotContains(t, before, private)
			for cause := err; cause != nil; cause = errors.Unwrap(cause) {
				require.NotContains(t, cause.Error(), private, "wrapped causes must not retain decoder/body text")
			}
			if args := openAIExcelMap(tc.call["arguments"]); args != nil {
				args["PRIVATE_LATE_KEY"] = "PRIVATE_LATE_VALUE"
				if envelope := openAIExcelMap(args["code"]); envelope != nil {
					envelope["name"] = "mutated_nested_name"
				}
			}
			tc.call["name"], tc.call["namespace"], tc.call["arguments"], tc.call["input"] = "mutated_name", "mutated_namespace", private, private
			for key, tool := range state.tools {
				tool.Name = "mutated_catalog_name"
				tool.Spec["parameters"] = map[string]any{private: private}
				state.tools[key] = tool
			}
			require.JSONEq(t, before, excelCompatJSON(t, openAIExcelToolDiagnostic(err)), "failure metadata must not retain the original mutable item, envelope or tool schema")
		})
	}
}
