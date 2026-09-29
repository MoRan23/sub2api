package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type excelMemoryHistory struct {
	mu     sync.Mutex
	values map[string]json.RawMessage
	err    error
}

func (h *excelMemoryHistory) LoadExcelNativeCall(_ context.Context, scope, call string) (json.RawMessage, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.err != nil {
		return nil, h.err
	}
	raw := h.values[scope+"/"+call]
	if raw == nil {
		return nil, ErrOpenAIExcelHistoryNotFound
	}
	return append(json.RawMessage(nil), raw...), nil
}
func (h *excelMemoryHistory) StoreExcelNativeCall(_ context.Context, scope, call string, raw json.RawMessage) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.err != nil {
		return h.err
	}
	h.values[scope+"/"+call] = append(json.RawMessage(nil), raw...)
	return nil
}
func excelTestHistory() *excelMemoryHistory {
	return &excelMemoryHistory{values: make(map[string]json.RawMessage)}
}
func excelTestHeaders() http.Header {
	return http.Header{"Authorization": {"Bearer synthetic-token"}, "Chatgpt-Account-Id": {"synthetic-account"}, "User-Agent": {"codex-tui/0.155.1 (Mac OS 26.0; arm64)"}, "X-Codex-Installation-Id": {"synthetic-install"}}
}
func excelTestPrepare(t *testing.T, body string, history OpenAIExcelNativeHistoryStore) (map[string]any, *OpenAIExcelWireState) {
	t.Helper()
	raw, _, state, err := PrepareOpenAIExcelWire(context.Background(), []byte(body), excelTestHeaders(), OpenAIExcelWireOptions{HistoryScope: "tenant/account/authorization/session", History: history})
	require.NoError(t, err)
	var parsed map[string]any
	require.NoError(t, openAIExcelJSON(raw, &parsed))
	return parsed, state
}

func TestExcelProtocolTextProjection(t *testing.T) {
	headers := excelTestHeaders()
	headers.Set("Cookie", "client=secret")
	headers.Set(responsesLiteHeader, "true")
	headers.Set("X-Codex-Turn-State", "opaque")
	headers.Set("Host", "chatgpt.com")
	body := []byte(`{"model":"gpt-6-astra","stream":true,"instructions":"Keep my instruction","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}],"prompt_cache_key":"cache","reasoning":{"effort":"xhigh"},"metadata":{"task_id":"untrusted","turn_id":"untrusted"}}`)
	raw, out, state, err := PrepareOpenAIExcelWire(context.Background(), body, headers, OpenAIExcelWireOptions{HistoryScope: "scope"})
	require.NoError(t, err)
	require.NotNil(t, state)
	var payload map[string]any
	require.NoError(t, openAIExcelJSON(raw, &payload))
	require.Equal(t, "gpt-6-astra", payload["model"])
	require.Equal(t, "explicit", payload["model_selection"])
	require.Equal(t, "xhigh", payload["reasoning_effort"])
	require.Equal(t, false, payload["store"])
	require.Equal(t, "cache", payload["prompt_cache_key"])
	items, ok := payload["input"].([]any)
	require.True(t, ok)
	require.Equal(t, "Keep my instruction", openAIExcelPartsText(openAIExcelMap(items[0])["content"]))
	require.Contains(t, openAIExcelPartsText(openAIExcelMap(items[1])["content"]), "Do not emit function_call or custom_tool_call")
	metadata := openAIExcelMap(payload["metadata"])
	require.NotEqual(t, "untrusted", metadata["task_id"])
	require.NotEqual(t, "untrusted", metadata["turn_id"])
	rawAgain, _, _, err := PrepareOpenAIExcelWire(context.Background(), body, headers, OpenAIExcelWireOptions{HistoryScope: "scope"})
	require.NoError(t, err)
	require.Equal(t, raw, rawAgain)
	require.Equal(t, openAIExcelUserAgent, out.Get("User-Agent"))
	require.Equal(t, "synthetic-install", out.Get("X-Codex-Installation-ID"))
	require.Equal(t, "PC", out.Get("X-OpenAI-Internal-Basispoints-Office-Platform"))
	require.Equal(t, "Windows", out.Get("X-Stainless-OS"))
	require.Equal(t, "x64", out.Get("X-Stainless-Arch"))
	require.Equal(t, "synthetic-account", out.Get("X-OpenAI-Account-ID"))
	require.Equal(t, "https://bps.openai.com", out.Get("Origin"))
	for _, key := range []string{"Cookie", responsesLiteHeader, "X-Codex-Turn-State", "Host"} {
		require.Empty(t, out.Get(key))
	}
	require.Equal(t, "client=secret", headers.Get("Cookie"))
}

func TestExcelProtocolRejectsUnsupportedRequest(t *testing.T) {
	for _, body := range []string{`{"model":"unknown","input":"hi"}`, `{"model":"gpt-6-astra","input":"hi","reasoning":{"effort":"ultra"}}`, `{"model":"gpt-6-astra","input":"hi","previous_response_id":"resp_old"}`, `{"model":"gpt-6-astra","input":[{"type":"item_reference","id":"old"}]}`, `{"model":"gpt-6-astra","stream":"true","input":"hi"}`} {
		t.Run(body, func(t *testing.T) {
			_, _, _, err := PrepareOpenAIExcelWire(context.Background(), []byte(body), excelTestHeaders(), OpenAIExcelWireOptions{})
			var requestErr *OpenAIExcelRequestError
			require.ErrorAs(t, err, &requestErr)
		})
	}
}

func TestExcelProtocolValidationBeforeAttachments(t *testing.T) {
	for _, body := range []string{
		`{"model":"unsupported","input":[{"role":"user","content":[{"type":"input_image","image_url":"https://images.invalid/picture.png"}]}]}`,
		`{"model":"gpt-6-astra","input":"hi","reasoning":{"effort":"ultra"}}`,
		`{"model":"gpt-6-astra","input":"hi","tools":[{"type":"image_generation"}]}`,
		`{"model":"gpt-6-astra","input":"hi","previous_response_id":"resp_previous"}`,
	} {
		var requestError *OpenAIExcelRequestError
		require.ErrorAs(t, ValidateOpenAIExcelRequest([]byte(body)), &requestError)
	}
	// Pure validation deliberately does not try to restore native tool history.
	require.NoError(t, ValidateOpenAIExcelRequest([]byte(`{"model":"gpt-6-astra","input":[{"type":"reasoning","encrypted_content":"synthetic"},{"type":"function_call","name":"echo","call_id":"missing-native","arguments":"{}"}]}`)))
}

func TestExcelProtocolUsesWindowsExcelIdentity(t *testing.T) {
	for _, ua := range []string{"codex-tui (Windows 11; x86_64)", "codex-tui (Darwin 24; aarch64)", "codex-tui (Linux Debian; x86_64)", "opaque-client"} {
		t.Run(ua, func(t *testing.T) {
			headers := excelTestHeaders()
			headers.Set("User-Agent", ua)
			out, err := PrepareOpenAIExcelHeaders(headers)
			require.NoError(t, err)
			require.Equal(t, openAIExcelUserAgent, out.Get("User-Agent"))
			require.Equal(t, "PC", out.Get("X-OpenAI-Internal-Basispoints-Office-Platform"))
			require.Equal(t, "Windows", out.Get("X-Stainless-OS"))
			require.Equal(t, "x64", out.Get("X-Stainless-Arch"))
		})
	}
}

func TestExcelProtocolPreservesBodyProjectedInstallation(t *testing.T) {
	_, headers, _, err := PrepareOpenAIExcelWire(context.Background(), []byte(`{"model":"gpt-6-astra","input":"hi","client_metadata":{"x-codex-installation-id":"managed-body-installation","ws_request_header_x_openai_internal_codex_responses_lite":"true"}}`), excelTestHeaders(), OpenAIExcelWireOptions{})
	require.NoError(t, err)
	require.Equal(t, "managed-body-installation", headers.Get("X-Codex-Installation-ID"))
	require.Empty(t, headers.Get(responsesLiteHeader))
}

func TestExcelProtocolNativeToolRoundTrip(t *testing.T) {
	history := excelTestHistory()
	_, state := excelTestPrepare(t, `{"model":"gpt-6-astra","input":"inspect","tools":[{"type":"namespace","name":"functions","tools":[{"type":"function","name":"exec","description":"run command","parameters":{"type":"object","required":["cmd"],"additionalProperties":false,"properties":{"cmd":{"type":"string"}}}}]},{"type":"custom","name":"apply_patch","format":{"type":"text"}}]}`, history)
	native := map[string]any{"type": "function_call", "id": "native-item-identity", "call_id": "call-1", "name": "run_officejs", "arguments": `{"code":"{\"name\":\"functions.exec\",\"arguments\":{\"cmd\":\"pwd\"}}","summary":"execute"}`}
	client, err := state.translateNativeCall(context.Background(), native)
	require.NoError(t, err)
	require.Equal(t, "exec", client["name"])
	require.Equal(t, "functions", client["namespace"])
	require.Equal(t, `{"cmd":"pwd"}`, client["arguments"])
	raw, _ := json.Marshal([]any{map[string]any{"type": "reasoning", "encrypted_content": "synthetic-cipher"}, client, map[string]any{"type": "function_call_output", "call_id": "call-1", "output": "workspace"}})
	body := `{"model":"gpt-6-astra","input":` + string(raw) + `}`
	prepared, _ := excelTestPrepare(t, body, history)
	items, ok := prepared["input"].([]any)
	require.True(t, ok)
	restored := openAIExcelMap(items[len(items)-2])
	require.Equal(t, "native-item-identity", restored["id"])
	require.Equal(t, native["arguments"], restored["arguments"])
	require.Equal(t, "run_officejs", restored["name"])
	_, _, _, err = PrepareOpenAIExcelWire(context.Background(), []byte(body), excelTestHeaders(), OpenAIExcelWireOptions{HistoryScope: "another-tenant", History: history})
	require.Error(t, err)
	require.Contains(t, err.Error(), "history is unavailable")
}

func TestExcelProtocolCustomNestedEnvelope(t *testing.T) {
	history := excelTestHistory()
	_, state := excelTestPrepare(t, `{"model":"gpt-6-astra","input":"hi","tools":[{"type":"custom","name":"apply_patch"}]}`, history)
	inner := map[string]any{"name": "apply_patch", "input": "*** Begin Patch\n*** End Patch"}
	code, _ := json.Marshal(inner)
	outerCode, _ := json.Marshal(map[string]any{"name": "functions.run_officejs", "arguments": map[string]any{"code": string(code)}})
	args, _ := json.Marshal(map[string]any{"code": string(outerCode)})
	client, err := state.translateNativeCall(context.Background(), map[string]any{"type": "function_call", "id": "native", "call_id": "call-custom", "name": "run_officejs", "arguments": string(args)})
	require.NoError(t, err)
	require.Equal(t, "custom_tool_call", client["type"])
	require.Equal(t, inner["input"], client["input"])
	items, err := state.translateHistory(context.Background(), []any{client, map[string]any{"type": "custom_tool_call_output", "call_id": "call-custom", "output": "applied"}})
	require.NoError(t, err)
	require.Equal(t, "function_call_output", openAIExcelMap(items[1])["type"])
}

func TestExcelProtocolSchemaAndToolGuards(t *testing.T) {
	history := excelTestHistory()
	_, state := excelTestPrepare(t, `{"model":"gpt-6-astra","input":"hi","tools":[{"type":"function","name":"echo","parameters":{"type":"object","required":["text"],"additionalProperties":false,"properties":{"text":{"type":"string"}}}}]}`, history)
	for _, code := range []string{`{"name":"unknown","arguments":{"text":"hello"}}`, `{"name":"echo","arguments":{"text":42}}`, `{"name":"echo","arguments":{"text":"hello","extra":true}}`, `{"name":"echo","arguments":{}}`} {
		args, _ := json.Marshal(map[string]any{"code": code})
		_, err := state.translateNativeCall(context.Background(), map[string]any{"type": "function_call", "name": "run_officejs", "id": "native", "call_id": "bad", "arguments": string(args)})
		require.Error(t, err)
	}
	require.Empty(t, history.values)
	decoded, err := openAIExcelDecodeEnvelope("```json\n{\"name\":\"echo\",\"arguments\":{\"text\":\"C:\\q\"}}\n```")
	require.NoError(t, err)
	require.Equal(t, `C:\q`, openAIExcelMap(decoded["arguments"])["text"])
}

func TestExcelProtocolHistoryStorageErrorNotClientError(t *testing.T) {
	history := excelTestHistory()
	history.err = errors.New("database details must be hidden")
	_, _, _, err := PrepareOpenAIExcelWire(context.Background(), []byte(`{"model":"gpt-6-astra","input":[{"type":"function_call","name":"echo","call_id":"call1","arguments":"{}"}]}`), excelTestHeaders(), OpenAIExcelWireOptions{History: history, HistoryScope: "scope"})
	require.ErrorIs(t, err, ErrOpenAIExcelHistoryStorageUnavailable)
	var requestErr *OpenAIExcelRequestError
	require.False(t, errors.As(err, &requestErr))
	require.NotContains(t, err.Error(), "database details")
}

func TestExcelProtocolTurnStableAcrossToolRound(t *testing.T) {
	first := []any{openAIExcelMessage("user", "inspect")}
	turn, iteration := openAIExcelTurnIdentity(first)
	require.Equal(t, 1, iteration)
	second := append(first, map[string]any{"type": "function_call", "name": "echo"}, map[string]any{"type": "function_call_output", "call_id": "a"}, map[string]any{"type": "function_call_output", "call_id": "b"})
	next, iteration := openAIExcelTurnIdentity(second)
	require.Equal(t, turn, next)
	require.Equal(t, 2, iteration)
	third := append(second, openAIExcelMessage("user", "again"))
	next, iteration = openAIExcelTurnIdentity(third)
	require.NotEqual(t, turn, next)
	require.Equal(t, 1, iteration)
}

func excelSSE(kind string, payload any) string {
	raw, _ := json.Marshal(payload)
	return "event: " + kind + "\ndata: " + string(raw) + "\n\n"
}
func excelWrappedBody(t *testing.T, state *OpenAIExcelWireState, text string) (string, error) {
	t.Helper()
	resp, err := WrapOpenAIExcelResponse(context.Background(), &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(text))}, state)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	return string(raw), err
}

func TestExcelProtocolSSEToolConversionAndRawModel(t *testing.T) {
	history := excelTestHistory()
	_, state := excelTestPrepare(t, `{"model":"gpt-6-astra","input":"hi","tools":[{"type":"function","name":"echo","parameters":{"type":"object"}}]}`, history)
	var models []string
	state.options.ObserveModel = func(model string) { models = append(models, model) }
	native := map[string]any{"type": "function_call", "id": "native", "call_id": "call1", "name": "run_officejs", "arguments": `{"code":"{\"name\":\"echo\",\"arguments\":{\"text\":\"ok\"}}"}`}
	stream := excelSSE("response.created", map[string]any{"response": map[string]any{"model": "gpt-6-luna", "status": "in_progress"}}) + excelSSE("response.output_item.added", map[string]any{"output_index": 0, "item": native}) + excelSSE("response.function_call_arguments.delta", map[string]any{"output_index": 0, "item_id": "native", "delta": "SECRET_NATIVE_ARGUMENTS"}) + excelSSE("response.output_item.done", map[string]any{"output_index": 0, "item": native}) + excelSSE("response.completed", map[string]any{"response": map[string]any{"model": "gpt-6-luna", "status": "completed", "output": []any{native}, "usage": map[string]any{"input_tokens": 123, "output_tokens": 9}}})
	actual, err := excelWrappedBody(t, state, stream)
	require.NoError(t, err)
	require.NotContains(t, actual, "run_officejs")
	require.NotContains(t, actual, "SECRET_NATIVE_ARGUMENTS")
	require.Contains(t, actual, `"name":"echo"`)
	require.Contains(t, actual, `"input_tokens":123`)
	require.Contains(t, actual, `"model":"gpt-6-luna"`)
	require.Len(t, models, 2)
	require.Equal(t, "gpt-6-luna", models[0])
	require.Len(t, history.values, 1)
}

func TestExcelProtocolSSERequiresRealTerminal(t *testing.T) {
	_, state := excelTestPrepare(t, `{"model":"gpt-6-astra","input":"hi"}`, nil)
	item := map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": "answer"}}}
	for _, stream := range []string{excelSSE("response.output_item.done", map[string]any{"output_index": 0, "item": item}), "data: [DONE]\n\n"} {
		actual, err := excelWrappedBody(t, state, stream)
		require.Error(t, err)
		require.NotContains(t, actual, "response.completed")
		require.Contains(t, actual, "excel_protocol_error")
	}
	actual, err := excelWrappedBody(t, state, excelSSE("response.failed", map[string]any{"response": map[string]any{"status": "failed", "error": map[string]any{"code": "upstream_failed"}}})+excelSSE("response.completed", map[string]any{"response": map[string]any{"status": "completed"}}))
	require.NoError(t, err)
	require.Contains(t, actual, "response.failed")
	require.NotContains(t, actual, "response.completed")
}

func TestExcelProtocolSSEStopsAtCompletedWithoutWaitingForEOF(t *testing.T) {
	_, state := excelTestPrepare(t, `{"model":"gpt-6-astra","input":"hi"}`, nil)
	upstreamReader, upstreamWriter := io.Pipe()
	defer func() { _ = upstreamWriter.Close() }()
	resp, err := WrapOpenAIExcelResponse(context.Background(), &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: upstreamReader}, state)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	go func() {
		_, _ = io.WriteString(upstreamWriter, excelSSE("response.completed", map[string]any{"response": map[string]any{"status": "completed", "output": []any{}}}))
	}()
	done := make(chan error, 1)
	go func() { _, err := io.ReadAll(resp.Body); done <- err }()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("waited for upstream EOF after completion")
	}
}

func TestExcelProtocolAcceptsResponseDoneAsCompleted(t *testing.T) {
	_, state := excelTestPrepare(t, `{"model":"gpt-6-astra","input":"hi"}`, nil)
	actual, err := excelWrappedBody(t, state, excelSSE("response.done", map[string]any{
		"response": map[string]any{"status": "completed", "output": []any{}, "model": "gpt-6-astra"},
	}))
	require.NoError(t, err)
	require.Contains(t, actual, `"type":"response.completed"`)
	require.NotContains(t, actual, `"type":"response.done"`)
}

func TestExcelProtocolCancellationClosesStream(t *testing.T) {
	_, state := excelTestPrepare(t, `{"model":"gpt-6-astra","input":"hi"}`, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reader, writer := io.Pipe()
	defer func() { _ = writer.Close() }()
	resp, err := WrapOpenAIExcelResponse(ctx, &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: reader}, state)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	done := make(chan error, 1)
	go func() { _, err := io.ReadAll(resp.Body); done <- err }()
	cancel()
	select {
	case err := <-done:
		require.Error(t, err)
	case <-time.After(time.Second):
		t.Fatal("canceled Excel stream did not close")
	}
}

func TestExcelProtocolJSONCompletionAndEvidence(t *testing.T) {
	_, state := excelTestPrepare(t, `{"model":"gpt-6-astra","input":"hi"}`, nil)
	var model string
	state.options.ObserveModel = func(value string) { model = value }
	for _, status := range []string{"completed", "incomplete", ""} {
		raw, _ := json.Marshal(map[string]any{"status": status, "model": "gpt-6-luna", "output": []any{}})
		response, err := WrapOpenAIExcelResponse(context.Background(), &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(string(raw)))}, state)
		if status == "completed" {
			require.NoError(t, err)
			defer func() { _ = response.Body.Close() }()
			out, err := io.ReadAll(response.Body)
			require.NoError(t, err)
			require.Contains(t, string(out), "gpt-6-luna")
		} else {
			require.Error(t, err)
		}
	}
	require.Equal(t, "gpt-6-luna", model)
}

func TestExcelProtocolRequiredToolAndNone(t *testing.T) {
	_, required := excelTestPrepare(t, `{"model":"gpt-6-astra","input":"hi","tool_choice":"required","tools":[{"type":"function","name":"echo"}]}`, excelTestHistory())
	require.Error(t, required.translateResponse(context.Background(), map[string]any{"output": []any{openAIExcelMessage("assistant", "text only")}}))
	_, none := excelTestPrepare(t, `{"model":"gpt-6-astra","input":"hi","tool_choice":"none","tools":[{"type":"function","name":"echo"}]}`, excelTestHistory())
	require.Empty(t, none.tools)
	_, err := none.translateNativeCall(context.Background(), map[string]any{"type": "function_call", "name": "echo", "call_id": "call", "arguments": "{}"})
	require.Error(t, err)
}

func TestExcelProtocolNativePlanNormalization(t *testing.T) {
	_, state := excelTestPrepare(t, `{"model":"gpt-6-astra","input":"hi","tools":[{"type":"function","name":"update_plan","parameters":{"type":"object","required":["plan"],"properties":{"plan":{"type":"array","items":{"type":"object","required":["step","status"],"properties":{"step":{"type":"string"},"status":{"type":"string","enum":["pending","in_progress","completed"]}}}}}}}]}`, excelTestHistory())
	call, err := state.translateNativeCall(context.Background(), map[string]any{"type": "function_call", "id": "plan", "call_id": "plan-call", "name": "update_plan", "arguments": `{"summary":"work","plan":[{"description":"inspect","status":"done"}]}`})
	require.NoError(t, err)
	require.JSONEq(t, `{"explanation":"work","plan":[{"step":"inspect","status":"completed"}]}`, openAIExcelString(call["arguments"]))
	items, err := state.translateHistory(context.Background(), []any{call, map[string]any{"type": "function_call_output", "call_id": "plan-call", "output": "Plan updated"}})
	require.NoError(t, err)
	require.Equal(t, `{"status":"ok"}`, openAIExcelMap(items[1])["output"])
}

func TestExcelProtocolSSEOversizedEventFailsClosed(t *testing.T) {
	_, state := excelTestPrepare(t, `{"model":"gpt-6-astra","input":"hi"}`, nil)
	actual, err := excelWrappedBody(t, state, "data: "+strings.Repeat("x", openAIExcelMaxItemBytes+1))
	require.Error(t, err)
	require.NotContains(t, actual, "response.completed")
	require.Contains(t, actual, "excel_protocol_error")
}

func TestExcelProtocolNativeHistoryMissingWithoutCipherRebuilds(t *testing.T) {
	_, state := excelTestPrepare(t, `{"model":"gpt-6-astra","input":"hi"}`, excelTestHistory())
	items, err := state.translateHistory(context.Background(), []any{
		map[string]any{"type": "function_call", "name": "echo", "call_id": "call1", "arguments": `{"text":"hello"}`},
		map[string]any{"type": "function_call_output", "call_id": "call1", "output": "hello"},
	})
	require.NoError(t, err)
	require.Equal(t, "run_officejs", openAIExcelMap(items[0])["name"])
	require.Contains(t, openAIExcelString(openAIExcelMap(items[0])["arguments"]), `\"name\":\"echo\"`)
}

func TestExcelProtocolMixedNativeToolsNeverLeak(t *testing.T) {
	_, state := excelTestPrepare(t, `{"model":"gpt-6-astra","input":"hi","tools":[{"type":"function","name":"echo"}]}`, excelTestHistory())
	valid := map[string]any{"type": "function_call", "id": "valid", "call_id": "call-valid", "name": "run_officejs", "arguments": `{"code":"{\"name\":\"echo\",\"arguments\":{}}"}`}
	unknown := map[string]any{"type": "function_call", "id": "unknown", "call_id": "call-unknown", "name": "delete_workbook", "arguments": `{"secret":"NEVER_FORWARD"}`}
	for _, terminal := range []string{"response.completed", "response.failed", "response.incomplete"} {
		t.Run(terminal, func(t *testing.T) {
			stream := excelSSE("response.in_progress", map[string]any{"response": map[string]any{"output": []any{unknown}}}) +
				excelSSE("response.output_item.added", map[string]any{"output_index": 0, "item": valid}) +
				excelSSE("response.output_item.added", map[string]any{"output_index": 1, "item": unknown}) +
				excelSSE(terminal, map[string]any{"response": map[string]any{"status": strings.TrimPrefix(terminal, "response."), "output": []any{valid, unknown}}})
			actual, err := excelWrappedBody(t, state, stream)
			if terminal == "response.completed" {
				require.Error(t, err)
				require.NotContains(t, actual, "response.completed")
			} else {
				require.NoError(t, err)
			}
			require.NotContains(t, actual, "delete_workbook")
			require.NotContains(t, actual, "NEVER_FORWARD")
			require.NotContains(t, actual, "run_officejs")
			require.NotContains(t, actual, "response.function_call_arguments")
		})
	}
}

func TestExcelProtocolMultipleToolsCompletedOutputMatchesEvents(t *testing.T) {
	_, state := excelTestPrepare(t, `{"model":"gpt-6-astra","input":"hi","tools":[{"type":"function","name":"echo"},{"type":"custom","name":"patch"}]}`, excelTestHistory())
	function := map[string]any{"type": "function_call", "id": "native1", "call_id": "call1", "name": "run_officejs", "arguments": `{"code":"{\"name\":\"echo\",\"arguments\":{\"text\":\"one\"}}"}`}
	custom := map[string]any{"type": "function_call", "id": "native2", "call_id": "call2", "name": "run_officejs", "arguments": `{"code":"{\"name\":\"patch\",\"input\":\"two\"}"}`}
	actual, err := excelWrappedBody(t, state, excelSSE("response.completed", map[string]any{"response": map[string]any{"status": "completed", "output": []any{function, custom}}}))
	require.NoError(t, err)
	require.Contains(t, actual, `"type":"function_call"`)
	require.Contains(t, actual, `"type":"custom_tool_call"`)
	require.Contains(t, actual, `"output_index":1`)
	require.NotContains(t, actual, "run_officejs")
	state.parallel = false
	actual, err = excelWrappedBody(t, state, excelSSE("response.completed", map[string]any{"response": map[string]any{"status": "completed", "output": []any{function, custom}}}))
	require.Error(t, err)
	require.NotContains(t, actual, "response.completed")
	require.NotContains(t, actual, "response.function_call_arguments")
}
