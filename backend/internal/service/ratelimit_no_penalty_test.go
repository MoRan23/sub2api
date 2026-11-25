package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestCyberPolicySkipsAccountErrorPolicies(t *testing.T) {
	for _, status := range []int{400, 401, 403, 404, 429, 500, 502, 504, 529} {
		for _, accountType := range []string{AccountTypeAPIKey, AccountTypeOAuth, AccountTypeSetupToken} {
			for _, body := range []string{
				`{"error":{"code":"cyber_policy","message":"model not found; account suspended; rate limit"}}`,
				`{"response":{"error":{"code":"cyber_policy","message":"model not found; account suspended; rate limit"}}}`,
			} {
				t.Run(fmt.Sprintf("%s/%d/%s", accountType, status, body), func(t *testing.T) {
					account := &Account{ID: 42, Platform: PlatformOpenAI, Type: accountType, Status: StatusActive, Schedulable: true}
					require.NoError(t, json.Unmarshal([]byte(fmt.Sprintf(`{
						"custom_error_codes_enabled":true,"custom_error_codes":[%d],
						"temp_unschedulable_enabled":true,"temp_unschedulable_rules":[
							{"error_code":%d,"keywords":["cyber_policy"],"duration_minutes":10}
						]}`, status, status)), &account.Credentials))
					repo := &openAIAPIKeyHealthAccountRepo{}
					cache := &openAIAPIKeyHealthCacheStub{}
					blocker := &openAIAPIKeyHealthRuntimeBlocker{}
					rateLimits := NewRateLimitService(repo, nil, &config.Config{}, nil, cache)
					rateLimits.SetAccountRuntimeBlocker(blocker)
					gateway := &OpenAIGatewayService{rateLimitService: rateLimits}
					ctx := context.Background()
					payload := []byte(body)

					require.Equal(t, ErrorPolicySkipped, rateLimits.CheckErrorPolicy(ctx, account, status, payload, "gpt-5.6-sol"))
					require.False(t, rateLimits.HandleUpstreamError(ctx, account, status, http.Header{}, payload, "gpt-5.6-sol"))
					require.False(t, rateLimits.HandleTempUnschedulable(ctx, account, status, payload, "gpt-5.6-sol"))
					require.False(t, rateLimits.HandleUpstreamModelNotFound(ctx, account, "gpt-5.6-sol", status, payload))
					require.False(t, gateway.handleOpenAIAccountUpstreamError(ctx, account, status, http.Header{}, payload, "gpt-5.6-sol"))
					require.False(t, gateway.isOpenAIAccountRuntimeBlocked(account))
					require.Zero(t, repo.setCalls)
					require.Zero(t, cache.setCalls)
					require.Zero(t, blocker.calls)
					require.Nil(t, account.TempUnschedulableUntil)
					require.Nil(t, account.RateLimitResetAt)
					require.Equal(t, StatusActive, account.Status)
					require.True(t, account.Schedulable)
				})
			}
		}
	}
}

func TestStreamTimeoutDoesNotTouchSettingsOrAccountState(t *testing.T) {
	for _, action := range []string{StreamTimeoutActionTempUnsched, StreamTimeoutActionError} {
		t.Run(action, func(t *testing.T) {
			settingRepo := &openAIAPIKeyHealthSettingRepo{value: fmt.Sprintf(`{"enabled":true,"action":%q,"threshold_count":1}`, action)}
			repo := &openAIAPIKeyHealthAccountRepo{}
			cache := &openAIAPIKeyHealthCacheStub{}
			blocker := &openAIAPIKeyHealthRuntimeBlocker{}
			rateLimits := NewRateLimitService(repo, nil, &config.Config{}, nil, cache)
			rateLimits.SetSettingService(NewSettingService(settingRepo, &config.Config{}))
			rateLimits.SetAccountRuntimeBlocker(blocker)
			account := openAIHealthPoolAccount()

			require.False(t, rateLimits.HandleStreamTimeout(context.Background(), account, "gpt-5.6-sol"))
			require.Zero(t, settingRepo.getCalls)
			require.Zero(t, repo.setCalls)
			require.Zero(t, cache.setCalls)
			require.Zero(t, blocker.calls)
			require.Nil(t, account.TempUnschedulableUntil)
		})
	}
}
