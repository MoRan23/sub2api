package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type openAIAPIKeyHealthSettingRepo struct {
	SettingRepository
	value    string
	getCalls int
}

func (r *openAIAPIKeyHealthSettingRepo) GetValue(context.Context, string) (string, error) {
	r.getCalls++
	return r.value, nil
}

type openAIAPIKeyHealthAccountRepo struct {
	AccountRepository
	setCalls int
	reason   string
}

func (r *openAIAPIKeyHealthAccountRepo) SetTempUnschedulable(_ context.Context, _ int64, _ time.Time, reason string) error {
	r.setCalls++
	r.reason = reason
	return nil
}

type openAIAPIKeyHealthCacheStub struct {
	TempUnschedCache
	recordCalls int
	setCalls    int
	tripped     bool
}

func (c *openAIAPIKeyHealthCacheStub) RecordOpenAIAPIKeyHealthFailure(context.Context, int64, int, int) (int64, bool, error) {
	c.recordCalls++
	return 3, c.tripped, nil
}

func (c *openAIAPIKeyHealthCacheStub) SetTempUnsched(context.Context, int64, *TempUnschedState) error {
	c.setCalls++
	return nil
}

type openAIAPIKeyHealthRuntimeBlocker struct{ calls int }

func (b *openAIAPIKeyHealthRuntimeBlocker) BlockAccountScheduling(*Account, time.Time, string) {
	b.calls++
}
func (*openAIAPIKeyHealthRuntimeBlocker) ClearAccountSchedulingBlock(int64) {}

func openAIHealthPoolAccount() *Account {
	return &Account{
		ID:       42,
		Platform: PlatformOpenAI,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"pool_mode": true,
		},
	}
}

func TestClassifyOpenAIAPIKeyHealthFailureExclusions(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		eligible bool
	}{
		{name: "account attributed 502", err: &UpstreamFailoverError{StatusCode: http.StatusBadGateway}, eligible: true},
		{name: "account attributed 504", err: &UpstreamFailoverError{StatusCode: http.StatusGatewayTimeout}, eligible: true},
		{name: "transport synthesized 502", err: &UpstreamFailoverError{StatusCode: http.StatusBadGateway, TransportFailure: true}},
		{name: "wrapped transport synthesized 502", err: fmt.Errorf("attempt: %w", &UpstreamFailoverError{StatusCode: http.StatusBadGateway, TransportFailure: true})},
		{name: "marked transport error", err: MarkUpstreamTransportFailure(errors.New("upstream connection closed"))},
		{name: "typed network error", err: &net.OpError{Op: "read", Net: "tcp", Err: errors.New("connection reset")}},
		{name: "cyber policy 429", err: &UpstreamFailoverError{StatusCode: http.StatusTooManyRequests, ResponseBody: []byte(`{"error":{"code":"cyber_policy"}}`)}},
		{name: "cyber policy response 502", err: &UpstreamFailoverError{StatusCode: http.StatusBadGateway, ResponseBody: []byte(`{"response":{"error":{"code":"cyber_policy"}}}`)}},
		{name: "cyber image policy 502", err: &OpenAIImagesUpstreamError{StatusCode: http.StatusBadGateway, Code: "cyber_policy"}},
		{name: "request scoped capacity", err: &UpstreamFailoverError{StatusCode: 529, RequestScopedTransient: true}},
		{name: "provider scoped overload", err: &UpstreamFailoverError{StatusCode: 529, Scope: GatewayFailureScopeProvider}},
		{name: "dedicated same account retry", err: &UpstreamFailoverError{StatusCode: http.StatusTooManyRequests, RetryableOnSameAccount: true}},
		{name: "credential disable path", err: &UpstreamFailoverError{StatusCode: http.StatusUnauthorized, Stage: GatewayFailureStageAccountAuth, Scope: GatewayFailureScopeAccount}},
		{name: "client request", err: &UpstreamFailoverError{StatusCode: http.StatusBadRequest}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, eligible := classifyOpenAIAPIKeyHealthFailure(tt.err)
			require.Equal(t, tt.eligible, eligible)
		})
	}
}

func TestOpenAIAPIKeyHealthExcludedFailureDoesNotTouchState(t *testing.T) {
	encoded, err := json.Marshal(OpenAIAPIKeyHealthBreakerSettings{Enabled: true, WindowMinutes: 1, FailureThreshold: 1, CooldownMinutes: 5})
	require.NoError(t, err)
	for _, failure := range []error{
		&UpstreamFailoverError{StatusCode: http.StatusBadGateway, TransportFailure: true},
		MarkUpstreamTransportFailure(&UpstreamFailoverError{StatusCode: http.StatusBadGateway}),
		&UpstreamFailoverError{StatusCode: http.StatusTooManyRequests, ResponseBody: []byte(`{"error":{"code":"cyber_policy"}}`)},
		&OpenAIImagesUpstreamError{StatusCode: http.StatusBadGateway, Code: "cyber_policy"},
	} {
		settingRepo := &openAIAPIKeyHealthSettingRepo{value: string(encoded)}
		cache := &openAIAPIKeyHealthCacheStub{tripped: true}
		repo := &openAIAPIKeyHealthAccountRepo{}
		blocker := &openAIAPIKeyHealthRuntimeBlocker{}
		svc := NewRateLimitService(repo, nil, &config.Config{}, nil, cache)
		svc.SetSettingService(NewSettingService(settingRepo, &config.Config{}))
		svc.SetOpenAIAPIKeyHealthCache(cache)
		svc.SetAccountRuntimeBlocker(blocker)
		account := openAIHealthPoolAccount()

		require.False(t, svc.ObserveOpenAIAPIKeyHealthFailure(context.Background(), account, failure))
		require.Zero(t, settingRepo.getCalls)
		require.Zero(t, cache.recordCalls)
		require.Zero(t, cache.setCalls)
		require.Zero(t, repo.setCalls)
		require.Zero(t, blocker.calls)
		require.Nil(t, account.TempUnschedulableUntil)
	}
}

func TestOpenAIAPIKeyHealthBreakerDefaultDisabled(t *testing.T) {
	settings := NewSettingService(&openAIAPIKeyHealthSettingRepo{}, &config.Config{})
	cache := &openAIAPIKeyHealthCacheStub{tripped: true}
	svc := NewRateLimitService(&openAIAPIKeyHealthAccountRepo{}, nil, &config.Config{}, nil, cache)
	svc.SetSettingService(settings)
	svc.SetOpenAIAPIKeyHealthCache(cache)

	require.False(t, svc.ObserveOpenAIAPIKeyHealthFailure(context.Background(), openAIHealthPoolAccount(), &UpstreamFailoverError{StatusCode: http.StatusBadGateway}))
	require.Zero(t, cache.recordCalls)
}

func TestOpenAIAPIKeyHealthBreakerTripsPersistedAndRuntimeState(t *testing.T) {
	encoded, err := json.Marshal(OpenAIAPIKeyHealthBreakerSettings{Enabled: true, WindowMinutes: 1, FailureThreshold: 3, CooldownMinutes: 5})
	require.NoError(t, err)
	settings := NewSettingService(&openAIAPIKeyHealthSettingRepo{value: string(encoded)}, &config.Config{})
	cache := &openAIAPIKeyHealthCacheStub{tripped: true}
	repo := &openAIAPIKeyHealthAccountRepo{}
	blocker := &openAIAPIKeyHealthRuntimeBlocker{}
	svc := NewRateLimitService(repo, nil, &config.Config{}, nil, cache)
	svc.SetSettingService(settings)
	svc.SetOpenAIAPIKeyHealthCache(cache)
	svc.SetAccountRuntimeBlocker(blocker)
	account := openAIHealthPoolAccount()

	require.True(t, svc.ObserveOpenAIAPIKeyHealthFailure(context.Background(), account, &UpstreamFailoverError{StatusCode: http.StatusBadGateway, ResponseBody: []byte(`{"error":"upstream"}`)}))
	require.Equal(t, 1, cache.recordCalls)
	require.Equal(t, 1, cache.setCalls)
	require.Equal(t, 1, repo.setCalls)
	require.Equal(t, 1, blocker.calls)
	require.NotNil(t, account.TempUnschedulableUntil)
	require.Contains(t, repo.reason, openAIAPIKeyHealthBreakerReason)
}

func TestOpenAIAPIKeyHealthSuccessDoesNotTouchSettingsOrCache(t *testing.T) {
	encoded, err := json.Marshal(OpenAIAPIKeyHealthBreakerSettings{Enabled: true, WindowMinutes: 1, FailureThreshold: 3, CooldownMinutes: 5})
	require.NoError(t, err)
	settingRepo := &openAIAPIKeyHealthSettingRepo{value: string(encoded)}
	settings := NewSettingService(settingRepo, &config.Config{})
	cache := &openAIAPIKeyHealthCacheStub{}
	svc := NewRateLimitService(&openAIAPIKeyHealthAccountRepo{}, nil, &config.Config{}, nil, cache)
	svc.SetSettingService(settings)
	svc.SetOpenAIAPIKeyHealthCache(cache)

	svc.ObserveOpenAIAPIKeyHealthSuccess(context.Background(), openAIHealthPoolAccount())
	svc.ObserveOpenAIAPIKeyHealthSuccess(context.Background(), &Account{ID: 43, Platform: PlatformOpenAI, Type: AccountTypeAPIKey})
	require.Zero(t, settingRepo.getCalls)
	require.Zero(t, cache.recordCalls)
}
