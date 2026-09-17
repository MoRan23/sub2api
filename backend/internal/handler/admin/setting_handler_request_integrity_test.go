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

func TestUpdateSettingsRequestIntegrityPublishesAndPreservesOmission(t *testing.T) {
	h, repo := newStepUpSwitchTestHandler(t, map[string]string{})
	require.True(t, h.settingService.IsOpenAIRequestIntegrityObserveEnabled(nil))
	for _, enabled := range []bool{false, true, false} {
		rec := doUpdateSettings(t, h, map[string]any{"openai_request_integrity_observe_enabled": enabled}, nil)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		require.Equal(t, enabled, repo.values[service.SettingKeyOpenAIRequestIntegrityObserveEnabled] == "true")
		require.Equal(t, enabled, h.settingService.IsOpenAIRequestIntegrityObserveEnabled(nil))
		var body struct {
			Data dto.SystemSettings `json:"data"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
		require.Equal(t, enabled, body.Data.OpenAIRequestIntegrityObserveEnabled)
	}
	for _, payload := range []map[string]any{
		{"site_name": "request-integrity-test"},
		{"openai_request_integrity_observe_enabled": nil},
		{"installation_observation_enabled": false, "codex_telemetry_enabled": false},
	} {
		rec := doUpdateSettings(t, h, payload, nil)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		require.Equal(t, "false", repo.values[service.SettingKeyOpenAIRequestIntegrityObserveEnabled])
		require.NotContains(t, repo.lastUpdates, service.SettingKeyOpenAIRequestIntegrityObserveEnabled)
		require.False(t, h.settingService.IsOpenAIRequestIntegrityObserveEnabled(nil))
	}
}

func TestSettingsRequestIntegrityDefaultAndAudit(t *testing.T) {
	h, _ := newStepUpSwitchTestHandler(t, map[string]string{})
	read := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(read)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/admin/settings", nil)
	h.GetSettings(c)
	require.Equal(t, http.StatusOK, read.Code)
	var body struct {
		Data dto.SystemSettings `json:"data"`
	}
	require.NoError(t, json.Unmarshal(read.Body.Bytes(), &body))
	require.True(t, body.Data.OpenAIRequestIntegrityObserveEnabled)
	changed := diffSettings(&service.SystemSettings{OpenAIRequestIntegrityObserveEnabled: true}, &service.SystemSettings{}, nil, nil, UpdateSettingsRequest{})
	require.Contains(t, changed, service.SettingKeyOpenAIRequestIntegrityObserveEnabled)
}
