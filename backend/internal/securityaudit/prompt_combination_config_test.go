package securityaudit

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

func testCombinationPolicy(id string) CombinationPolicy {
	return CombinationPolicy{ID: id, Name: "自定义 " + id, Enabled: true, TopicRules: []string{"target"}, IntentRules: []string{"attack"}, MigrateKeywords: []string{}}
}

func TestCombinationConfigLegacyMigrationAndExplicitListAuthority(t *testing.T) {
	legacy, err := ParseStorageConfig(`{"all_groups":true,"cbrn_blocking_enabled":true,"cbrn_topic_rules":["old-topic"],"cbrn_intent_rules":["old-intent"]}`)
	require.NoError(t, err)
	require.Len(t, legacy.CombinationPolicies, 1)
	require.Equal(t, "cbrn", legacy.CombinationPolicies[0].ID)
	require.True(t, legacy.CombinationPolicies[0].Enabled)
	require.Equal(t, []string{"old-topic"}, legacy.CombinationPolicies[0].TopicRules)
	deleted, err := ParseStorageConfig(`{"all_groups":true,"cbrn_blocking_enabled":true,"combination_policies":[]}`)
	require.NoError(t, err)
	require.Empty(t, deleted.CombinationPolicies)
	require.False(t, deleted.CBRNBlockingEnabled, "explicit generic policy list is authoritative")
	require.False(t, deleted.synchronousBlockingEnabled())
	public := PublicFromStorage(deleted, true, nil)
	require.Len(t, public.CombinationTemplates, 2)
	for _, template := range public.CombinationTemplates {
		require.False(t, template.Enabled)
		require.NoError(t, validateCombinationPolicies([]CombinationPolicy{template}))
	}
	require.False(t, public.CombinationBlockingActive)
	public.CombinationTemplates[0].TopicRules[0] = "modified"
	require.NotEqual(t, public.CombinationTemplates, PublicFromStorage(deleted, true, nil).CombinationTemplates)

	manager := &ConfigManager{encryptor: prefixEncryptor{}}
	request := cbrnConfigUpdateRequest()
	policies := []CombinationPolicy{testCombinationPolicy("custom")}
	request.CombinationPolicies = &policies
	enabled := true
	invalidLegacy := []string{"regex:["}
	request.CBRNBlockingEnabled, request.CBRNTopicRules = &enabled, &invalidLegacy
	next, err := manager.buildNextStorage(legacy, request, 7)
	require.NoError(t, err, "explicit generic list ignores obsolete legacy values")
	require.Equal(t, policies, next.CombinationPolicies)
	require.False(t, next.CBRNBlockingEnabled)

	request = cbrnConfigUpdateRequest()
	request.CBRNTopicRules = &legacy.CBRNTopicRules
	request.CBRNIntentRules = &legacy.CBRNIntentRules
	disabled := false
	request.CBRNBlockingEnabled = &disabled
	unchanged, err := manager.buildNextStorage(next, request, 7)
	require.NoError(t, err)
	require.Equal(t, next.CombinationPolicies, unchanged.CombinationPolicies, "ordinary legacy saves do not resurrect deleted CBRN")
	request.CBRNBlockingEnabled = &enabled
	stale, err := manager.buildNextStorage(next, request, 7)
	require.NoError(t, err)
	require.Equal(t, next.CombinationPolicies, stale.CombinationPolicies, "stale legacy enabled=true cannot recreate deleted CBRN")
	readded := append(cloneCombinationPolicies(next.CombinationPolicies), CombinationPolicy{ID: "cbrn", Name: "CBRN", Enabled: true, TopicRules: legacy.CBRNTopicRules, IntentRules: legacy.CBRNIntentRules})
	request.CombinationPolicies = &readded
	recreated, err := manager.buildNextStorage(next, request, 7)
	require.NoError(t, err)
	require.Len(t, recreated.CombinationPolicies, 2)
	require.Equal(t, next.CombinationPolicies[0], recreated.CombinationPolicies[0])
	require.Equal(t, "cbrn", recreated.CombinationPolicies[1].ID)
	require.Equal(t, legacy.CBRNTopicRules, recreated.CombinationPolicies[1].TopicRules)
	request.CombinationPolicies = nil
	request.CBRNBlockingEnabled = &disabled
	updated, err := manager.buildNextStorage(recreated, request, 7)
	require.NoError(t, err)
	require.False(t, updated.CombinationPolicies[1].Enabled)
	require.True(t, updated.CombinationPolicies[0].Enabled)

	empty := []CombinationPolicy{}
	request.CombinationPolicies = &empty
	deleted, err = manager.buildNextStorage(updated, request, 7)
	require.NoError(t, err)
	require.NotNil(t, deleted.CombinationPolicies)
	require.Empty(t, deleted.CombinationPolicies)
	raw, err := json.Marshal(deleted)
	require.NoError(t, err)
	restored, err := ParseStorageConfig(string(raw))
	require.NoError(t, err)
	require.Empty(t, restored.CombinationPolicies)
}

func TestCombinationConfigDeclarativeKeywordMigration(t *testing.T) {
	manager := &ConfigManager{encryptor: prefixEncryptor{}}
	request := cbrnConfigUpdateRequest()
	request.KeywordBlockingEnabled = true
	request.BlockedKeywords = []string{" .GoV ", ".gov.", "custom-term", "agency.gov", `regex:\.gov\.`}
	policy := testCombinationPolicy("custom-domain")
	policy.MigrateKeywords = []string{" .gov ", ".gov.", "CUSTOM-TERM"}
	policies := []CombinationPolicy{policy}
	request.CombinationPolicies = &policies
	next, err := manager.buildNextStorage(DefaultStorageConfig(), request, 7)
	require.NoError(t, err)
	require.Equal(t, []string{"agency.gov", `regex:\.gov\.`}, next.BlockedKeywords)
	require.True(t, next.KeywordBlockingEnabled)
	require.Equal(t, " .GoV ", request.BlockedKeywords[0], "request slices remain untouched")

	// An old client cannot restore retired literals while the policy is enabled.
	request.CombinationPolicies = nil
	request.BlockedKeywords = []string{".GOV", "custom-term"}
	again, err := manager.buildNextStorage(next, request, 7)
	require.NoError(t, err)
	require.Empty(t, again.BlockedKeywords)
	require.False(t, again.KeywordBlockingEnabled)
	request.KeywordBlockingEnabled, request.BlockedKeywords = false, nil
	policies[0].Enabled = false
	request.CombinationPolicies = &policies
	disabled, err := manager.buildNextStorage(again, request, 7)
	require.NoError(t, err)
	require.Empty(t, disabled.BlockedKeywords, "disabling does not restore retired literals")
	request.KeywordBlockingEnabled, request.BlockedKeywords = true, []string{".gov"}
	disabled, err = manager.buildNextStorage(disabled, request, 7)
	require.NoError(t, err)
	require.Equal(t, []string{".gov"}, disabled.BlockedKeywords)
	policies[0].Enabled = true
	policies[0].MigrateKeywords = []string{"strasse", "Σ"}
	request.BlockedKeywords = []string{" Straße ", "ς"}
	folded, err := manager.buildNextStorage(disabled, request, 7)
	require.NoError(t, err)
	require.Empty(t, folded.BlockedKeywords, "declarative migration compares Unicode casefolded literals")
}

func TestCombinationConfigValidationAndSharedRuleBudget(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*CombinationPolicy)
		reason string
	}{
		{"empty id", func(p *CombinationPolicy) { p.ID = "" }, "prompt_audit_invalid_combination_policy_id"},
		{"long id", func(p *CombinationPolicy) { p.ID = strings.Repeat("a", 65) }, "prompt_audit_invalid_combination_policy_id"},
		{"invalid id", func(p *CombinationPolicy) { p.ID = "a b" }, "prompt_audit_invalid_combination_policy_id"},
		{"empty name", func(p *CombinationPolicy) { p.Name = "" }, "prompt_audit_invalid_combination_policy_name"},
		{"long name", func(p *CombinationPolicy) { p.Name = strings.Repeat("中", 81) }, "prompt_audit_invalid_combination_policy_name"},
		{"control name", func(p *CombinationPolicy) { p.Name = "a\nb" }, "prompt_audit_invalid_combination_policy_name"},
		{"empty topics", func(p *CombinationPolicy) { p.TopicRules = nil }, "prompt_audit_combination_rules_required"},
		{"empty intents", func(p *CombinationPolicy) { p.IntentRules = nil }, "prompt_audit_combination_rules_required"},
		{"zero match", func(p *CombinationPolicy) { p.TopicRules = []string{"regex:a*"} }, "prompt_audit_combination_empty_match"},
		{"regex migration", func(p *CombinationPolicy) { p.MigrateKeywords = []string{"ReGeX:foo"} }, "prompt_audit_invalid_combination_migration_keyword"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := testCombinationPolicy("example")
			tc.mutate(&p)
			require.Equal(t, tc.reason, infraerrors.Reason(validateCombinationPolicies([]CombinationPolicy{p})))
		})
	}
	p := testCombinationPolicy("same")
	require.Equal(t, "prompt_audit_duplicate_combination_policy_id", infraerrors.Reason(validateCombinationPolicies([]CombinationPolicy{p, p})))
	p.Enabled, p.TopicRules, p.IntentRules = false, nil, nil
	require.NoError(t, validateCombinationPolicies([]CombinationPolicy{p}), "disabled draft groups may have empty sides")
	p.TopicRules = make([]string, MaxBlockedKeywords)
	for i := range p.TopicRules {
		p.TopicRules[i] = fmt.Sprintf("term-%d", i)
	}
	q := testCombinationPolicy("other")
	require.Equal(t, "prompt_audit_too_many_combination_rules", infraerrors.Reason(validateCombinationPolicies([]CombinationPolicy{p, q})))

	current := DefaultStorageConfig()
	current.KeywordAllGroups, current.KeywordGroupIDs = false, nil
	request := cbrnConfigUpdateRequest()
	policies := []CombinationPolicy{q}
	request.CombinationPolicies = &policies
	_, err := (&ConfigManager{encryptor: prefixEncryptor{}}).buildNextStorage(current, request, 7)
	require.Equal(t, "prompt_audit_keyword_groups_required", infraerrors.Reason(err))
}

func TestCombinationConfigOrderedMatchingAndImmutableSnapshots(t *testing.T) {
	storage := DefaultStorageConfig()
	storage.CombinationPolicies = []CombinationPolicy{testCombinationPolicy("first"), testCombinationPolicy("second")}
	active, err := ActiveFromStorage(storage, true, prefixEncryptor{})
	require.NoError(t, err)
	require.True(t, active.CombinationBlockingActive())
	require.False(t, active.CBRNBlockingActive())
	require.Equal(t, ModeOff, active.EffectiveMode())
	request := Request{Protocol: "responses", Body: []byte(`{"input":"target attack"}`)}
	match, err := active.MatchCombination(request)
	require.NoError(t, err)
	require.Equal(t, "first", match.PolicyID)
	require.Equal(t, "自定义 first", match.PolicyName)
	clone := cloneActiveConfig(active)
	require.Same(t, active.CombinationPolicies[0].matcher, clone.CombinationPolicies[0].matcher)
	clone.CombinationPolicies[0].TopicRules[0] = "changed"
	require.Equal(t, "target", active.CombinationPolicies[0].TopicRules[0])
	clone.CombinationPolicies[0].Enabled = false
	match, err = clone.MatchCombination(request)
	require.NoError(t, err)
	require.Equal(t, "second", match.PolicyID)
	clone.RiskControlEnabled = false
	require.False(t, clone.CombinationBlockingActive())
	match, err = clone.MatchCombination(request)
	require.NoError(t, err)
	require.Nil(t, match)
	storedClone := cloneStorageConfig(storage)
	storedClone.CombinationPolicies[0].IntentRules[0] = "changed"
	require.Equal(t, "attack", storage.CombinationPolicies[0].IntentRules[0])
	public := PublicFromStorage(storage, true, nil)
	require.True(t, public.CombinationBlockingActive)
	public.CombinationPolicies[0].TopicRules[0] = "changed"
	require.Equal(t, "target", storage.CombinationPolicies[0].TopicRules[0])

	cold := &ConfigManager{}
	cold.observeExpectedState(`{"combination_policies":[{"id":"custom","enabled":true}],"config_version":3}`, true)
	require.True(t, cold.BlockingActivationDegraded())
	cold.observeExpectedState(`{"cbrn_blocking_enabled":true,"combination_policies":[],"config_version":3}`, true)
	require.False(t, cold.BlockingActivationDegraded(), "explicit empty list overrides obsolete CBRN intent")
}

func TestCombinationConfigLazyPreparedMatching(t *testing.T) {
	emptyMatcher, err := newPromptCombinationMatcher(nil, nil)
	require.NoError(t, err)
	active := ActiveConfig{RiskControlEnabled: true, CombinationPolicies: []ActiveCombinationPolicy{
		{CombinationPolicy: CombinationPolicy{ID: "disabled", Enabled: false}},
		{CombinationPolicy: CombinationPolicy{ID: "missing-matcher", Enabled: true}},
		{CombinationPolicy: CombinationPolicy{ID: "empty", Enabled: true}, matcher: emptyMatcher},
	}}
	invalid := Request{Protocol: "responses", Body: []byte(`{"input":`)}
	match, err := active.MatchCombination(invalid)
	require.NoError(t, err, "no enabled rules means no JSON preparation")
	require.Nil(t, match)

	first := testCombinationPolicy("first")
	first.TopicRules = []string{"different-target"}
	second, third := testCombinationPolicy("second"), testCombinationPolicy("third")
	storage := DefaultStorageConfig()
	storage.CombinationPolicies = []CombinationPolicy{first, second, third}
	preparedConfig, err := ActiveFromStorage(storage, true, prefixEncryptor{})
	require.NoError(t, err)
	active.CombinationPolicies = append(active.CombinationPolicies, preparedConfig.CombinationPolicies...)
	match, err = active.MatchCombination(invalid)
	require.EqualError(t, err, "prompt audit request JSON is invalid")
	require.Nil(t, match)
	match, err = active.MatchCombination(Request{Protocol: "responses", Body: []byte(`{"input":"ｔａｒｇｅｔ ａｔｔａｃｋ"}`)})
	require.NoError(t, err)
	require.Equal(t, "second", match.PolicyID, "first matching policy wins after earlier policy misses")
	require.Equal(t, "ｔａｒｇｅｔ", match.Topic.Content)
	require.Equal(t, "ａｔｔａｃｋ", match.Intent.Content)
	match, err = active.MatchCombination(Request{Protocol: "responses", Body: []byte(`{"input":"no matching intent"}`)})
	require.NoError(t, err)
	require.Nil(t, match, "prepared text must not survive across requests")
}
