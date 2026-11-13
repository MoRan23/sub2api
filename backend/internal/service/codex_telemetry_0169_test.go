package service

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCodexTelemetry0169ObservedTurnTriggerOnActivityEvents(t *testing.T) {
	for _, observation := range []bool{false, true} {
		for _, trigger := range []string{"", "collaboration"} {
			profile := codexTelemetryEventTestProfile()
			profile.observationEnabled, profile.input.TurnTrigger = observation, trigger
			profile.fieldSources = map[string]string{"model": "observed"}
			events := codexTerminalEvents(profile, codexTelemetryTerminal{status: "completed", finished: profile.started.Add(time.Second)})
			events = append(events, codexHookEvent(profile, "Stop"))
			counts := make(map[string]int)
			for _, event := range events {
				switch event.EventType {
				case "codex_dynamic_tool_call_event", "codex_command_execution_event", "codex_file_change_event", "codex_hook_run":
					counts[event.EventType]++
					if observation && trigger != "" {
						require.Equal(t, trigger, event.EventParams["turn_trigger"])
						sources := codexAnalyticsFieldSources(profile, []codexAnalyticsEvent{event})
						require.Equal(t, "observed", sources.fieldSources["turn_trigger"])
					} else {
						require.NotContains(t, event.EventParams, "turn_trigger")
					}
					require.Equal(t, "simulated", event.source, "an observed trigger does not turn virtual activity into client evidence")
				}
			}
			require.Equal(t, map[string]int{"codex_dynamic_tool_call_event": 1, "codex_command_execution_event": 1, "codex_file_change_event": 1, "codex_hook_run": 1}, counts)
			require.NotContains(t, profile.fieldSources, "turn_trigger", "provenance must not mutate the shared profile")
		}
	}
}

func TestCodexTelemetry0169GuardianLabelsShareExistingReview(t *testing.T) {
	profile := codexTelemetryEventTestProfile()
	result := codexTelemetryTerminal{status: "completed", finished: profile.started.Add(2 * time.Second)}
	profile.ended = result.finished
	events := codexTerminalEvents(profile, result)
	reviews := 0
	for _, event := range events {
		if event.EventType != "codex_guardian_review" {
			continue
		}
		reviews++
		require.Equal(t, "simulated", event.source)
		require.Equal(t, "codex-auto-review", event.EventParams["guardian_model"])
		require.Equal(t, false, event.EventParams["reviewed_action_truncated"])
		for _, field := range []string{"guardian_context_mode", "risk_level", "user_authorization", "outcome", "guardian_reasoning_effort"} {
			require.Contains(t, event.EventParams, field)
			require.Nil(t, event.EventParams[field])
		}
	}
	require.Equal(t, 1, reviews)
	require.Len(t, events, 9, "schema alignment does not create extra virtual reviews")
	metrics := newCodexTelemetryMetricStore()
	metrics.record(profile, result)
	batches := metrics.flush(profile.started.Add(2 * time.Minute))
	require.Len(t, batches, 1)
	require.Equal(t, "simulated", batches[0].source)
	for _, descriptor := range []struct{ name, point string }{
		{"codex.guardian.review", "sum.dataPoints.0"},
		{"codex.guardian.review.duration_ms", "histogram.dataPoints.0"},
	} {
		point := codexMetricsTestMetric(batches[0].body, descriptor.name).Get(descriptor.point)
		require.True(t, point.Exists())
		for key, value := range map[string]string{
			"context_mode": "unknown", "risk_level": "none", "user_authorization": "none", "outcome": "none",
			"guardian_model": "codex-auto-review", "guardian_reasoning_effort": "none", "reviewed_action_truncated": "false",
		} {
			require.Equal(t, value, codexMetricsTestAttribute(point, key), key)
		}
		if descriptor.name == "codex.guardian.review" {
			require.Equal(t, "false", codexMetricsTestAttribute(point, "context_changed"))
			require.EqualValues(t, 1, point.Get("asInt").Int())
		} else {
			require.Empty(t, codexMetricsTestAttribute(point, "context_changed"))
			require.EqualValues(t, 1, point.Get("count").Int())
			require.EqualValues(t, codexActivityDuration(profile, "guardian", 100, 800), point.Get("sum").Float())
		}
	}
}

func TestCodexTelemetry0169FinalGuardianMarkerAndSnapshot(t *testing.T) {
	for _, test := range []struct {
		values   []string
		expected string
	}{
		{nil, ""}, {[]string{"reviewer"}, "reviewer"}, {[]string{"classifier"}, "classifier"},
		{[]string{"unknown"}, ""}, {[]string{"reviewer", "classifier"}, ""}, {[]string{"reviewer", "reviewer"}, ""},
	} {
		headers := http.Header{}
		headers[openAIGuardianHeader] = test.values
		input := codexTelemetryInputFromWire(&Account{ID: 12, Platform: PlatformOpenAI, Type: AccountTypeOAuth}, headers, []byte(`{"model":"gpt-6-astra"}`), "", true, CodexWireProfile{})
		require.Equal(t, test.expected, input.GuardianRequest)
		profile := codexTelemetryEventTestProfile()
		profile.input.GuardianRequest = input.GuardianRequest
		encoded, err := marshalCodexTelemetryProfile(profile)
		require.NoError(t, err)
		restored, err := unmarshalCodexTelemetryProfile(encoded)
		require.NoError(t, err)
		require.Equal(t, test.expected, restored.input.GuardianRequest)
		result := codexTelemetryTerminal{status: "completed", finished: profile.started.Add(time.Second), result: CodexTelemetryResult{EventCount: 1}}
		events := codexTerminalEvents(restored, result)
		if test.expected == "" {
			require.NotEmpty(t, events)
		} else {
			require.Empty(t, events, "a Guardian request does not prove a client lifecycle")
			metrics := newCodexTelemetryMetricStore()
			metrics.touch(restored)
			metrics.record(restored, result)
			metrics.recordAttempt(restored, result)
			batches := metrics.flush(profile.started.Add(2 * time.Minute))
			require.Len(t, batches, 1)
			require.Equal(t, "observed", batches[0].source)
			for _, name := range batches[0].names {
				require.Equal(t, "codex.sse_event", name)
			}
		}
	}
}

func TestCodexTelemetry0169ControlKindsKeepTerminalAndUsage(t *testing.T) {
	var stream codexTelemetryStream
	now := time.Now()
	for _, kind := range []string{"response.interrupt.accepted", "response.interrupt.failed", "codex.rate_limits"} {
		payload, err := json.Marshal(map[string]any{"type": kind})
		require.NoError(t, err)
		stream.observe(payload, "", now, time.Millisecond)
		require.Equal(t, kind, codexTelemetryEventKind(kind))
		require.Empty(t, stream.result.Status)
		require.Zero(t, stream.result.InputTokens)
		require.True(t, stream.result.FirstTokenAt.IsZero())
	}
	require.Len(t, stream.result.EventMetrics, 3)
	require.EqualValues(t, 3, stream.result.EventCount)
	terminal := []byte(`{"type":"response.completed","response":{"id":"resp_final","usage":{"input_tokens":12,"output_tokens":4}}}`)
	stream.observe(terminal, "", now.Add(time.Second), time.Millisecond)
	stream.observe(terminal, "", now.Add(time.Second), time.Millisecond)
	require.Equal(t, "completed", stream.result.Status)
	require.EqualValues(t, 12, stream.result.InputTokens)
	require.EqualValues(t, 4, stream.result.OutputTokens)
	require.EqualValues(t, 4, stream.result.EventCount)
}
