package service

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCodexTelemetryObservationsFilterAndCopyProvenance(t *testing.T) {
	t.Setenv("CODEX_TELEMETRY_ENABLED", "")
	s := &CodexTelemetryService{configured: true, simulationEnabled: true, observationEnabled: true}
	for i, os := range []string{"windows", "macos", "linux"} {
		profile := codexTelemetryProfile{
			input:  CodexTelemetryInput{OSFamily: os},
			client: codexTelemetryClient{localID: 42, name: "example"},
			poolID: "pool-" + os, source: []string{"observed", "mixed", "simulated"}[i],
			reasons: []string{"next_turn_boundary"}, fieldSources: map[string]string{"model": "observed"},
		}
		entry := s.newObservationLocked(profile, uint64(i+1), "analytics", []string{"codex_turn_event"}, 1)
		entry.BatchID = "batch-" + os
		s.finishObservationLocked(entry, "unknown", 0, "delivery_unknown")
	}
	result := s.Observations(CodexTelemetryObservationQuery{AccountID: 42, OSFamily: "macos", Source: "mixed", Status: "unknown"})
	require.Equal(t, 1, result.Total)
	require.Equal(t, "pool-macos", result.Items[0].PoolID)
	require.Equal(t, "batch-macos", result.Items[0].BatchID)
	require.True(t, result.Items[0].ContainsSimulated)
	require.EqualValues(t, 3, result.Counters.Unknown)
	result.Items[0].Reasons[0] = "changed"
	result.Items[0].FieldSources["model"] = "simulated"
	again := s.Observations(CodexTelemetryObservationQuery{OSFamily: "macos"})
	require.Equal(t, []string{"next_turn_boundary"}, again.Items[0].Reasons)
	require.Equal(t, "observed", again.Items[0].FieldSources["model"])
	observed := s.Observations(CodexTelemetryObservationQuery{Source: "observed"})
	require.Len(t, observed.Items, 1)
	require.False(t, observed.Items[0].ContainsSimulated, "enabled simulation does not change an observed batch's source")
	encoded, err := json.Marshal(result)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "installation_id")
	require.NotContains(t, string(encoded), "access_token")
}

func TestCodexTelemetryObservationModesAffectEffectiveState(t *testing.T) {
	t.Setenv("CODEX_TELEMETRY_ENABLED", "")
	s := &CodexTelemetryService{configured: true}
	result := s.Observations(CodexTelemetryObservationQuery{})
	require.True(t, result.ConfiguredEnabled)
	require.False(t, result.EffectiveEnabled)
	require.False(t, result.SimulationEnabled)
	require.False(t, result.ObservationEnabled)
}
