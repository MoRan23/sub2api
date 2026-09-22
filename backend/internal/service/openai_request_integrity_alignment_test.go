package service

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOpenAIRequestIntegrityPromotesSystemAfterPrivateMetadataRemoval(t *testing.T) {
	for _, jsonObject := range []bool{false, true} {
		t.Run(fmt.Sprintf("json_object=%t", jsonObject), func(t *testing.T) {
			baseline := map[string]any{"input": []any{
				map[string]any{"role": "system", "content": "Follow user.", "internal_chat_message_metadata_passthrough": map[string]any{"source": "private"}},
				map[string]any{"role": "user", "content": "Hi"},
			}}
			if jsonObject {
				baseline["text"] = map[string]any{"format": map[string]any{"type": "json_object"}}
			}
			wire := cloneRequestIntegrityValue(baseline).(map[string]any)
			applyCodexOAuthTransformWithOptions(wire, codexOAuthTransformOptions{OmitPromotedSystemMessagesFromInput: !jsonObject})
			got := NewOpenAIRequestIntegrityState(true, "responses", integrityTestJSON(t, baseline)).Check(integrityTestAccount(), integrityTestJSON(t, wire), RequestIntegrityCheckOptions{})
			require.Equal(t, "expected_transform", got.Status, "%+v", got)
			require.Empty(t, got.ChangedFields)
			require.Contains(t, got.RuleCodes, "codex_input_metadata_removed")
			require.Contains(t, got.RuleCodes, "system_instruction_promotion")
			require.NotContains(t, string(integrityTestJSON(t, got)), "private")
		})
	}

	// Removing the specifically approved private field must not approve the
	// simultaneous loss of unrelated system-message attributes.
	baseline := []byte(`{"input":[{"role":"system","content":"Follow user.","internal_chat_message_metadata_passthrough":{},"annotation":"retain"},{"role":"user","content":"Hi"}]}`)
	wire := []byte(`{"instructions":"Follow user.","input":[{"role":"user","content":"Hi"}]}`)
	got := NewOpenAIRequestIntegrityState(true, "responses", baseline).Check(integrityTestAccount(), wire, RequestIntegrityCheckOptions{})
	require.Equal(t, "difference", got.Status)
	require.NotEmpty(t, got.ChangedFields)
}

func TestOpenAIRequestIntegrityInputAlignmentReportsStructuralChangesOnce(t *testing.T) {
	message := func(text string) map[string]any {
		return map[string]any{"type": "message", "role": "user", "content": text}
	}
	a, b, d := message("first private text"), message("second private text"), message("third private text")
	reasoning := map[string]any{"type": "reasoning", "encrypted_content": "private-encrypted-reasoning", "summary": []any{}}
	call := func(arguments string) map[string]any {
		return map[string]any{"type": "function_call", "call_id": "fc_private", "name": "private_tool", "arguments": arguments}
	}
	for _, test := range []struct {
		name          string
		before, after []any
		fields        []string
	}{
		{"delete middle replay", []any{a, reasoning, b, d}, []any{a, b, d}, []string{"input.before[1]"}},
		{"insert middle item", []any{a, b, d}, []any{a, reasoning, b, d}, []string{"input.after[1]"}},
		{"move item", []any{a, b, d}, []any{b, a, d}, []string{"input.before[0]", "input.after[1]"}},
		{"tool arguments changed", []any{a, call(`{"text":"old private argument"}`), d}, []any{a, call(`{"text":"new private argument"}`), d}, []string{"input[1].arguments"}},
		{"shift then edit", []any{a, call(`{"text":"old private argument"}`), d}, []any{b, a, call(`{"text":"new private argument"}`), d}, []string{"input.after[0]", "input.before[1].after[2].arguments"}},
		{"duplicate item removed", []any{a, a, b}, []any{a, b}, []string{"input.before[1]"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			baseline := integrityTestJSON(t, map[string]any{"input": test.before})
			wire := integrityTestJSON(t, map[string]any{"input": test.after})
			got := NewOpenAIRequestIntegrityState(true, "responses", baseline).Check(integrityTestAccount(), wire, RequestIntegrityCheckOptions{})
			require.Equal(t, "difference", got.Status)
			require.Equal(t, "content_changed", got.Reason)
			require.Equal(t, test.fields, got.ChangedFields)
			require.False(t, got.Truncated)
			require.NotContains(t, string(integrityTestJSON(t, got)), "private")
		})
	}
}

func TestOpenAIRequestIntegrityInputAlignmentIsBounded(t *testing.T) {
	for _, count := range []int{256, 257} {
		t.Run(fmt.Sprintf("items=%d", count), func(t *testing.T) {
			before, after := make([]any, count), make([]any, count)
			for i := range before {
				before[i] = map[string]any{"role": "user", "content": fmt.Sprintf("before-%d", i)}
				after[i] = map[string]any{"role": "user", "content": fmt.Sprintf("after-%d", i)}
			}
			got := NewOpenAIRequestIntegrityState(true, "responses", integrityTestJSON(t, map[string]any{"input": before})).Check(integrityTestAccount(), integrityTestJSON(t, map[string]any{"input": after}), RequestIntegrityCheckOptions{})
			require.Equal(t, "difference", got.Status)
			if count == 256 {
				require.Len(t, got.ChangedFields, requestIntegrityMaxDifferences)
				require.True(t, got.Truncated)
			} else {
				require.Equal(t, []string{"input"}, got.ChangedFields)
				require.False(t, got.Truncated)
			}
		})
	}
	// A long unchanged prefix and suffix do not consume the quadratic budget.
	before := make([]any, 900)
	for i := range before {
		before[i] = map[string]any{"role": "user", "content": fmt.Sprintf("message-%d", i)}
	}
	after := append([]any(nil), before[:450]...)
	after = append(after, before[451:]...)
	got := NewOpenAIRequestIntegrityState(true, "responses", integrityTestJSON(t, map[string]any{"input": before})).Check(integrityTestAccount(), integrityTestJSON(t, map[string]any{"input": after}), RequestIntegrityCheckOptions{})
	require.Equal(t, "difference", got.Status)
	require.Equal(t, []string{"input.before[450]"}, got.ChangedFields)
}

func TestOpenAIRequestIntegrityInputPathsRetainOriginalIndicesAfterPromotion(t *testing.T) {
	for _, test := range []struct {
		name, baseline, wire string
		fields               []string
	}{
		{
			"delete after promoted system",
			`{"input":[{"role":"system","content":"S"},{"type":"reasoning","encrypted_content":"private","summary":[]},{"role":"user","content":"U"}]}`,
			`{"instructions":"S","input":[{"role":"user","content":"U"}]}`,
			[]string{"input.before[1]"},
		},
		{
			"edit after promoted system",
			`{"input":[{"role":"system","content":"S"},{"role":"user","content":"old"}]}`,
			`{"instructions":"S","input":[{"role":"user","content":"new"}]}`,
			[]string{"input.before[1].after[0].content[0].text"},
		},
		{
			"outbound normalization also keeps indices",
			`{"instructions":"S","input":[{"role":"user","content":"old"}]}`,
			`{"input":[{"role":"system","content":"S"},{"role":"user","content":"new"}]}`,
			[]string{"input.before[0].after[1].content[0].text"},
		},
		{
			"scalar input has no original array index",
			`{"input":"old"}`,
			`{"input":[{"role":"user","content":"new"}]}`,
			[]string{"input"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := NewOpenAIRequestIntegrityState(true, "responses", []byte(test.baseline)).Check(integrityTestAccount(), []byte(test.wire), RequestIntegrityCheckOptions{})
			require.Equal(t, "difference", got.Status)
			require.Equal(t, test.fields, got.ChangedFields)
		})
	}

	baseline := map[string]any{"input": []any{map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": "old"}}}}}
	wire := cloneRequestIntegrityValue(baseline).(map[string]any)
	input := wire["input"].([]any)
	input[0].(map[string]any)["content"].([]any)[0].(map[string]any)["text"] = "new"
	require.True(t, appendOpenAICompatClaudeCodeTodoGuardToRequestBody(wire))
	got := NewOpenAIRequestIntegrityState(true, "messages", integrityTestJSON(t, baseline)).Check(integrityTestAccount(), integrityTestJSON(t, wire), RequestIntegrityCheckOptions{CompatTodoGuard: true})
	require.Equal(t, "difference", got.Status)
	require.Equal(t, []string{"input.before[0].after[1].content[0].text"}, got.ChangedFields)
}

func TestOpenAIRequestIntegrityOnlyApprovesExactModelDefaultInstructions(t *testing.T) {
	const model = "gpt-5.6-sol"
	for _, test := range []struct {
		name         string
		baseline     map[string]any
		instructions string
		status       string
	}{
		{"missing", map[string]any{"model": model, "input": "Hi"}, defaultCodexSynthInstructions(model), "expected_transform"},
		{"blank", map[string]any{"model": model, "input": "Hi", "instructions": " \n "}, defaultCodexSynthInstructions(model), "expected_transform"},
		{"custom injected", map[string]any{"model": model, "input": "Hi"}, "Arbitrary injected instruction.", "difference"},
		{"default plus suffix", map[string]any{"model": model, "input": "Hi"}, defaultCodexSynthInstructions(model) + "\nExtra instruction.", "difference"},
		{"user overwritten", map[string]any{"model": model, "input": "Hi", "instructions": "Keep my instruction."}, defaultCodexSynthInstructions(model), "difference"},
		{"developer present", map[string]any{"model": model, "input": []any{map[string]any{"role": "developer", "content": "Keep my instruction."}, map[string]any{"role": "user", "content": "Hi"}}}, defaultCodexSynthInstructions(model), "difference"},
	} {
		t.Run(test.name, func(t *testing.T) {
			wire := cloneRequestIntegrityValue(test.baseline).(map[string]any)
			wire["instructions"] = test.instructions
			got := NewOpenAIRequestIntegrityState(true, "responses", integrityTestJSON(t, test.baseline)).Check(integrityTestAccount(), integrityTestJSON(t, wire), RequestIntegrityCheckOptions{})
			require.Equal(t, test.status, got.Status, "%+v", got)
			if test.status == "expected_transform" {
				require.Contains(t, got.RuleCodes, "codex_default_instructions")
				require.Empty(t, got.ChangedFields)
			} else {
				require.Contains(t, got.ChangedFields, "instructions")
				require.NotContains(t, got.RuleCodes, "codex_default_instructions")
			}
		})
	}

	baseline := []byte(`{"model":"public-alias","input":"Hi"}`)
	wire := integrityTestJSON(t, map[string]any{"model": model, "input": "Hi", "instructions": defaultCodexSynthInstructions(model)})
	got := NewOpenAIRequestIntegrityState(true, "responses", baseline).Check(integrityTestAccount(), wire, RequestIntegrityCheckOptions{ExpectedModel: model})
	require.Equal(t, "expected_transform", got.Status)
	require.Contains(t, got.RuleCodes, "account_model_mapping")
	require.Contains(t, got.RuleCodes, "codex_default_instructions")
	require.NotContains(t, string(integrityTestJSON(t, got)), defaultCodexSynthInstructions(model))
}
