package securityaudit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

func TestPromptKeywordMatcherPreservesMixedRuleOrderAndCase(t *testing.T) {
	for _, tc := range []struct {
		name, text, want string
		rules            []string
	}{
		{"literal priority", "attack example.gov.cn then SECRET", "secret", []string{"secret", `regex:(?i)attack [a-z.]+\.gov\.cn`}},
		{"regex priority", "SECRET then attack example.gov.cn", `regex:(?i)attack [a-z.]+\.gov\.cn`, []string{`regex:(?i)attack [a-z.]+\.gov\.cn`, "secret"}},
		{"failed regex then literal", "SECRET", "secret", []string{`regex:attack`, "secret"}},
		{"regex case sensitive", "ATTACK", "", []string{`regex:attack`}},
		{"regex inline case flag", "ATTACK", `regex:(?i)attack`, []string{`regex:(?i)attack`}},
		{"regex classes retain case", "123", `regex:\d+`, []string{`regex:\D+`, `regex:\d+`}},
		{"unicode regex", "攻击政务网站", `regex:攻击.{0,4}政务`, []string{`regex:攻击.{0,4}政务`}},
		{"literal metacharacters stay literal", "govXcn", "", []string{`.gov.`}},
		{"literal case unchanged", "HELLO", "hello", []string{"hello"}},
		{"prefix is explicit", "ReGeX:attack", "REGEX:attack", []string{"REGEX:attack"}},
		{"empty input", "", "", []string{`regex:.*`}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			matcher, err := newPromptKeywordMatcher(tc.rules)
			require.NoError(t, err)
			got, hit := matcher.Match(tc.text)
			require.Equal(t, tc.want, got)
			require.Equal(t, tc.want != "", hit)
		})
	}
}

func TestPromptRegexCanonicalizationAndHashPreserveSemantics(t *testing.T) {
	rules := canonicalBlockedKeywords([]string{" Word ", "WORD", `regex:\d+`, `regex:\D+`, `regex:\d+`, "REGEX:attack", "regex:attack", "regex:Attack"})
	require.Equal(t, []string{"Word", `regex:\d+`, `regex:\D+`, "REGEX:attack", "regex:attack", "regex:Attack"}, rules)
	require.NotEqual(t, blockedKeywordsHash([]string{`regex:\d+`}), blockedKeywordsHash([]string{`regex:\D+`}))
	require.NotEqual(t, promptKeywordEvidenceText(`regex:\d+`), promptKeywordEvidenceText(`regex:\D+`))
	require.Equal(t, promptKeywordEvidenceText("WORD"), promptKeywordEvidenceText("word"))
}

func TestPromptRegexValidationAtSaveAndLoad(t *testing.T) {
	for _, rule := range []string{"regex:", "regex:  ", "regex:[", `regex:(?=secret)`, `regex:(a)\1`} {
		t.Run(rule, func(t *testing.T) {
			request := promptAuditUpdateRequest(1, 1, "")
			request.BlockedKeywords = []string{rule}
			manager := &ConfigManager{encryptor: prefixEncryptor{}}
			_, err := manager.buildNextStorage(DefaultStorageConfig(), request, 1)
			require.Equal(t, "prompt_audit_invalid_keyword_regex", infraerrors.Reason(err))
			storage := DefaultStorageConfig()
			storage.BlockedKeywords = []string{rule}
			raw, err := json.Marshal(storage)
			require.NoError(t, err)
			_, err = ParseStorageConfig(string(raw))
			require.Equal(t, "prompt_audit_invalid_keyword_regex", infraerrors.Reason(err))
			_, err = ActiveFromStorage(storage, true, prefixEncryptor{})
			require.Equal(t, "prompt_audit_invalid_keyword_regex", infraerrors.Reason(err))
		})
	}
	longRegex := "regex:" + strings.Repeat("a", MaxBlockedRegexRunes-len(promptKeywordRegexPrefix))
	require.NoError(t, validateBlockedKeywords([]string{longRegex}))
	require.Equal(t, "prompt_audit_keyword_regex_too_long", infraerrors.Reason(validateBlockedKeywords([]string{longRegex + "a"})))
	require.Equal(t, "prompt_audit_keyword_too_long", infraerrors.Reason(validateBlockedKeywords([]string{strings.Repeat("a", MaxBlockedKeywordRunes+1)})))
}

func TestPromptServiceRegexMatchEvidenceAndKeyWhitelist(t *testing.T) {
	const rule = `regex:(?i)attack [a-z.]+\.gov\.cn`
	active, err := ActiveFromStorage(storageConfig{
		KeywordBlockingEnabled: true, KeywordAllGroups: true,
		BlockedKeywords: []string{rule}, KeywordWhitelistKeys: []KeywordWhitelistKey{{UserID: 1, APIKeyID: 2}},
	}, true, prefixEncryptor{})
	require.NoError(t, err)
	require.Same(t, active.keywordMatcher, cloneActiveConfig(active).keywordMatcher, "snapshot clones reuse immutable compiled patterns")
	service := &PromptService{config: &fakeConfigStore{active: true, cfg: active}}
	req := Request{UserID: 1, APIKeyID: 3, Protocol: "openai_responses", Body: []byte(`{"input":"attack agency.gov.cn"}`)}
	decision, err := service.CheckKeyword(context.Background(), req)
	require.NoError(t, err)
	require.Equal(t, ErrorCodeKeywordBlocked, decision.ErrorCode)
	require.Equal(t, rule, decision.Result.matchedKeyword)
	digest := sha256.Sum256([]byte(rule))
	require.Equal(t, "sha256:"+hex.EncodeToString(digest[:]), decision.Result.ScannerEvidence["keyword"])
	clientDecision := prioritize(nil, decision)
	require.Contains(t, clientDecision.ClientMessage, "regex:")
	require.NotContains(t, clientDecision.ClientMessage, "agency.gov.cn")
	req.APIKeyID = 2
	decision, err = service.CheckKeyword(context.Background(), req)
	require.NoError(t, err)
	require.Equal(t, DecisionAllow, decision.Kind)
	require.True(t, decision.AllowNextStage)
}
