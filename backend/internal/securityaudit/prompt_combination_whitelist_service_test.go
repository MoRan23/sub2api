package securityaudit

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCombinationWhitelistServicePreservesOtherStages(t *testing.T) {
	pair := KeywordWhitelistKey{UserID: 1, APIKeyID: 11}
	for _, tc := range []struct {
		name              string
		userID, keyID     int64
		second            bool
		secondWhitelisted bool
		global            bool
		other, jailbreak  bool
		wantPolicy        string
		wantCode          string
		wantGuard         int64
	}{
		{name: "only matching group skipped", userID: 1, keyID: 11, second: true, wantPolicy: "second", wantCode: ErrorCodeCombinationBlocked},
		{name: "same user other key", userID: 1, keyID: 12, wantPolicy: "first", wantCode: ErrorCodeCombinationBlocked},
		{name: "different owner", userID: 2, keyID: 11, wantPolicy: "first", wantCode: ErrorCodeCombinationBlocked},
		{name: "missing identity", wantPolicy: "first", wantCode: ErrorCodeCombinationBlocked},
		{name: "all combinations skipped still calls Guard", userID: 1, keyID: 11, second: true, secondWhitelisted: true, wantCode: ErrorCodeBlocked, wantGuard: 1},
		{name: "other keyword still blocks", userID: 1, keyID: 11, other: true, wantCode: ErrorCodeKeywordBlocked},
		{name: "jailbreak still blocks first", userID: 1, keyID: 11, jailbreak: true, wantCode: ErrorCodeKeywordBlocked},
		{name: "global exemption precedes all local rules", userID: 1, keyID: 11, global: true, second: true, other: true, jailbreak: true, wantCode: ErrorCodeBlocked, wantGuard: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := DefaultStorageConfig()
			cfg.Enabled, cfg.BlockingEnabled, cfg.AllGroups, cfg.KeywordAllGroups = true, true, true, true
			first := testCombinationPolicy("first")
			first.KeywordWhitelistKeys = []KeywordWhitelistKey{pair}
			cfg.CombinationPolicies = []CombinationPolicy{first}
			if tc.second {
				second := testCombinationPolicy("second")
				if tc.secondWhitelisted {
					second.KeywordWhitelistKeys = []KeywordWhitelistKey{pair}
				}
				cfg.CombinationPolicies = append(cfg.CombinationPolicies, second)
			}
			if tc.other {
				cfg.KeywordBlockingEnabled, cfg.OtherKeywords = true, []string{"ordinary"}
			}
			if tc.jailbreak {
				cfg.KeywordBlockingEnabled, cfg.JailbreakKeywords = true, []string{"jailbreak"}
			}
			if tc.global {
				cfg.KeywordWhitelistKeys = []KeywordWhitelistKey{pair}
			}
			active, err := ActiveFromStorage(cfg, true, prefixEncryptor{})
			require.NoError(t, err)
			guard := &keywordWhitelistGuard{PromptService: &PromptService{config: &fakeConfigStore{cfg: active, active: true}}}
			legacy := &fakeLegacyEngine{}
			decision := NewCoordinator(legacy, guard).Check(context.Background(), Request{
				UserID: tc.userID, APIKeyID: tc.keyID, Protocol: "responses",
				Body: []byte(`{"input":"target attack ordinary jailbreak","user_id":1,"api_key_id":11}`),
			})
			require.Equal(t, tc.wantCode, decision.ErrorCode)
			require.Equal(t, tc.wantGuard, guard.calls.Load())
			require.Equal(t, tc.wantGuard, legacy.calls.Load())
			if tc.wantPolicy != "" {
				require.Equal(t, tc.wantPolicy, decision.Prompt.Result.combinationMatch.PolicyID)
			}
		})
	}
}

func TestCombinationWhitelistAllExemptSkipsBodyPreparation(t *testing.T) {
	cfg := DefaultStorageConfig()
	cfg.KeywordAllGroups = true
	policy := testCombinationPolicy("only")
	policy.KeywordWhitelistKeys = []KeywordWhitelistKey{{UserID: 1, APIKeyID: 11}}
	cfg.CombinationPolicies = []CombinationPolicy{policy}
	active, err := ActiveFromStorage(cfg, true, prefixEncryptor{})
	require.NoError(t, err)
	svc := &PromptService{config: &fakeConfigStore{cfg: active, active: true}}
	req := Request{UserID: 1, APIKeyID: 11, Protocol: "responses", Body: []byte(`{"input":`)}
	decision, err := svc.CheckKeyword(context.Background(), req)
	require.NoError(t, err, "an exempt rule must not decode the request body")
	require.Equal(t, DecisionAllow, decision.Kind)
	require.True(t, decision.AllowNextStage)
	match, err := active.MatchCombination(req)
	require.NoError(t, err)
	require.Nil(t, match)
	req.APIKeyID = 12
	_, err = svc.CheckKeyword(context.Background(), req)
	require.Error(t, err, "other keys retain the normal malformed-body behavior")
}
