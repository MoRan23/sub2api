package service

import (
	"context"
	"maps"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestAccountConfigurationPreservesLatestAfterStaleSnapshot(t *testing.T) {
	current := &Account{ID: 7, Platform: PlatformOpenAI, Type: AccountTypeOAuth,
		Credentials: map[string]any{"user_agent": "codex-tui/0.154.0 (new environment)"},
		Extra: map[string]any{openAIPinnedInstallationIDKey: uuid.NewString(),
			openAIInstallationPinEnabledKey: false, "enable_tls_fingerprint": false,
			"tls_fingerprint_profile_id": float64(12)}}
	for _, extra := range []map[string]any{nil, {}, {
		openAIPinnedInstallationIDKey: uuid.NewString(), openAIInstallationPinEnabledKey: true,
		openAIInstallationRotateEnabledKey: true, "enable_tls_fingerprint": true, "tls_fingerprint_profile_id": 1,
	}} {
		target := &Account{ID: 7, Platform: current.Platform, Type: current.Type, Extra: extra,
			Credentials: map[string]any{"user_agent": "stale or forged", "access_token": "refreshed", "model_mapping": "retained"}}
		require.NoError(t, PreserveAccountConfiguration(current, target, AccountConfigurationIntent{}))
		require.Equal(t, current.Extra, target.Extra)
		require.Equal(t, current.Credentials["user_agent"], target.Credentials["user_agent"])
		require.Equal(t, "refreshed", target.Credentials["access_token"])
		require.Equal(t, "retained", target.Credentials["model_mapping"])
	}
}

func TestAccountConfigurationExplicitIntentIsScopedAndCopied(t *testing.T) {
	environment := "(Ubuntu 24.04.4; x86_64) screen-256color"
	extra := map[string]any{openAIInstallationPinEnabledKey: false, "enable_tls_fingerprint": false, "tls_fingerprint_profile_id": nil}
	ctx := withAccountConfigurationIntent(context.Background(), []int64{7, 8}, extra, &environment)
	extra["enable_tls_fingerprint"] = true
	environment = "changed later"
	intent := AccountConfigurationIntentFromContext(ctx, 7)
	require.Equal(t, false, intent.Extra["enable_tls_fingerprint"])
	require.Contains(t, *intent.Environment, "Ubuntu")
	intent.Extra["enable_tls_fingerprint"] = true
	require.Equal(t, false, AccountConfigurationIntentFromContext(ctx, 7).Extra["enable_tls_fingerprint"])
	require.Empty(t, AccountConfigurationIntentFromContext(ctx, 9).Extra)

	current := &Account{ID: 7, Platform: PlatformOpenAI, Type: AccountTypeOAuth,
		Credentials: map[string]any{"user_agent": "codex-tui/0.154.0 (old environment)"},
		Extra:       map[string]any{openAIPinnedInstallationIDKey: uuid.NewString(), "enable_tls_fingerprint": true, "tls_fingerprint_profile_id": 1}}
	target := *current
	target.Credentials = map[string]any{"user_agent": "client spoof"}
	require.NoError(t, PreserveAccountConfiguration(current, &target, AccountConfigurationIntentFromContext(ctx, 7)))
	require.Equal(t, false, target.Extra[openAIInstallationPinEnabledKey])
	require.Equal(t, false, target.Extra["enable_tls_fingerprint"])
	require.Contains(t, target.Extra, "tls_fingerprint_profile_id")
	require.Nil(t, target.Extra["tls_fingerprint_profile_id"])
	require.Contains(t, target.GetOpenAIUserAgent(), "Ubuntu")
	require.Equal(t, true, current.Extra["enable_tls_fingerprint"], "merge must not mutate the locked snapshot")
}

func TestAccountConfigurationConversionAndShadow(t *testing.T) {
	current := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
		Credentials: map[string]any{"user_agent": "codex-tui/0.154.0 (stable)"}}
	converted := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth,
		Extra: map[string]any{openAIPinnedInstallationIDKey: "forged"}}
	require.NoError(t, PreserveAccountConfiguration(current, converted, AccountConfigurationIntent{}))
	generated, err := uuid.Parse(converted.GetPinnedOpenAIInstallationID())
	require.NoError(t, err)
	require.Equal(t, uuid.Version(4), generated.Version())

	parent := int64(7)
	shadow := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth, ParentAccountID: &parent,
		Credentials: maps.Clone(current.Credentials), Extra: maps.Clone(converted.Extra)}
	require.NoError(t, PreserveAccountConfiguration(converted, shadow, AccountConfigurationIntent{}))
	require.NotContains(t, shadow.Extra, openAIPinnedInstallationIDKey)
	require.NotContains(t, shadow.Credentials, "user_agent")
	environment := "(Ubuntu)"
	require.Error(t, PreserveAccountConfiguration(converted, shadow, AccountConfigurationIntent{Environment: &environment}))

	converted.Type = AccountTypeAPIKey
	require.NoError(t, PreserveAccountConfiguration(current, converted, AccountConfigurationIntent{}))
	require.NotContains(t, converted.Extra, openAIPinnedInstallationIDKey)
}
