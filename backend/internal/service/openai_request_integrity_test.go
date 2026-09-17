package service

import (
	"bytes"
	"encoding/json"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/stretchr/testify/require"
)

func integrityTestAccount() *Account {
	return &Account{ID: 71, Platform: "openai", Type: "oauth"}
}

func integrityTestJSON(t *testing.T, value any) []byte {
	t.Helper()
	body, err := json.Marshal(value)
	require.NoError(t, err)
	return body
}

func TestOpenAIRequestIntegrityDisabledAndBaselineStages(t *testing.T) {
	body := []byte(`{"model":"gpt-5","input":"hello"}`)
	disabled := NewOpenAIRequestIntegrityState(false, "responses", body)
	require.Nil(t, disabled.Check(integrityTestAccount(), []byte(`not JSON`), RequestIntegrityCheckOptions{}))

	for _, tc := range []struct{ protocol, stage string }{
		{"responses", "ingress"},
		{"messages", "responses_adapter_output"},
		{"chat_completions", "responses_adapter_output"},
	} {
		t.Run(tc.protocol, func(t *testing.T) {
			state := NewOpenAIRequestIntegrityState(true, tc.protocol, body)
			got := state.Check(integrityTestAccount(), body, RequestIntegrityCheckOptions{Transport: "http"})
			require.NotNil(t, got)
			require.Equal(t, "observe", got.Mode)
			require.Equal(t, "unchanged", got.Status)
			require.Equal(t, tc.protocol, got.BaselineProtocol)
			require.Equal(t, tc.stage, got.BaselineStage)
			require.Equal(t, int64(1), got.Attempt)
			require.Equal(t, "http", got.Transport)
			require.Empty(t, got.ChangedFields)
		})
	}
}

func TestOpenAIRequestIntegrityDoesNotMutateOrRetainCallerBodies(t *testing.T) {
	original := []byte(`{"model":"gpt-5","input":"original"}`)
	baseline := bytes.Clone(original)
	state := NewOpenAIRequestIntegrityState(true, "responses", baseline)
	copy(baseline, bytes.ReplaceAll(baseline, []byte("original"), []byte("modified")))
	wire := bytes.Clone(original)
	got := state.Check(integrityTestAccount(), wire, RequestIntegrityCheckOptions{})
	require.Equal(t, "unchanged", got.Status, "the ingress body must be snapshotted before subsequent adaptation")
	require.Equal(t, original, wire)

	copy(wire, bytes.ReplaceAll(wire, []byte("original"), []byte("modified")))
	got = state.Check(integrityTestAccount(), wire, RequestIntegrityCheckOptions{})
	require.Equal(t, "difference", got.Status)
	require.Equal(t, int64(2), got.Attempt)
	require.Equal(t, []byte(`{"model":"gpt-5","input":"modified"}`), wire, "observation must not rewrite the outbound request")
	got = state.Check(integrityTestAccount(), original, RequestIntegrityCheckOptions{})
	require.Equal(t, "unchanged", got.Status, "checking a retry must not replace the ingress baseline")
}

func TestOpenAIRequestIntegrityIgnoresTransportAndIdentityMetadata(t *testing.T) {
	baseline := []byte(`{"model":"gpt-5","input":"hello","session_id":"client-session","thread_id":"client-thread","metadata":{"token":"secret-a"},"stream":false,"store":true}`)
	wire := []byte(`{"model":"gpt-5","input":"hello","session_id":"daily-root","thread_id":"child-thread","metadata":{"token":"secret-b","window_id":"child:0"},"stream":true,"store":false}`)
	got := NewOpenAIRequestIntegrityState(true, "responses", baseline).Check(integrityTestAccount(), wire, RequestIntegrityCheckOptions{})
	require.Equal(t, "unchanged", got.Status)
	require.Empty(t, got.ChangedFields)
}

func TestOpenAIRequestIntegrityModelMappingRequiresExplicitExpectedValue(t *testing.T) {
	baseline := []byte(`{"model":"public-model","input":"hello"}`)
	for _, tc := range []struct{ name, expected, actual, status string }{
		{"known mapping", "gpt-5", "gpt-5", "expected_transform"},
		{"different wire model", "gpt-5", "unapproved-model", "difference"},
		{"no mapping evidence", "", "unapproved-model", "difference"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wire := integrityTestJSON(t, map[string]any{"model": tc.actual, "input": "hello"})
			got := NewOpenAIRequestIntegrityState(true, "responses", baseline).Check(integrityTestAccount(), wire, RequestIntegrityCheckOptions{ExpectedModel: tc.expected})
			require.Equal(t, tc.status, got.Status)
			if tc.status == "expected_transform" {
				require.NotEmpty(t, got.RuleCodes)
			}
		})
	}
}

func TestOpenAIRequestIntegrityContentLossIsNeverApprovedByRecoveryLabel(t *testing.T) {
	for _, tc := range []struct{ name, baseline, wire string }{
		{"instructions", `{"input":"hello","instructions":"retain this instruction"}`, `{"input":"hello"}`},
		{"tools", `{"input":"hello","tools":[{"type":"function","name":"lookup"}]}`, `{"input":"hello","tools":[]}`},
		{"reasoning", `{"input":"hello","reasoning":{"effort":"high"}}`, `{"input":"hello"}`},
		{"previous response", `{"input":"hello","previous_response_id":"resp_actual"}`, `{"input":"hello"}`},
		{"encrypted reasoning", `{"input":[{"type":"reasoning","encrypted_content":"private-reasoning"},{"role":"user","content":"hello"}]}`, `{"input":[{"role":"user","content":"hello"}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := NewOpenAIRequestIntegrityState(true, "responses", []byte(tc.baseline)).Check(integrityTestAccount(), []byte(tc.wire), RequestIntegrityCheckOptions{KnownRecovery: "invalid_encrypted_content"})
			require.Equal(t, "difference", got.Status)
			require.NotEmpty(t, got.ChangedFields)
		})
	}
}

func TestOpenAIRequestIntegrityAcceptsOnlyLosslessProtocolNormalizations(t *testing.T) {
	for _, tc := range []struct{ name, baseline, wire string }{
		{"string to message", `{"input":"hello"}`, `{"input":[{"role":"user","content":"hello"}]}`},
		{"string to input text", `{"input":"hello"}`, `{"input":[{"role":"user","content":[{"type":"input_text","text":"hello"}]}]}`},
		{"reasoning minimal", `{"input":"hello","reasoning":{"effort":"minimal"}}`, `{"input":"hello","reasoning":{"effort":"none"}}`},
		{"system promotion", `{"instructions":"existing instruction","input":[{"role":"system","content":"system instruction"},{"role":"user","content":"hello"}]}`, `{"instructions":"system instruction\n\nexisting instruction","input":[{"role":"user","content":"hello"}]}`},
		{"tool result shape", `{"input":[{"role":"tool","tool_call_id":"fc_123","content":"tool result"}]}`, `{"input":[{"type":"function_call_output","call_id":"fc_123","output":"tool result"}]}`},
		{"legacy function", `{"input":"hello","functions":[{"name":"lookup","description":"Find a record","parameters":{"type":"object","properties":{}}}],"function_call":{"name":"lookup"}}`, `{"input":"hello","tools":[{"type":"function","name":"lookup","description":"Find a record","parameters":{"type":"object","properties":{}}}],"tool_choice":{"type":"function","name":"lookup"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := NewOpenAIRequestIntegrityState(true, "responses", []byte(tc.baseline)).Check(integrityTestAccount(), []byte(tc.wire), RequestIntegrityCheckOptions{})
			require.Equal(t, "expected_transform", got.Status)
			require.NotEmpty(t, got.RuleCodes)
		})
	}

	got := NewOpenAIRequestIntegrityState(true, "responses", []byte(`{"input":"hello"}`)).Check(integrityTestAccount(), []byte(`{"input":[{"role":"user","content":[{"type":"input_text","text":"different"}]}]}`), RequestIntegrityCheckOptions{})
	require.Equal(t, "difference", got.Status)
	got = NewOpenAIRequestIntegrityState(true, "responses", []byte(`{"input":[{"role":"system","content":[{"type":"input_text","text":"instruction","annotation":"preserve"}]}]}`)).Check(integrityTestAccount(), []byte(`{"instructions":"instruction","input":[]}`), RequestIntegrityCheckOptions{})
	require.Equal(t, "difference", got.Status, "system promotion must not discard unrecognized content attributes")
	got = NewOpenAIRequestIntegrityState(true, "responses", []byte(`{"input":[{"role":"tool","tool_call_id":"fc_123","content":"result","annotation":"preserve"}]}`)).Check(integrityTestAccount(), []byte(`{"input":[{"type":"function_call_output","call_id":"fc_123","output":"result"}]}`), RequestIntegrityCheckOptions{})
	require.Equal(t, "difference", got.Status, "tool-role normalization must not silently discard unknown attributes")
}

func TestOpenAIRequestIntegrityUnsupportedOutputLimitsAreOAuthSpecific(t *testing.T) {
	baseline := []byte(`{"input":"hello","max_output_tokens":128,"max_completion_tokens":128}`)
	wire := []byte(`{"input":"hello"}`)
	got := NewOpenAIRequestIntegrityState(true, "responses", baseline).Check(integrityTestAccount(), wire, RequestIntegrityCheckOptions{})
	require.Equal(t, "expected_transform", got.Status)

	apiKeyAccount := &Account{ID: 72, Platform: "openai", Type: "apikey"}
	got = NewOpenAIRequestIntegrityState(true, "responses", baseline).Check(apiKeyAccount, wire, RequestIntegrityCheckOptions{})
	require.Equal(t, "difference", got.Status)
	got = NewOpenAIRequestIntegrityState(true, "responses", []byte(`{"input":"hello","max_tokens":128}`)).Check(integrityTestAccount(), wire, RequestIntegrityCheckOptions{})
	require.Equal(t, "difference", got.Status, "the Codex exception must not approve unrelated token-limit removal")
}

func TestOpenAIRequestIntegrityCompatTodoGuardRequiresExactMessagesEvidence(t *testing.T) {
	baseline := []byte(`{"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}]}`)
	for _, tc := range []struct {
		name, protocol, status string
		flag                   bool
		mutate                 func(map[string]any)
	}{
		{name: "actual Messages guard", protocol: "messages", status: "expected_transform", flag: true},
		{name: "missing insertion evidence", protocol: "messages", status: "difference", flag: false},
		{
			name: "modified guard text", protocol: "messages", status: "difference", flag: true,
			mutate: func(body map[string]any) {
				guard := body["input"].([]any)[0].(map[string]any)
				part := guard["content"].([]any)[0].(map[string]any)
				part["text"] = openAICompatClaudeCodeTodoGuardText + "\nUnapproved additional instruction."
			},
		},
		{name: "native Responses cannot claim Messages adaptation", protocol: "responses", status: "difference", flag: true},
		{
			name: "additional developer message", protocol: "messages", status: "difference", flag: true,
			mutate: func(body map[string]any) {
				body["input"] = append(body["input"].([]any), map[string]any{
					"type": "message", "role": "developer",
					"content": []any{map[string]any{"type": "input_text", "text": "Unapproved additional instruction."}},
				})
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var body map[string]any
			require.NoError(t, json.Unmarshal(baseline, &body))
			require.True(t, appendOpenAICompatClaudeCodeTodoGuardToRequestBody(body))
			if tc.mutate != nil {
				tc.mutate(body)
			}
			wire := integrityTestJSON(t, body)
			got := NewOpenAIRequestIntegrityState(true, tc.protocol, baseline).Check(integrityTestAccount(), wire, RequestIntegrityCheckOptions{CompatTodoGuard: tc.flag})
			require.Equal(t, tc.status, got.Status)
			if tc.status == "expected_transform" {
				require.Contains(t, got.RuleCodes, "compat_todo_guard")
				require.Empty(t, got.ChangedFields)
			} else {
				require.NotEmpty(t, got.ChangedFields)
			}
		})
	}
}

func TestOpenAIRequestIntegrityRejectsInvalidOrAmbiguousJSON(t *testing.T) {
	valid := []byte(`{"input":"hello"}`)
	for _, bad := range []string{
		``,
		`not-json-private-prompt`,
		`{"input":`,
		`{"input":"hello","input":"secret"}`,
		`{"input":"hello","tools":[{"type":"function","name":"a","name":"b"}]}`,
		`{"input":"hello"} {"input":"secret"}`,
		`["hello"]`,
		`null`,
	} {
		for _, malformedBaseline := range []bool{true, false} {
			baseline, wire := valid, []byte(bad)
			if malformedBaseline {
				baseline, wire = []byte(bad), valid
			}
			got := NewOpenAIRequestIntegrityState(true, "responses", baseline).Check(integrityTestAccount(), wire, RequestIntegrityCheckOptions{})
			require.Equal(t, "skipped", got.Status, "ambiguous input must never be reported as unchanged")
			require.NotEmpty(t, got.Reason)
			require.NotContains(t, got.Reason, "private-prompt")
			require.NotContains(t, got.Reason, "secret")
		}
	}
}

func TestOpenAIRequestIntegrityResourceLimitsApplyToBothBodies(t *testing.T) {
	valid := []byte(`{"input":"hello"}`)
	large := []byte(`{"input":"` + strings.Repeat("x", 8*1024*1024) + `"}`)
	deep := []byte(`{"input":` + strings.Repeat("[", 130) + `0` + strings.Repeat("]", 130) + `}`)
	manyNodes := []byte(`{"input":[` + strings.Repeat(`0,`, 17000) + `0]}`)
	for _, tc := range []struct {
		name string
		body []byte
	}{
		{"bytes", large}, {"depth", deep}, {"nodes", manyNodes},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, oversizedBaseline := range []bool{true, false} {
				baseline, wire := valid, tc.body
				if oversizedBaseline {
					baseline, wire = tc.body, valid
				}
				before := bytes.Clone(wire)
				got := NewOpenAIRequestIntegrityState(true, "responses", baseline).Check(integrityTestAccount(), wire, RequestIntegrityCheckOptions{})
				require.Equal(t, "skipped", got.Status)
				require.NotEmpty(t, got.Reason)
				require.Equal(t, before, wire)
			}
		})
	}
}

func TestOpenAIRequestIntegrityBoundsDifferencesAndRedactsUntrustedKeys(t *testing.T) {
	beforeInput, afterInput := make([]any, 64), make([]any, 64)
	for i := range beforeInput {
		beforeInput[i] = map[string]any{"role": "user", "content": "private-before"}
		afterInput[i] = map[string]any{"role": "user", "content": "private-after"}
	}
	baseline := integrityTestJSON(t, map[string]any{"input": beforeInput})
	wire := integrityTestJSON(t, map[string]any{"input": afterInput})
	got := NewOpenAIRequestIntegrityState(true, "responses", baseline).Check(integrityTestAccount(), wire, RequestIntegrityCheckOptions{})
	require.Equal(t, "difference", got.Status)
	require.LessOrEqual(t, len(got.ChangedFields), 32)
	require.True(t, got.Truncated)

	baseline = []byte(`{"tools":[{"type":"function","name":"private-tool-name","parameters":{"type":"object","properties":{"private-email@example.test":{"type":"string","const":"private-value-a"}}}}],"text":{"format":{"type":"json_schema","schema":{"properties":{"private-field-key":{"const":"private-a"}}}}}}`)
	wire = []byte(`{"tools":[{"type":"function","name":"private-tool-name","parameters":{"type":"object","properties":{"private-email@example.test":{"type":"string","const":"private-value-b"}}}}],"text":{"format":{"type":"json_schema","schema":{"properties":{"private-field-key":{"const":"private-b"}}}}}}`)
	got = NewOpenAIRequestIntegrityState(true, "responses", baseline).Check(integrityTestAccount(), wire, RequestIntegrityCheckOptions{})
	require.Equal(t, "difference", got.Status)
	encoded := string(integrityTestJSON(t, got))
	for _, secret := range []string{"private-email", "example.test", "private-field-key", "private-value", "private-tool-name", "private-a", "private-b"} {
		require.NotContains(t, encoded, secret)
	}
}

func TestOpenAIRequestIntegrityPreservesJSONNumberPrecision(t *testing.T) {
	baseline := []byte(`{"tools":[{"type":"function","name":"count","parameters":{"type":"object","properties":{"number":{"const":9007199254740992}}}}]}`)
	wire := []byte(`{"tools":[{"type":"function","name":"count","parameters":{"type":"object","properties":{"number":{"const":9007199254740993}}}}]}`)
	got := NewOpenAIRequestIntegrityState(true, "responses", baseline).Check(integrityTestAccount(), wire, RequestIntegrityCheckOptions{})
	require.Equal(t, "difference", got.Status)
}

func TestOpenAIRequestIntegrityConcurrentChecksKeepUniqueAttempts(t *testing.T) {
	body := []byte(`{"input":"hello"}`)
	state := NewOpenAIRequestIntegrityState(true, "responses", body)
	const count = 96
	results := make(chan *RequestIntegrityObservation, count)
	var wg sync.WaitGroup
	for range count {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- state.Check(integrityTestAccount(), body, RequestIntegrityCheckOptions{Transport: "ws"})
		}()
	}
	wg.Wait()
	close(results)
	attempts := make([]int, 0, count)
	for result := range results {
		require.NotNil(t, result)
		require.Equal(t, "unchanged", result.Status)
		attempts = append(attempts, int(result.Attempt))
	}
	sort.Ints(attempts)
	for i, attempt := range attempts {
		require.Equal(t, i+1, attempt)
	}
}

func TestOpenAIRequestIntegrityConcurrentModelMappingDoesNotMutateAccount(t *testing.T) {
	baseline := []byte(`{"model":"client-model","input":"hello"}`)
	wire := []byte(`{"model":"gpt-5.4","input":"hello"}`)
	account := integrityTestAccount()
	account.Credentials = map[string]any{"model_mapping": map[string]any{"client-model": "gpt-5.4"}}
	state := NewOpenAIRequestIntegrityState(true, "responses", baseline)
	const count = 64
	results := make(chan *RequestIntegrityObservation, count)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for range count {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			results <- state.Check(account, wire, RequestIntegrityCheckOptions{Transport: "ws"})
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	for result := range results {
		require.Equal(t, "expected_transform", result.Status)
	}
	require.False(t, account.modelMappingCacheReady, "the observer must not populate shared mutable account caches")
}

func TestOpenAIRequestIntegrityTimezoneRequiresFrozenExactRewrite(t *testing.T) {
	baseline := []byte(`{"input":"hello","tools":[{"type":"web_search_preview","user_location":{"timezone":"Asia/Tokyo","country":"JP"}}]}`)
	prepared, timezone := PrepareOpenAIRequestTimezone(baseline, openai.RequestPolicy{TimezoneConversionEnabled: true, PassthroughTimezoneConversionEnabled: true}, time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC), false, true)
	require.NotEqual(t, baseline, prepared)
	state := NewOpenAIRequestIntegrityState(true, "responses", baseline)
	got := state.Check(integrityTestAccount(), prepared, RequestIntegrityCheckOptions{TimezoneState: timezone})
	require.Equal(t, "expected_transform", got.Status)
	require.NotEmpty(t, got.RuleCodes)

	got = state.Check(integrityTestAccount(), prepared, RequestIntegrityCheckOptions{})
	require.Equal(t, "difference", got.Status, "a matching target zone is not enough without frozen conversion evidence")
	changedCountry := bytes.ReplaceAll(prepared, []byte(`"US"`), []byte(`"FR"`))
	got = state.Check(integrityTestAccount(), changedCountry, RequestIntegrityCheckOptions{TimezoneState: timezone})
	require.Equal(t, "difference", got.Status, "timezone evidence must not approve other location changes")
}

func TestOpenAIRequestIntegrityLocationAdditionRequiresFrozenSource(t *testing.T) {
	baseline := []byte(`{"input":"hello","tools":[{"type":"web_search_preview"}]}`)
	prepared, timezone := PrepareOpenAIRequestTimezone(baseline, openai.DefaultRequestPolicy(), time.Now(), false, true)
	state := NewOpenAIRequestIntegrityState(true, "responses", baseline)
	require.Equal(t, "expected_transform", state.Check(integrityTestAccount(), prepared, RequestIntegrityCheckOptions{TimezoneState: timezone}).Status)
	require.Equal(t, "difference", state.Check(integrityTestAccount(), prepared, RequestIntegrityCheckOptions{}).Status)
	changed := bytes.ReplaceAll(prepared, []byte(`"California"`), []byte(`"Ohio"`))
	require.Equal(t, "difference", state.Check(integrityTestAccount(), changed, RequestIntegrityCheckOptions{TimezoneState: timezone}).Status)
	other := bytes.ReplaceAll(baseline, []byte(`"web_search_preview"`), []byte(`"web_search"`))
	_, unrelated := PrepareOpenAIRequestTimezone(other, openai.DefaultRequestPolicy(), time.Now(), false, true)
	// A frozen location patch alone does not authorize changing the tool type.
	changed = bytes.ReplaceAll(prepared, []byte(`"web_search_preview"`), []byte(`"web_search"`))
	require.Equal(t, "difference", state.Check(integrityTestAccount(), changed, RequestIntegrityCheckOptions{TimezoneState: unrelated}).Status)
}

func TestOpenAIRequestIntegrityAllowsOnlyKnownInputMetadataRemoval(t *testing.T) {
	body := timezoneTestBody(t, map[string]any{"input": []any{timezoneTestMessage(timezoneTestEnvironment("Asia/Tokyo", "2026-09-10"))}})
	prepared, timezone := PrepareOpenAIRequestTimezone(body, openai.DefaultRequestPolicy(), timezoneTestAcceptedAt(), false, true)
	actual, changed, err := normalizeOpenAIOAuthResponsesCompatibilityBody(prepared)
	require.NoError(t, err)
	require.True(t, changed)
	state := NewOpenAIRequestIntegrityState(true, "responses", body)
	result := state.Check(integrityTestAccount(), actual, RequestIntegrityCheckOptions{TimezoneState: timezone})
	require.Equal(t, "expected_transform", result.Status)
	require.Contains(t, result.RuleCodes, "codex_input_metadata_removed")
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(body, &decoded))
	decoded["input"].([]any)[0].(map[string]any)["unrelated_private_metadata"] = "must remain"
	withUnknown, err := json.Marshal(decoded)
	require.NoError(t, err)
	_, timezone = PrepareOpenAIRequestTimezone(withUnknown, openai.DefaultRequestPolicy(), timezoneTestAcceptedAt(), false, true)
	state = NewOpenAIRequestIntegrityState(true, "responses", withUnknown)
	require.Equal(t, "difference", state.Check(integrityTestAccount(), actual, RequestIntegrityCheckOptions{TimezoneState: timezone}).Status)
}
