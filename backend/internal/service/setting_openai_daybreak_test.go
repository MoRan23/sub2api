//go:build unit

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestOpenAIDaybreakSettingsDefaultsAndPreference(t *testing.T) {
	for _, tc := range []struct {
		name, value string
		want        bool
	}{
		{name: "missing", want: true},
		{name: "enabled", value: "true", want: true},
		{name: "disabled", value: "false"},
		{name: "malformed", value: "invalid"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			values := map[string]string{}
			if tc.value != "" {
				values[SettingKeyOpenAIDaybreakEnabled] = tc.value
			}
			svc := NewSettingService(&forwardedIPMigrationRepoStub{values: values}, &config.Config{})
			require.Equal(t, tc.want, svc.parseSettings(values).OpenAIDaybreakEnabled)
			require.Equal(t, tc.want, svc.IsOpenAIDaybreakEnabled(context.Background()))
		})
	}
	var absent *SettingService
	require.True(t, absent.IsOpenAIDaybreakEnabled(context.Background()))
	require.True(t, (&SettingService{}).IsOpenAIDaybreakEnabled(context.Background()))
}

func TestOpenAIDaybreakOnlyPublishesSuccessfulExplicitWrites(t *testing.T) {
	repo := &forwardedIPMigrationRepoStub{values: map[string]string{SettingKeyOpenAIDaybreakEnabled: "true"}}
	svc := NewSettingService(repo, &config.Config{})
	state := func() bool { return svc.IsOpenAIDaybreakEnabled(context.Background()) }
	require.True(t, state())
	repo.setMultipleErr = errors.New("write failed")
	_, err := svc.persistSettingsAndRefreshOpenAIPolicies(context.Background(), map[string]string{SettingKeyOpenAIDaybreakEnabled: "false"}, nil)
	require.Error(t, err)
	require.True(t, state())

	repo.setMultipleErr = nil
	repo.getAllErr = errors.New("readback failed")
	_, err = svc.persistSettingsAndRefreshOpenAIPolicies(context.Background(), map[string]string{SettingKeyOpenAIDaybreakEnabled: "false"}, OmittedSettingKeys{SettingKeySiteName: {}})
	require.NoError(t, err)
	require.False(t, state(), "successful disable survives a later readback failure")

	_, err = svc.persistSettingsAndRefreshOpenAIPolicies(context.Background(), map[string]string{SettingKeySiteName: "name"}, OmittedSettingKeys{SettingKeyOpenAIDaybreakEnabled: {}})
	require.NoError(t, err)
	require.False(t, state(), "unrelated updates do not replay stale state")
}

func TestOpenAIDaybreakCacheRefreshesExternalUpdates(t *testing.T) {
	repo := &integritySettingRepoStub{forwardedIPMigrationRepoStub: forwardedIPMigrationRepoStub{values: map[string]string{SettingKeyOpenAIDaybreakEnabled: "false"}}}
	svc := NewSettingService(repo, &config.Config{})
	require.False(t, svc.IsOpenAIDaybreakEnabled(context.Background()))
	repo.values[SettingKeyOpenAIDaybreakEnabled] = "true"
	require.False(t, svc.IsOpenAIDaybreakEnabled(context.Background()), "hot reads keep their immutable cached snapshot")
	svc.openAIDaybreakCache.Store(&cachedOpenAIDaybreak{enabled: false, expiresAt: time.Now().Add(-time.Second)})
	require.True(t, svc.IsOpenAIDaybreakEnabled(context.Background()), "expired cache observes another instance's committed setting")

	svc.openAIDaybreakCache.Store(&cachedOpenAIDaybreak{enabled: false, expiresAt: time.Now().Add(-time.Second)})
	repo.err = errors.New("database unavailable")
	require.False(t, svc.IsOpenAIDaybreakEnabled(context.Background()), "read failure preserves last known explicit disable")
	require.WithinDuration(t, time.Now().Add(openAIRequestPolicyErrorTTL), svc.openAIDaybreakCache.Load().expiresAt, time.Second)

	svc.openAIDaybreakCache.Store(nil)
	require.True(t, svc.IsOpenAIDaybreakEnabled(context.Background()), "first read failure uses the compatibility default")
	require.WithinDuration(t, time.Now().Add(openAIRequestPolicyErrorTTL), svc.openAIDaybreakCache.Load().expiresAt, time.Second)
}
