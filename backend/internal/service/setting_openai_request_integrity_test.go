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

func TestOpenAIRequestIntegrityObserveSettingsDefaultsAndPreference(t *testing.T) {
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
				values[SettingKeyOpenAIRequestIntegrityObserveEnabled] = tc.value
			}
			svc := NewSettingService(&forwardedIPMigrationRepoStub{values: values}, &config.Config{})
			require.Equal(t, tc.want, svc.parseSettings(values).OpenAIRequestIntegrityObserveEnabled)
			require.Equal(t, tc.want, svc.IsOpenAIRequestIntegrityObserveEnabled(context.Background()))
		})
	}
	var absent *SettingService
	require.True(t, absent.IsOpenAIRequestIntegrityObserveEnabled(nil))
}

func TestOpenAIRequestIntegrityObserveOnlyPublishesSuccessfulExplicitWrites(t *testing.T) {
	repo := &forwardedIPMigrationRepoStub{values: map[string]string{SettingKeyOpenAIRequestIntegrityObserveEnabled: "true"}}
	svc := NewSettingService(repo, &config.Config{})
	state := func() bool { return svc.IsOpenAIRequestIntegrityObserveEnabled(context.Background()) }
	require.True(t, state())
	repo.setMultipleErr = errors.New("write failed")
	_, err := svc.persistSettingsAndRefreshOpenAIPolicies(context.Background(), map[string]string{SettingKeyOpenAIRequestIntegrityObserveEnabled: "false"}, nil)
	require.Error(t, err)
	require.True(t, state())

	repo.setMultipleErr = nil
	repo.getAllErr = errors.New("readback failed")
	_, err = svc.persistSettingsAndRefreshOpenAIPolicies(context.Background(), map[string]string{SettingKeyOpenAIRequestIntegrityObserveEnabled: "false"}, OmittedSettingKeys{SettingKeySiteName: {}})
	require.NoError(t, err)
	require.False(t, state(), "successful disable survives a later readback failure")

	_, err = svc.persistSettingsAndRefreshOpenAIPolicies(context.Background(), map[string]string{SettingKeySiteName: "name"}, OmittedSettingKeys{SettingKeyOpenAIRequestIntegrityObserveEnabled: {}})
	require.NoError(t, err)
	require.False(t, state(), "unrelated updates do not replay stale state")
}

func TestOpenAIRequestIntegrityObserveCacheRefreshesExternalUpdates(t *testing.T) {
	repo := &integritySettingRepoStub{forwardedIPMigrationRepoStub: forwardedIPMigrationRepoStub{values: map[string]string{SettingKeyOpenAIRequestIntegrityObserveEnabled: "false"}}}
	svc := NewSettingService(repo, &config.Config{})
	require.False(t, svc.IsOpenAIRequestIntegrityObserveEnabled(nil))
	repo.values[SettingKeyOpenAIRequestIntegrityObserveEnabled] = "true"
	require.False(t, svc.IsOpenAIRequestIntegrityObserveEnabled(nil), "hot reads keep their immutable cached snapshot")
	svc.openAIRequestIntegrityObserveCache.Store(&cachedOpenAIRequestIntegrityObserve{enabled: false, expiresAt: time.Now().Add(-time.Second)})
	require.True(t, svc.IsOpenAIRequestIntegrityObserveEnabled(nil), "expired cache observes another instance's committed setting")

	svc.openAIRequestIntegrityObserveCache.Store(&cachedOpenAIRequestIntegrityObserve{enabled: false, expiresAt: time.Now().Add(-time.Second)})
	repo.err = errors.New("database unavailable")
	require.False(t, svc.IsOpenAIRequestIntegrityObserveEnabled(nil), "read failure preserves last known explicit disable")
	require.WithinDuration(t, time.Now().Add(openAIRequestPolicyErrorTTL), svc.openAIRequestIntegrityObserveCache.Load().expiresAt, time.Second)
}

type integritySettingRepoStub struct {
	forwardedIPMigrationRepoStub
	err error
}

func (s *integritySettingRepoStub) GetValue(ctx context.Context, key string) (string, error) {
	if s.err != nil {
		return "", s.err
	}
	return s.forwardedIPMigrationRepoStub.GetValue(ctx, key)
}
