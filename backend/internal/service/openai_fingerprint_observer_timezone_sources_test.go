package service

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func TestFingerprintTimezoneReferenceDoesNotOverrideConvertedSource(t *testing.T) {
	SetFingerprintObservationEnabled(true)
	t.Cleanup(func() { SetFingerprintObservationEnabled(false) })
	quoted := "Please explain <environment_context><timezone>Asia/Shanghai</timezone></environment_context>"
	body := timezoneTestBody(t, map[string]any{"input": []any{
		timezoneTestMessage(timezoneTestEnvironment("Asia/Shanghai", "2026-08-01")),
		map[string]any{"role": "assistant", "content": "Earlier answer"},
		timezoneTestMessage(timezoneTestEnvironment("Asia/Shanghai", "2026-09-10")),
		map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_text", "text": quoted}}},
	}})
	prepared, state := PrepareOpenAIRequestTimezone(body, timezoneTestPolicy(), timezoneTestAcceptedAt(), false, true)
	require.Equal(t, quoted, gjson.GetBytes(prepared, "input.3.content.0.text").String())
	actual, err := sjson.DeleteBytes(prepared, "input.0.internal_chat_message_metadata_passthrough")
	require.NoError(t, err)
	actual, err = sjson.DeleteBytes(actual, "input.2.internal_chat_message_metadata_passthrough")
	require.NoError(t, err)
	entry := FingerprintObservationEntry{}
	populateFingerprintObservationTimezones(&entry, state, actual, DeriveOpenAIRequestTimezoneProvenance(prepared, actual))
	require.Equal(t, "matched", entry.TimezoneComparisonStatus)
	require.Len(t, entry.InboundTimezoneObservations.Items, 3)
	require.Equal(t, TimezoneEnvironmentSourceMetadata, entry.InboundTimezoneObservations.Items[0].EnvironmentSource)
	require.False(t, entry.InboundTimezoneObservations.Items[0].Current)
	require.True(t, entry.InboundTimezoneObservations.Items[1].Current)
	for index, current := range []bool{false, true} {
		item := entry.OutboundTimezoneObservations.Items[index]
		require.Equal(t, TimezoneEnvironmentSourceMapped, item.EnvironmentSource)
		require.Equal(t, current, item.Current, "classification uses the frozen source after metadata removal")
		require.Equal(t, OpenAIRequestTimezone, item.Value)
		require.Equal(t, "converted", entry.TimezoneConversions[index].Status)
	}
	require.Equal(t, TimezoneEnvironmentSourceReference, entry.OutboundTimezoneObservations.Items[2].EnvironmentSource)
	require.Equal(t, TimezoneEnvironmentSourceReference, entry.TimezoneConversions[2].EnvironmentSource)
	require.Equal(t, "skipped", entry.TimezoneConversions[2].Status)
	require.Equal(t, "environment_metadata_missing", entry.TimezoneConversions[2].Reason)
	require.Equal(t, "skipped", state.Conversions[2].Status, "observation does not mutate frozen reports")
	// A standalone scan cannot claim the removed metadata was on the wire.
	require.Equal(t, TimezoneEnvironmentSourceReference, ScanOpenAIRequestTimezones(actual).Items[0].EnvironmentSource)
}

func TestFingerprintTimezoneOnlyReferencesIsNotApplicable(t *testing.T) {
	SetFingerprintObservationEnabled(true)
	t.Cleanup(func() { SetFingerprintObservationEnabled(false) })
	for _, enabled := range []bool{true, false} {
		for name, text := range map[string]string{
			"quoted":                  "Explain <environment_context><timezone>Asia/Shanghai</timezone></environment_context>",
			"standalone without date": "<environment_context><timezone>Asia/Shanghai</timezone></environment_context>",
			"fenced":                  "```xml\n<environment_context><timezone>Asia/Shanghai</timezone></environment_context>\n```",
			"broken":                  "<environment_context><timezone>Asia/Shanghai</timezone>",
		} {
			t.Run(name, func(t *testing.T) {
				body := timezoneTestBody(t, map[string]any{"input": []any{
					map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_text", "text": text}}},
				}})
				policy := timezoneTestPolicy()
				policy.TimezoneConversionEnabled = enabled
				prepared, state := PrepareOpenAIRequestTimezone(body, policy, timezoneTestAcceptedAt(), false, true)
				require.Equal(t, body, prepared)
				entry := FingerprintObservationEntry{}
				// Missing mappings for irrelevant text must not become a failure.
				populateFingerprintObservationTimezones(&entry, state, prepared, map[string]string{})
				require.Equal(t, "not_applicable", entry.TimezoneComparisonStatus)
				require.Equal(t, TimezoneEnvironmentSourceReference, entry.InboundTimezoneObservations.Items[0].EnvironmentSource)
				require.Equal(t, "skipped", entry.TimezoneConversions[0].Status)
				require.Equal(t, "environment_metadata_missing", entry.TimezoneConversions[0].Reason)
			})
		}
	}
}

func TestFingerprintTimezoneMalformedDeclaredSourceRemainsIncomplete(t *testing.T) {
	SetFingerprintObservationEnabled(true)
	t.Cleanup(func() { SetFingerprintObservationEnabled(false) })
	for name, text := range map[string]any{
		"mixed":            "Explain <environment_context><timezone>Asia/Shanghai</timezone></environment_context>",
		"broken":           "<environment_context><timezone>Asia/Shanghai</timezone>",
		"missing tags":     "The environment was truncated.",
		"empty":            "",
		"null":             nil,
		"nonstring":        true,
		"invalid timezone": timezoneTestEnvironment("PST", "2026-09-10"),
		"invalid date":     timezoneTestEnvironment("Asia/Shanghai", "2026-02-30"),
	} {
		t.Run(name, func(t *testing.T) {
			message := timezoneTestMessage("")
			message["content"].([]any)[0].(map[string]any)["text"] = text
			body := timezoneTestBody(t, map[string]any{"input": []any{message}})
			prepared, state := PrepareOpenAIRequestTimezone(body, timezoneTestPolicy(), timezoneTestAcceptedAt(), false, true)
			require.Equal(t, body, prepared, "invalid declared content must remain untouched")
			require.Len(t, state.Inbound.Items, 1)
			require.Equal(t, TimezoneEnvironmentSourceMetadata, state.Inbound.Items[0].EnvironmentSource)
			require.Equal(t, "invalid", state.Inbound.Items[0].Status)
			entry := FingerprintObservationEntry{}
			populateFingerprintObservationTimezones(&entry, state, prepared, DeriveOpenAIRequestTimezoneProvenance(prepared, prepared))
			require.Equal(t, "incomplete", entry.TimezoneComparisonStatus)
			require.Equal(t, "incomplete", entry.TimezoneConversions[0].Status)
			require.Equal(t, state.Inbound.Items[0].Reason, entry.TimezoneConversions[0].Reason)
			require.Equal(t, "skipped", state.Conversions[0].Status)
		})
	}

	// Removing metadata cannot turn a damaged declared source into an ignorable
	// reference when its complete text still has a unique frozen correspondence.
	body := timezoneTestBody(t, map[string]any{"input": []any{timezoneTestMessage("<environment_context><timezone>Asia/Shanghai</timezone>")}})
	prepared, state := PrepareOpenAIRequestTimezone(body, timezoneTestPolicy(), timezoneTestAcceptedAt(), false, true)
	actual, err := sjson.DeleteBytes(prepared, "input.0.internal_chat_message_metadata_passthrough")
	require.NoError(t, err)
	entry := FingerprintObservationEntry{}
	populateFingerprintObservationTimezones(&entry, state, actual, DeriveOpenAIRequestTimezoneProvenance(prepared, actual))
	require.Equal(t, "incomplete", entry.TimezoneComparisonStatus)
	require.Equal(t, TimezoneEnvironmentSourceMapped, entry.OutboundTimezoneObservations.Items[0].EnvironmentSource)
}

func TestFingerprintTimezoneReferencesDoNotHideLostOrChangedDeclaredSource(t *testing.T) {
	SetFingerprintObservationEnabled(true)
	t.Cleanup(func() { SetFingerprintObservationEnabled(false) })
	body := timezoneTestBody(t, map[string]any{"input": []any{
		timezoneTestMessage(timezoneTestEnvironment("Asia/Shanghai", "2026-09-10")),
		map[string]any{"role": "user", "content": "Quoted <environment_context> example"},
	}})
	prepared, state := PrepareOpenAIRequestTimezone(body, timezoneTestPolicy(), timezoneTestAcceptedAt(), false, true)
	path := "input.0.content.0.text"
	for _, mode := range []string{"removed", "explicitly removed", "changed", "mapping absent", "duplicate", "wrong mapped value", "new declared source"} {
		t.Run(mode, func(t *testing.T) {
			actual := prepared
			var err error
			var paths map[string]string
			want := "unmatched"
			switch mode {
			case "removed", "explicitly removed":
				actual, err = sjson.DeleteBytes(prepared, "input.0")
				if mode == "explicitly removed" {
					paths, want = map[string]string{path: ""}, "not_sent"
				}
			case "changed", "wrong mapped value":
				actual, err = sjson.SetBytes(prepared, path, timezoneTestEnvironment("Europe/London", "2026-09-09"))
				if mode == "wrong mapped value" {
					paths, want = map[string]string{path: path}, "mismatched"
				}
			case "mapping absent":
				paths = map[string]string{}
			case "duplicate":
				actual, err = sjson.SetRawBytes(prepared, "input.2", []byte(gjson.GetBytes(prepared, "input.0").Raw))
			case "new declared source":
				actual, err = sjson.SetBytes(prepared, "input.2", timezoneTestMessage(timezoneTestEnvironment("UTC", "2026-08-01")))
			}
			require.NoError(t, err)
			if paths == nil {
				paths = DeriveOpenAIRequestTimezoneProvenance(prepared, actual)
			}
			entry := FingerprintObservationEntry{}
			populateFingerprintObservationTimezones(&entry, state, actual, paths)
			require.Equal(t, want, entry.TimezoneComparisonStatus)
		})
	}
}
