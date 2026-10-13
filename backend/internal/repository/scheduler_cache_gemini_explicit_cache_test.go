//go:build unit

package repository

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestSchedulerCacheGeminiExplicitCacheCandidatesRoundTrip(t *testing.T) {
	ctx := context.Background()
	cache := newSchedulerCacheUnit(t)
	bucket := service.SchedulerBucket{GroupID: 71, Platform: service.PlatformGemini, Mode: service.SchedulerModeSingle}
	cases := []struct {
		name        string
		accountType string
		credentials map[string]any
		want        bool
	}{
		{"official", service.AccountTypeAPIKey, map[string]any{"api_key": "secret-official"}, true},
		{"official explicit url", service.AccountTypeAPIKey, map[string]any{"api_key": "secret-official", "base_url": "https://generativelanguage.googleapis.com/"}, true},
		{"declared upstream", service.AccountTypeAPIKey, map[string]any{"api_key": "secret-upstream", "base_url": "https://relay.example.com", service.GeminiExplicitCacheUpstreamCredentialKey: true, service.GeminiExplicitCacheUpstreamMaxTTLCredentialKey: 3600}, true},
		{"undeclared upstream", service.AccountTypeAPIKey, map[string]any{"api_key": "secret-unsupported", "base_url": "https://relay.example.com"}, false},
		{"oauth", service.AccountTypeOAuth, map[string]any{"access_token": "secret-oauth"}, false},
		{"vertex service account", service.AccountTypeServiceAccount, map[string]any{"service_account_json": "secret-service-account"}, true},
		{"vertex api key", service.AccountTypeServiceAccount, map[string]any{"auth_mode": "apikey", "api_key": "secret-vertex-key"}, false},
	}
	accounts := make([]service.Account, len(cases))
	for i, tc := range cases {
		accounts[i] = service.Account{ID: int64(i + 1), Name: tc.name, Platform: service.PlatformGemini, Type: tc.accountType, Credentials: tc.credentials}
	}
	token, err := cache.CaptureBucketWriteToken(ctx, bucket)
	require.NoError(t, err)
	require.NoError(t, cache.SetSnapshot(ctx, bucket, token, accounts))
	snapshot, hit, err := cache.GetSnapshot(ctx, bucket)
	require.NoError(t, err)
	require.True(t, hit)
	require.Len(t, snapshot, len(cases))
	for i, candidate := range snapshot {
		t.Run(cases[i].name, func(t *testing.T) {
			require.Equal(t, cases[i].want, service.IsGeminiExplicitCacheCandidate(candidate))
			for _, key := range service.SensitiveCredentialKeys {
				require.NotContains(t, candidate.Credentials, key)
			}
			full, err := cache.GetAccount(ctx, candidate.ID)
			require.NoError(t, err)
			require.NotNil(t, full)
			require.Equal(t, cases[i].want, service.IsGeminiExplicitCacheAccount(full))
			if candidate.GeminiExplicitCacheUpstreamEnabled() {
				require.Equal(t, full.GeminiExplicitCacheUpstreamMaxTTL(), candidate.GeminiExplicitCacheUpstreamMaxTTL())
			}
			if candidate.Type == service.AccountTypeAPIKey {
				require.False(t, service.IsGeminiExplicitCacheAccount(candidate), "candidate metadata cannot be used as hydrated credentials")
				require.NotEmpty(t, full.GetCredential("api_key"))
			}
		})
	}
}
