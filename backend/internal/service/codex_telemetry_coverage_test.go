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
	pendingMetrics := map[string][]string{
		"exec_command_timing":        {"codex.exec_command.duration_total_ms", "codex.exec_command.calls"},
		"multi_agent_spawn":          {"codex.multi_agent.spawn.failure", "codex.multi_agent.spawn.phase.duration_ms"},
		"additional_context":         {"codex.additional_context", "codex.additional_context.value_bytes"},
		"tool_registrations":         {"codex.tools.registered"},
		"mcp_binding_catalog":        {"codex.mcp.binding_catalog.raw_definition_json_bytes"},
		"model_search_discovery":     {"codex.model_search.discovery"},
		"artifact_operation_metrics": {"codex.artifact.operation.started", "codex.artifact.operation.expected_output_count"},
	}
	for _, policy := range []struct{ enabled, simulation, observation bool }{
		{true, true, true}, {true, false, true}, {true, true, false}, {true, false, false},
		{false, true, true}, {false, false, true}, {false, true, false}, {false, false, false},
	} {
		s := &CodexTelemetryService{configured: policy.enabled, simulationEnabled: policy.simulation, observationEnabled: policy.observation}
		result := s.Observations(CodexTelemetryObservationQuery{})
		require.Equal(t, policy.enabled && (policy.simulation || policy.observation), result.EffectiveEnabled)
		require.Empty(t, result.Items, "capability diagnostics are not telemetry events")
		require.Equal(t, CodexTelemetryCounters{}, result.Counters)
		require.Len(t, result.Coverage, 15)
		require.Equal(t, CodexTelemetrySignalCoverage{
			Signal: "skill_invocation", Type: "logs", Status: "unavailable",
			EventNames: []string{"codex.skill_invocation"}, Reason: "client_skill_event_not_on_responses_wire",
		}, result.Coverage[2])
		require.Equal(t, "awaiting_source", result.Coverage[3].Status, "a recognized metric schema is not a collected sample")
		require.Equal(t, "client_auth_storage_not_on_responses_wire", result.Coverage[3].Reason)
		require.Equal(t, CodexTelemetrySignalCoverage{Signal: "websocket_continuation", Type: "metrics", Status: "awaiting_source", EventNames: []string{"codex.websocket.continuation"}, Reason: "client_websocket_continuation_not_on_responses_wire"}, result.Coverage[4])
		require.Contains(t, result.Coverage[3].EventNames, "codex.auth_storage.lock_acquire")
		require.Contains(t, result.Coverage[3].EventNames, "codex.auth_storage.lock_wait.duration")
		require.Contains(t, result.Coverage[3].EventNames, "codex.auth_storage.lock_hold.duration")
		require.Equal(t, CodexTelemetrySignalCoverage{Signal: "used_dictation", Type: "analytics", Status: "awaiting_source", EventNames: []string{"codex_turn_event"}, Reason: "client_dictation_source_not_on_responses_wire"}, result.Coverage[5])
		require.Equal(t, CodexTelemetrySignalCoverage{Signal: "guardian_v2_classification", Type: "analytics", Status: "awaiting_source", EventNames: []string{"codex_guardian_v2_classification"}, Reason: "client_guardian_v2_classification_not_on_responses_wire"}, result.Coverage[6])
		for signal, eventNames := range pendingMetrics {
			found := false
			for _, coverage := range result.Coverage {
				if coverage.Signal != signal {
					continue
				}
				found = true
				require.Equal(t, "metrics", coverage.Type)
				require.Equal(t, "awaiting_source", coverage.Status)
				require.Equal(t, eventNames, coverage.EventNames)
				require.True(t, strings.HasPrefix(coverage.Reason, "client_") && strings.HasSuffix(coverage.Reason, "_not_on_responses_wire"))
			}
			require.True(t, found, signal)
		}
		require.Contains(t, result.Coverage, CodexTelemetrySignalCoverage{
			Signal: "artifact_operation_analytics", Type: "analytics", Status: "awaiting_source",
			EventNames: []string{"codex_artifact_operation"}, Reason: "client_artifact_operation_not_on_responses_wire",
		})
		encoded, err := json.Marshal(result.Coverage)
		require.NoError(t, err)
		require.NotContains(t, string(encoded), "user.account_id")
		require.NotContains(t, string(encoded), "skill.name")
		result.Coverage[2].EventNames[0] = "changed"
		result.Coverage[3].Reason = "changed"
		again := s.Observations(CodexTelemetryObservationQuery{})
		require.Equal(t, "codex.skill_invocation", again.Coverage[2].EventNames[0])
		require.Equal(t, "client_auth_storage_not_on_responses_wire", again.Coverage[3].Reason)
	}

	t.Setenv("CODEX_TELEMETRY_ENABLED", "false")
	s := &CodexTelemetryService{configured: true, simulationEnabled: true, observationEnabled: true}
	result := s.Observations(CodexTelemetryObservationQuery{})
	require.False(t, result.EffectiveEnabled)
	require.Equal(t, "CODEX_TELEMETRY_ENABLED", result.ForcedOffReason)
	require.Len(t, result.Coverage, 15, "capabilities remain visible while collection is disabled")
}

func TestCodexTelemetryClientOnlySignalsAreNeverInferredFromResponses(t *testing.T) {
	for _, simulation := range []bool{false, true} {
		for _, observation := range []bool{false, true} {
			store, profile := newCodexTelemetryMetricStore(), codexMetricsTestProfile()
			profile.simulationEnabled, profile.observationEnabled = simulation, observation
			profile.firstThread, profile.dynamicTool, profile.command, profile.fileChange = true, true, true, true
			profile.input.SandboxMode = "workspace-write"
			result := codexTelemetryTerminal{
				status: "completed", finished: profile.started.Add(time.Second),
				result: CodexTelemetryResult{
					EventCount:   1,
					EventMetrics: []CodexTelemetryEventMetric{codexMetricsTestEventMetric("response.completed", true, 5)},
					ServerTiming: map[string]float64{"responses_duration_excl_engine_and_client_tool_time_ms": 10},
				},
			}
			store.touch(profile)
			store.recordAttempt(profile, result)
			store.record(profile, result)
			batches := store.flush(profile.started.Add(time.Minute))
			require.Equal(t, simulation || observation, len(batches) > 0)
			for _, batch := range batches {
				for _, coverage := range codexTelemetrySignalCoverage() {
					if coverage.Type != "metrics" {
						continue
					}
					for _, name := range coverage.EventNames {
						require.False(t, codexMetricsTestMetric(batch.body, name).Exists(), name)
					}
				}
				require.Equal(t, observation, codexMetricsTestMetric(batch.body, "codex.sse_event").Exists())
				require.Equal(t, observation, codexMetricsTestMetric(batch.body, "codex.responses_api_overhead.duration_ms").Exists())
				require.Equal(t, simulation, codexMetricsTestMetric(batch.body, "codex.turn.e2e_duration_ms").Exists())
				require.NotContains(t, string(batch.body), "codex.skill_invocation")
				require.NotContains(t, string(batch.body), "codex.responses.reasoning", "missing collection must not synthesize an unavailable replay report")
			}
			events := append(codexInitializationEvents(profile), codexTerminalEvents(profile, result)...)
			require.Equal(t, simulation, len(events) > 0)
			for _, event := range events {
				require.NotEqual(t, "codex.skill_invocation", event.EventType)
				require.NotEqual(t, "codex_guardian_v2_classification", event.EventType)
				require.NotEqual(t, "codex_artifact_operation", event.EventType)
				require.NotContains(t, event.EventParams, "used_dictation")
				require.NotContains(t, event.EventParams, "classifier_turn_id")
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
