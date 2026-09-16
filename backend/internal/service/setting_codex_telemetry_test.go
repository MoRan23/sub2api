//go:build unit

package service

import (
	"context"
	"errors"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestCodexTelemetrySettingsDefaultAndExplicitPreference(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value string
		want  bool
	}{
		{name: "missing", want: true},
		{name: "enabled", value: "true", want: true},
		{name: "disabled", value: "false"},
		{name: "malformed", value: "invalid"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			values := map[string]string{}
			if tc.value != "" {
				values[SettingKeyCodexTelemetryEnabled] = tc.value
			}
			svc := NewSettingService(&forwardedIPMigrationRepoStub{values: values}, &config.Config{})
			require.Equal(t, tc.want, svc.parseSettings(values).CodexTelemetryEnabled)
			require.Equal(t, tc.want, svc.IsCodexTelemetryEnabled(context.Background()))
		})
	}
}

func TestCodexTelemetrySettingsOnlyPublishSuccessfulExplicitWrites(t *testing.T) {
	t.Setenv("CODEX_TELEMETRY_ENABLED", "")
	repo := &forwardedIPMigrationRepoStub{values: map[string]string{SettingKeyCodexTelemetryEnabled: "true"}}
	svc := NewSettingService(repo, &config.Config{})
	telemetry := NewCodexTelemetryService(nil)
	t.Cleanup(telemetry.Stop)
	svc.SetCodexTelemetryService(telemetry)
	state := func() bool { return telemetry.Observations(CodexTelemetryObservationQuery{}).ConfiguredEnabled }
	require.True(t, state())

	repo.setMultipleErr = errors.New("write failed")
	_, err := svc.persistSettingsAndRefreshOpenAIPolicies(context.Background(), map[string]string{SettingKeyCodexTelemetryEnabled: "false"}, nil)
	require.Error(t, err)
	require.True(t, state(), "failed persistence must not publish a disable")
	repo.setMultipleErr = nil
	repo.getAllErr = errors.New("readback failed")
	_, err = svc.persistSettingsAndRefreshOpenAIPolicies(context.Background(), map[string]string{SettingKeyCodexTelemetryEnabled: "false"}, OmittedSettingKeys{SettingKeySiteName: {}})
	require.NoError(t, err)
	require.False(t, state(), "successful disable must apply even when the full readback fails")
	telemetry.SetEnabled(true)
	_, err = svc.persistSettingsAndRefreshOpenAIPolicies(context.Background(), map[string]string{SettingKeySiteName: "name"}, OmittedSettingKeys{SettingKeyCodexTelemetryEnabled: {}})
	require.NoError(t, err)
	require.True(t, state(), "an unrelated omitted-key write cannot publish stale telemetry state")
}
