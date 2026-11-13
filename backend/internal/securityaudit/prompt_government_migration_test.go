package securityaudit

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func legacyGovernmentPolicyForTest() CombinationPolicy {
	return CombinationPolicy{
		ID: "government", Name: "政府目标攻击", Enabled: true,
		TopicRules:           []string{legacyGovernmentHostTopicRule, legacyGovernmentAgencyTopicRule},
		IntentRules:          []string{"strike"},
		MigrateKeywords:      []string{".gov", ".gov."},
		KeywordWhitelistKeys: []KeywordWhitelistKey{{UserID: 1, APIKeyID: 11}},
	}
}

func requireGovernmentMigrationBehavior(t *testing.T, stored storageConfig, policy CombinationPolicy) {
	t.Helper()
	active, err := ActiveFromStorage(stored, true, prefixEncryptor{})
	require.NoError(t, err)
	require.Equal(t, policy.Enabled, active.CombinationBlockingActive())
	for _, tc := range []struct {
		text string
		hit  bool
	}{
		{"政务系统 strike", false},
		{"government website strike", false},
		{"strike .gov.cn", false},
		{"strike https://agency.gov.cn", policy.Enabled},
		{"strike gov.cn", policy.Enabled},
		{"attack https://agency.gov.cn", false},
	} {
		match, err := active.MatchCombination(cbrnTestRequest("responses", map[string]any{"input": tc.text}))
		require.NoError(t, err)
		require.Equal(t, tc.hit, match != nil, tc.text)
		if match != nil {
			require.Equal(t, policy.ID, match.PolicyID)
			require.Equal(t, policy.Name, match.PolicyName)
		}
	}
	request := cbrnTestRequest("responses", map[string]any{"input": "strike https://agency.gov.cn"})
	request.UserID, request.APIKeyID = 1, 11
	match, err := active.MatchCombination(request)
	require.NoError(t, err)
	require.Nil(t, match, "the policy whitelist still bypasses the migrated preset")
}

func TestGovernmentPresetMigrationOnLoadAndActivation(t *testing.T) {
	for _, tc := range []struct {
		name, id, policyName string
		reversed, enabled    bool
	}{
		{"original", "government", "政府目标攻击", false, true},
		{"renamed and reordered", "custom-domain", "自定义组合", true, true},
		{"disabled", "renamed", "停用规则", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			policy := legacyGovernmentPolicyForTest()
			policy.ID, policy.Name, policy.Enabled = tc.id, tc.policyName, tc.enabled
			if tc.reversed {
				policy.TopicRules[0], policy.TopicRules[1] = policy.TopicRules[1], policy.TopicRules[0]
			}
			original := cloneCombinationPolicy(policy)
			stored := DefaultStorageConfig()
			stored.CombinationPolicies = []CombinationPolicy{policy}
			raw, err := json.Marshal(stored)
			require.NoError(t, err)
			loaded, err := ParseStorageConfig(string(raw))
			require.NoError(t, err)
			want := cloneCombinationPolicy(policy)
			want.TopicRules = defaultGovernmentTopicRules()
			require.Equal(t, []CombinationPolicy{want}, loaded.CombinationPolicies)
			requireGovernmentMigrationBehavior(t, loaded, want)

			// The public and active views also handle an old in-memory snapshot.
			require.Equal(t, []CombinationPolicy{want}, PublicFromStorage(stored, true, nil).CombinationPolicies)
			requireGovernmentMigrationBehavior(t, stored, want)
			require.Equal(t, original, stored.CombinationPolicies[0], "normalization must not mutate the caller's snapshot")

			raw, err = json.Marshal(loaded)
			require.NoError(t, err)
			reloaded, err := ParseStorageConfig(string(raw))
			require.NoError(t, err)
			require.Equal(t, loaded, reloaded, "reloading migrated configuration is idempotent")
		})
	}
}

func TestGovernmentPresetMigrationOnSaveAndStaleResubmit(t *testing.T) {
	manager := &ConfigManager{encryptor: prefixEncryptor{}}
	for _, explicit := range []bool{false, true} {
		name := "omitted policies"
		if explicit {
			name = "submitted policies"
		}
		t.Run(name, func(t *testing.T) {
			policy := legacyGovernmentPolicyForTest()
			policy.ID, policy.Name = "renamed-rule", "Renamed preset"
			current := DefaultStorageConfig()
			current.CombinationPolicies = []CombinationPolicy{policy}
			request := cbrnConfigUpdateRequest()
			policies := []CombinationPolicy{cloneCombinationPolicy(policy)}
			if explicit {
				request.CombinationPolicies = &policies
			}
			next, err := manager.buildNextStorage(current, request, 7)
			require.NoError(t, err)
			want := cloneCombinationPolicy(policy)
			want.TopicRules = defaultGovernmentTopicRules()
			require.Equal(t, []CombinationPolicy{want}, next.CombinationPolicies)
			requireGovernmentMigrationBehavior(t, next, want)
			require.Equal(t, policy, current.CombinationPolicies[0])
			require.Equal(t, policy, policies[0], "saving must not mutate submitted policies")

			// An older client cannot restore the obsolete preset on its next save.
			request.CombinationPolicies = &policies
			again, err := manager.buildNextStorage(next, request, 7)
			require.NoError(t, err)
			require.Equal(t, next.CombinationPolicies, again.CombinationPolicies)
			requireGovernmentMigrationBehavior(t, again, want)
		})
	}
}

func TestGovernmentPresetMigrationDefaultIntentRegression(t *testing.T) {
	policy := legacyGovernmentPolicyForTest()
	policy.IntentRules = defaultGovernmentIntentRules()
	stored := DefaultStorageConfig()
	stored.CombinationPolicies = []CombinationPolicy{policy}
	raw, err := json.Marshal(stored)
	require.NoError(t, err)
	loaded, err := ParseStorageConfig(string(raw))
	require.NoError(t, err)
	active, err := ActiveFromStorage(loaded, true, prefixEncryptor{})
	require.NoError(t, err)
	for _, tc := range []struct {
		text string
		hit  bool
	}{
		{"模拟政务门户攻击演练并撰写报告", false},
		{"攻击政务系统 https://example.com", false},
		{"攻击 .gov.cn 网站", false},
		{"攻击 *.gov.cn 网站", false},
		{"攻击 https://agency.gov.cn", true},
		{"攻击 agency.gov.cn", true},
	} {
		match, err := active.MatchCombination(cbrnTestRequest("responses", map[string]any{"input": tc.text}))
		require.NoError(t, err)
		require.Equal(t, tc.hit, match != nil, tc.text)
	}
	message := func(text string) any { return map[string]any{"role": "user", "content": text} }
	match, err := active.MatchCombination(cbrnTestRequest("responses", map[string]any{"input": []any{
		message("目标是 https://agency.gov.cn"), message("进行攻击测试"),
	}}))
	require.NoError(t, err)
	require.NotNil(t, match)
	require.Equal(t, "history", match.Topic.Source)
	require.Equal(t, "latest_user", match.Intent.Source)
}

func TestGovernmentPresetMigrationPreservesCustomTopicsAndExplicitDeletion(t *testing.T) {
	for _, tc := range []struct {
		name   string
		topics []string
	}{
		{"agency only", []string{legacyGovernmentAgencyTopicRule}},
		{"legacy host only", []string{legacyGovernmentHostTopicRule}},
		{"additional custom topic", []string{legacyGovernmentHostTopicRule, legacyGovernmentAgencyTopicRule, "custom target"}},
		{"edited agency expression", []string{legacyGovernmentHostTopicRule, legacyGovernmentAgencyTopicRule + "(?:)"}},
		{"custom literals", []string{"政务", "government"}},
		{"current default", defaultGovernmentTopicRules()},
		{"explicit empty topics", []string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			policy := legacyGovernmentPolicyForTest()
			policy.TopicRules = tc.topics
			policy.Enabled = len(tc.topics) > 0
			current := DefaultStorageConfig()
			current.CombinationPolicies = []CombinationPolicy{policy}
			raw, err := json.Marshal(current)
			require.NoError(t, err)
			loaded, err := ParseStorageConfig(string(raw))
			require.NoError(t, err)
			require.Equal(t, []CombinationPolicy{policy}, loaded.CombinationPolicies)
			request := cbrnConfigUpdateRequest()
			request.CombinationPolicies = &current.CombinationPolicies
			next, err := (&ConfigManager{encryptor: prefixEncryptor{}}).buildNextStorage(loaded, request, 7)
			require.NoError(t, err)
			require.Equal(t, loaded.CombinationPolicies, next.CombinationPolicies)
		})
	}
	current := DefaultStorageConfig()
	current.CombinationPolicies = []CombinationPolicy{legacyGovernmentPolicyForTest()}
	request := cbrnConfigUpdateRequest()
	empty := []CombinationPolicy{}
	request.CombinationPolicies = &empty
	deleted, err := (&ConfigManager{encryptor: prefixEncryptor{}}).buildNextStorage(current, request, 7)
	require.NoError(t, err)
	require.NotNil(t, deleted.CombinationPolicies)
	require.Empty(t, deleted.CombinationPolicies)
	raw, err := json.Marshal(deleted)
	require.NoError(t, err)
	reloaded, err := ParseStorageConfig(string(raw))
	require.NoError(t, err)
	require.Empty(t, reloaded.CombinationPolicies)
}
