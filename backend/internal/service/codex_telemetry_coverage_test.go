package service

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCodexTelemetryCoverageReportsClientOnlySignalsWithoutCollection(t *testing.T) {
	t.Setenv("CODEX_TELEMETRY_ENABLED", "")
	for _, policy := range []struct{ enabled, simulation, observation bool }{
		{true, true, true}, {true, false, true}, {true, true, false}, {false, false, false},
	} {
		s := &CodexTelemetryService{configured: policy.enabled, simulationEnabled: policy.simulation, observationEnabled: policy.observation}
		result := s.Observations(CodexTelemetryObservationQuery{})
		require.Equal(t, policy.enabled && (policy.simulation || policy.observation), result.EffectiveEnabled)
		require.Empty(t, result.Items, "capability diagnostics are not telemetry events")
		require.Equal(t, CodexTelemetryCounters{}, result.Counters)
		require.Len(t, result.Coverage, 2)
		require.Equal(t, CodexTelemetrySignalCoverage{
			Signal: "skill_invocation", Type: "logs", Status: "unavailable",
			EventNames: []string{"codex.skill_invocation"}, Reason: "client_skill_event_not_on_responses_wire",
		}, result.Coverage[0])
		require.Equal(t, "awaiting_source", result.Coverage[1].Status, "a recognized metric schema is not a collected sample")
		require.Equal(t, "client_auth_storage_not_on_responses_wire", result.Coverage[1].Reason)
		encoded, err := json.Marshal(result.Coverage)
		require.NoError(t, err)
		require.NotContains(t, string(encoded), "user.account_id")
		require.NotContains(t, string(encoded), "skill.name")
		result.Coverage[0].EventNames[0] = "changed"
		result.Coverage[1].Reason = "changed"
		again := s.Observations(CodexTelemetryObservationQuery{})
		require.Equal(t, "codex.skill_invocation", again.Coverage[0].EventNames[0])
		require.Equal(t, "client_auth_storage_not_on_responses_wire", again.Coverage[1].Reason)
	}

	t.Setenv("CODEX_TELEMETRY_ENABLED", "false")
	s := &CodexTelemetryService{configured: true, simulationEnabled: true, observationEnabled: true}
	result := s.Observations(CodexTelemetryObservationQuery{})
	require.False(t, result.EffectiveEnabled)
	require.Equal(t, "CODEX_TELEMETRY_ENABLED", result.ForcedOffReason)
	require.Len(t, result.Coverage, 2, "capabilities remain visible while collection is disabled")
}

func TestCodexTelemetryClientOnlySignalsAreNeverInferredFromResponses(t *testing.T) {
	for _, simulation := range []bool{false, true} {
		for _, observation := range []bool{false, true} {
			store, profile := newCodexTelemetryMetricStore(), codexMetricsTestProfile()
			profile.simulationEnabled, profile.observationEnabled = simulation, observation
			profile.firstThread = true
			result := codexTelemetryTerminal{status: "completed", finished: profile.started.Add(time.Second), result: CodexTelemetryResult{EventCount: 1}}
			store.touch(profile)
			store.recordAttempt(profile, result)
			store.record(profile, result)
			for _, batch := range store.flush(profile.started.Add(time.Minute)) {
				require.NotContains(t, string(batch.body), "codex.auth_storage.")
				require.NotContains(t, string(batch.body), "codex.skill_invocation")
			}
			for _, event := range append(codexInitializationEvents(profile), codexTerminalEvents(profile, result)...) {
				require.NotEqual(t, "codex.skill_invocation", event.EventType)
				require.False(t, strings.HasPrefix(event.EventType, "codex.auth_storage."))
			}
		}
	}
}

func TestCodexTelemetryAuthStorageMetricSchemaPreservesProvidedSamples(t *testing.T) {
	profile := codexMetricsTestProfile()
	profile.simulationEnabled = false
	store := newCodexTelemetryMetricStore()
	state, _ := store.state(profile, profile.started)
	labels := map[string]string{
		"credential_kind": "codex", "store_mode": "auto", "selected_store": "secrets", "actual_store": "file",
		"operation": "load", "secure_outcome": "not_found", "outcome": "success",
		"fallback_reason": "secure_entry_missing", "secure_error": "none", "storage_phase": "policy", "originator": "codex_cli_rs",
	}
	expected := map[string]string{
		"codex.auth_storage.operation": "sum", "codex.auth_storage.duration": "histogram",
		"codex.auth_storage.refresh_persist": "sum", "codex.auth_storage.refresh_persist.duration": "histogram",
	}
	found := 0
	for _, descriptor := range codexMetricDescriptors {
		kind, exists := expected[descriptor.name]
		if !exists {
			continue
		}
		found++
		require.Equal(t, kind, descriptor.kind)
		require.False(t, codexMetricIsStartup(descriptor.name))
		require.Len(t, strings.Split(descriptor.attributes, ","), 11)
		value := float64(1)
		if kind == "histogram" {
			require.Equal(t, "ms", descriptor.unit)
			value = 25
		} else {
			require.Empty(t, descriptor.unit)
		}
		if strings.Contains(descriptor.name, "refresh_persist") {
			labels["operation"], labels["storage_phase"] = "refresh_persist", "pinned"
		}
		// Exercise schema compatibility with explicitly provided observations;
		// current gateway entry points deliberately supply none of these samples.
		state.addWithSource(descriptor, value, profile, "", profile.started.Add(time.Second), labels, "observed")
	}
	require.Equal(t, len(expected), found)
	encoded, err := marshalCodexTelemetryMetricStore(store)
	require.NoError(t, err)
	restored, err := unmarshalCodexTelemetryMetricStore(encoded)
	require.NoError(t, err)
	batches := restored.flush(profile.started.Add(time.Minute))
	require.Len(t, batches, 1)
	require.Equal(t, "observed", batches[0].source)
	for name, kind := range expected {
		metric := codexMetricsTestMetric(batches[0].body, name)
		point := metric.Get(kind + ".dataPoints.0")
		require.Len(t, point.Get("attributes").Array(), 11)
		require.Empty(t, codexMetricsTestAttribute(point, "auth_mode"))
		require.Empty(t, codexMetricsTestAttribute(point, "model"))
		require.Equal(t, "secrets", codexMetricsTestAttribute(point, "selected_store"))
		if kind == "histogram" {
			require.Equal(t, float64(25), point.Get("sum").Float())
			require.Len(t, point.Get("explicitBounds").Array(), len(codexHistogramBounds))
		} else {
			require.EqualValues(t, 1, point.Get("asInt").Uint())
		}
	}
	require.NotContains(t, string(batches[0].body), "credential-not-in-payload")
	require.NotContains(t, string(batches[0].body), "proxy-secret")
	require.NotContains(t, string(batches[0].body), "upstream-account")
}
