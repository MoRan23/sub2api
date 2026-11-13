package service

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCodexStandaloneFunctionOutputShapes(t *testing.T) {
	for _, tc := range []struct {
		name, item string
		want       bool
	}{
		{"plan", `{"type":"function_call_output","id":"fco_plan","name":"proposed_plan","namespace":"agent","output":"plan"}`, true},
		{"unnamespaced", `{"type":"function_call_output","name":"context","output":""}`, true},
		{"nulls", `{"type":"function_call_output","call_id":null,"namespace":null,"name":" context ","output":[]}`, true},
		{"structured", `{"type":"function_call_output","name":"context","output":[{"type":"input_text","text":"hello"}]}`, true},
		{"escaped key", `{"type":"function_call_output","name":"context","out\u0070ut":"text"}`, true},
		{"fully escaped key", `{"type":"function_call_output","name":"context","\u006f\u0075\u0074\u0070\u0075\u0074":"text"}`, true},
		{"output last", `{"type":"function_call_output","name":"context","extra":{"output":null,"nested":[{"text":"ignored"}]},"output":[]}`, true},
		{"empty call", `{"type":"function_call_output","call_id":"","name":"context","output":"text"}`, false},
		{"blank call", `{"type":"function_call_output","call_id":"  ","name":"context","output":"text"}`, false},
		{"numeric call", `{"type":"function_call_output","call_id":12,"name":"context","output":"text"}`, false},
		{"real call", `{"type":"function_call_output","call_id":"call_1","name":"context","output":"text"}`, false},
		{"missing name", `{"type":"function_call_output","output":"text"}`, false},
		{"blank name", `{"type":"function_call_output","name":"  ","output":"text"}`, false},
		{"numeric name", `{"type":"function_call_output","name":12,"output":"text"}`, false},
		{"numeric namespace", `{"type":"function_call_output","name":"context","namespace":12,"output":"text"}`, false},
		{"missing output", `{"type":"function_call_output","name":"context"}`, false},
		{"null output", `{"type":"function_call_output","name":"context","output":null}`, false},
		{"object output", `{"type":"function_call_output","name":"context","output":{"text":"text"}}`, false},
		{"numeric output", `{"type":"function_call_output","name":"context","output":12}`, false},
		{"nested output only", `{"type":"function_call_output","name":"context","extra":{"output":"text"}}`, false},
		{"custom output", `{"type":"custom_tool_call_output","name":"context","output":"text"}`, false},
		{"mcp output", `{"type":"mcp_tool_call_output","name":"context","output":"text"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var item map[string]any
			require.NoError(t, json.Unmarshal([]byte(tc.item), &item))
			require.Equal(t, tc.want, isCodexStandaloneFunctionOutput(item))
			require.Equal(t, tc.want, isCodexStandaloneFunctionOutputRaw(parseRawJSONView([]byte(tc.item))))
		})
	}
}

func TestApplyCodexOAuthTransform_StandaloneFunctionOutput(t *testing.T) {
	for _, output := range []string{`"plan"`, `[{"type":"input_text","text":"plan"}]`} {
		for _, callID := range []string{"", `,"call_id":null`} {
			raw := `{"type":"function_call_output","id":"call_context","namespace":"agent","name":"proposed_plan","output":` + output + callID + `}`
			var item map[string]any
			require.NoError(t, json.Unmarshal([]byte(raw), &item))
			body := map[string]any{"model": "gpt-6.1-sol", "input": []any{item}}
			applyCodexOAuthTransform(body, false, false)
			input := body["input"].([]any)
			require.Len(t, input, 1)
			actual, err := json.Marshal(input[0])
			require.NoError(t, err)
			require.JSONEq(t, raw, string(actual))
			require.False(t, HasFunctionCallOutput(body))
		}
	}
}

func TestCodexStandaloneFunctionOutputContinuation(t *testing.T) {
	standalone := json.RawMessage(`{"type":"function_call_output","id":"fco_plan","namespace":"agent","name":"proposed_plan","output":"plan"}`)
	for _, tc := range []struct {
		name, tail               string
		output, missing, covered bool
	}{
		{"only standalone", "", false, false, false},
		{"real orphan", `,{"type":"function_call_output","call_id":"call_real","output":"result"}`, true, false, false},
		{"missing real call", `,{"type":"function_call_output","output":"result"}`, true, true, false},
		{"paired call", `,{"type":"function_call","call_id":"call_real","name":"read_file","arguments":"{}"},{"type":"function_call_output","call_id":"call_real","output":"result"}`, true, false, true},
		{"partial history", `,{"type":"function_call","call_id":"call_real","name":"read_file","arguments":"{}"},{"type":"function_call_output","call_id":"call_other","output":"result"}`, true, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte(`{"input":[` + string(standalone) + tc.tail + `]}`)
			var request map[string]any
			require.NoError(t, json.Unmarshal(body, &request))
			signals := AnalyzeToolContinuationSignals(request)
			require.Equal(t, tc.output, signals.HasFunctionCallOutput)
			require.Equal(t, tc.missing, signals.HasFunctionCallOutputMissingCallID)
			require.Equal(t, ValidateFunctionCallOutputContext(request), ValidateFunctionCallOutputContextBytes(body))
			coverage := AnalyzeToolCallOutputContextCoverageBytes(body)
			require.Equal(t, tc.output, coverage.HasFunctionCallOutput)
			require.Equal(t, tc.covered, coverage.ContextCoversAllCallIDs)
			require.Equal(t, tc.output, openAIWSFrameContinuesCurrentTurn(body))
			var items struct {
				Input []json.RawMessage `json:"input"`
			}
			require.NoError(t, json.Unmarshal(body, &items))
			require.Equal(t, tc.output, openAIWSRawItemsHasFunctionCallOutput(items.Input))
			require.Equal(t, tc.covered, openAIWSRawItemsHaveToolCallContextForOutputs(items.Input))
			if !tc.output {
				require.False(t, NeedsToolContinuation(request))
			}
		})
	}
}

func TestCodexStandaloneFunctionOutputPreservesIdentityAndPayload(t *testing.T) {
	item := map[string]any{"type": "function_call_output", "id": "call_standalone", "call_id": nil,
		"namespace": "agent", "name": "proposed_plan", "output": []any{map[string]any{"type": "input_text", "text": "plan"}}}
	before, err := json.Marshal(item)
	require.NoError(t, err)
	for _, preserve := range []bool{false, true} {
		input := []any{item}
		filtered := filterCodexInputWithOptions(input, codexInputFilterOptions{PreserveReferences: preserve})
		require.Equal(t, input, filtered)
		require.Equal(t, fmt.Sprintf("%p", item), fmt.Sprintf("%p", filtered[0]), "unchanged item is shared read-only")
		body := map[string]any{"input": input}
		rules := map[string]bool{}
		canonicalizeRequestIntegrityCodex(body, rules)
		require.Equal(t, input, body["input"])
		require.False(t, rules["tool_call_identity"])
		require.False(t, sanitizeOpenAIResponsesOrphanToolOutputs(body, input, false))
	}
	after, err := json.Marshal(item)
	require.NoError(t, err)
	require.Equal(t, before, after)
	refs := []any{item, map[string]any{"type": "item_reference", "id": "call_standalone"}}
	filtered := filterCodexInput(refs, true)
	require.Equal(t, "call_standalone", filtered[1].(map[string]any)["id"])
}

func TestCodexStandaloneFunctionOutputRawDoesNotDecodeLargePayload(t *testing.T) {
	for _, size := range []int{64, 1024 * 1024} {
		raw := []byte(`{"type":"function_call_output","name":"context","output":"` + strings.Repeat(`\n`, size) + `"}`)
		item := parseRawJSONView(raw)
		require.True(t, isCodexStandaloneFunctionOutputRaw(item))
		require.Zero(t, testing.AllocsPerRun(5, func() { _ = isCodexStandaloneFunctionOutputRaw(item) }))
	}
}

func BenchmarkCodexStandaloneFunctionOutputRaw(b *testing.B) {
	for _, size := range []int{64 * 1024, 1024 * 1024, 8 * 1024 * 1024} {
		b.Run(fmt.Sprint(size), func(b *testing.B) {
			raw := []byte(`{"type":"function_call_output","name":"context","output":"` + strings.Repeat(`\n`, size/2) + `"}`)
			item := parseRawJSONView(raw)
			b.SetBytes(int64(len(raw)))
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				isCodexStandaloneFunctionOutputRaw(item)
			}
		})
	}
}
