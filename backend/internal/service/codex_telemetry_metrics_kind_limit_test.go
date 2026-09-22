package service

import (
	"encoding/json"
	"fmt"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type codexMetricKindLimitPair struct {
	kind    string
	success string
}

func codexMetricKindLimitEvents(t *testing.T) []CodexTelemetryEventMetric {
	t.Helper()
	// Real allowlisted protocol kinds provide 130 named (kind, success) pairs.
	// Keep this fixture independent of the implementation's registry and limit.
	kinds := []string{
		"parse_error", "error",
		"response.created", "response.queued", "response.in_progress",
		"response.completed", "response.failed", "response.incomplete",
		"response.done", "response.cancelled", "response.canceled",
		"response.output_item.added", "response.output_item.done",
		"response.content_part.added", "response.content_part.done",
		"response.output_text.delta", "response.output_text.done", "response.output_text.annotation.added",
		"response.refusal.delta", "response.refusal.done",
		"response.function_call_arguments.delta", "response.function_call_arguments.done",
		"response.custom_tool_call_input.delta", "response.custom_tool_call_input.done",
		"response.reasoning_summary_part.added", "response.reasoning_summary_part.done",
		"response.reasoning_summary_text.delta", "response.reasoning_summary_text.done",
		"response.reasoning_text.delta", "response.reasoning_text.done", "response.reasoning.delta",
		"response.metadata", "codex.response.metadata", "responsesapi.websocket_timing",
		"response.audio.delta", "response.audio.done", "response.audio_transcript.delta", "response.audio_transcript.done",
		"response.output_audio.delta", "response.output_audio.done",
		"response.output_audio_transcript.delta", "response.output_audio_transcript.done",
		"response.file_search_call.in_progress", "response.file_search_call.searching", "response.file_search_call.completed",
		"response.web_search_call.in_progress", "response.web_search_call.searching", "response.web_search_call.completed",
		"response.image_generation_call.in_progress", "response.image_generation_call.generating",
		"response.image_generation_call.partial_image", "response.image_generation_call.completed",
		"response.code_interpreter_call.in_progress", "response.code_interpreter_call.interpreting", "response.code_interpreter_call.completed",
		"response.code_interpreter_call_code.delta", "response.code_interpreter_call_code.done",
		"response.mcp_call_arguments.delta", "response.mcp_call_arguments.done",
		"response.mcp_call.in_progress", "response.mcp_call.completed", "response.mcp_call.failed",
		"response.mcp_list_tools.in_progress", "response.mcp_list_tools.completed", "response.mcp_list_tools.failed",
	}
	events := make([]CodexTelemetryEventMetric, 0, len(kinds)*2)
	for _, kind := range kinds {
		require.Equal(t, kind, codexTelemetryEventKind(kind), "fixture must exercise named kinds instead of unknown normalization")
		for _, success := range []bool{false, true} {
			wait := float64(len(events) + 1)
			events = append(events, codexMetricsTestEventMetric(kind, success, wait, wait+0.5))
		}
	}
	require.Greater(t, len(events), 128)
	return events
}

func codexMetricKindLimitCountPairs(t *testing.T, body []byte, name string) map[codexMetricKindLimitPair]uint64 {
	t.Helper()
	counts := make(map[codexMetricKindLimitPair]uint64)
	for _, point := range codexMetricsTestMetric(body, name).Get("sum.dataPoints").Array() {
		pair := codexMetricKindLimitPair{codexMetricsTestAttribute(point, "kind"), codexMetricsTestAttribute(point, "success")}
		require.NotContains(t, counts, pair, "a label pair must have exactly one point")
		counts[pair] = point.Get("asInt").Uint()
	}
	return counts
}

func codexMetricKindLimitAssertBatch(t *testing.T, body []byte, name string, events []CodexTelemetryEventMetric) map[codexMetricKindLimitPair]uint64 {
	t.Helper()
	counts := codexMetricKindLimitCountPairs(t, body, name)
	require.Len(t, counts, 128, "126 named pairs and both unknown success buckets must fit the window")
	require.Contains(t, counts, codexMetricKindLimitPair{"unknown", "true"})
	require.Contains(t, counts, codexMetricKindLimitPair{"unknown", "false"})
	expectedCounts := make(map[codexMetricKindLimitPair]uint64)
	expectedWaits := make(map[codexMetricKindLimitPair]uint64)
	expectedSums := make(map[codexMetricKindLimitPair]float64)
	expectedBuckets := make(map[codexMetricKindLimitPair][]uint64)
	for _, event := range events {
		pair := codexMetricKindLimitPair{event.Kind, strconv.FormatBool(event.Success)}
		if _, retained := counts[pair]; !retained {
			pair.kind = "unknown"
		}
		expectedCounts[pair] += event.Count
		expectedWaits[pair] += event.WaitCount
		expectedSums[pair] += event.WaitSumMS
		if expectedBuckets[pair] == nil {
			expectedBuckets[pair] = make([]uint64, len(event.WaitBuckets))
		}
		for index, count := range event.WaitBuckets {
			expectedBuckets[pair][index] += count
		}
	}
	require.Equal(t, expectedCounts, counts, "overflow must preserve every event count under its success label")
	waitCounts := make(map[codexMetricKindLimitPair]uint64)
	for _, point := range codexMetricsTestMetric(body, name+".duration_ms").Get("histogram.dataPoints").Array() {
		pair := codexMetricKindLimitPair{codexMetricsTestAttribute(point, "kind"), codexMetricsTestAttribute(point, "success")}
		require.NotContains(t, waitCounts, pair)
		waitCounts[pair] = point.Get("count").Uint()
		require.Equal(t, expectedSums[pair], point.Get("sum").Float(), "wait durations must follow the count metric's kind mapping")
		buckets := point.Get("bucketCounts").Array()
		require.Len(t, buckets, len(expectedBuckets[pair]))
		for index, count := range expectedBuckets[pair] {
			require.Equal(t, count, buckets[index].Uint(), "pair %v bucket %d", pair, index)
		}
	}
	require.Len(t, waitCounts, 128)
	require.Equal(t, expectedWaits, waitCounts, "count and duration must share the same retained kind/success pairs")
	return counts
}

func codexMetricKindLimitOverflow(t *testing.T, events []CodexTelemetryEventMetric, retained map[codexMetricKindLimitPair]uint64) CodexTelemetryEventMetric {
	t.Helper()
	for _, event := range events {
		pair := codexMetricKindLimitPair{event.Kind, strconv.FormatBool(event.Success)}
		if _, exists := retained[pair]; !exists {
			return event
		}
	}
	t.Fatal("fixture did not overflow any named kind")
	return CodexTelemetryEventMetric{}
}

func TestCodexTelemetryMetricsKindLimitSpansAttemptsAndResetsAfterFlush(t *testing.T) {
	for _, websocket := range []bool{false, true} {
		name := "codex.sse_event"
		if websocket {
			name = "codex.websocket.event"
		}
		t.Run(name, func(t *testing.T) {
			profile := runtimeTestAttempt(time.Unix(1_800_000_000, 0).UTC(), "windows", "turn", "sample").profile
			profile.websocket, profile.simulationEnabled = websocket, false
			store := newCodexTelemetryMetricStore()
			store.touch(profile)
			events := codexMetricKindLimitEvents(t)
			split := len(events) / 2
			for index, chunk := range [][]CodexTelemetryEventMetric{events[:split], events[split:]} {
				require.Less(t, len(chunk), 128, "each physical attempt is individually below the cap")
				store.recordAttempt(profile, codexTelemetryTerminal{
					status: "completed", finished: profile.started.Add(time.Duration(index+1) * 10 * time.Second),
					result: CodexTelemetryResult{EventMetrics: chunk},
				})
			}
			batches := store.flush(profile.started.Add(time.Minute))
			require.Len(t, batches, 1)
			retained := codexMetricKindLimitAssertBatch(t, batches[0].body, name, events)
			overflow := codexMetricKindLimitOverflow(t, events, retained)

			profile.started = profile.started.Add(70 * time.Second)
			store.recordAttempt(profile, codexTelemetryTerminal{
				status: "completed", finished: profile.started.Add(time.Second),
				result: CodexTelemetryResult{EventMetrics: []CodexTelemetryEventMetric{overflow}},
			})
			next := store.flush(profile.started.Add(time.Minute))
			require.Len(t, next, 1)
			expectedPair := codexMetricKindLimitPair{overflow.Kind, strconv.FormatBool(overflow.Success)}
			require.Equal(t, map[codexMetricKindLimitPair]uint64{expectedPair: overflow.Count}, codexMetricKindLimitCountPairs(t, next[0].body, name),
				"the next delta must admit a kind that overflowed the previous window")
			points := codexMetricsTestMetric(next[0].body, name+".duration_ms").Get("histogram.dataPoints").Array()
			require.Len(t, points, 1)
			require.Equal(t, overflow.Kind, codexMetricsTestAttribute(points[0], "kind"))
			require.Equal(t, expectedPair.success, codexMetricsTestAttribute(points[0], "success"))
			require.Equal(t, overflow.WaitCount, points[0].Get("count").Uint())
			require.Equal(t, overflow.WaitSumMS, points[0].Get("sum").Float())
		})
	}
}

func codexMetricKindLimitSnapshot(t *testing.T, profile codexTelemetryProfile, version int, name string, waitsFirst bool, events []CodexTelemetryEventMetric) codexMetricStoreSnapshot {
	t.Helper()
	encodedProfile, err := marshalCodexTelemetryProfile(profile)
	require.NoError(t, err)
	counts := make([]codexMetricAggregateSnapshot, 0, len(events))
	waits := make([]codexMetricAggregateSnapshot, 0, len(events))
	for _, event := range events {
		attributes := []any{
			codexMetricMigrationStringAttribute("app.version", profile.client.version),
			codexMetricMigrationStringAttribute("auth_mode", "Chatgpt"),
			codexMetricMigrationStringAttribute("kind", event.Kind),
			codexMetricMigrationStringAttribute("model", profile.model),
			codexMetricMigrationStringAttribute("originator", profile.client.originator),
			codexMetricMigrationStringAttribute("session_source", "cli"),
			codexMetricMigrationStringAttribute("success", strconv.FormatBool(event.Success)),
		}
		counts = append(counts, codexMetricAggregateSnapshot{
			Name: name, Kind: "sum", Attributes: attributes,
			Started: profile.started, Finished: profile.started.Add(20 * time.Second),
			Count: 1, Sum: float64(event.Count), Minimum: float64(event.Count), Maximum: float64(event.Count),
			Buckets: []uint64{0, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0},
		})
		waits = append(waits, codexMetricAggregateSnapshot{
			Name: name + ".duration_ms", Kind: "histogram", Unit: "ms", Attributes: attributes,
			Started: profile.started, Finished: profile.started.Add(20 * time.Second),
			Count: event.WaitCount, Sum: event.WaitSumMS, Minimum: event.WaitMinMS, Maximum: event.WaitMaxMS,
			Buckets: append([]uint64(nil), event.WaitBuckets...),
		})
	}
	// Process the second instrument in the opposite order to catch selectors
	// that accidentally reserve separate sets for counts and durations.
	first, second := counts, waits
	if waitsFirst {
		first, second = waits, counts
	}
	pending := append([]codexMetricAggregateSnapshot(nil), first...)
	for index := len(second) - 1; index >= 0; index-- {
		pending = append(pending, second[index])
	}
	return codexMetricStoreSnapshot{
		Version: version, Clients: map[string]time.Time{},
		States: []codexMetricStateSnapshot{{
			Profile: encodedProfile, LastSeen: profile.started.Add(20 * time.Second), CollectedAt: profile.started,
			Source: "observed", Attempts: 2, Pending: pending,
		}},
	}
}

func TestCodexTelemetryMetricsKindLimitRestoresAcrossSnapshotVersions(t *testing.T) {
	for _, version := range []int{1, 2} {
		for _, websocket := range []bool{false, true} {
			for _, waitsFirst := range []bool{false, true} {
				name := "codex.sse_event"
				if websocket {
					name = "codex.websocket.event"
				}
				t.Run(fmt.Sprintf("v%d/%s/waits_first=%t", version, name, waitsFirst), func(t *testing.T) {
					profile := runtimeTestAttempt(time.Unix(1_800_000_000, 0).UTC(), "windows", "turn", "sample").profile
					profile.websocket, profile.simulationEnabled = websocket, false
					events := codexMetricKindLimitEvents(t)
					snapshot := codexMetricKindLimitSnapshot(t, profile, version, name, waitsFirst, events)
					restored, err := unmarshalCodexTelemetryMetricStore(codexMetricMigrationEncode(t, snapshot))
					require.NoError(t, err)
					state := restored.states[codexMetricStateKey(profile)]
					require.NotNil(t, state)
					retained := codexMetricKindLimitAssertBatch(t, state.batch().body, name, events)
					encoded, err := marshalCodexTelemetryMetricStore(restored)
					require.NoError(t, err)
					var saved codexMetricStoreSnapshot
					require.NoError(t, json.Unmarshal(encoded, &saved))
					require.Equal(t, 2, saved.Version)
					require.Len(t, saved.States[0].Pending, 256, "both instruments must persist no more than 128 pairs each")

					reloaded, err := unmarshalCodexTelemetryMetricStore(encoded)
					require.NoError(t, err)
					overflow := codexMetricKindLimitOverflow(t, events, retained)
					extra := codexMetricsTestEventMetric(overflow.Kind, overflow.Success, 1234, 4321)
					reloaded.recordAttempt(profile, codexTelemetryTerminal{
						status: "completed", finished: profile.started.Add(25 * time.Second),
						result: CodexTelemetryResult{EventMetrics: []CodexTelemetryEventMetric{extra}},
					})
					batches := reloaded.flush(profile.started.Add(time.Minute))
					require.Len(t, batches, 1)
					updated := codexMetricKindLimitAssertBatch(t, batches[0].body, name, append(events, extra))
					require.NotContains(t, updated, codexMetricKindLimitPair{overflow.Kind, strconv.FormatBool(overflow.Success)},
						"reloading must rebuild the full window registry before accepting another attempt")
				})
			}
		}
	}
}
