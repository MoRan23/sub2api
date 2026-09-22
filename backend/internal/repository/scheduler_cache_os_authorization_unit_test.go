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

func TestSchedulerMetadataOmitsOAuthAuthorizationAndOSIdentity(t *testing.T) {
	retryAt := time.Now().Add(time.Hour)
	account := service.Account{
		ID: 7801, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth,
		Credentials: map[string]any{"access_token": "secret-access", "refresh_token": "secret-refresh", "id_token": "secret-id", "api_key": "secret-key"},
		OpenAIOAuthOSProfiles: &service.OpenAIOAuthOSProfiles{
			DefaultOS: service.OpenAIOSMacOS,
			Authorization: &service.OpenAIOAuthOSAuthorizationSummary{
				Status: service.OpenAIOAuthAuthorizationReauthRequired, LastError: "private-account-error", RefreshRetryAfter: &retryAt,
			},
			Profiles: map[string]service.OpenAIOAuthOSProfile{
				service.OpenAIOSMacOS: {
					OSFamily: service.OpenAIOSMacOS, InstallationID: "private-installation", UserAgent: "private-agent", SyncSessionID: "private-root",
					Authorization: service.OpenAIOAuthOSAuthorizationSummary{Status: service.OpenAIOAuthAuthorizationAuthorized},
				},
				service.OpenAIOSLinux: {
					OSFamily:      service.OpenAIOSLinux,
					Authorization: service.OpenAIOAuthOSAuthorizationSummary{Status: service.OpenAIOAuthAuthorizationReauthRequired, LastError: "private-profile-error", RefreshRetryAfter: &retryAt},
				},
			},
		},
	}
	metadata := buildSchedulerMetadataAccount(account)
	require.Nil(t, metadata.OpenAIOAuthOSProfiles)
	payload, err := json.Marshal(metadata)
	require.NoError(t, err)
	for _, secret := range []string{"secret-access", "secret-refresh", "secret-id", "secret-key", "private-installation", "private-agent", "private-root", "private-account-error", "private-profile-error"} {
		require.NotContains(t, string(payload), secret)
	}
	for _, field := range []string{"requires_os_authorization", "oauth_credentials_available", "openai_oauth_os_profiles", "refresh_retry_after"} {
		require.NotContains(t, string(payload), field)
	}
}

func TestSchedulerMetadataPreservesOAuthSchemeAndParentAccount(t *testing.T) {
	for _, key := range []string{"auth_mode", "openai_auth_mode"} {
		for _, mode := range []string{service.OpenAIAuthModePersonalAccessToken, service.OpenAIAuthModeAgentIdentity} {
			t.Run(key+"/"+mode, func(t *testing.T) {
				account := service.Account{Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Credentials: map[string]any{key: mode}}
				metadata := buildSchedulerMetadataAccount(account)
				require.Equal(t, mode, metadata.GetCredential(key))
			})
		}
	}
	parentID := int64(99)
	shadow := buildSchedulerMetadataAccount(service.Account{Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, ParentAccountID: &parentID})
	require.Equal(t, parentID, *shadow.ParentAccountID)
}

func TestSchedulerCacheSnapshotDoesNotGateOnOAuthCredentialsOrOSAuthorization(t *testing.T) {
	retryAt := time.Now().Add(time.Hour)
	for _, tc := range []struct {
		name          string
		credentials   map[string]any
		authorization *service.OpenAIOAuthOSAuthorizationSummary
	}{
		{name: "access token without profiles", credentials: map[string]any{"access_token": "secret-access"}},
		{name: "refresh token only", credentials: map[string]any{"refresh_token": "secret-refresh"}},
		{name: "missing credentials and profiles"},
		{name: "blank tokens", credentials: map[string]any{"access_token": " \t", "refresh_token": "\n"}},
		{name: "authorized profile without tokens", authorization: &service.OpenAIOAuthOSAuthorizationSummary{Status: service.OpenAIOAuthAuthorizationAuthorized}},
		{name: "reauth required", credentials: map[string]any{"access_token": "secret-access"}, authorization: &service.OpenAIOAuthOSAuthorizationSummary{Status: service.OpenAIOAuthAuthorizationReauthRequired}},
		{name: "refresh cooldown", credentials: map[string]any{"access_token": "secret-access"}, authorization: &service.OpenAIOAuthOSAuthorizationSummary{Status: service.OpenAIOAuthAuthorizationAuthorized, RefreshRetryAfter: &retryAt}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			cache := newSchedulerCacheUnit(t)
			account := service.Account{
				ID: 7802, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth,
				Status: service.StatusActive, Schedulable: true, Credentials: tc.credentials,
			}
			if tc.authorization != nil {
				account.OpenAIOAuthOSProfiles = &service.OpenAIOAuthOSProfiles{
					DefaultOS: service.OpenAIOSMacOS, Authorization: tc.authorization,
					Profiles: map[string]service.OpenAIOAuthOSProfile{
						service.OpenAIOSMacOS: {
							OSFamily: service.OpenAIOSMacOS, InstallationID: "secret-installation", UserAgent: "secret-user-agent", SyncSessionID: "secret-session",
							Authorization: *tc.authorization,
						},
					},
				}
			}
			bucket := service.SchedulerBucket{GroupID: 78, Platform: service.PlatformOpenAI, Mode: service.SchedulerModeSingle}
			token, err := cache.CaptureBucketWriteToken(ctx, bucket)
			require.NoError(t, err)
			require.NoError(t, cache.SetSnapshot(ctx, bucket, token, []service.Account{account}))

			candidates, hit, err := cache.GetSnapshot(ctx, bucket)
			require.NoError(t, err)
			require.True(t, hit, "OAuth credentials and OS authorization are not snapshot admission conditions")
			require.Len(t, candidates, 1)
			require.True(t, candidates[0].IsSchedulable())
			require.Nil(t, candidates[0].OpenAIOAuthOSProfiles)
			for _, key := range []string{"access_token", "refresh_token", "id_token", "api_key"} {
				require.NotContains(t, candidates[0].Credentials, key)
			}
			payload, err := cache.rdb.Get(ctx, schedulerAccountMetaKey(strconv.FormatInt(account.ID, 10))).Result()
			require.NoError(t, err)
			for _, secret := range []string{"secret-access", "secret-refresh", "secret-installation", "secret-user-agent", "secret-session", "requires_os_authorization", "oauth_credentials_available"} {
				require.NotContains(t, payload, secret)
			}
		})
	}
}

func TestSchedulerCacheSnapshotIgnoresLegacyOAuthAdmissionSummaries(t *testing.T) {
	for _, tc := range []struct {
		name   string
		fields map[string]any
	}{
		{name: "no summaries"},
		{name: "requires authorization only", fields: map[string]any{"requires_os_authorization": true}},
		{name: "unavailable credentials", fields: map[string]any{"requires_os_authorization": true, "oauth_credentials_available": false}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			cache, redis := newSchedulerCacheUnitWithRedis(t)
			account := service.Account{ID: 7803, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth,
				Status: service.StatusActive, Schedulable: true}
			bucket := service.SchedulerBucket{GroupID: 78, Platform: service.PlatformOpenAI, Mode: service.SchedulerModeSingle}
			token, err := cache.CaptureBucketWriteToken(ctx, bucket)
			require.NoError(t, err)
			require.NoError(t, cache.SetSnapshot(ctx, bucket, token, []service.Account{account}))

			key := schedulerAccountMetaKey(strconv.FormatInt(account.ID, 10))
			payload, err := redis.Get(key)
			require.NoError(t, err)
			var legacy map[string]any
			require.NoError(t, json.Unmarshal([]byte(payload), &legacy))
			for key, value := range tc.fields {
				legacy[key] = value
			}
			legacyPayload, err := json.Marshal(legacy)
			require.NoError(t, err)
			require.NoError(t, redis.Set(key, string(legacyPayload)))

			candidates, hit, err := cache.GetSnapshot(ctx, bucket)
			require.NoError(t, err)
			require.True(t, hit, "legacy authorization summaries must not force a snapshot rebuild")
			require.Len(t, candidates, 1)
			require.True(t, candidates[0].IsSchedulable())
		})
	}
}
