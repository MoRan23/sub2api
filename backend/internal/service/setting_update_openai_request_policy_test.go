//go:build unit

package service

import (
	"context"
	"errors"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/stretchr/testify/require"
)

func TestSettingsOpenAIRequestPolicyDefaults(t *testing.T) {
	repo := &forwardedIPMigrationRepoStub{values: map[string]string{}}
	svc := NewSettingService(repo, &config.Config{})
	require.NoError(t, svc.InitializeDefaultSettings(context.Background()))
	for _, key := range openAIRequestPolicySettingKeys {
		require.Equal(t, "true", repo.values[key])
	}
	for _, values := range []map[string]string{nil, {SettingKeyEnableOpenAICodexResidencyUS: "bad"}} {
		settings := svc.parseSettings(values)
		require.True(t, settings.EnableOpenAIRequestTimezoneConversion)
		require.True(t, settings.EnableOpenAIPassthroughTimezoneConversion)
		require.True(t, settings.EnableOpenAICodexResidencyUS)
	}
}

func TestSettingsOpenAIRequestPolicyPublishesPersistedSnapshot(t *testing.T) {
	repo := &settingUpdateRepoStub{}
	svc := NewSettingService(repo, &config.Config{})
	require.NoError(t, svc.UpdateSettings(context.Background(), &SystemSettings{
		EnableOpenAIRequestTimezoneConversion:     true,
		EnableOpenAIPassthroughTimezoneConversion: false,
		EnableOpenAICodexResidencyUS:              false,
	}))
	require.Equal(t, "true", repo.updates[SettingKeyEnableOpenAIRequestTimezoneConversion])
	require.Equal(t, "false", repo.updates[SettingKeyEnableOpenAIPassthroughTimezoneConversion])
	require.Equal(t, "false", repo.updates[SettingKeyEnableOpenAICodexResidencyUS])
	require.Equal(t, openai.RequestPolicy{TimezoneConversionEnabled: true}, svc.GetOpenAIRequestPolicy(nil))

	repo.setMultipleErr = errors.New("write failed")
	require.Error(t, svc.UpdateSettings(context.Background(), &SystemSettings{}))
	require.Equal(t, openai.RequestPolicy{TimezoneConversionEnabled: true}, svc.GetOpenAIRequestPolicy(nil))
}

func TestSettingsOpenAIRequestPolicyOmittedKeepsAuthoritativeSnapshot(t *testing.T) {
	repo := &codexPolicyUpdateRepo{values: map[string]string{SettingKeyEnableOpenAICodexResidencyUS: "false"}}
	svc := NewSettingService(repo, &config.Config{})
	want := svc.GetOpenAIRequestPolicy(nil)
	omitted := OmittedSettingKeys{}
	for _, key := range openAIRequestPolicySettingKeys {
		omitted[key] = struct{}{}
	}
	require.NoError(t, svc.UpdateSettingsOmitting(context.Background(), &SystemSettings{
		EnableOpenAIRequestTimezoneConversion:     false,
		EnableOpenAIPassthroughTimezoneConversion: false,
		EnableOpenAICodexResidencyUS:              true,
	}, omitted))
	require.Equal(t, want, svc.GetOpenAIRequestPolicy(nil))
	require.Equal(t, "false", repo.values[SettingKeyEnableOpenAICodexResidencyUS])
	require.NotContains(t, repo.values, SettingKeyEnableOpenAIRequestTimezoneConversion)
	require.NotContains(t, repo.values, SettingKeyEnableOpenAIPassthroughTimezoneConversion)
	require.Equal(t, 1, repo.getMultipleCalls)

	repo.getAllErr = errors.New("readback unavailable")
	repo.getMultipleErr = errors.New("runtime read unavailable")
	require.NoError(t, svc.UpdateSettingsOmitting(context.Background(), &SystemSettings{}, omitted))
	require.Equal(t, want, svc.GetOpenAIRequestPolicy(nil))
}
