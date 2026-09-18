package service

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOpenAIEnvironmentSourceDiagnosticQualification(t *testing.T) {
	environment := timezoneTestEnvironment("Asia/Shanghai", "2026-09-18")
	for _, tc := range []struct {
		name    string
		edit    func(map[string]any, map[string]any)
		blocker string
		marker  string
	}{
		{"other memory kind", func(_ map[string]any, msg map[string]any) {
			msg["internal_chat_message_metadata_passthrough"] = map[string]any{"content_item_kinds": []any{"memories.extraction_evidence"}}
		}, "message_content_item_kinds_present", "marker_mismatch"},
		{"wrong index", func(_ map[string]any, msg map[string]any) {
			msg["internal_chat_message_metadata_passthrough"] = map[string]any{"content_item_kinds": []any{"user.text", "environments.environment_context"}}
		}, "message_content_item_kinds_present", "marker_mismatch"},
		{"null kinds", func(_ map[string]any, msg map[string]any) {
			msg["internal_chat_message_metadata_passthrough"] = map[string]any{"content_item_kinds": nil}
		}, "message_content_item_kinds_present", "kinds_not_array"},
		{"null metadata", func(_ map[string]any, msg map[string]any) { msg["internal_chat_message_metadata_passthrough"] = nil }, "message_metadata_invalid", "kinds_missing"},
		{"null marker", func(_ map[string]any, msg map[string]any) {
			msg["internal_chat_message_metadata_passthrough"] = map[string]any{"content_item_kinds": []any{nil}}
		}, "message_content_item_kinds_present", "marker_not_string"},
		{"missing index", func(_ map[string]any, msg map[string]any) {
			msg["internal_chat_message_metadata_passthrough"] = map[string]any{"content_item_kinds": []any{}}
		}, "message_content_item_kinds_present", "index_missing"},
		{"object kinds", func(_ map[string]any, msg map[string]any) {
			msg["internal_chat_message_metadata_passthrough"] = map[string]any{"content_item_kinds": map[string]any{"0": "environments.environment_context"}}
		}, "message_content_item_kinds_present", "kinds_not_array"},
		{"developer role", func(_ map[string]any, msg map[string]any) { msg["role"] = "developer" }, "role_not_user", "kinds_missing"},
		{"text type", func(_ map[string]any, msg map[string]any) {
			msg["content"].([]any)[0].(map[string]any)["type"] = "text"
		}, "content_not_input_text", "kinds_missing"},
		{"request metadata", func(root map[string]any, _ map[string]any) { root["content_item_kinds"] = []any{"user.text"} }, "request_content_item_kinds_present", "kinds_missing"},
		{"part metadata", func(_ map[string]any, msg map[string]any) {
			msg["content"].([]any)[0].(map[string]any)["content_item_kinds"] = []any{"user.text"}
		}, "part_content_item_kinds_present", "kinds_missing"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			message := timezoneStructuralFallbackMessage(environment)
			payload := map[string]any{"input": []any{message}}
			tc.edit(payload, message)
			body := timezoneTestBody(t, payload)
			prepared, state := PrepareOpenAIRequestTimezone(body, timezoneTestPolicy(), timezoneTestAcceptedAt(), false, false)
			require.Equal(t, body, prepared, "diagnostics cannot alter conversion eligibility")
			require.Nil(t, state.Inbound, "fingerprint collection remains disabled")
			require.Len(t, state.projectionSources, 1)
			d := state.projectionSources[0].occurrence.environmentDiagnostic
			require.NotNil(t, d)
			require.Equal(t, "input.0.content.0.text", d.Path)
			require.Equal(t, tc.marker, d.MarkerStatus)
			require.Contains(t, d.FallbackBlockers, tc.blocker)
			require.Equal(t, 1, d.Structure.TimezoneOpen)
			if tc.name == "wrong index" {
				require.Equal(t, []int{1}, d.Metadata[0].EnvironmentIndices)
			}
		})
	}
}

func TestOpenAIEnvironmentSourceDiagnosticInvalidStructure(t *testing.T) {
	for _, text := range []string{
		"example before " + timezoneTestEnvironment("Asia/Shanghai", "2026-09-18"),
		"<environment_context><timezone>Asia/Shanghai</timezone>",
		"```xml\n" + timezoneTestEnvironment("Asia/Shanghai", "2026-09-18") + "\n```",
	} {
		body := timezoneTestBody(t, map[string]any{"input": []any{timezoneStructuralFallbackMessage(text)}})
		out, state := PrepareOpenAIRequestTimezone(body, timezoneTestPolicy(), timezoneTestAcceptedAt(), false, true)
		require.Equal(t, body, out)
		d := state.projectionSources[0].occurrence.environmentDiagnostic
		require.Contains(t, d.FallbackBlockers, "environment_structure_invalid")
		require.Equal(t, len(text), d.Structure.TextBytes)
		require.Equal(t, 1, d.Structure.TimezoneOpen)
	}
}

func TestOpenAIEnvironmentSourceDiagnosticBoundedAndPrivate(t *testing.T) {
	message := timezoneStructuralFallbackMessage(timezoneTestEnvironment("Asia/Shanghai", "2026-09-18"))
	kinds := make([]any, 1024)
	for i := range kinds {
		kinds[i] = "environments.environment_context"
	}
	kinds[0] = "Bearer private-marker-secret"
	message["internal_chat_message_metadata_passthrough"] = map[string]any{"content_item_kinds": kinds, "credential": "private-credential"}
	message["content"].([]any)[0].(map[string]any)["text"] = "private-body-secret" + timezoneTestEnvironment("Asia/Shanghai", "2026-09-18")
	_, state := PrepareOpenAIRequestTimezone(timezoneTestBody(t, map[string]any{"input": []any{message}}), timezoneTestPolicy(), timezoneTestAcceptedAt(), false, false)
	d := state.projectionSources[0].occurrence.environmentDiagnostic
	require.Equal(t, "other_string", d.Metadata[0].SelectedMarker)
	require.Equal(t, environmentDiagnosticMarkerLimit, d.Metadata[0].InspectedKinds)
	require.Len(t, d.Metadata[0].EnvironmentIndices, 8)
	require.True(t, d.Metadata[0].Truncated)
	raw, err := json.Marshal(d)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "private-")
	require.Less(t, len(raw), 3000)

	// Diagnosis follows immutable provenance through route changes and remapping.
	remapped := RemapRequestTimezoneState(state, map[string]string{"input.0.content.0.text": "input.2.content.0.text"})
	projected := remapped.WithTarget(RequestLocationObservation{Timezone: "Asia/Tokyo"})
	require.Equal(t, "input.0.content.0.text", projected.projectionSources[0].occurrence.environmentDiagnostic.Path)
	require.Equal(t, "input.2.content.0.text", projected.projectionSources[0].occurrence.item.Path)
	require.Equal(t, "input.0.content.0.text", state.projectionSources[0].occurrence.item.Path)
}

func TestOpenAIEnvironmentSourceDiagnosticValidFallbackAndFinalScan(t *testing.T) {
	body := timezoneTestBody(t, map[string]any{"input": []any{timezoneStructuralFallbackMessage(timezoneTestEnvironment("Asia/Shanghai", "2026-09-18"))}})
	out, state := PrepareOpenAIRequestTimezone(body, timezoneTestPolicy(), timezoneTestAcceptedAt(), false, false)
	require.True(t, strings.Contains(string(out), OpenAIRequestTimezone))
	require.Empty(t, state.projectionSources[0].occurrence.environmentDiagnostic.FallbackBlockers)
	final := scanOpenAIRequestTimezones(out)
	require.Nil(t, final.occurrences[0].environmentDiagnostic, "final body cannot replace original metadata diagnostics")
}
