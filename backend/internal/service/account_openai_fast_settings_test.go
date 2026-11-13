package service

import (
	"context"
	"encoding/json"
	"maps"
	"testing"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

func TestAccountOpenAIFastSettingsDefaultsAndOwnership(t *testing.T) {
	getters := []struct {
		key string
		get func(*Account) bool
	}{
		{ForceOpenAIFastExtraKey, (*Account).IsForceOpenAIFastEnabled},
		{ForceOpenAIUltrafastExtraKey, (*Account).IsForceOpenAIUltrafastEnabled},
		{FreeOpenAIFastExtraKey, (*Account).IsFreeOpenAIFastEnabled},
		{FreeOpenAIUltrafastExtraKey, (*Account).IsFreeOpenAIUltrafastEnabled},
	}
	parentID := int64(1)
	for _, getter := range getters {
		t.Run(getter.key, func(t *testing.T) {
			require.False(t, getter.get(nil))
			for _, value := range []any{nil, false, "true", 1} {
				require.False(t, getter.get(&Account{Platform: PlatformOpenAI, Extra: map[string]any{getter.key: value}}))
			}
			require.False(t, getter.get(&Account{Platform: PlatformOpenAI}))
			require.False(t, getter.get(&Account{Platform: PlatformAnthropic, Extra: map[string]any{getter.key: true}}))
			for _, account := range []*Account{
				{Platform: PlatformOpenAI, Type: AccountTypeOAuth},
				{Platform: PlatformOpenAI, Type: AccountTypeOAuth, ParentAccountID: &parentID},
				{Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Extra: map[string]any{OpenAIAPIKeyModeExtraKey: "codex_engine"}},
			} {
				if account.Extra == nil {
					account.Extra = map[string]any{}
				}
				account.Extra[getter.key] = true
				require.True(t, getter.get(account))
			}
		})
	}
}

func TestAccountOpenAIFastSettingsValidationAndPartialSwitch(t *testing.T) {
	require.NoError(t, ValidateOpenAIAccountFastSettings(PlatformOpenAI, nil))
	for _, key := range openAIAccountFastSettingKeys {
		for _, value := range []any{nil, "false", 1, []bool{true}} {
			err := ValidateOpenAIAccountFastSettings(PlatformOpenAI, map[string]any{key: value})
			require.Equal(t, "OPENAI_ACCOUNT_FAST_INVALID", infraerrors.Reason(err))
		}
		require.Equal(t, "OPENAI_ACCOUNT_FAST_UNSUPPORTED", infraerrors.Reason(ValidateOpenAIAccountFastSettings(PlatformGrok, map[string]any{key: false})))
	}
	current := &Account{Platform: PlatformOpenAI, Extra: map[string]any{ForceOpenAIFastExtraKey: true, FreeOpenAIFastExtraKey: true}}
	require.Equal(t, "OPENAI_ACCOUNT_FAST_CONFLICT", infraerrors.Reason(ValidateOpenAIAccountFastSettingsPatch(current, map[string]any{ForceOpenAIUltrafastExtraKey: true})))
	require.NoError(t, ValidateOpenAIAccountFastSettingsPatch(current, map[string]any{ForceOpenAIUltrafastExtraKey: true, ForceOpenAIFastExtraKey: false}))
	require.NoError(t, ValidateOpenAIAccountFastSettingsPatch(current, map[string]any{FreeOpenAIUltrafastExtraKey: true}))
}

func TestAccountOpenAIFastSettingsProtectLatestAndScopeIntent(t *testing.T) {
	current := &Account{ID: 7, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Extra: map[string]any{
		ForceOpenAIFastExtraKey: false, ForceOpenAIUltrafastExtraKey: true,
		FreeOpenAIFastExtraKey: true, FreeOpenAIUltrafastExtraKey: false,
	}}
	stale := &Account{ID: 7, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Extra: map[string]any{ForceOpenAIFastExtraKey: true, FreeOpenAIFastExtraKey: false}}
	require.NoError(t, PreserveAccountConfiguration(current, stale, AccountConfigurationIntent{}))
	require.Equal(t, current.Extra, stale.Extra)
	patch := map[string]any{ForceOpenAIUltrafastExtraKey: false, ForceOpenAIFastExtraKey: true, FreeOpenAIUltrafastExtraKey: true}
	ctx := withAccountConfigurationIntent(context.Background(), []int64{7}, patch, nil)
	patch[ForceOpenAIUltrafastExtraKey] = true
	require.Empty(t, AccountConfigurationIntentFromContext(ctx, 8).Extra)
	require.NoError(t, PreserveAccountConfiguration(current, stale, AccountConfigurationIntentFromContext(ctx, 7)))
	require.True(t, stale.IsForceOpenAIFastEnabled())
	require.False(t, stale.IsForceOpenAIUltrafastEnabled())
	require.True(t, stale.IsFreeOpenAIFastEnabled(), "omitted free preference survives")
	require.True(t, stale.IsFreeOpenAIUltrafastEnabled())
	require.False(t, current.IsForceOpenAIFastEnabled(), "merge must not mutate latest row")
	// A second writer's old read cannot enable the other force tier after this
	// commit: persistence revalidates its intent against the locked latest row.
	other := &Account{Platform: PlatformOpenAI, Extra: maps.Clone(current.Extra)}
	err := PreserveAccountConfiguration(stale, other, AccountConfigurationIntent{Extra: map[string]any{ForceOpenAIUltrafastExtraKey: true}})
	require.Equal(t, "OPENAI_ACCOUNT_FAST_CONFLICT", infraerrors.Reason(err))
}

func TestScopedOpenAIOptionsAcceptAccountFastSettings(t *testing.T) {
	require.NoError(t, validateScopedOpenAIOptions(json.RawMessage(`{"force_openai_ultrafast":true,"free_openai_fast":true,"free_openai_ultrafast":true}`)))
	require.Error(t, validateScopedOpenAIOptions(json.RawMessage(`{"force_openai_fast":true,"force_openai_ultrafast":true}`)))
	require.Error(t, validateScopedOpenAIOptions(json.RawMessage(`{"free_openai_fast":null}`)))
}
