package service

import (
	"context"
	"maps"
	"testing"

	"github.com/stretchr/testify/require"
)

func codexConfigAccount(t *testing.T) *Account {
	t.Helper()
	a := &Account{ID: 7, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"access_token": "initial", "plan_type": "plus"}}
	require.NoError(t, PrepareCodexTurnStateForCreate(a, &CodexTurnStateConfig{Enabled: true, AccountType: "auto"}))
	return a
}

func TestCodexTurnStateConfigurationClassifiesOnlyKnownPlans(t *testing.T) {
	a := codexConfigAccount(t)
	for plan, want := range map[string]string{"plus": "personal", "pro": "personal", "free": "personal", "team": "team_business", "business": "team_business", "enterprise": "", "": "", "future_plan": ""} {
		a.Credentials["plan_type"] = plan
		require.Equal(t, want, CodexTurnStateAccountTypeForAccount(a), plan)
	}
	a.Credentials["auth_mode"] = OpenAIAuthModePersonalAccessToken
	require.False(t, CodexTurnStateConfigForAccount(a).Enabled)
	require.Error(t, ValidateCodexTurnStateConfig(a, &CodexTurnStateConfig{AccountType: "auto"}))
	a.Credentials["auth_mode"] = OpenAIAuthModeAgentIdentity
	require.False(t, IsCodexTurnStateAccount(a))
}

func TestCodexTurnStateConfigurationPreservesLiveConfigAgainstSnapshots(t *testing.T) {
	current := codexConfigAccount(t)
	generation := CodexTurnStateGenerationForAccount(current)
	stale := *current
	stale.Extra = map[string]any{CodexTurnStateExtraKey: map[string]any{"enabled": false}, CodexTurnStateGenerationExtraKey: "attacker", "usage": 1}
	require.NoError(t, PreserveAccountConfiguration(current, &stale, AccountConfigurationIntent{}))
	require.True(t, CodexTurnStateConfigForAccount(&stale).Enabled)
	require.Equal(t, generation, CodexTurnStateGenerationForAccount(&stale))
	require.Equal(t, 1, stale.Extra["usage"])
	requested := CodexTurnStateConfig{AccountType: "auto"}
	ctx := withAccountConfigurationIntent(context.Background(), []int64{current.ID}, nil, nil, &requested)
	require.NoError(t, PreserveAccountConfiguration(current, &stale, AccountConfigurationIntentFromContext(ctx, current.ID)))
	require.False(t, CodexTurnStateConfigForAccount(&stale).Enabled)
	require.NotEqual(t, generation, CodexTurnStateGenerationForAccount(&stale))
	require.Nil(t, AccountConfigurationIntentFromContext(ctx, 999).CodexTurnState)
}

func TestCodexTurnStateGenerationChangesOnlyForRelevantIdentity(t *testing.T) {
	current := codexConfigAccount(t)
	generation := CodexTurnStateGenerationForAccount(current)
	for _, key := range []string{"usage", "model_mapping", "user_agent"} {
		target := *current
		target.Credentials = maps.Clone(current.Credentials)
		target.Credentials[key] = "updated"
		require.NoError(t, PreserveAccountConfiguration(current, &target, AccountConfigurationIntent{}))
		require.Equal(t, generation, CodexTurnStateGenerationForAccount(&target), key)
	}
	for _, key := range []string{"access_token", "refresh_token", "chatgpt_account_id", "plan_type"} {
		target := *current
		target.Credentials = maps.Clone(current.Credentials)
		target.Credentials[key] = "new"
		require.NoError(t, PreserveAccountConfiguration(current, &target, AccountConfigurationIntent{}))
		require.NotEqual(t, generation, CodexTurnStateGenerationForAccount(&target), key)
	}
}

func TestCodexTurnStateConfigurationRejectsShadowAndInvalidFields(t *testing.T) {
	a := codexConfigAccount(t)
	parent := int64(8)
	a.ParentAccountID = &parent
	require.Error(t, ValidateCodexTurnStateConfig(a, &CodexTurnStateConfig{AccountType: "auto"}))
	a.ParentAccountID = nil
	require.Error(t, ValidateCodexTurnStateConfig(a, &CodexTurnStateConfig{AccountType: "both"}))
	zero := int64(0)
	require.Error(t, ValidateCodexTurnStateConfig(a, &CodexTurnStateConfig{AccountType: "auto", CollectorProxyID: &zero}))
	clean := StripCodexTurnStateManagedExtra(map[string]any{CodexTurnStateExtraKey: "bad", CodexTurnStateGenerationExtraKey: "bad", "codex_turn_state_token": "secret", "usage": 1})
	require.Equal(t, map[string]any{"usage": 1}, clean)
}
