package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

var telemetryCriticalPathDurations = []string{
	"critical_path_duration_ms", "critical_path_inference_ms", "critical_path_auto_review_ms",
	"critical_path_user_wait_ms", "critical_path_tools_ms", "critical_path_overhead_ms", "critical_path_unknown_ms",
}

func requireTelemetryCriticalPathDisabled(t *testing.T, params map[string]json.RawMessage) {
	t.Helper()
	require.JSONEq(t, `"disabled"`, string(params["critical_path_status"]))
	for _, field := range telemetryCriticalPathDurations {
		require.Contains(t, params, field)
		require.Equal(t, "null", string(params[field]), "missing critical-path capture must not become a zero duration")
	}
}

func TestCodexTelemetry0166CriticalPathIsSimulationOnly(t *testing.T) {
	for _, simulate := range []bool{false, true} {
		name := "observation_only"
		if simulate {
			name = "simulation"
		}
		t.Run(name, func(t *testing.T) {
			profile := codexTelemetryEventTestProfile()
			profile.simulationEnabled = simulate
			profile.fieldSources = map[string]string{"model": "observed"}
			terminal := codexTelemetryTerminal{status: "completed", finished: profile.started.Add(20 * time.Second)}
			events := codexTerminalEvents(profile, terminal)
			if !simulate {
				require.Empty(t, events, "observation-only must not start publishing simulated analytics")
			}
			// Exercise both producers: a real upstream duration does not make
			// critical-path category durations known, including for title turns.
			for _, event := range []codexAnalyticsEvent{codexMainTurnEvent(profile, terminal), codexTitleTurnEvent(profile, "title-thread")} {
				body, err := json.Marshal(event.EventParams)
				require.NoError(t, err)
				var params map[string]json.RawMessage
				require.NoError(t, json.Unmarshal(body, &params))
				withSources := codexAnalyticsFieldSources(profile, []codexAnalyticsEvent{event})
				if simulate {
					requireTelemetryCriticalPathDisabled(t, params)
					require.Equal(t, "simulated", withSources.fieldSources["critical_path_status"])
				} else {
					require.NotContains(t, params, "critical_path_status")
					require.NotContains(t, withSources.fieldSources, "critical_path_status")
					for _, field := range telemetryCriticalPathDurations {
						require.NotContains(t, params, field)
					}
				}
				for _, field := range telemetryCriticalPathDurations {
					require.NotContains(t, withSources.fieldSources, field)
				}
				require.Equal(t, map[string]string{"model": "observed"}, profile.fieldSources, "shared profile provenance stays immutable")
				require.NotContains(t, string(body), "field_sources")
			}
		})
	}
}

func TestCodexTelemetry0166RestoredUnsealedTurnUsesCurrentSchema(t *testing.T) {
	now := time.Now().UTC()
	profile := codexTelemetryEventTestProfile()
	profile.started = now
	profile.client.version = "0.165.0"
	profile.fieldSources = map[string]string{"model": "observed"}
	encoded, err := marshalCodexTelemetryProfile(profile)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "critical_path", "old unsealed snapshots store facts, not event schema")
	turn := codexTelemetryRuntimeTurn{Profile: encoded, Explicit: true, ThreadID: profile.threadID, LastSeen: now,
		Attempts: 1, Samples: 1, Result: CodexTelemetryResult{Status: "completed", HTTPStatus: 200, FinishedAt: now.Add(time.Second)}}
	tx := CodexTelemetryPoolTransaction{Policy: CodexTelemetryPolicy{Epoch: 1, Enabled: true, Simulation: true, Observation: true}, Activities: map[string]CodexTelemetryActivity{}}
	require.NoError(t, putRuntimeActivity(&tx, "turn", "old-turn", turn, now, now.Add(time.Hour)))
	// A fresh in-memory runtime sees only serialized durable state.
	snapshot, err := json.Marshal(tx)
	require.NoError(t, err)
	var restored CodexTelemetryPoolTransaction
	require.NoError(t, json.Unmarshal(snapshot, &restored))
	require.NoError(t, sealCodexTelemetryRuntime(&restored, "old-turn", now.Add(2*time.Second)))
	require.Len(t, restored.Batches, 1)
	batch := restored.Batches[0]
	require.Equal(t, "0.165.0", batch.ClientVersion, "restoration does not rewrite the captured client version")
	var payload struct {
		Events []struct {
			Type   string                     `json:"event_type"`
			Params map[string]json.RawMessage `json:"event_params"`
		} `json:"events"`
	}
	require.NoError(t, json.Unmarshal(batch.Payload, &payload))
	turns := 0
	for _, event := range payload.Events {
		if event.Type == "codex_turn_event" {
			turns++
			requireTelemetryCriticalPathDisabled(t, event.Params)
		}
	}
	require.Equal(t, 2, turns, "main and title turn both use the updated schema")
	restoredProfile, err := unmarshalCodexTelemetryProfile(batch.Metadata)
	require.NoError(t, err)
	require.Equal(t, "simulated", restoredProfile.fieldSources["critical_path_status"])
	for _, field := range telemetryCriticalPathDurations {
		require.NotContains(t, restoredProfile.fieldSources, field)
	}
	require.NotContains(t, string(batch.Payload), "field_sources")
	require.NoError(t, sealCodexTelemetryRuntime(&restored, "old-turn", now.Add(3*time.Second)))
	require.Len(t, restored.Batches, 1, "repeat recovery cannot publish a second copy")
}

func TestCodexTelemetry0166SealedLegacyBatchKeepsPayloadOnRecovery(t *testing.T) {
	now := time.Now().UTC()
	store := NewMemoryCodexTelemetryStore()
	profile := codexTelemetryEventTestProfile()
	profile.client.version = "0.165.0"
	profile.fieldSources = nil
	profile.source = "simulated"
	key := CodexTelemetryPoolKey{OwnerAccountID: profile.client.localID, OSFamily: "linux", InstallationID: "legacy-installation"}
	legacyPayload := []byte(`{"events":[{"event_type":"codex_turn_event","event_params":{"status":"completed","duration_ms":1000}}]}`)
	_, err := store.TransactPool(context.Background(), key, now, func(tx *CodexTelemetryPoolTransaction) error {
		return appendRuntimeBatch(tx, profile, "analytics", legacyPayload, now)
	})
	require.NoError(t, err)
	claimed, err := store.ClaimBatches(context.Background(), now, 1)
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	// A process disappeared after claim but before sending. Safe recovery may
	// reclaim this batch, but must not regenerate its frozen analytics events.
	reclaimed, err := store.ClaimBatches(context.Background(), now.Add(31*time.Second), 1)
	require.NoError(t, err)
	require.Len(t, reclaimed, 1)
	require.NotEqual(t, claimed[0].ClaimID, reclaimed[0].ClaimID)
	require.Equal(t, legacyPayload, []byte(reclaimed[0].Payload))
	require.Equal(t, claimed[0].Metadata, reclaimed[0].Metadata)
	restoredProfile, err := unmarshalCodexTelemetryProfile(reclaimed[0].Metadata)
	require.NoError(t, err)
	require.NotContains(t, restoredProfile.fieldSources, "critical_path_status")
	sender := &CodexTelemetryService{analyticsURL: "https://example.test/events"}
	code, reason, cancelled := sender.send(context.Background(), func(_ context.Context, req *http.Request, _ CodexTelemetryInput, _ bool) (*http.Response, error) {
		body, err := io.ReadAll(req.Body)
		require.NoError(t, err)
		require.Equal(t, legacyPayload, body)
		return &http.Response{StatusCode: http.StatusAccepted, Body: io.NopCloser(strings.NewReader("{}"))}, nil
	}, codexTelemetryJob{profile: restoredProfile, body: reclaimed[0].Payload})
	require.Equal(t, http.StatusAccepted, code)
	require.Empty(t, reason)
	require.False(t, cancelled)
}
