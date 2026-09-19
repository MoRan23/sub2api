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
	for plan, want := range map[string]string{
		"plus": "personal", "pro": "personal", "free": "personal", "go": "personal", "personal": "personal",
		"chatgpt_pro": "personal", "CHATGPT--_Pro": "personal", "prolite": "personal", " pro_-lite ": "personal",
		"team": "team_business", "business": "team_business", "ChatGPT_Team": "team_business", "chatgpt-business": "team_business",
		"self_serve_business_prolite": "team_business", "SELF--SERVE__BUSINESS_-PROLITE": "team_business", "self\tserve business\nprolite": "team_business",
		"enterprise": "", "": "", "future_plan": "", "future_business": "", "self_serve_business_prolite_trial": "",
	} {
		a.Credentials["plan_type"] = plan
		require.Equal(t, want, CodexTurnStateAccountTypeForAccount(a), plan)
	}
	a.Credentials["auth_mode"] = OpenAIAuthModePersonalAccessToken
	require.False(t, CodexTurnStateConfigForAccount(a).Enabled)
	require.Error(t, ValidateCodexTurnStateConfig(a, &CodexTurnStateConfig{AccountType: "auto"}))
	a.Credentials["auth_mode"] = OpenAIAuthModeAgentIdentity
	require.False(t, IsCodexTurnStateAccount(a))
}

func TestCodexTurnStateConfigurationManualAccountTypeTakesPrecedence(t *testing.T) {
	a := codexConfigAccount(t)
	for _, manual := range []string{"personal", "team_business"} {
		for _, plan := range []string{"enterprise", "self_serve_business_prolite", "chatgpt_pro"} {
			a.Extra[CodexTurnStateExtraKey] = codexTurnStateConfigMap(CodexTurnStateConfig{AccountType: manual})
			a.Credentials["plan_type"] = plan
			require.Equal(t, manual, CodexTurnStateAccountTypeForAccount(a), plan)
		}
	}
}

func TestCodexTurnStateConfigurationPlanAliasesPreserveGeneration(t *testing.T) {
	current := codexConfigAccount(t)
	current.Credentials["plan_type"] = "self_serve_business_prolite"
	generation := CodexTurnStateGenerationForAccount(current)
	target := *current
	target.Credentials = maps.Clone(current.Credentials)
	target.Credentials["plan_type"] = "Self--Serve__Business Prolite"
	require.NoError(t, PreserveAccountConfiguration(current, &target, AccountConfigurationIntent{}))
	require.Equal(t, generation, CodexTurnStateGenerationForAccount(&target))

	target.Credentials["plan_type"] = "chatgpt_pro"
	require.NoError(t, PreserveAccountConfiguration(current, &target, AccountConfigurationIntent{}))
	require.NotEqual(t, generation, CodexTurnStateGenerationForAccount(&target))
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

func TestCodexTurnStateCredentialEpochExistsWithoutCacheConfiguration(t *testing.T) {
	a := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"access_token": "initial"},
		Extra: map[string]any{CodexTurnStateCredentialEpochExtraKey: "untrusted"}}
	require.NoError(t, PrepareCodexTurnStateForCreate(a, nil))
	epoch := CodexTurnStateCredentialEpochForAccount(a)
	require.NotEmpty(t, epoch)
	require.NotEqual(t, "untrusted", epoch)
	require.NotContains(t, a.Extra, CodexTurnStateExtraKey)
	require.Empty(t, CodexTurnStateGenerationForAccount(a))

	legacy := *a
	legacy.Extra = nil
	target := legacy
	require.NoError(t, PreserveAccountConfiguration(&legacy, &target, AccountConfigurationIntent{}))
	require.NotEmpty(t, CodexTurnStateCredentialEpochForAccount(&target))
	require.NotContains(t, target.Extra, CodexTurnStateExtraKey)
}

func TestCodexTurnStateCredentialEpochPreservesConfigurationAndUsageChanges(t *testing.T) {
	current := codexConfigAccount(t)
	epoch := CodexTurnStateCredentialEpochForAccount(current)
	proxyID := int64(9)
	for _, config := range []CodexTurnStateConfig{
		{AccountType: "auto"},
		{Enabled: true, AccountType: "team_business"},
		{Enabled: true, AccountType: "auto", CollectorProxyID: &proxyID},
	} {
		target := *current
		target.Extra = map[string]any{CodexTurnStateCredentialEpochExtraKey: "spoofed"}
		require.NoError(t, PreserveAccountConfiguration(current, &target, AccountConfigurationIntent{CodexTurnState: &config}))
		require.Equal(t, epoch, CodexTurnStateCredentialEpochForAccount(&target))
	}
	for _, key := range []string{"plan_type", "usage", "model_mapping", "user_agent", "_token_version"} {
		target := *current
		target.Credentials = maps.Clone(current.Credentials)
		target.Credentials[key] = "updated"
		require.NoError(t, PreserveAccountConfiguration(current, &target, AccountConfigurationIntent{}))
		require.Equal(t, epoch, CodexTurnStateCredentialEpochForAccount(&target), key)
	}
	require.NotContains(t, StripCodexTurnStateManagedExtra(current.Extra), CodexTurnStateCredentialEpochExtraKey)
}

func TestCodexTurnStateCredentialEpochRotatesOnlyForAuthOrQualification(t *testing.T) {
	current := codexConfigAccount(t)
	epoch := CodexTurnStateCredentialEpochForAccount(current)
	for _, key := range CodexTurnStateCredentialKeys {
		target := *current
		target.Credentials = maps.Clone(current.Credentials)
		target.Credentials[key] = "different"
		require.NoError(t, PreserveAccountConfiguration(current, &target, AccountConfigurationIntent{}))
		require.NotEmpty(t, CodexTurnStateCredentialEpochForAccount(&target), key)
		require.NotEqual(t, epoch, CodexTurnStateCredentialEpochForAccount(&target), key)
	}
	for _, authModeKey := range []string{"auth_mode", "openai_auth_mode"} {
		for _, mode := range []string{OpenAIAuthModePersonalAccessToken, "  AgentIdentity  "} {
			ineligible := *current
			ineligible.Credentials = maps.Clone(current.Credentials)
			ineligible.Credentials[authModeKey] = mode
			require.NoError(t, PreserveAccountConfiguration(current, &ineligible, AccountConfigurationIntent{}))
			require.Empty(t, CodexTurnStateCredentialEpochForAccount(&ineligible))
			require.NotContains(t, ineligible.Extra, CodexTurnStateCredentialEpochExtraKey)
			restored := ineligible
			restored.Credentials = maps.Clone(current.Credentials)
			require.NoError(t, PreserveAccountConfiguration(&ineligible, &restored, AccountConfigurationIntent{}))
			require.NotEmpty(t, CodexTurnStateCredentialEpochForAccount(&restored))
			require.NotEqual(t, epoch, CodexTurnStateCredentialEpochForAccount(&restored))
		}
	}
	parent := int64(8)
	shadow := *current
	shadow.ParentAccountID = &parent
	require.NoError(t, PreserveAccountConfiguration(current, &shadow, AccountConfigurationIntent{}))
	require.NotContains(t, shadow.Extra, CodexTurnStateCredentialEpochExtraKey)
}
