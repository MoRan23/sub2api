package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCodexTurnStateTicketProxyDefaultsAndExplicitValues(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want bool
	}{
		{"legacy", `{"enabled":true,"account_type":"auto"}`, true},
		{"enabled", `{"enabled":true,"account_type":"auto","use_ticket_proxy":true}`, true},
		{"disabled", `{"enabled":true,"account_type":"auto","use_ticket_proxy":false}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var config CodexTurnStateConfig
			require.NoError(t, json.Unmarshal([]byte(tc.body), &config))
			account := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth}
			require.NoError(t, PrepareCodexTurnStateForCreate(account, &config))
			actual := CodexTurnStateConfigForAccount(account)
			require.NotNil(t, actual.UseTicketProxy)
			require.Equal(t, tc.want, CodexTurnStateUseTicketProxy(actual))
			require.Equal(t, tc.want, account.Extra[CodexTurnStateExtraKey].(map[string]any)["use_ticket_proxy"])
		})
	}
	for _, value := range []string{"null", `"false"`, "0", "{}", "[]"} {
		var config CodexTurnStateConfig
		require.ErrorContains(t, json.Unmarshal([]byte(`{"use_ticket_proxy":`+value+`}`), &config), "use_ticket_proxy")
	}
	for _, account := range []*Account{nil, {Platform: PlatformOpenAI, Type: AccountTypeOAuth}, {
		Platform: PlatformOpenAI, Type: AccountTypeOAuth,
		Extra: map[string]any{CodexTurnStateExtraKey: map[string]any{"enabled": true, "account_type": "auto"}},
	}} {
		require.True(t, CodexTurnStateUseTicketProxy(CodexTurnStateConfigForAccount(account)))
	}
}

func TestCodexTurnStateTicketProxyUpdatesPreserveBundlesAndLegacyIntent(t *testing.T) {
	current := codexConfigAccount(t)
	generation := CodexTurnStateGenerationForAccount(current)
	epoch := CodexTurnStateCredentialEpochForAccount(current)
	for _, requested := range []*bool{new(false), nil, new(true), nil} {
		target := *current
		config := CodexTurnStateConfig{Enabled: true, AccountType: "auto", UseTicketProxy: requested}
		require.NoError(t, PreserveAccountConfiguration(current, &target, AccountConfigurationIntent{CodexTurnState: &config}))
		want := CodexTurnStateUseTicketProxy(CodexTurnStateConfigForAccount(current))
		if requested != nil {
			want = *requested
		}
		require.Equal(t, want, CodexTurnStateUseTicketProxy(CodexTurnStateConfigForAccount(&target)))
		require.Equal(t, generation, CodexTurnStateGenerationForAccount(&target), "routing changes must not revoke existing bundles")
		require.Equal(t, epoch, CodexTurnStateCredentialEpochForAccount(&target))
		current = &target
	}
	config := CodexTurnStateConfig{Enabled: false, AccountType: "auto", UseTicketProxy: new(false)}
	target := *current
	require.NoError(t, PreserveAccountConfiguration(current, &target, AccountConfigurationIntent{CodexTurnState: &config}))
	require.NotEqual(t, generation, CodexTurnStateGenerationForAccount(&target), "disabling the cache still revokes existing bundles")
}

func TestCodexTurnStateTicketProxyIntentCopiesPointer(t *testing.T) {
	config := CodexTurnStateConfig{UseTicketProxy: new(false)}
	ctx := withAccountConfigurationIntent(context.Background(), []int64{7}, nil, nil, &config)
	*config.UseTicketProxy = true
	intent := AccountConfigurationIntentFromContext(ctx, 7)
	require.False(t, *intent.CodexTurnState.UseTicketProxy)
	*intent.CodexTurnState.UseTicketProxy = true
	require.False(t, *AccountConfigurationIntentFromContext(ctx, 7).CodexTurnState.UseTicketProxy)
}
