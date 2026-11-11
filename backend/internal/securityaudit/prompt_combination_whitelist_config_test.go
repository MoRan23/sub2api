package securityaudit

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

func combinationWhitelistStorage(t *testing.T, policies ...CombinationPolicy) storageConfig {
	t.Helper()
	cfg := DefaultStorageConfig()
	cfg.CombinationPolicies = policies
	raw, err := json.Marshal(cfg)
	require.NoError(t, err)
	cfg, err = ParseStorageConfig(string(raw))
	require.NoError(t, err)
	return cfg
}

func combinationWhitelistRequest(userID, apiKeyID int64) Request {
	return Request{UserID: userID, APIKeyID: apiKeyID, Protocol: "responses", Body: []byte(`{"input":"attack target"}`)}
}

func TestCombinationWhitelistConfigNormalizesAndPublishesPairs(t *testing.T) {
	policy := testCombinationPolicy("garden")
	policy.KeywordWhitelistKeys = []KeywordWhitelistKey{
		{UserID: 2, APIKeyID: 21}, {UserID: 1, APIKeyID: 12},
		{UserID: 1, APIKeyID: 11}, {UserID: 1, APIKeyID: 12},
	}
	want := []KeywordWhitelistKey{{UserID: 1, APIKeyID: 11}, {UserID: 1, APIKeyID: 12}, {UserID: 2, APIKeyID: 21}}
	stored := combinationWhitelistStorage(t, policy, testCombinationPolicy("other"))
	require.Equal(t, want, stored.CombinationPolicies[0].KeywordWhitelistKeys)
	require.Len(t, policy.KeywordWhitelistKeys, 4, "normalization must not change caller-owned slices")
	public := PublicFromStorage(stored, true, nil)
	require.Equal(t, want, public.CombinationPolicies[0].KeywordWhitelistKeys)
	raw, err := json.Marshal(public)
	require.NoError(t, err)
	var response struct {
		Policies []map[string]json.RawMessage `json:"combination_policies"`
	}
	require.NoError(t, json.Unmarshal(raw, &response))
	require.Len(t, response.Policies, 2)
	encodedPairs, exists := response.Policies[0]["keyword_whitelist_keys"]
	require.True(t, exists)
	var pairs []KeywordWhitelistKey
	require.NoError(t, json.Unmarshal(encodedPairs, &pairs))
	require.Equal(t, want, pairs)
	require.JSONEq(t, `[]`, string(response.Policies[1]["keyword_whitelist_keys"]))

	active, err := ActiveFromStorage(stored, true, prefixEncryptor{})
	require.NoError(t, err)
	// An exemption skips only its own group; the next matching group still runs.
	match, err := active.MatchCombination(combinationWhitelistRequest(1, 11))
	require.NoError(t, err)
	require.NotNil(t, match)
	require.Equal(t, "other", match.PolicyID)
	for _, pair := range []KeywordWhitelistKey{{UserID: 1, APIKeyID: 13}, {UserID: 2, APIKeyID: 11}, {UserID: 0, APIKeyID: 11}, {UserID: 1, APIKeyID: 0}} {
		match, err := active.MatchCombination(combinationWhitelistRequest(pair.UserID, pair.APIKeyID))
		require.NoError(t, err)
		require.NotNil(t, match)
		require.Equal(t, "garden", match.PolicyID, "only an exact authenticated user/key pair is exempt")
	}
}

func TestCombinationWhitelistUpdatePreservesByTrimmedPolicyID(t *testing.T) {
	first, second := testCombinationPolicy("first"), testCombinationPolicy("second")
	first.KeywordWhitelistKeys = []KeywordWhitelistKey{{UserID: 1, APIKeyID: 11}}
	second.KeywordWhitelistKeys = []KeywordWhitelistKey{{UserID: 2, APIKeyID: 22}}
	current := combinationWhitelistStorage(t, first, second)
	manager := &ConfigManager{encryptor: prefixEncryptor{}}
	for _, field := range []string{"", `,"keyword_whitelist_keys":null`} {
		t.Run(fmt.Sprintf("field=%q", field), func(t *testing.T) {
			req := cbrnConfigUpdateRequest()
			// Reorder groups and pad their IDs to catch index/name-based inheritance.
			raw := fmt.Sprintf(`{"combination_policies":[{"id":" second ","name":"renamed second","enabled":true,"topic_rules":["target"],"intent_rules":["attack"]%s},{"id":" first ","name":"renamed first","enabled":true,"topic_rules":["target"],"intent_rules":["attack"]%s},{"id":"new","name":"renamed first","enabled":true,"topic_rules":["target"],"intent_rules":["attack"]%s}]}`, field, field, field)
			require.NoError(t, json.Unmarshal([]byte(raw), &req))
			next, err := manager.buildNextStorage(current, req, 7)
			require.NoError(t, err)
			require.Equal(t, "second", next.CombinationPolicies[0].ID)
			require.Equal(t, second.KeywordWhitelistKeys, next.CombinationPolicies[0].KeywordWhitelistKeys)
			require.Equal(t, first.KeywordWhitelistKeys, next.CombinationPolicies[1].KeywordWhitelistKeys)
			require.Empty(t, next.CombinationPolicies[2].KeywordWhitelistKeys, "a new ID must not inherit by name or position")
			require.Nil(t, (*req.CombinationPolicies)[0].KeywordWhitelistKeys, "merging must not change the request")
			next.CombinationPolicies[0].KeywordWhitelistKeys[0].APIKeyID = 999
			require.Equal(t, second.KeywordWhitelistKeys, current.CombinationPolicies[1].KeywordWhitelistKeys)
		})
	}

	req := cbrnConfigUpdateRequest()
	updated := cloneCombinationPolicies(current.CombinationPolicies)
	updated[0].KeywordWhitelistKeys = []KeywordWhitelistKey{}
	updated[1].KeywordWhitelistKeys = nil
	req.CombinationPolicies = &updated
	cleared, err := manager.buildNextStorage(current, req, 7)
	require.NoError(t, err)
	require.NotNil(t, cleared.CombinationPolicies[0].KeywordWhitelistKeys)
	require.Empty(t, cleared.CombinationPolicies[0].KeywordWhitelistKeys)
	require.Equal(t, second.KeywordWhitelistKeys, cleared.CombinationPolicies[1].KeywordWhitelistKeys)
	storedJSON, err := json.Marshal(cleared)
	require.NoError(t, err)
	reloaded, err := ParseStorageConfig(string(storedJSON))
	require.NoError(t, err)
	require.Empty(t, reloaded.CombinationPolicies[0].KeywordWhitelistKeys)
	// Reintroducing a previously deleted ID starts with an empty whitelist.
	remaining := []CombinationPolicy{second}
	req.CombinationPolicies = &remaining
	deleted, err := manager.buildNextStorage(current, req, 7)
	require.NoError(t, err)
	remaining = []CombinationPolicy{testCombinationPolicy("first"), second}
	recreated, err := manager.buildNextStorage(deleted, req, 7)
	require.NoError(t, err)
	require.Empty(t, recreated.CombinationPolicies[0].KeywordWhitelistKeys)
}

func TestCombinationWhitelistLegacyCBRNUpdatePreservesPairs(t *testing.T) {
	policy := testCombinationPolicy("cbrn")
	policy.KeywordWhitelistKeys = []KeywordWhitelistKey{{UserID: 1, APIKeyID: 11}}
	other := testCombinationPolicy("custom")
	other.KeywordWhitelistKeys = []KeywordWhitelistKey{{UserID: 2, APIKeyID: 22}}
	current := combinationWhitelistStorage(t, policy, other)
	manager := &ConfigManager{encryptor: prefixEncryptor{}}
	for _, enabled := range []bool{false, true} {
		req := cbrnConfigUpdateRequest()
		topics, intents := []string{"new target"}, []string{"new intent"}
		req.CBRNBlockingEnabled, req.CBRNTopicRules, req.CBRNIntentRules = &enabled, &topics, &intents
		next, err := manager.buildNextStorage(current, req, 7)
		require.NoError(t, err)
		require.Equal(t, enabled, next.CombinationPolicies[0].Enabled)
		require.Equal(t, topics, next.CombinationPolicies[0].TopicRules)
		require.Equal(t, intents, next.CombinationPolicies[0].IntentRules)
		require.Equal(t, policy.KeywordWhitelistKeys, next.CombinationPolicies[0].KeywordWhitelistKeys)
		require.Equal(t, other.KeywordWhitelistKeys, next.CombinationPolicies[1].KeywordWhitelistKeys)
	}
}

func TestCombinationWhitelistRejectsInvalidPairsAndBoundsEachList(t *testing.T) {
	manager := &ConfigManager{encryptor: prefixEncryptor{}}
	for _, pair := range []KeywordWhitelistKey{{UserID: 0, APIKeyID: 1}, {UserID: 1, APIKeyID: 0}, {UserID: -1, APIKeyID: 1}, {UserID: 1, APIKeyID: -1}} {
		t.Run(fmt.Sprintf("user=%d/key=%d", pair.UserID, pair.APIKeyID), func(t *testing.T) {
			policy := testCombinationPolicy("invalid")
			policy.KeywordWhitelistKeys = []KeywordWhitelistKey{pair}
			policies := []CombinationPolicy{policy}
			req := cbrnConfigUpdateRequest()
			req.CombinationPolicies = &policies
			_, err := manager.buildNextStorage(DefaultStorageConfig(), req, 7)
			require.Equal(t, "prompt_audit_invalid_keyword_whitelist_key", infraerrors.Reason(err))
			cfg := DefaultStorageConfig()
			cfg.CombinationPolicies = policies
			raw, err := json.Marshal(cfg)
			require.NoError(t, err)
			_, err = ParseStorageConfig(string(raw))
			require.Equal(t, "prompt_audit_invalid_keyword_whitelist_key", infraerrors.Reason(err))
			_, err = ActiveFromStorage(cfg, true, prefixEncryptor{})
			require.Equal(t, "prompt_audit_invalid_keyword_whitelist_key", infraerrors.Reason(err))
		})
	}

	first, second := testCombinationPolicy("first"), testCombinationPolicy("second")
	first.KeywordWhitelistKeys = make([]KeywordWhitelistKey, MaxKeywordWhitelistKeys)
	second.KeywordWhitelistKeys = make([]KeywordWhitelistKey, MaxKeywordWhitelistKeys)
	for index := range MaxKeywordWhitelistKeys {
		first.KeywordWhitelistKeys[index] = KeywordWhitelistKey{UserID: 1, APIKeyID: int64(index + 1)}
		second.KeywordWhitelistKeys[index] = KeywordWhitelistKey{UserID: 2, APIKeyID: int64(index + 1)}
	}
	policies := []CombinationPolicy{first, second}
	req := cbrnConfigUpdateRequest()
	req.CombinationPolicies = &policies
	next, err := manager.buildNextStorage(DefaultStorageConfig(), req, 7)
	require.NoError(t, err, "each group has its own 1000-key limit")
	require.Len(t, next.CombinationPolicies[0].KeywordWhitelistKeys, MaxKeywordWhitelistKeys)
	require.Len(t, next.CombinationPolicies[1].KeywordWhitelistKeys, MaxKeywordWhitelistKeys)
	policies[0].KeywordWhitelistKeys = append(policies[0].KeywordWhitelistKeys, policies[0].KeywordWhitelistKeys[0])
	next, err = manager.buildNextStorage(DefaultStorageConfig(), req, 7)
	require.NoError(t, err, "duplicates collapse before the effective-list limit")
	require.Len(t, next.CombinationPolicies[0].KeywordWhitelistKeys, MaxKeywordWhitelistKeys)
	policies[0].KeywordWhitelistKeys[len(policies[0].KeywordWhitelistKeys)-1] = KeywordWhitelistKey{UserID: 1, APIKeyID: MaxKeywordWhitelistKeys + 1}
	_, err = manager.buildNextStorage(DefaultStorageConfig(), req, 7)
	require.Equal(t, "prompt_audit_too_many_keyword_whitelist_keys", infraerrors.Reason(err))
	cfg := DefaultStorageConfig()
	cfg.CombinationPolicies = policies
	raw, err := json.Marshal(cfg)
	require.NoError(t, err)
	_, err = ParseStorageConfig(string(raw))
	require.Equal(t, "prompt_audit_too_many_keyword_whitelist_keys", infraerrors.Reason(err))
}

func TestCombinationWhitelistBoundsAggregateAfterNormalizationAndMerge(t *testing.T) {
	makePolicies := func(count int) []CombinationPolicy {
		policies := make([]CombinationPolicy, count)
		for index := range policies {
			policies[index] = testCombinationPolicy(fmt.Sprintf("group-%d", index+1))
			for keyID := range MaxKeywordWhitelistKeys {
				// Each group deliberately uses the same pairs: exemptions are counted
				// separately for every policy, rather than deduplicated globally.
				policies[index].KeywordWhitelistKeys = append(policies[index].KeywordWhitelistKeys, KeywordWhitelistKey{UserID: 1, APIKeyID: int64(keyID + 1)})
			}
			// Repeated entries within a group collapse before either limit applies.
			policies[index].KeywordWhitelistKeys = append(policies[index].KeywordWhitelistKeys, policies[index].KeywordWhitelistKeys[0])
		}
		return policies
	}
	manager := &ConfigManager{encryptor: prefixEncryptor{}}
	const aggregateReason = "prompt_audit_too_many_combination_whitelist_keys"
	for _, count := range []int{10, 11} {
		t.Run(fmt.Sprintf("groups=%d", count), func(t *testing.T) {
			cfg := DefaultStorageConfig()
			cfg.CombinationPolicies = makePolicies(count)
			raw, err := json.Marshal(cfg)
			require.NoError(t, err)
			parsed, parseErr := ParseStorageConfig(string(raw))
			req := cbrnConfigUpdateRequest()
			req.CombinationPolicies = &cfg.CombinationPolicies
			next, updateErr := manager.buildNextStorage(DefaultStorageConfig(), req, 7)
			active, activeErr := ActiveFromStorage(cfg, true, prefixEncryptor{})
			if count == 11 {
				require.Equal(t, aggregateReason, infraerrors.Reason(parseErr))
				require.Equal(t, aggregateReason, infraerrors.Reason(updateErr))
				require.Equal(t, aggregateReason, infraerrors.Reason(activeErr))
				// Omitting all policies on update must still validate retained state.
				_, err = manager.buildNextStorage(cfg, cbrnConfigUpdateRequest(), 7)
				require.Equal(t, aggregateReason, infraerrors.Reason(err))
				return
			}
			require.NoError(t, parseErr)
			require.NoError(t, updateErr)
			require.NoError(t, activeErr)
			require.Len(t, parsed.CombinationPolicies, 10)
			require.Len(t, next.CombinationPolicies, 10)
			require.Len(t, active.CombinationPolicies, 10)
			for index := range parsed.CombinationPolicies {
				require.Len(t, parsed.CombinationPolicies[index].KeywordWhitelistKeys, MaxKeywordWhitelistKeys)
				require.Len(t, next.CombinationPolicies[index].KeywordWhitelistKeys, MaxKeywordWhitelistKeys)
				require.Len(t, active.CombinationPolicies[index].KeywordWhitelistKeys, MaxKeywordWhitelistKeys)
			}
		})
	}

	current := combinationWhitelistStorage(t, makePolicies(10)...)
	updated := cloneCombinationPolicies(current.CombinationPolicies)
	for index := range updated {
		updated[index].ID = " " + updated[index].ID + " "
		updated[index].KeywordWhitelistKeys = nil
	}
	extra := makePolicies(1)[0]
	extra.ID = "new-group"
	updated = append(updated, extra)
	req := cbrnConfigUpdateRequest()
	req.CombinationPolicies = &updated
	_, err := manager.buildNextStorage(current, req, 7)
	require.Equal(t, aggregateReason, infraerrors.Reason(err), "retained lists must be counted after ID-based merging")
	for index := range current.CombinationPolicies {
		require.Len(t, current.CombinationPolicies[index].KeywordWhitelistKeys, MaxKeywordWhitelistKeys)
		require.Nil(t, updated[index].KeywordWhitelistKeys, "merging must not mutate the caller's omitted lists")
	}

	updated[0].KeywordWhitelistKeys = []KeywordWhitelistKey{}
	next, err := manager.buildNextStorage(current, req, 7)
	require.NoError(t, err, "clearing one saved list makes room for a new 1000-key group")
	require.Empty(t, next.CombinationPolicies[0].KeywordWhitelistKeys)
	for _, policy := range next.CombinationPolicies[1:] {
		require.Len(t, policy.KeywordWhitelistKeys, MaxKeywordWhitelistKeys)
	}
}

func TestCombinationWhitelistClonesIsolateListsAndShareImmutableLookup(t *testing.T) {
	policy := testCombinationPolicy("garden")
	policy.KeywordWhitelistKeys = []KeywordWhitelistKey{{UserID: 1, APIKeyID: 11}}
	stored := combinationWhitelistStorage(t, policy)
	storageClone := cloneStorageConfig(stored)
	storageClone.CombinationPolicies[0].KeywordWhitelistKeys[0] = KeywordWhitelistKey{UserID: 2, APIKeyID: 22}
	public := PublicFromStorage(stored, true, nil)
	public.CombinationPolicies[0].KeywordWhitelistKeys[0] = KeywordWhitelistKey{UserID: 3, APIKeyID: 33}
	require.Equal(t, policy.KeywordWhitelistKeys, stored.CombinationPolicies[0].KeywordWhitelistKeys)
	active, err := ActiveFromStorage(stored, true, prefixEncryptor{})
	require.NoError(t, err)
	clone := cloneActiveConfig(active)
	require.NotEmpty(t, active.CombinationPolicies[0].whitelistKeys)
	require.Equal(t, fmt.Sprintf("%p", active.CombinationPolicies[0].whitelistKeys), fmt.Sprintf("%p", clone.CombinationPolicies[0].whitelistKeys), "compiled lookups are shared rather than rebuilt per request")
	clone.CombinationPolicies[0].KeywordWhitelistKeys[0] = KeywordWhitelistKey{UserID: 4, APIKeyID: 44}
	require.Equal(t, policy.KeywordWhitelistKeys, active.CombinationPolicies[0].KeywordWhitelistKeys)
	active.CombinationPolicies[0].KeywordWhitelistKeys[0] = KeywordWhitelistKey{UserID: 5, APIKeyID: 55}
	stored.CombinationPolicies[0].KeywordWhitelistKeys[0] = KeywordWhitelistKey{UserID: 6, APIKeyID: 66}
	for _, cfg := range []ActiveConfig{active, clone} {
		match, err := cfg.MatchCombination(combinationWhitelistRequest(1, 11))
		require.NoError(t, err)
		require.Nil(t, match, "compiled exemptions survive mutations of detached presentation lists")
		for _, pair := range []KeywordWhitelistKey{{UserID: 4, APIKeyID: 44}, {UserID: 5, APIKeyID: 55}, {UserID: 6, APIKeyID: 66}} {
			match, err := cfg.MatchCombination(combinationWhitelistRequest(pair.UserID, pair.APIKeyID))
			require.NoError(t, err)
			require.NotNil(t, match, "mutating a list cannot grant a new compiled exemption")
		}
	}
	for _, values := range [][]KeywordWhitelistKey{nil, {}} {
		cloned := cloneCombinationPolicy(CombinationPolicy{KeywordWhitelistKeys: values})
		require.Equal(t, values == nil, cloned.KeywordWhitelistKeys == nil, "clone must preserve omitted versus explicit clear")
	}
}

func TestCombinationWhitelistHotReloadPreservesPreviousSnapshot(t *testing.T) {
	settings := staticSettingRepository{values: map[string]string{
		SettingKeyRiskControl:       "true",
		SettingKeyPromptAuditConfig: `{"config_version":2,"combination_policies":[{"id":"garden","name":"garden","enabled":true,"topic_rules":["target"],"intent_rules":["attack"],"keyword_whitelist_keys":[{"user_id":1,"api_key_id":11}]}]}`,
	}}
	manager := &ConfigManager{settings: settings, encryptor: prefixEncryptor{}, clock: fixedClock{}}
	require.NoError(t, manager.Reload(context.Background()))
	before, ok := manager.Active()
	require.True(t, ok)
	settings.values[SettingKeyPromptAuditConfig] = `{"config_version":3,"combination_policies":[{"id":"garden","name":"garden","enabled":true,"topic_rules":["target"],"intent_rules":["attack"],"keyword_whitelist_keys":[{"user_id":2,"api_key_id":22}]}]}`
	require.NoError(t, manager.Reload(context.Background()))
	after, ok := manager.Active()
	require.True(t, ok)
	require.EqualValues(t, 2, before.ConfigVersion)
	require.EqualValues(t, 3, after.ConfigVersion)
	for _, tc := range []struct {
		cfg             ActiveConfig
		exempt, blocked KeywordWhitelistKey
	}{
		{before, KeywordWhitelistKey{UserID: 1, APIKeyID: 11}, KeywordWhitelistKey{UserID: 2, APIKeyID: 22}},
		{after, KeywordWhitelistKey{UserID: 2, APIKeyID: 22}, KeywordWhitelistKey{UserID: 1, APIKeyID: 11}},
	} {
		match, err := tc.cfg.MatchCombination(combinationWhitelistRequest(tc.exempt.UserID, tc.exempt.APIKeyID))
		require.NoError(t, err)
		require.Nil(t, match)
		match, err = tc.cfg.MatchCombination(combinationWhitelistRequest(tc.blocked.UserID, tc.blocked.APIKeyID))
		require.NoError(t, err)
		require.NotNil(t, match)
	}
	after.CombinationPolicies[0].KeywordWhitelistKeys[0] = KeywordWhitelistKey{UserID: 3, APIKeyID: 33}
	fresh, ok := manager.Active()
	require.True(t, ok)
	require.Equal(t, []KeywordWhitelistKey{{UserID: 2, APIKeyID: 22}}, fresh.CombinationPolicies[0].KeywordWhitelistKeys)
	match, err := fresh.MatchCombination(combinationWhitelistRequest(2, 22))
	require.NoError(t, err)
	require.Nil(t, match)
}
