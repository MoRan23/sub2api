//go:build unit

package repository

import (
	"context"
	"encoding/json"
	"strconv"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestSchedulerMetadataOSAuthorizationContainsNoCredentialsOrInstallation(t *testing.T) {
	retryAt := time.Now().Add(time.Hour)
	account := service.Account{ID: 7801, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth,
		Credentials: map[string]any{"access_token": "secret-access", "refresh_token": "secret-refresh", "id_token": "secret-id", "api_key": "secret-key"},
		OpenAIOAuthOSProfiles: &service.OpenAIOAuthOSProfiles{DefaultOS: service.OpenAIOSMacOS, Profiles: map[string]service.OpenAIOAuthOSProfile{
			service.OpenAIOSMacOS: {OSFamily: service.OpenAIOSMacOS, InstallationID: "private-installation", UserAgent: "private-agent", SyncSessionID: "private-root",
				Authorization: service.OpenAIOAuthOSAuthorizationSummary{Status: service.OpenAIOAuthAuthorizationAuthorized}},
			service.OpenAIOSLinux: {OSFamily: service.OpenAIOSLinux,
				Authorization: service.OpenAIOAuthOSAuthorizationSummary{Status: service.OpenAIOAuthAuthorizationReauthRequired, LastError: "private-error", RefreshRetryAfter: &retryAt}},
		}}}
	metadata := buildSchedulerMetadataAccount(account)
	require.NotNil(t, metadata.OpenAIOAuthRequiresOSAuthorization)
	require.True(t, *metadata.OpenAIOAuthRequiresOSAuthorization)
	require.NotNil(t, metadata.OpenAIOAuthCredentialsAvailable)
	require.True(t, *metadata.OpenAIOAuthCredentialsAvailable)
	require.True(t, service.OpenAIOAuthOSAuthorizationAvailable(&metadata, ""))
	require.True(t, service.OpenAIOAuthOSAuthorizationAvailable(&metadata, service.OpenAIOSLinux), "OS profile status must not override account credential availability")
	require.Equal(t, retryAt, *metadata.OpenAIOAuthOSProfiles.Profiles[service.OpenAIOSLinux].Authorization.RefreshRetryAfter)
	payload, err := json.Marshal(metadata)
	require.NoError(t, err)
	for _, secret := range []string{"secret-access", "secret-refresh", "secret-id", "secret-key", "private-installation", "private-agent", "private-root", "private-error"} {
		require.NotContains(t, string(payload), secret)
	}
	require.Contains(t, string(payload), `"requires_os_authorization":true`)
	require.Contains(t, string(payload), `"oauth_credentials_available":true`)
	var restored service.Account
	require.NoError(t, json.Unmarshal(payload, &restored))
	require.True(t, service.OpenAIOAuthOSAuthorizationAvailable(&restored, service.OpenAIOSLinux))
}

func TestSchedulerMetadataPreservesOAuthSchemeExemptions(t *testing.T) {
	for _, key := range []string{"auth_mode", "openai_auth_mode"} {
		for _, mode := range []string{service.OpenAIAuthModePersonalAccessToken, service.OpenAIAuthModeAgentIdentity} {
			account := service.Account{Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Credentials: map[string]any{key: mode}}
			metadata := buildSchedulerMetadataAccount(account)
			require.Equal(t, mode, metadata.GetCredential(key))
			require.False(t, *metadata.OpenAIOAuthRequiresOSAuthorization)
			require.False(t, service.RequiresOpenAIOAuthOSAuthorization(&metadata))
			require.True(t, service.OpenAIOAuthOSAuthorizationAvailable(&metadata, ""))
		}
	}
	parentID := int64(99)
	shadow := buildSchedulerMetadataAccount(service.Account{Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, ParentAccountID: &parentID})
	require.True(t, *shadow.OpenAIOAuthRequiresOSAuthorization)
	require.Equal(t, parentID, *shadow.ParentAccountID)
}

func TestSchedulerCacheOAuthCredentialAvailabilitySurvivesSnapshot(t *testing.T) {
	for _, tc := range []struct {
		name        string
		credentials map[string]any
		available   bool
	}{
		{name: "access token", credentials: map[string]any{"access_token": "secret-access"}, available: true},
		{name: "refresh token only", credentials: map[string]any{"refresh_token": "secret-refresh"}, available: true},
		{name: "authorized profile without tokens", credentials: map[string]any{"plan_type": "pro"}},
		{name: "blank tokens", credentials: map[string]any{"access_token": " \t", "refresh_token": "\n"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			cache := newSchedulerCacheUnit(t)
			account := service.Account{
				ID: 7802, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth,
				Status: service.StatusActive, Schedulable: true, Credentials: tc.credentials,
				OpenAIOAuthOSProfiles: &service.OpenAIOAuthOSProfiles{
					DefaultOS:     service.OpenAIOSMacOS,
					Authorization: &service.OpenAIOAuthOSAuthorizationSummary{Status: service.OpenAIOAuthAuthorizationAuthorized},
					Profiles: map[string]service.OpenAIOAuthOSProfile{
						service.OpenAIOSMacOS: {OSFamily: service.OpenAIOSMacOS, InstallationID: "secret-installation", UserAgent: "secret-user-agent", SyncSessionID: "secret-session",
							Authorization: service.OpenAIOAuthOSAuthorizationSummary{Status: service.OpenAIOAuthAuthorizationAuthorized}},
					},
				},
			}
			bucket := service.SchedulerBucket{GroupID: 78, Platform: service.PlatformOpenAI, Mode: service.SchedulerModeSingle}
			token, err := cache.CaptureBucketWriteToken(ctx, bucket)
			require.NoError(t, err)
			require.NoError(t, cache.SetSnapshot(ctx, bucket, token, []service.Account{account}))

			candidates, hit, err := cache.GetSnapshot(ctx, bucket)
			require.NoError(t, err)
			require.True(t, hit)
			require.Len(t, candidates, 1)
			candidate := candidates[0]
			require.True(t, candidate.IsSchedulable(), "the normal account status remains separate from OAuth availability")
			require.NotNil(t, candidate.OpenAIOAuthCredentialsAvailable)
			require.Equal(t, tc.available, *candidate.OpenAIOAuthCredentialsAvailable)
			for _, os := range []string{"", service.OpenAIOSMacOS, service.OpenAIOSLinux, service.OpenAIOSWindows} {
				require.Equal(t, tc.available, service.OpenAIOAuthOSAuthorizationAvailable(candidate, os), os)
			}
			for _, key := range []string{"access_token", "refresh_token", "id_token", "api_key"} {
				require.NotContains(t, candidate.Credentials, key)
			}
			payload, err := cache.rdb.Get(ctx, schedulerAccountMetaKey(strconv.FormatInt(account.ID, 10))).Result()
			require.NoError(t, err)
			for _, secret := range []string{"secret-access", "secret-refresh", "secret-installation", "secret-user-agent", "secret-session"} {
				require.NotContains(t, payload, secret)
			}
		})
	}
}

func TestSchedulerCacheLegacyOAuthMetadataMissesAndRebuilds(t *testing.T) {
	for _, hasRequiresSummary := range []bool{true, false} {
		t.Run("requires summary="+strconv.FormatBool(hasRequiresSummary), func(t *testing.T) {
			ctx := context.Background()
			cache, redis := newSchedulerCacheUnitWithRedis(t)
			account := service.Account{ID: 7803, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth,
				Status: service.StatusActive, Schedulable: true, Credentials: map[string]any{"access_token": "secret-access"},
				OpenAIOAuthOSProfiles: &service.OpenAIOAuthOSProfiles{DefaultOS: service.OpenAIOSMacOS, Profiles: map[string]service.OpenAIOAuthOSProfile{
					service.OpenAIOSMacOS: {OSFamily: service.OpenAIOSMacOS, Authorization: service.OpenAIOAuthOSAuthorizationSummary{Status: service.OpenAIOAuthAuthorizationAuthorized}},
				}}}
			bucket := service.SchedulerBucket{GroupID: 78, Platform: service.PlatformOpenAI, Mode: service.SchedulerModeSingle}
			token, err := cache.CaptureBucketWriteToken(ctx, bucket)
			require.NoError(t, err)
			require.NoError(t, cache.SetSnapshot(ctx, bucket, token, []service.Account{account}))

			key := schedulerAccountMetaKey(strconv.FormatInt(account.ID, 10))
			payload, err := redis.Get(key)
			require.NoError(t, err)
			var legacy map[string]any
			require.NoError(t, json.Unmarshal([]byte(payload), &legacy))
			delete(legacy, "oauth_credentials_available")
			if !hasRequiresSummary {
				delete(legacy, "requires_os_authorization")
			}
			legacyPayload, err := json.Marshal(legacy)
			require.NoError(t, err)
			require.NoError(t, redis.Set(key, string(legacyPayload)))

			candidates, hit, err := cache.GetSnapshot(ctx, bucket)
			require.NoError(t, err)
			require.False(t, hit, "legacy credential-free metadata must request a database rebuild")
			require.Empty(t, candidates)

			token, err = cache.CaptureBucketWriteToken(ctx, bucket)
			require.NoError(t, err)
			require.NoError(t, cache.SetSnapshot(ctx, bucket, token, []service.Account{account}))
			candidates, hit, err = cache.GetSnapshot(ctx, bucket)
			require.NoError(t, err)
			require.True(t, hit)
			require.Len(t, candidates, 1)
			require.True(t, service.OpenAIOAuthOSAuthorizationAvailable(candidates[0], ""))
			require.Empty(t, candidates[0].GetOpenAIAccessToken())
		})
	}
}

func TestSchedulerCacheLegacyOAuthMetadataPreservesExemptions(t *testing.T) {
	for _, tc := range []struct {
		name        string
		accountType string
		credentials map[string]any
	}{
		{name: "API key", accountType: service.AccountTypeAPIKey, credentials: map[string]any{"api_key": "secret-key"}},
		{name: "PAT", accountType: service.AccountTypeOAuth, credentials: map[string]any{"auth_mode": service.OpenAIAuthModePersonalAccessToken}},
		{name: "legacy PAT key", accountType: service.AccountTypeOAuth, credentials: map[string]any{"openai_auth_mode": service.OpenAIAuthModePersonalAccessToken}},
		{name: "agent identity", accountType: service.AccountTypeOAuth, credentials: map[string]any{"auth_mode": service.OpenAIAuthModeAgentIdentity}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			cache, redis := newSchedulerCacheUnitWithRedis(t)
			account := service.Account{ID: 7804, Platform: service.PlatformOpenAI, Type: tc.accountType,
				Status: service.StatusActive, Schedulable: true, Credentials: tc.credentials}
			bucket := service.SchedulerBucket{GroupID: 78, Platform: service.PlatformOpenAI, Mode: service.SchedulerModeSingle}
			token, err := cache.CaptureBucketWriteToken(ctx, bucket)
			require.NoError(t, err)
			require.NoError(t, cache.SetSnapshot(ctx, bucket, token, []service.Account{account}))

			key := schedulerAccountMetaKey(strconv.FormatInt(account.ID, 10))
			payload, err := redis.Get(key)
			require.NoError(t, err)
			var legacy map[string]any
			require.NoError(t, json.Unmarshal([]byte(payload), &legacy))
			delete(legacy, "oauth_credentials_available")
			legacyPayload, err := json.Marshal(legacy)
			require.NoError(t, err)
			require.NoError(t, redis.Set(key, string(legacyPayload)))
			candidates, hit, err := cache.GetSnapshot(ctx, bucket)
			require.NoError(t, err)
			require.True(t, hit, "schemes without account OAuth credentials do not need the new summary")
			require.Len(t, candidates, 1)
			require.False(t, service.RequiresOpenAIOAuthOSAuthorization(candidates[0]))
			require.True(t, service.OpenAIOAuthOSAuthorizationAvailable(candidates[0], ""))
			require.NotContains(t, candidates[0].Credentials, "api_key")
		})
	}
}
