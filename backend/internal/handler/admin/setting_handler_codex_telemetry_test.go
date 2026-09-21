package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/handler/dto"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestUpdateSettingsCodexTelemetryPublishesAndPreservesOmission(t *testing.T) {
	t.Setenv("CODEX_TELEMETRY_ENABLED", "")
	h, repo := newStepUpSwitchTestHandler(t, map[string]string{})
	telemetry := service.NewCodexTelemetryService(nil)
	t.Cleanup(telemetry.Stop)
	h.settingService.SetCodexTelemetryService(telemetry)
	callbackCount := 0
	h.settingService.SetOnUpdateCallback(func() { callbackCount++ })
	require.True(t, telemetry.Observations(service.CodexTelemetryObservationQuery{}).EffectiveEnabled)

	for _, enabled := range []bool{false, true, false} {
		rec := doUpdateSettings(t, h, map[string]any{"codex_telemetry_enabled": enabled}, nil)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		require.Equal(t, enabled, repo.values[service.SettingKeyCodexTelemetryEnabled] == "true")
		require.Equal(t, enabled, telemetry.Observations(service.CodexTelemetryObservationQuery{}).EffectiveEnabled)
	}
	for _, payload := range []map[string]any{
		{"site_name": "telemetry-test"},
		{"codex_telemetry_enabled": nil},
	} {
		rec := doUpdateSettings(t, h, payload, nil)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		require.Equal(t, "false", repo.values[service.SettingKeyCodexTelemetryEnabled])
		require.NotContains(t, repo.lastUpdates, service.SettingKeyCodexTelemetryEnabled)
		require.False(t, telemetry.Observations(service.CodexTelemetryObservationQuery{}).EffectiveEnabled)
	}
	require.Equal(t, 5, callbackCount, "telemetry registration must not replace existing settings callbacks")
}

func TestSettingsCodexTelemetryConfiguredAndEnvironmentEffectiveState(t *testing.T) {
	t.Setenv("CODEX_TELEMETRY_ENABLED", "false")
	h, repo := newStepUpSwitchTestHandler(t, map[string]string{})
	read := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(read)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/admin/settings", nil)
	h.GetSettings(c)
	require.Equal(t, http.StatusOK, read.Code)
	write := doUpdateSettings(t, h, map[string]any{
		"codex_telemetry_enabled":           true,
		"codex_telemetry_effective_enabled": true,
		"codex_telemetry_forced_off_reason": "override-attempt",
	}, nil)
	require.Equal(t, http.StatusOK, write.Code, write.Body.String())
	for _, rec := range []*httptest.ResponseRecorder{read, write} {
		var body struct {
			Data dto.SystemSettings `json:"data"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
		require.True(t, body.Data.CodexTelemetryEnabled)
		require.False(t, body.Data.CodexTelemetryEffectiveEnabled)
		require.Equal(t, "CODEX_TELEMETRY_ENABLED", body.Data.CodexTelemetryForcedOffReason)
	}
	require.NotContains(t, repo.lastUpdates, "codex_telemetry_effective_enabled")
	require.NotContains(t, repo.lastUpdates, "codex_telemetry_forced_off_reason")
}

func TestDiffSettingsIncludesCodexTelemetrySwitch(t *testing.T) {
	changed := diffSettings(&service.SystemSettings{CodexTelemetryEnabled: true}, &service.SystemSettings{}, nil, nil, UpdateSettingsRequest{})
	require.Contains(t, changed, service.SettingKeyCodexTelemetryEnabled)
}

func TestSettingsCodexTelemetryModesAreIndependentAndPreserveOmission(t *testing.T) {
	t.Setenv("CODEX_TELEMETRY_ENABLED", "")
	h, repo := newStepUpSwitchTestHandler(t, map[string]string{})
	telemetry := service.NewCodexTelemetryService(nil)
	t.Cleanup(telemetry.Stop)
	h.settingService.SetCodexTelemetryService(telemetry)
	for _, modes := range [][2]bool{{true, true}, {false, true}, {true, false}, {false, false}} {
		rec := doUpdateSettings(t, h, map[string]any{"codex_telemetry_simulation_enabled": modes[0], "codex_telemetry_observation_enabled": modes[1]}, nil)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		state := telemetry.Observations(service.CodexTelemetryObservationQuery{})
		require.True(t, state.ConfiguredEnabled)
		require.Equal(t, modes[0], state.SimulationEnabled)
		require.Equal(t, modes[1], state.ObservationEnabled)
		require.Equal(t, modes[0] || modes[1], state.EffectiveEnabled)
	}
	rec := doUpdateSettings(t, h, map[string]any{"codex_telemetry_simulation_enabled": nil, "site_name": "keep-modes"}, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NotContains(t, repo.lastUpdates, service.SettingKeyCodexTelemetrySimulationEnabled)
	require.NotContains(t, repo.lastUpdates, service.SettingKeyCodexTelemetryObservationEnabled)
	require.False(t, telemetry.Observations(service.CodexTelemetryObservationQuery{}).SimulationEnabled)
	require.False(t, telemetry.Observations(service.CodexTelemetryObservationQuery{}).ObservationEnabled)
}
