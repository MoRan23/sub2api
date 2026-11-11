package securityaudit

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPromptConfigReadOnlySnapshotAndPublicOwnership(t *testing.T) {
	policy := testCombinationPolicy("first")
	policy.KeywordWhitelistKeys = []KeywordWhitelistKey{{UserID: 1, APIKeyID: 11}}
	stored := combinationWhitelistStorage(t, policy)
	active, err := ActiveFromStorage(stored, true, prefixEncryptor{})
	require.NoError(t, err)
	manager := &ConfigManager{}
	manager.snapshot.Store(&activeConfigSnapshot{storage: stored, active: active})
	borrowed, ok := readOnlyActiveConfig(manager)
	require.True(t, ok)
	require.Same(t, &active.CombinationPolicies[0], &borrowed.CombinationPolicies[0])
	require.Same(t, &active.CombinationPolicies[0].KeywordWhitelistKeys[0], &borrowed.CombinationPolicies[0].KeywordWhitelistKeys[0])
	owned, ok := manager.Active()
	require.True(t, ok)
	owned.CombinationPolicies[0].Name = "changed by consumer"
	owned.CombinationPolicies[0].KeywordWhitelistKeys[0].APIKeyID = 99
	owned.CombinationPolicies[0].TopicRules[0] = "changed topic"
	owned.Scanners[0] = "changed scanner"
	again, ok := readOnlyActiveConfig(manager)
	require.True(t, ok)
	require.Equal(t, policy.Name, again.CombinationPolicies[0].Name)
	require.Equal(t, policy.KeywordWhitelistKeys, again.CombinationPolicies[0].KeywordWhitelistKeys)
	require.Equal(t, policy.TopicRules, again.CombinationPolicies[0].TopicRules)
	require.Equal(t, active.Scanners, again.Scanners)
	match, err := again.MatchCombination(combinationWhitelistRequest(1, 11))
	require.NoError(t, err)
	require.Nil(t, match)

	// Injected/legacy ConfigStore implementations retain their original API.
	fallback, ok := readOnlyActiveConfig(&fakeConfigStore{cfg: active, active: true})
	require.True(t, ok)
	require.Equal(t, active.ConfigVersion, fallback.ConfigVersion)
	_, ok = readOnlyActiveConfig(nil)
	require.False(t, ok)
	_, ok = readOnlyActiveConfig((*ConfigManager)(nil))
	require.False(t, ok)
}

type configMutatingScanner struct{}

func (configMutatingScanner) Scan(_ context.Context, _ ActiveEndpoint, _ string, scanners []string) (*NormalizedResult, error) {
	if len(scanners) > 0 {
		scanners[0] = "scanner-side mutation"
	}
	return &NormalizedResult{Action: ActionAllow}, nil
}

func TestPromptConfigReadOnlyScannerBoundary(t *testing.T) {
	for _, async := range []bool{false, true} {
		t.Run(map[bool]string{false: "blocking", true: "async"}[async], func(t *testing.T) {
			scanners := []string{"prompt_injection"}
			var err error
			if async {
				_, err = scanWithFailover(context.Background(), configMutatingScanner{}, scanners, []ActiveEndpoint{{ID: "guard"}}, "hello", nil)
			} else {
				_, err = callPromptScanner(context.Background(), configMutatingScanner{}, ActiveEndpoint{ID: "guard"}, "hello", scanners)
			}
			require.NoError(t, err)
			require.Equal(t, []string{"prompt_injection"}, scanners)
		})
	}
}

func TestCombinationWhitelistAddedPairsAreScopedToPolicy(t *testing.T) {
	first, second := testCombinationPolicy("first"), testCombinationPolicy("second")
	first.KeywordWhitelistKeys = []KeywordWhitelistKey{{UserID: 1, APIKeyID: 11}, {UserID: 1, APIKeyID: 12}}
	second.KeywordWhitelistKeys = []KeywordWhitelistKey{{UserID: 2, APIKeyID: 21}}
	current := []CombinationPolicy{first, second}
	next := cloneCombinationPolicies(current)
	next[0].KeywordWhitelistKeys = []KeywordWhitelistKey{{UserID: 1, APIKeyID: 11}, {UserID: 2, APIKeyID: 21}}
	next[1].KeywordWhitelistKeys = nil
	require.Equal(t, []KeywordWhitelistKey{{UserID: 2, APIKeyID: 21}}, addedCombinationWhitelistKeys(current, next))
	require.Empty(t, addedCombinationWhitelistKeys(current, current))
	next[0].ID = "new-policy"
	require.Equal(t, next[0].KeywordWhitelistKeys, addedCombinationWhitelistKeys(current, next))
}
