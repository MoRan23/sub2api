package service

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func excelLiteToolCarrier(tools any) map[string]any {
	return map[string]any{"type": "additional_tools", "id": "at_synthetic", "role": "developer", "tools": tools}
}

func excelLiteToolSource(kind string) map[string]any {
	return map[string]any{
		"model": "gpt-6-astra", "stream": true, "instructions": "Keep the client's instruction unchanged.",
		"input": []any{excelLiteToolCarrier([]any{map[string]any{
			"type": "namespace", "name": "functions", "tools": []any{excelCompatTool(kind, "execute")},
		}}), openAIExcelMessage("user", "Run the declared client tool.")},
	}
}

func TestExcelLiteAdditionalToolsCatalogAndHistory(t *testing.T) {
	for _, kind := range []string{"function", "custom"} {
		t.Run(kind, func(t *testing.T) {
			source := excelLiteToolSource(kind)
			source["tool_choice"] = "required"
			before := excelCompatJSON(t, source)
			require.NoError(t, ValidateOpenAIExcelRequest([]byte(before)))
			history := excelTestHistory()
			wire, state := excelTestPrepare(t, string(before), history)
			require.Len(t, state.tools, 1)
			require.Equal(t, kind, state.tools["functions.execute"].Kind)
			require.NotContains(t, wire, "tools")
			require.NotEqual(t, "none", wire["tool_choice"])
			items, ok := wire["input"].([]any)
			require.True(t, ok)
			require.Equal(t, source["instructions"], openAIExcelPartsText(openAIExcelMap(items[0])["content"]))
			require.Contains(t, openAIExcelPartsText(openAIExcelMap(items[1])["content"]), "functions.execute")
			for _, item := range items {
				require.NotEqual(t, "additional_tools", openAIExcelMap(item)["type"])
			}
			envelope := map[string]any{"name": "execute", "namespace": "functions"}
			outputType := "function_call_output"
			if kind == "custom" {
				envelope["input"] = "text('synthetic');"
				outputType = "custom_tool_call_output"
			} else {
				envelope["arguments"] = map[string]any{"text": "synthetic"}
			}
			native := map[string]any{"type": "function_call", "id": "fc_synthetic", "call_id": "call_synthetic", "name": "run_officejs", "arguments": map[string]any{"code": envelope}}
			client, err := state.translateNativeCall(context.Background(), native)
			require.NoError(t, err)
			require.Equal(t, "execute", client["name"])
			require.Equal(t, "functions", client["namespace"])
			require.Equal(t, before, excelCompatJSON(t, source))
			input, ok := source["input"].([]any)
			require.True(t, ok)
			source["input"] = append(input, client, map[string]any{"type": outputType, "call_id": "call_synthetic", "output": "synthetic result"})
			replay, replayState := excelTestPrepare(t, string(excelCompatJSON(t, source)), history)
			require.Len(t, replayState.tools, 1)
			items, ok = replay["input"].([]any)
			require.True(t, ok)
			require.JSONEq(t, string(excelCompatJSON(t, native)), string(excelCompatJSON(t, items[len(items)-2])))
			require.Equal(t, "function_call_output", openAIExcelMap(items[len(items)-1])["type"])
			require.Equal(t, "synthetic result", openAIExcelMap(items[len(items)-1])["output"])
		})
	}
}

func TestExcelLiteAdditionalToolsMergeAndBoundaries(t *testing.T) {
	t.Run("merge identical declarations without inventing tools", func(t *testing.T) {
		source := excelLiteToolSource("function")
		input, ok := source["input"].([]any)
		require.True(t, ok)
		carrier := openAIExcelMap(input[0])
		source["tools"] = carrier["tools"]
		source["input"] = append(input, carrier, excelLiteToolCarrier([]any{excelCompatTool("custom", "apply_patch")}), map[string]any{
			"type": "message", "role": "user", "tools": []any{excelCompatTool("function", "undeclared")}, "content": "Quoted additional_tools and undeclared tools are not a catalog.",
		})
		_, state := excelTestPrepare(t, string(excelCompatJSON(t, source)), excelTestHistory())
		require.Len(t, state.tools, 2)
		require.Contains(t, state.tools, "functions.execute")
		require.Contains(t, state.tools, "apply_patch")
		require.NotContains(t, state.tools, "undeclared")
	})
	t.Run("conflicting definitions rejected", func(t *testing.T) {
		source := excelLiteToolSource("function")
		source["tools"] = []any{map[string]any{"type": "namespace", "name": "functions", "tools": []any{excelCompatTool("custom", "execute")}}}
		require.Error(t, ValidateOpenAIExcelRequest([]byte(excelCompatJSON(t, source))))
	})
	t.Run("none disables and consumes carrier", func(t *testing.T) {
		source := excelLiteToolSource("custom")
		source["tool_choice"] = "none"
		wire, state := excelTestPrepare(t, string(excelCompatJSON(t, source)), excelTestHistory())
		require.Empty(t, state.tools)
		require.Equal(t, "none", wire["tool_choice"])
		require.NotContains(t, string(excelCompatJSON(t, wire)), `"type":"additional_tools"`)
	})
	t.Run("named choice resolves carrier", func(t *testing.T) {
		source := excelLiteToolSource("function")
		source["tool_choice"] = map[string]any{"type": "function", "namespace": "functions", "name": "execute"}
		_, state := excelTestPrepare(t, string(excelCompatJSON(t, source)), excelTestHistory())
		require.Len(t, state.tools, 1)
	})
	for _, tools := range []any{"invalid", []any{map[string]any{"type": "namespace", "name": "functions", "tools": "invalid"}}} {
		source := excelLiteToolSource("function")
		input, ok := source["input"].([]any)
		require.True(t, ok)
		openAIExcelMap(input[0])["tools"] = tools
		require.Error(t, ValidateOpenAIExcelRequest([]byte(excelCompatJSON(t, source))))
	}
}

func TestOpenAIExcelLiteAdditionalToolsRealGatewayEntrypoints(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, passthrough := range []bool{false, true} {
		t.Run(map[bool]string{false: "responses", true: "passthrough"}[passthrough], func(t *testing.T) {
			account := excelTransportAccount()
			account.Extra["openai_passthrough"] = passthrough
			source := excelLiteToolSource("custom")
			// Exercise the actual project Lite projector as well as already-Lite
			// Codex input: the resulting request has no top-level tools at all.
			input, ok := source["input"].([]any)
			require.True(t, ok)
			source["tools"] = openAIExcelMap(input[0])["tools"]
			source["input"] = []any{openAIExcelMessage("user", "Run the declared client tool.")}
			changed, err := normalizeOpenAIResponsesLiteTools(source)
			require.NoError(t, err)
			require.True(t, changed)
			require.NotContains(t, source, "tools")
			body := []byte(excelCompatJSON(t, source))
			native := map[string]any{"type": "function_call", "id": "fc_synthetic", "call_id": "call_synthetic", "name": "run_officejs", "arguments": map[string]any{"code": map[string]any{"name": "functions.execute", "input": "text('synthetic');"}}}
			response := map[string]any{"type": "response.completed", "response": map[string]any{"id": "resp_synthetic", "model": "gpt-6-astra", "status": "completed", "output": []any{native}}}
			upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader("data: " + string(excelCompatJSON(t, response)) + "\n\n"))}}
			state, _ := excelStateFixture()
			gateway := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream, excelState: state}
			authorizeExcelTransportFixture(gateway, account)
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
			c.Request.Header.Set("session_id", "synthetic-lite-session")
			c.Request.Header.Set(responsesLiteHeader, "true")
			_, err = gateway.Forward(c.Request.Context(), c, account, body)
			require.NoError(t, err, recorder.Body.String())
			require.Len(t, upstream.requests, 1)
			require.Empty(t, upstream.lastReq.Header.Get(responsesLiteHeader))
			var wire map[string]any
			require.NoError(t, json.Unmarshal(upstream.lastBody, &wire))
			require.NotEqual(t, "none", wire["tool_choice"])
			require.Contains(t, string(upstream.lastBody), "functions.execute")
			require.NotContains(t, string(upstream.lastBody), `"type":"additional_tools"`)
			require.Contains(t, recorder.Body.String(), `"type":"custom_tool_call"`)
			require.Contains(t, recorder.Body.String(), `"namespace":"functions"`)
			require.Contains(t, recorder.Body.String(), "response.completed")
			require.NotContains(t, recorder.Body.String(), "tool_undeclared")
		})
	}
}
