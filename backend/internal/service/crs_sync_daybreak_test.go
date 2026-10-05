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
