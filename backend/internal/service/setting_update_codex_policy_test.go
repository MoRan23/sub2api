//go:build unit

package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type codexPolicyUpdateRepo struct {
	mu sync.Mutex

	values           map[string]string
	getAllErr        error
	getMultipleErr   error
	getAllCalls      int
	getMultipleCalls int

	blockUnrelatedWrite   bool
	unrelatedWriteStarted chan struct{}
	releaseUnrelatedWrite chan struct{}
	policyWriteStarted    chan struct{}
	unrelatedStartedOnce  sync.Once
	policyStartedOnce     sync.Once
}

func (r *codexPolicyUpdateRepo) Get(context.Context, string) (*Setting, error) {
	return nil, errors.New("unexpected Get call")
}

func (r *codexPolicyUpdateRepo) GetValue(context.Context, string) (string, error) {
	return "", errors.New("unexpected GetValue call")
}

func (r *codexPolicyUpdateRepo) Set(context.Context, string, string) error {
	return errors.New("unexpected Set call")
}

func (r *codexPolicyUpdateRepo) GetMultiple(_ context.Context, keys []string) (map[string]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.getMultipleCalls++
	if r.getMultipleErr != nil {
		return nil, r.getMultipleErr
	}
	return selectSettingValues(r.values, keys), nil
}

func (r *codexPolicyUpdateRepo) SetMultiple(_ context.Context, settings map[string]string) error {
	_, changesPolicy := settings[SettingKeyEnableOpenAICodexFingerprintNormalization]

	r.mu.Lock()
	if r.values == nil {
		r.values = make(map[string]string)
	}
	for key, value := range settings {
		r.values[key] = value
	}
	shouldBlock := r.blockUnrelatedWrite && !changesPolicy
	r.mu.Unlock()

	if changesPolicy && r.policyWriteStarted != nil {
		r.policyStartedOnce.Do(func() { close(r.policyWriteStarted) })
	}
	if shouldBlock {
		r.unrelatedStartedOnce.Do(func() { close(r.unrelatedWriteStarted) })
		<-r.releaseUnrelatedWrite
	}
	return nil
}

func (r *codexPolicyUpdateRepo) GetAll(context.Context) (map[string]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.getAllCalls++
	if r.getAllErr != nil {
		return nil, r.getAllErr
	}
	return selectSettingValues(r.values, nil), nil
}

func (r *codexPolicyUpdateRepo) Delete(context.Context, string) error {
	return errors.New("unexpected Delete call")
}

func selectSettingValues(values map[string]string, keys []string) map[string]string {
	if keys == nil {
		keys = make([]string, 0, len(values))
		for key := range values {
			keys = append(keys, key)
		}
	}
	result := make(map[string]string, len(keys))
	for _, key := range keys {
		if value, ok := values[key]; ok {
			result[key] = value
		}
	}
	return result
}

func codexPolicySettings(enabled bool) *SystemSettings {
	return &SystemSettings{
		EnableOpenAICodexFingerprintNormalization:    enabled,
		EnableOpenAICodexInstallationIDNormalization: enabled,
		EnableOpenAIUUIDv7SessionIdentity:            enabled,
		EnableOpenAICodexClientIdentityNormalization: enabled,
	}
}

func codexPolicyValues(enabled bool) map[string]string {
	value := "false"
	if enabled {
		value = "true"
	}
	return map[string]string{
		SettingKeyEnableOpenAICodexFingerprintNormalization:    value,
		SettingKeyEnableOpenAICodexInstallationIDNormalization: value,
		SettingKeyEnableOpenAIUUIDv7SessionIdentity:            value,
		SettingKeyEnableOpenAICodexClientIdentityNormalization: value,
	}
}

func omittedCodexPolicyKeys() OmittedSettingKeys {
	return OmittedSettingKeys{
		SettingKeyEnableOpenAICodexFingerprintNormalization:    {},
		SettingKeyEnableOpenAICodexInstallationIDNormalization: {},
		SettingKeyEnableOpenAIUUIDv7SessionIdentity:            {},
		SettingKeyEnableOpenAICodexClientIdentityNormalization: {},
	}
}

func requireCodexPolicyEnabled(t *testing.T, policy CodexFingerprintPolicySnapshot, enabled bool) {
	t.Helper()
	require.Equal(t, enabled, policy.MasterEnabled)
	require.Equal(t, enabled, policy.InstallationIDEnabled)
	require.Equal(t, enabled, policy.TurnIdentityEnabled)
	require.Equal(t, enabled, policy.ClientIdentityEnabled)
}

func TestUpdateSettingsOmittingPublishesAuthoritativePolicyForStalePartialRequest(t *testing.T) {
	repo := &codexPolicyUpdateRepo{values: codexPolicyValues(false)}
	svc := NewSettingService(repo, &config.Config{})
	requireCodexPolicyEnabled(t, svc.GetOpenAICodexFingerprintPolicy(context.Background()), false)

	staleRequest := codexPolicySettings(true)
	staleRequest.RiskControlEnabled = true
	require.NoError(t, svc.UpdateSettingsOmitting(
		context.Background(), staleRequest, omittedCodexPolicyKeys(),
	))

	requireCodexPolicyEnabled(t, svc.GetOpenAICodexFingerprintPolicy(context.Background()), false)
	repo.mu.Lock()
	require.Equal(t, "false", repo.values[SettingKeyEnableOpenAICodexFingerprintNormalization])
	require.Equal(t, 1, repo.getAllCalls)
	require.Equal(t, 1, repo.getMultipleCalls,
		"the published authoritative readback should satisfy the final cache read")
	repo.mu.Unlock()
}

func TestSettingsUpdatesSerializeWriteThroughPolicyPublish(t *testing.T) {
	repo := &codexPolicyUpdateRepo{
		values:                codexPolicyValues(true),
		blockUnrelatedWrite:   true,
		unrelatedWriteStarted: make(chan struct{}),
		releaseUnrelatedWrite: make(chan struct{}),
		policyWriteStarted:    make(chan struct{}),
	}
	svc := NewSettingService(repo, &config.Config{})

	unrelatedDone := make(chan error, 1)
	go func() {
		staleRequest := codexPolicySettings(true)
		staleRequest.RiskControlEnabled = true
		unrelatedDone <- svc.UpdateSettingsOmitting(
			context.Background(), staleRequest, omittedCodexPolicyKeys(),
		)
	}()
	<-repo.unrelatedWriteStarted

	policyDone := make(chan error, 1)
	go func() {
		policyDone <- svc.UpdateSettings(context.Background(), codexPolicySettings(false))
	}()

	select {
	case <-repo.policyWriteStarted:
		t.Fatal("a second settings write entered the repository before the first policy publication completed")
	case <-time.After(50 * time.Millisecond):
	}

	close(repo.releaseUnrelatedWrite)
	require.NoError(t, <-unrelatedDone)
	require.NoError(t, <-policyDone)
	requireCodexPolicyEnabled(t, svc.GetOpenAICodexFingerprintPolicy(context.Background()), false)
}

func TestUpdateSettingsOmittingReadbackFailurePreservesLastKnownGoodPolicy(t *testing.T) {
	repo := &codexPolicyUpdateRepo{values: codexPolicyValues(false)}
	svc := NewSettingService(repo, &config.Config{})
	want := svc.GetOpenAICodexFingerprintPolicy(context.Background())
	requireCodexPolicyEnabled(t, want, false)

	repo.mu.Lock()
	repo.getAllErr = errors.New("readback unavailable")
	repo.getMultipleErr = errors.New("policy read unavailable")
	repo.mu.Unlock()

	staleRequest := codexPolicySettings(true)
	staleRequest.RiskControlEnabled = true
	require.NoError(t, svc.UpdateSettingsOmitting(
		context.Background(), staleRequest, omittedCodexPolicyKeys(),
	))

	got := svc.GetOpenAICodexFingerprintPolicy(context.Background())
	requireCodexPolicyEnabled(t, got, false)
	require.Greater(t, got.Generation, want.Generation)

	cached, ok := svc.openAIUUIDv7SessionIdentityCache.Load().(*cachedOpenAICodexFingerprintPolicy)
	require.True(t, ok)
	require.True(t, cached.hasLastSuccessful)
	requireCodexPolicyEnabled(t, cached.lastSuccessful, false)
	require.Greater(t, cached.expiresAt, time.Now().UnixNano())
	require.LessOrEqual(t, time.Until(time.Unix(0, cached.expiresAt)), openAIUUIDv7SessionIdentityErrorTTL)
}
