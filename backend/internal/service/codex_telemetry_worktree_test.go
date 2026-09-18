package service

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCodexTelemetryThreadInitializationWorktreeIsExplicitlyUnknown(t *testing.T) {
	for _, userAgent := range []string{
		"codex-tui/0.155.0 (Ubuntu 24.04.4; x86_64)",
		"Codex Desktop/0.155.0 (Mac OS 26.6.2; arm64)",
		"codex-tui/0.155.0 (Windows 10.0.26200; x86_64)",
	} {
		t.Run(userAgent, func(t *testing.T) {
			profile := codexTelemetryEventTestProfile()
			profile.client.userAgent = userAgent
			seen := make(map[string]bool)
			for _, event := range codexInitializationEvents(profile) {
				encoded, err := json.Marshal(event)
				require.NoError(t, err)
				var wire struct {
					Params map[string]json.RawMessage `json:"event_params"`
				}
				require.NoError(t, json.Unmarshal(encoded, &wire))
				if event.EventType != "codex_thread_initialized" {
					require.NotContains(t, wire.Params, "is_worktree")
					continue
				}
				require.Contains(t, wire.Params, "is_worktree")
				require.Equal(t, "null", string(wire.Params["is_worktree"]))
				seen[event.EventParams["thread_source"].(string)] = true
			}
			require.Equal(t, map[string]bool{"subagent": true, "guardian_review": true, "thread_title": true}, seen)
		})
	}
}

func TestCodexTelemetryWorktreeObservationMatchesAnalyticsOnly(t *testing.T) {
	s, sent := telemetryCaptureService(t)
	attempt := s.Begin(context.Background(), telemetryTestInput())
	require.NotNil(t, attempt)
	attempt.Finish(CodexTelemetryResult{Status: "completed", HTTPStatus: 200})
	s.flushMetrics(time.Now())
	telemetryWaitDrained(t, s)
	initializations := 0
	for _, call := range sent() {
		if call.metrics {
			continue
		}
		var payload struct {
			Events []struct {
				Type   string                     `json:"event_type"`
				Params map[string]json.RawMessage `json:"event_params"`
			} `json:"events"`
		}
		require.NoError(t, json.Unmarshal(call.body, &payload))
		for _, event := range payload.Events {
			if event.Type == "codex_thread_initialized" {
				initializations++
				require.Equal(t, "null", string(event.Params["is_worktree"]))
			}
		}
	}
	require.Equal(t, 3, initializations)

	withWorktree, withoutWorktree := 0, 0
	snapshot := s.Observations(CodexTelemetryObservationQuery{PageSize: 100})
	for _, item := range snapshot.Items {
		encoded, err := json.Marshal(item)
		require.NoError(t, err)
		var wire map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(encoded, &wire))
		if len(item.IsWorktree) > 0 {
			withWorktree++
			require.Equal(t, "analytics", item.Type)
			require.Contains(t, item.EventNames, "codex_thread_initialized")
			require.Equal(t, "null", string(wire["is_worktree"]))
			copy(item.IsWorktree, "true") // Returned snapshots must not mutate the history.
		} else {
			withoutWorktree++
			require.NotContains(t, wire, "is_worktree")
		}
	}
	require.Equal(t, 1, withWorktree)
	require.Positive(t, withoutWorktree)
	for _, item := range s.Observations(CodexTelemetryObservationQuery{PageSize: 100}).Items {
		if len(item.IsWorktree) > 0 {
			require.Equal(t, "null", string(item.IsWorktree))
		}
	}
	legacy, err := json.Marshal(CodexTelemetryObservation{Type: "analytics", EventNames: []string{"codex_thread_initialized"}})
	require.NoError(t, err)
	require.NotContains(t, string(legacy), "is_worktree", "uncollected state is distinct from an explicit unknown")
}

func TestCodexTelemetryThreadStartedWorktreeUnknownAggregates(t *testing.T) {
	store := newCodexTelemetryMetricStore()
	profile := codexMetricsTestProfile()
	profile.firstThread = true
	store.record(profile, codexTelemetryTerminal{status: "completed", finished: profile.started.Add(time.Second)})
	profile.threadID, profile.turnID = "another-thread", "another-turn"
	store.record(profile, codexTelemetryTerminal{status: "completed", finished: profile.started.Add(2 * time.Second)})
	batches := store.flush(profile.started.Add(time.Minute))
	require.Len(t, batches, 1)
	points := codexMetricsTestMetric(batches[0].body, "codex.thread.started").Get("sum.dataPoints").Array()
	require.Len(t, points, 1)
	require.Equal(t, "unknown", codexMetricsTestAttribute(points[0], "is_worktree"))
	require.EqualValues(t, 2, points[0].Get("asInt").Uint())
	require.Equal(t, 2, batches[0].turns)
	require.Empty(t, batches[0].profile.threadID)
	require.Empty(t, batches[0].profile.turnID)
	require.Len(t, codexMetricDescriptors, 66)
}
