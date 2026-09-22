//go:build unit

package repository

import (
	"encoding/json"
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
	require.True(t, service.OpenAIOAuthOSAuthorizationAvailable(&metadata, ""))
	require.True(t, service.OpenAIOAuthOSAuthorizationAvailable(&metadata, service.OpenAIOSLinux), "legacy default summary represents the shared account authorization")
	require.Equal(t, retryAt, *metadata.OpenAIOAuthOSProfiles.Profiles[service.OpenAIOSLinux].Authorization.RefreshRetryAfter)
	payload, err := json.Marshal(metadata)
	require.NoError(t, err)
	for _, secret := range []string{"secret-access", "secret-refresh", "secret-id", "secret-key", "private-installation", "private-agent", "private-root", "private-error"} {
		require.NotContains(t, string(payload), secret)
	}
	require.Contains(t, string(payload), `"requires_os_authorization":true`)
}

func TestSchedulerMetadataPreservesOAuthSchemeExemptions(t *testing.T) {
	for _, key := range []string{"auth_mode", "openai_auth_mode"} {
		for _, mode := range []string{service.OpenAIAuthModePersonalAccessToken, service.OpenAIAuthModeAgentIdentity} {
			account := service.Account{Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Credentials: map[string]any{key: mode}}
			metadata := buildSchedulerMetadataAccount(account)
			require.Equal(t, mode, metadata.GetCredential(key))
			require.False(t, *metadata.OpenAIOAuthRequiresOSAuthorization)
			require.False(t, service.RequiresOpenAIOAuthOSAuthorization(&metadata))
		}
	}
	parentID := int64(99)
	shadow := buildSchedulerMetadataAccount(service.Account{Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, ParentAccountID: &parentID})
	require.True(t, *shadow.OpenAIOAuthRequiresOSAuthorization)
	require.Equal(t, parentID, *shadow.ParentAccountID)
}
