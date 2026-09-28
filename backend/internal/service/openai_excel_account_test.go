package service

import (
	"context"
	"maps"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestOpenAIExcelUpstreamEligibility(t *testing.T) {
	parent := int64(7)
	for _, tc := range []struct {
		name    string
		account *Account
		allowed bool
	}{
		{"nil", nil, false},
		{"oauth", &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth}, true},
		{"api-key", &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}, false},
		{"setup-token", &Account{Platform: PlatformOpenAI, Type: AccountTypeSetupToken}, false},
		{"spark", &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth, ParentAccountID: &parent}, false},
		{"other-platform", &Account{Platform: PlatformAnthropic, Type: AccountTypeOAuth}, false},
		{"pat", &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"auth_mode": "personalAccessToken"}}, false},
		{"legacy-pat", &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"openai_auth_mode": "personal_access_token"}}, false},
		{"agent", &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"auth_mode": "agentIdentity"}}, false},
		{"legacy-agent", &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"openai_auth_mode": "agent_identity"}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.False(t, tc.account.IsOpenAIExcelUpstreamEnabled())
			if tc.account != nil {
				tc.account.Extra = map[string]any{OpenAIExcelUpstreamEnabledExtraKey: true}
			}
			require.Equal(t, tc.allowed, tc.account.IsOpenAIExcelUpstreamEnabled())
			err := ValidateOpenAIExcelUpstreamExtra(tc.account, map[string]any{OpenAIExcelUpstreamEnabledExtraKey: true})
			if tc.allowed {
				require.NoError(t, err)
				require.Equal(t, OpenAIUpstreamKindExcel, tc.account.OpenAIUpstreamKind())
			} else {
				require.Error(t, err)
				require.Equal(t, OpenAIUpstreamKindCodex, tc.account.OpenAIUpstreamKind())
			}
		})
	}
}

func TestOpenAIExcelUpstreamRejectsAmbiguousToggle(t *testing.T) {
	account := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	for _, raw := range []any{nil, "true", 1, []bool{true}} {
		require.Error(t, ValidateOpenAIExcelUpstreamExtra(account, map[string]any{OpenAIExcelUpstreamEnabledExtraKey: raw}))
	}
}

func TestOpenAIExcelRouteGenerationSurvivesRefreshAndChangesOnlyOnToggle(t *testing.T) {
	current := &Account{ID: 7, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Extra: map[string]any{}}
	apply := func(enabled bool) *Account {
		target := *current
		target.Extra = maps.Clone(current.Extra)
		ctx := withAccountConfigurationIntent(context.Background(), []int64{7}, map[string]any{OpenAIExcelUpstreamEnabledExtraKey: enabled}, nil)
		require.NoError(t, PreserveAccountConfiguration(current, &target, AccountConfigurationIntentFromContext(ctx, 7)))
		return &target
	}
	current = apply(true)
	first := current.OpenAIUpstreamRouteGeneration()
	_, err := uuid.Parse(first)
	require.NoError(t, err)
	current = apply(true)
	require.Equal(t, first, current.OpenAIUpstreamRouteGeneration())
	stale := &Account{ID: 7, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Extra: map[string]any{OpenAIExcelUpstreamEnabledExtraKey: false, OpenAIUpstreamRouteGenerationExtraKey: "forged"}}
	require.NoError(t, PreserveAccountConfiguration(current, stale, AccountConfigurationIntent{}))
	require.True(t, stale.IsOpenAIExcelUpstreamEnabled())
	require.Equal(t, first, stale.OpenAIUpstreamRouteGeneration())
	current = apply(false)
	second := current.OpenAIUpstreamRouteGeneration()
	require.NotEqual(t, first, second)
	current = apply(true)
	require.NotEqual(t, first, current.OpenAIUpstreamRouteGeneration())
	require.NotEqual(t, second, current.OpenAIUpstreamRouteGeneration())
}

func TestOpenAIExcelConfigurationCreateAndConversion(t *testing.T) {
	account := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Extra: map[string]any{OpenAIExcelUpstreamEnabledExtraKey: true, OpenAIUpstreamRouteGenerationExtraKey: "imported"}}
	require.NoError(t, prepareOpenAIExcelUpstreamForCreate(account))
	require.NotEqual(t, "imported", account.OpenAIUpstreamRouteGeneration())
	converted := *account
	converted.Credentials = map[string]any{"auth_mode": "personalAccessToken"}
	require.NoError(t, PreserveAccountConfiguration(account, &converted, AccountConfigurationIntent{}))
	require.NotContains(t, converted.Extra, OpenAIExcelUpstreamEnabledExtraKey)
	require.NotEmpty(t, converted.OpenAIUpstreamRouteGeneration(), "ineligible credentials retain a private route fence")
	require.NotEqual(t, account.OpenAIUpstreamRouteGeneration(), converted.OpenAIUpstreamRouteGeneration(), "leaving Excel advances the route fence")
	require.True(t, account.IsOpenAIExcelUpstreamEnabled(), "conversion must not mutate source snapshot")
}

func TestOpenAIExcelRouteGenerationIsVisibleAfterEligibilityConversion(t *testing.T) {
	account := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Extra: map[string]any{
		OpenAIUpstreamRouteGenerationExtraKey: "old",
	}}
	// A non-eligible account may retain the private generation while its public
	// upstream kind remains Codex; adapters must gate on OAuth eligibility too.
	account.Credentials = map[string]any{"auth_mode": "personalAccessToken"}
	require.Equal(t, "old", account.OpenAIUpstreamRouteGeneration())
	require.Equal(t, OpenAIUpstreamKindCodex, account.OpenAIUpstreamKind())
}

func TestOpenAIExcelCredentialEligibilityRoundTripCannotRestoreQueuedRoute(t *testing.T) {
	initial := &Account{ID: 7, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Extra: map[string]any{}}
	queued := &CandyTestItem{ExpectedUpstreamKind: initial.OpenAIUpstreamKind(), ExpectedRouteGeneration: initial.OpenAIUpstreamRouteGeneration()}
	enabled := *initial
	require.NoError(t, PreserveAccountConfiguration(initial, &enabled, AccountConfigurationIntent{Extra: map[string]any{OpenAIExcelUpstreamEnabledExtraKey: true}}))
	pat := enabled
	pat.Credentials = map[string]any{"auth_mode": "personalAccessToken"}
	require.NoError(t, PreserveAccountConfiguration(&enabled, &pat, AccountConfigurationIntent{}))
	require.False(t, candyTestRouteMatches(queued, &pat))
	patGeneration := pat.OpenAIUpstreamRouteGeneration()
	stale := pat
	stale.Extra = map[string]any{OpenAIExcelUpstreamEnabledExtraKey: true, OpenAIUpstreamRouteGenerationExtraKey: "stale"}
	require.NoError(t, PreserveAccountConfiguration(&pat, &stale, AccountConfigurationIntent{}))
	require.Equal(t, patGeneration, stale.OpenAIUpstreamRouteGeneration(), "background writes to an ineligible account must not rotate or erase its fence")
	require.NotContains(t, stale.Extra, OpenAIExcelUpstreamEnabledExtraKey)
	restored := stale
	restored.Credentials = map[string]any{"auth_mode": "oauth"}
	require.NoError(t, PreserveAccountConfiguration(&stale, &restored, AccountConfigurationIntent{}))
	require.NotEqual(t, patGeneration, restored.OpenAIUpstreamRouteGeneration())
	require.False(t, restored.IsOpenAIExcelUpstreamEnabled())
	require.False(t, candyTestRouteMatches(queued, &restored), "returning to Codex must not restore the original queued snapshot")
}
