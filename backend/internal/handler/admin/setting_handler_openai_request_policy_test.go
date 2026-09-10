//go:build unit

package admin

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestUpdateSettingsOpenAIRequestPolicyPartialPayload(t *testing.T) {
	keys := []string{
		service.SettingKeyEnableOpenAIRequestTimezoneConversion,
		service.SettingKeyEnableOpenAIPassthroughTimezoneConversion,
		service.SettingKeyEnableOpenAICodexResidencyUS,
	}
	for _, key := range keys {
		for _, enabled := range []bool{false, true} {
			t.Run(key+"/"+boolSettingValue(enabled), func(t *testing.T) {
				stored := map[string]string{}
				for _, storedKey := range keys {
					stored[storedKey] = boolSettingValue(!enabled)
				}
				h, repo := newStepUpSwitchTestHandler(t, stored)
				rec := doUpdateSettings(t, h, map[string]any{key: enabled}, nil)
				require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
				require.Equal(t, boolSettingValue(enabled), repo.values[key])
				require.Equal(t, boolSettingValue(enabled), repo.lastUpdates[key])
				var body struct {
					Data map[string]any `json:"data"`
				}
				require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
				for _, otherKey := range keys {
					if otherKey == key {
						require.Equal(t, enabled, body.Data[otherKey])
					} else {
						require.NotContains(t, repo.lastUpdates, otherKey)
						require.Equal(t, !enabled, body.Data[otherKey])
					}
				}
				policy := h.settingService.GetOpenAIRequestPolicy(nil)
				require.Equal(t, repo.values[keys[0]] == "true", policy.TimezoneConversionEnabled)
				require.Equal(t, repo.values[keys[1]] == "true", policy.PassthroughTimezoneConversionEnabled)
				require.Equal(t, repo.values[keys[2]] == "true", policy.CodexResidencyUS)
			})
		}
	}
}

func TestUpdateSettingsOpenAIRequestPolicyOmittedDefaults(t *testing.T) {
	h, repo := newStepUpSwitchTestHandler(t, map[string]string{})
	rec := doUpdateSettings(t, h, map[string]any{"risk_control_enabled": true}, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	var body struct {
		Data map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	for _, key := range []string{
		service.SettingKeyEnableOpenAIRequestTimezoneConversion,
		service.SettingKeyEnableOpenAIPassthroughTimezoneConversion,
		service.SettingKeyEnableOpenAICodexResidencyUS,
	} {
		require.Equal(t, true, body.Data[key])
		require.NotContains(t, repo.values, key)
	}
}
