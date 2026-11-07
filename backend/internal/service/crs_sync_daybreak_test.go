//go:build unit

package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCRSSyncDaybreakDefaultsOffAndKeepsLocalPreferences(t *testing.T) {
	for _, existing := range []bool{false, true} {
		var account *Account
		if existing {
			account = &Account{ID: 41, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Extra: map[string]any{"crs_account_id": "crs-openai-1", OpenAIDaybreakBlueEnabledKey: false, OpenAIDaybreakRedEnabledKey: false}}
		}
		repo := newCRSLongContextAccountRepo(account)
		result := runCRSOpenAILongContextSync(t, repo, crsOpenAILongContextSource{collection: "openaiOAuthAccounts", credentials: map[string]any{"access_token": "oauth-token"}, extra: map[string]any{OpenAIDaybreakBlueEnabledKey: true, OpenAIDaybreakRedEnabledKey: true}})
		require.Zero(t, result.Failed)
		require.Len(t, result.Items, 1)
		require.NotEmpty(t, result.Items[0].Warning)
		stored := repo.accounts["crs-openai-1"]
		blue, _ := stored.Extra[OpenAIDaybreakBlueEnabledKey].(bool)
		red, _ := stored.Extra[OpenAIDaybreakRedEnabledKey].(bool)
		require.False(t, blue)
		require.False(t, red)
	}
}

func TestCRSSyncAPIKeyDaybreakImport(t *testing.T) {
	for _, tc := range []struct {
		name          string
		sourceExtra   map[string]any
		existingExtra map[string]any
		want          map[string]any
		wantFailed    bool
		wantWarning   bool
	}{
		{name: "omitted preferences stay absent"},
		{name: "new account keeps enabled preferences", sourceExtra: map[string]any{OpenAIDaybreakBlueEnabledKey: true, OpenAIDaybreakRedEnabledKey: true}, want: map[string]any{OpenAIDaybreakBlueEnabledKey: true, OpenAIDaybreakRedEnabledKey: true}},
		{name: "disabling Blue disables Red", sourceExtra: map[string]any{OpenAIDaybreakBlueEnabledKey: false, OpenAIDaybreakRedEnabledKey: true}, want: map[string]any{OpenAIDaybreakBlueEnabledKey: false, OpenAIDaybreakRedEnabledKey: false}},
		{name: "Red requires Blue", sourceExtra: map[string]any{OpenAIDaybreakRedEnabledKey: true}, wantFailed: true},
		{name: "boolean required", sourceExtra: map[string]any{OpenAIDaybreakBlueEnabledKey: "true"}, wantFailed: true},
		{name: "Engine excluded", sourceExtra: map[string]any{OpenAIAPIKeyModeExtraKey: "codex_engine", OpenAIDaybreakBlueEnabledKey: true, OpenAIDaybreakRedEnabledKey: true}, wantWarning: true},
		{name: "existing local disabled preferences remain", sourceExtra: map[string]any{OpenAIDaybreakBlueEnabledKey: true, OpenAIDaybreakRedEnabledKey: true}, existingExtra: map[string]any{OpenAIDaybreakBlueEnabledKey: false, OpenAIDaybreakRedEnabledKey: false}, want: map[string]any{OpenAIDaybreakBlueEnabledKey: false, OpenAIDaybreakRedEnabledKey: false}, wantWarning: true},
		{name: "existing local enabled preferences remain", sourceExtra: map[string]any{OpenAIDaybreakBlueEnabledKey: false, OpenAIDaybreakRedEnabledKey: false}, existingExtra: map[string]any{OpenAIDaybreakBlueEnabledKey: true, OpenAIDaybreakRedEnabledKey: true}, want: map[string]any{OpenAIDaybreakBlueEnabledKey: true, OpenAIDaybreakRedEnabledKey: true}, wantWarning: true},
		{name: "existing omission cannot become enabled", sourceExtra: map[string]any{OpenAIDaybreakBlueEnabledKey: true, OpenAIDaybreakRedEnabledKey: true}, existingExtra: map[string]any{}, wantWarning: true},
		{name: "existing Engine excluded", sourceExtra: map[string]any{OpenAIDaybreakBlueEnabledKey: true}, existingExtra: map[string]any{OpenAIAPIKeyModeExtraKey: "codex_engine"}, wantWarning: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var existing *Account
			if tc.existingExtra != nil {
				existing = &Account{ID: 41, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "old-key"}, Extra: mergeMap(tc.existingExtra, map[string]any{"crs_account_id": "crs-openai-1"})}
			}
			repo := newCRSLongContextAccountRepo(existing)
			result := runCRSOpenAILongContextSync(t, repo, crsOpenAILongContextSource{collection: "openaiResponsesAccounts", credentials: map[string]any{"api_key": "new-key"}, extra: tc.sourceExtra})
			require.Len(t, result.Items, 1)
			if tc.wantFailed {
				require.Equal(t, 1, result.Failed)
				require.Equal(t, "failed", result.Items[0].Action)
				require.NotEmpty(t, result.Items[0].Error)
				return
			}
			require.Zero(t, result.Failed)
			require.Equal(t, tc.wantWarning, result.Items[0].Warning != "")
			stored := repo.accounts["crs-openai-1"]
			require.Equal(t, "new-key", stored.Credentials["api_key"])
			for _, key := range []string{OpenAIDaybreakBlueEnabledKey, OpenAIDaybreakRedEnabledKey} {
				if value, exists := tc.want[key]; exists {
					require.Equal(t, value, stored.Extra[key])
				} else {
					require.NotContains(t, stored.Extra, key)
				}
			}
		})
	}
}
