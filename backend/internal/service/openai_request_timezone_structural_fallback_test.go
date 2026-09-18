package service

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func timezoneStructuralFallbackMessage(text string) map[string]any {
	return map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_text", "text": text}}}
}

func TestOpenAIRequestTimezoneStructuralFallbackValidAndActualObservation(t *testing.T) {
	SetFingerprintObservationEnabled(true)
	t.Cleanup(func() { SetFingerprintObservationEnabled(false) })
	// Do not impose an unverified cwd requirement on the fallback contract.
	environment := "<environment_context><current_date>2026-09-18</current_date><timezone>Asia/Shanghai</timezone></environment_context>"
	body := timezoneTestBody(t, map[string]any{"input": []any{timezoneStructuralFallbackMessage(environment)}})
	prepared, state := PrepareOpenAIRequestTimezone(body, timezoneTestPolicy(), timezoneTestAcceptedAt(), false, true)
	require.Contains(t, gjson.GetBytes(prepared, "input.0.content.0.text").String(), "<timezone>America/Los_Angeles</timezone>")
	require.Contains(t, gjson.GetBytes(prepared, "input.0.content.0.text").String(), "<current_date>2026-09-09</current_date>")
	require.False(t, gjson.GetBytes(prepared, "input.0.internal_chat_message_metadata_passthrough").Exists(), "fallback must not manufacture a metadata marker")
	require.Equal(t, "structural_fallback", state.Inbound.Items[0].EnvironmentSource)
	require.Equal(t, "structural_fallback", state.Conversions[0].EnvironmentSource)
	entry := FingerprintObservationEntry{}
	populateFingerprintObservationTimezones(&entry, state, prepared, DeriveOpenAIRequestTimezoneProvenance(prepared, prepared))
	require.Equal(t, "matched", entry.TimezoneComparisonStatus)
	require.Equal(t, "structural_fallback", entry.OutboundTimezoneObservations.Items[0].EnvironmentSource)
	require.Equal(t, OpenAIRequestTimezone, entry.OutboundTimezoneObservations.Items[0].Value)
	actual, err := sjson.SetBytes(prepared, "input.0.content.0.text", strings.ReplaceAll(gjson.GetBytes(prepared, "input.0.content.0.text").String(), OpenAIRequestTimezone, "Asia/Shanghai"))
	require.NoError(t, err)
	changed := FingerprintObservationEntry{}
	populateFingerprintObservationTimezones(&changed, state, actual, map[string]string{"input.0.content.0.text": "input.0.content.0.text"})
	require.Equal(t, "mismatched", changed.TimezoneComparisonStatus)
	require.Equal(t, "Asia/Shanghai", changed.OutboundTimezoneObservations.Items[0].Value, "final observation must never substitute the prepared value")
}

func TestOpenAIRequestTimezoneStructuralFallbackMetadataAuthority(t *testing.T) {
	environment := timezoneTestEnvironment("Asia/Shanghai", "2026-09-18")
	for _, tc := range []struct {
		name     string
		metadata any
	}{
		{"null", nil}, {"empty object", map[string]any{}},
		{"string", "environments.environment_context"}, {"array", []any{"environments.environment_context"}},
		{"wrong marker", map[string]any{"content_item_kinds": []any{"user_message"}}},
		{"empty markers", map[string]any{"content_item_kinds": []any{}}},
		{"object markers", map[string]any{"content_item_kinds": map[string]any{"0": "environments.environment_context"}}},
		{"wrong index", map[string]any{"content_item_kinds": []any{nil, "environments.environment_context"}}},
		{"padded marker", map[string]any{"content_item_kinds": []any{" environments.environment_context "}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			message := timezoneStructuralFallbackMessage(environment)
			message["internal_chat_message_metadata_passthrough"] = tc.metadata
			body := timezoneTestBody(t, map[string]any{"input": []any{message}})
			prepared, state := PrepareOpenAIRequestTimezone(body, timezoneTestPolicy(), timezoneTestAcceptedAt(), false, true)
			require.Equal(t, body, prepared)
			require.Empty(t, state.patches)
			require.NotEqual(t, "structural_fallback", state.Inbound.Items[0].EnvironmentSource)
		})
	}
	for _, text := range []string{environment, "<environment_context><timezone>Asia/Shanghai</timezone></environment_context>"} {
		body := timezoneTestBody(t, map[string]any{"input": []any{timezoneTestMessage(text)}})
		prepared, state := PrepareOpenAIRequestTimezone(body, timezoneTestPolicy(), timezoneTestAcceptedAt(), false, true)
		require.Equal(t, "metadata", state.Inbound.Items[0].EnvironmentSource)
		require.Equal(t, "metadata", state.Conversions[0].EnvironmentSource)
		require.Contains(t, gjson.GetBytes(prepared, "input.0.content.0.text").String(), OpenAIRequestTimezone, "explicit tags retain their existing eligibility rules")
	}
	for _, location := range []string{"message metadata", "contentpart metadata"} {
		t.Run(location, func(t *testing.T) {
			message := timezoneStructuralFallbackMessage(environment)
			if location == "message metadata" {
				message["metadata"] = map[string]any{"content_item_kinds": []any{"user_message"}}
			} else {
				message["content"].([]any)[0].(map[string]any)["internal_chat_message_metadata_passthrough"] = map[string]any{"content_item_kinds": []any{"environments.environment_context"}}
			}
			body := timezoneTestBody(t, map[string]any{"input": []any{message}})
			prepared, state := PrepareOpenAIRequestTimezone(body, timezoneTestPolicy(), timezoneTestAcceptedAt(), false, true)
			require.Equal(t, body, prepared, "fallback must not bypass an explicit marker at a known alternative location")
			require.Empty(t, state.patches)
		})
	}
}

func TestOpenAIRequestTimezoneStructuralFallbackRejectsOtherShapesAndQuotedXML(t *testing.T) {
	environment := timezoneTestEnvironment("Asia/Shanghai", "2026-09-18")
	for name, payload := range map[string]map[string]any{
		"messages":        {"messages": []any{timezoneStructuralFallbackMessage(environment)}},
		"string input":    {"input": environment},
		"string content":  {"input": []any{map[string]any{"role": "user", "content": environment}}},
		"assistant":       {"input": []any{map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "input_text", "text": environment}}}}},
		"developer":       {"input": []any{map[string]any{"role": "developer", "content": []any{map[string]any{"type": "input_text", "text": environment}}}}},
		"text type":       {"input": []any{map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": environment}}}}},
		"untyped content": {"input": []any{map[string]any{"role": "user", "content": []any{map[string]any{"text": environment}}}}},
	} {
		t.Run(name, func(t *testing.T) {
			body := timezoneTestBody(t, payload)
			prepared, state := PrepareOpenAIRequestTimezone(body, timezoneTestPolicy(), timezoneTestAcceptedAt(), false, true)
			require.Equal(t, body, prepared)
			require.Empty(t, state.patches)
		})
	}
	for name, text := range map[string]string{
		"mixed prefix": "Here is an example: " + environment,
		"mixed suffix": environment + "\nExplain this example.",
		"fenced":       "```xml\n" + environment + "\n```", "quoted": "> " + environment,
		"comment":            strings.Replace(environment, "<current_date>", "<!-- example --><current_date>", 1),
		"cdata":              strings.Replace(environment, "<current_date>", "<![CDATA[example]]><current_date>", 1),
		"duplicate":          strings.Replace(environment, "</timezone>", "</timezone><timezone>UTC</timezone>", 1),
		"missing date":       "<environment_context><timezone>Asia/Shanghai</timezone></environment_context>",
		"missing zone":       "<environment_context><current_date>2026-09-18</current_date></environment_context>",
		"invalid date":       timezoneTestEnvironment("Asia/Shanghai", "2026-02-30"),
		"invalid timezone":   timezoneTestEnvironment("PST", "2026-09-18"),
		"timezone attribute": strings.Replace(environment, "<timezone>", "<timezone source='example'>", 1),
	} {
		t.Run(name, func(t *testing.T) {
			body := timezoneTestBody(t, map[string]any{"input": []any{timezoneStructuralFallbackMessage(text)}})
			prepared, state := PrepareOpenAIRequestTimezone(body, timezoneTestPolicy(), timezoneTestAcceptedAt(), false, true)
			require.Equal(t, body, prepared)
			require.Empty(t, state.patches)
		})
	}
}

func TestOpenAIRequestTimezoneStructuralFallbackHistoryAndInvalidTail(t *testing.T) {
	old := timezoneTestEnvironment("Asia/Shanghai", "2026-08-01")
	current := timezoneTestEnvironment("Asia/Shanghai", "2026-09-18")
	body := timezoneTestBody(t, map[string]any{"input": []any{timezoneStructuralFallbackMessage(old), map[string]any{"role": "assistant", "content": "earlier answer"}, timezoneStructuralFallbackMessage(current)}})
	prepared, state := PrepareOpenAIRequestTimezone(body, timezoneTestPolicy(), timezoneTestAcceptedAt(), false, true)
	require.Equal(t, timezoneTestEnvironment(OpenAIRequestTimezone, "2026-08-01"), gjson.GetBytes(prepared, "input.0.content.0.text").String())
	require.Equal(t, timezoneTestEnvironment(OpenAIRequestTimezone, "2026-09-09"), gjson.GetBytes(prepared, "input.2.content.0.text").String())
	require.False(t, state.Inbound.Items[0].Current)
	require.True(t, state.Inbound.Items[1].Current)
	for name, invalid := range map[string]string{
		"invalid date":   timezoneTestEnvironment("Asia/Shanghai", "2026-02-30"),
		"invalid zone":   timezoneTestEnvironment("PST", "2026-09-18"),
		"duplicate zone": strings.Replace(current, "</timezone>", "</timezone><timezone>UTC</timezone>", 1),
	} {
		t.Run(name, func(t *testing.T) {
			body := timezoneTestBody(t, map[string]any{"input": []any{timezoneStructuralFallbackMessage(old), timezoneStructuralFallbackMessage(invalid)}})
			prepared, state := PrepareOpenAIRequestTimezone(body, timezoneTestPolicy(), timezoneTestAcceptedAt(), false, true)
			require.Equal(t, timezoneTestEnvironment(OpenAIRequestTimezone, "2026-08-01"), gjson.GetBytes(prepared, "input.0.content.0.text").String(), "an invalid later environment must not make an earlier date current")
			require.Equal(t, invalid, gjson.GetBytes(prepared, "input.1.content.0.text").String())
			require.False(t, state.Inbound.Items[0].Current)
		})
	}
}

func TestOpenAIRequestTimezoneStructuralFallbackSwitchesAndNoObservation(t *testing.T) {
	body := timezoneTestBody(t, map[string]any{"input": []any{timezoneStructuralFallbackMessage(timezoneTestEnvironment("Asia/Shanghai", "2026-09-18"))}})
	for _, enabled := range []bool{false, true} {
		for _, passthrough := range []bool{false, true} {
			for _, passthroughEnabled := range []bool{false, true} {
				policy := timezoneTestPolicy()
				policy.TimezoneConversionEnabled, policy.PassthroughTimezoneConversionEnabled = enabled, passthroughEnabled
				prepared, _ := PrepareOpenAIRequestTimezone(body, policy, timezoneTestAcceptedAt(), passthrough, false)
				if enabled && (!passthrough || passthroughEnabled) {
					require.Contains(t, gjson.GetBytes(prepared, "input.0.content.0.text").String(), OpenAIRequestTimezone, "conversion must not require observation")
				} else {
					require.Equal(t, body, prepared)
				}
			}
		}
	}
}

func TestOpenAIRequestTimezoneStructuralFallbackPlainReferenceDoesNotRetireCurrent(t *testing.T) {
	environment := timezoneTestEnvironment("Asia/Shanghai", "2026-09-18")
	for name, reference := range map[string]string{
		"plain prefix": "Explain this example: " + environment,
		"plain suffix": environment + "\nExplain this example.",
	} {
		t.Run(name, func(t *testing.T) {
			body := timezoneTestBody(t, map[string]any{"input": []any{timezoneStructuralFallbackMessage(environment), timezoneStructuralFallbackMessage(reference)}})
			prepared, state := PrepareOpenAIRequestTimezone(body, timezoneTestPolicy(), timezoneTestAcceptedAt(), false, true)
			require.Equal(t, timezoneTestEnvironment(OpenAIRequestTimezone, "2026-09-09"), gjson.GetBytes(prepared, "input.0.content.0.text").String(), "ordinary reference text must not prevent the real current environment's date from refreshing")
			require.Equal(t, reference, gjson.GetBytes(prepared, "input.1.content.0.text").String())
			require.True(t, state.Inbound.Items[0].Current)
			require.Equal(t, "reference", state.Inbound.Items[1].EnvironmentSource)
		})
	}
}

func TestOpenAIRequestTimezoneStructuralFallbackHTTPFrozenSource(t *testing.T) {
	for _, account := range []*Account{newOpenAIIdentityPathOAuthAccount(1465), newOpenAIIdentityPathAPIKeyAccount(1466)} {
		t.Run(account.Type, func(t *testing.T) {
			old := timezoneTestEnvironment("Asia/Shanghai", "2026-08-01")
			body := timezoneTestBody(t, map[string]any{"model": "original", "input": []any{timezoneStructuralFallbackMessage(old), map[string]any{"type": "function_call_output", "output": "heartbeat"}}})
			c, _ := newOpenAIIdentityPathContext(t, "/responses", body, 10)
			svc := &OpenAIGatewayService{}
			svc.CaptureOpenAIRequestTimezone(c, body)
			captured, _ := c.Get(openAIRequestTimezoneCaptureKey)
			captured.(*openAIRequestTimezoneCapture).acceptedAt = timezoneTestAcceptedAt()
			adapted, err := sjson.SetBytes(body, "input.1", timezoneStructuralFallbackMessage(timezoneTestEnvironment("Asia/Shanghai", "2026-09-18")))
			require.NoError(t, err)
			adapted, err = sjson.SetBytes(adapted, "model", "mapped-model")
			require.NoError(t, err)
			prepared := svc.prepareOpenAIRequestTimezone(context.Background(), c, account, adapted, false)
			require.Equal(t, timezoneTestEnvironment(OpenAIRequestTimezone, "2026-08-01"), gjson.GetBytes(prepared, "input.0.content.0.text").String())
			require.Equal(t, gjson.GetBytes(adapted, "input.1").Raw, gjson.GetBytes(prepared, "input.1").Raw, "account adaptation cannot create a new conversion source")
			require.Equal(t, "mapped-model", gjson.GetBytes(prepared, "model").String())
			retry := svc.prepareOpenAIRequestTimezone(context.Background(), c, account, adapted, false)
			require.Equal(t, prepared, retry)
			require.Equal(t, old, gjson.GetBytes(body, "input.0.content.0.text").String(), "original body remains immutable")
		})
	}
}

func TestOpenAIRequestTimezoneStructuralFallbackWSFrozenSourceAndNewFrame(t *testing.T) {
	accepted := time.Date(2026, 1, 2, 7, 59, 0, 0, time.UTC)
	body := timezoneTestBody(t, map[string]any{"type": "response.create", "model": "gpt-5.1", "input": []any{timezoneStructuralFallbackMessage(timezoneWSEnvironment)}})
	c, _ := newOpenAIIdentityPathContext(t, "/responses", body, 10)
	svc := &OpenAIGatewayService{}
	account := newOpenAIIdentityPathOAuthAccount(1465)
	ctx := openai.WithRequestPolicy(context.Background(), timezoneTestPolicy())
	c.Set(openAIRequestTimezoneCaptureKey, &openAIRequestTimezoneCapture{acceptedAt: accepted, body: body})
	first, state := svc.prepareOpenAIWSFrameTimezone(ctx, c, account, body, false, true, accepted.Add(time.Hour))
	require.Equal(t, accepted, state.AcceptedAt)
	require.Contains(t, gjson.GetBytes(first, "input.0.content.0.text").String(), "2026-01-01")
	require.Contains(t, gjson.GetBytes(first, "input.0.content.0.text").String(), OpenAIRequestTimezone)
	retry, retryState := svc.prepareOpenAIWSFrameTimezone(ctx, c, account, body, false, true, accepted.Add(2*time.Hour))
	require.Equal(t, first, retry)
	require.Equal(t, state.AcceptedAt, retryState.AcceptedAt)
	next, nextState := svc.prepareOpenAIWSFrameTimezone(ctx, c, account, body, false, false, accepted.Add(2*time.Hour))
	require.Contains(t, gjson.GetBytes(next, "input.0.content.0.text").String(), "2026-01-02")
	require.NotEqual(t, state.AcceptedAt, nextState.AcceptedAt)
	require.Contains(t, gjson.GetBytes(body, "input.0.content.0.text").String(), "Asia/Shanghai")
}
