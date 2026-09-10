package service

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/stretchr/testify/require"
)

func TestOpenAIRequestPolicyDefaultAndIndependentSwitches(t *testing.T) {
	var nilService *SettingService
	require.Equal(t, openai.DefaultRequestPolicy(), nilService.GetOpenAIRequestPolicy(nil))
	for _, key := range openAIRequestPolicySettingKeys {
		t.Run(key, func(t *testing.T) {
			values := map[string]string{key: "false", SettingKeyEnableOpenAICodexFingerprintNormalization: "false"}
			svc := NewSettingService(&openAIUUIDv7RuntimeRepo{values: values}, nil)
			policy := svc.GetOpenAIRequestPolicy(nil)
			require.Equal(t, key != SettingKeyEnableOpenAIRequestTimezoneConversion, policy.TimezoneConversionEnabled)
			require.Equal(t, key != SettingKeyEnableOpenAIPassthroughTimezoneConversion, policy.PassthroughTimezoneConversionEnabled)
			require.Equal(t, key != SettingKeyEnableOpenAICodexResidencyUS, policy.CodexResidencyUS)
		})
	}
}

func TestOpenAIRequestPolicyCachesInvalidatesAndPreservesLastGood(t *testing.T) {
	repo := &openAIUUIDv7RuntimeRepo{values: map[string]string{SettingKeyEnableOpenAICodexResidencyUS: "false"}}
	svc := NewSettingService(repo, nil)
	want := svc.GetOpenAIRequestPolicy(context.Background())
	require.False(t, want.CodexResidencyUS)
	require.Equal(t, want, svc.GetOpenAIRequestPolicy(context.Background()))
	require.Equal(t, int32(1), repo.getCalls.Load())

	for _, raw := range []string{"invalid", " TRUE ", "1", ""} {
		repo.mu.Lock()
		repo.values[SettingKeyEnableOpenAIRequestTimezoneConversion] = raw
		repo.values[SettingKeyEnableOpenAICodexResidencyUS] = "true"
		repo.mu.Unlock()
		svc.InvalidateOpenAIRequestPolicyCache()
		require.Equal(t, want, svc.GetOpenAIRequestPolicy(context.Background()), "malformed row must not partially apply other switches")
	}
	repo.mu.Lock()
	repo.err = errors.New("database unavailable")
	repo.mu.Unlock()
	svc.InvalidateOpenAIRequestPolicyCache()
	require.Equal(t, want, svc.GetOpenAIRequestPolicy(context.Background()))
	repo.mu.Lock()
	repo.err = nil
	repo.values = map[string]string{}
	repo.mu.Unlock()
	svc.InvalidateOpenAIRequestPolicyCache()
	require.Equal(t, openai.DefaultRequestPolicy(), svc.GetOpenAIRequestPolicy(context.Background()))
}

func TestOpenAIRequestPolicyColdFailureDefaultsAndCaches(t *testing.T) {
	for _, repo := range []*openAIUUIDv7RuntimeRepo{
		{err: errors.New("database unavailable")},
		{values: map[string]string{SettingKeyEnableOpenAIRequestTimezoneConversion: "false", SettingKeyEnableOpenAICodexResidencyUS: "bad"}},
	} {
		svc := NewSettingService(repo, nil)
		require.Equal(t, openai.DefaultRequestPolicy(), svc.GetOpenAIRequestPolicy(nil))
		require.Equal(t, openai.DefaultRequestPolicy(), svc.GetOpenAIRequestPolicy(nil))
		require.Equal(t, int32(1), repo.getCalls.Load())
	}
}

func TestOpenAIRequestPolicyPublishWinsInflightRead(t *testing.T) {
	repo := &openAIUUIDv7RuntimeRepo{firstStarted: make(chan struct{}), firstRelease: make(chan struct{})}
	svc := NewSettingService(repo, nil)
	result := make(chan openai.RequestPolicy, 1)
	go func() { result <- svc.GetOpenAIRequestPolicy(nil) }()
	<-repo.firstStarted
	want := openai.RequestPolicy{TimezoneConversionEnabled: true}
	svc.PublishOpenAIRequestPolicy(want)
	close(repo.firstRelease)
	require.Equal(t, want, <-result)
	require.Equal(t, want, svc.GetOpenAIRequestPolicy(nil))
	require.Equal(t, int32(1), repo.getCalls.Load())
}

func TestOpenAIRequestPolicyConcurrentColdReadersShareSnapshot(t *testing.T) {
	repo := &openAIUUIDv7RuntimeRepo{values: map[string]string{SettingKeyEnableOpenAICodexResidencyUS: "false"}}
	svc := NewSettingService(repo, nil)
	var wg sync.WaitGroup
	results := make(chan openai.RequestPolicy, 32)
	for range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- svc.GetOpenAIRequestPolicy(nil)
		}()
	}
	wg.Wait()
	close(results)
	want := openai.DefaultRequestPolicy()
	want.CodexResidencyUS = false
	for result := range results {
		require.Equal(t, want, result)
	}
	require.Equal(t, int32(1), repo.getCalls.Load())
}
