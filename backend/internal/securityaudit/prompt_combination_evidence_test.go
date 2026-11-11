package securityaudit

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func combinationEvidenceFixture() *CombinationMatch {
	return &CombinationMatch{
		PolicyID: "custom-policy_1", PolicyName: "自定义主题策略",
		CBRNMatch: CBRNMatch{
			Topic:  CBRNMatchEvidence{Rule: `regex:(?i)\.gov(?:\.[a-z]+)?`, Content: "．ｇｏｖ", Source: "history", SegmentIndex: 0, Start: 2, End: 6},
			Intent: CBRNMatchEvidence{Rule: `regex:入侵`, Content: "入\u200b侵", Source: "latest_user", SegmentIndex: 2, Start: 3, End: 6},
		},
	}
}

func combinationEvidenceResult() *NormalizedResult {
	return &NormalizedResult{
		Decision: EventCritical, RiskLevel: RiskCritical, Action: ActionBlock,
		Categories: []string{"combination"}, MatchedScanners: []string{"combination"},
		ScannerScores: map[string]float64{"combination": 1},
		ScannerEvidence: map[string]string{
			"combination_topic":         "sha256:" + strings.Repeat("1234567890abcdef", 4),
			"combination_intent":        "sha256:" + strings.Repeat("0123456789abcdef", 4),
			combinationMatchEvidenceKey: "must not use caller-supplied map value",
		},
		ScannerBackend: "local-combination", PolicyID: "combination-topic-intent", combinationMatch: combinationEvidenceFixture(),
	}
}

func TestPromptCombinationEvidencePrivateStorageRoundTrip(t *testing.T) {
	result := combinationEvidenceResult()
	evidence := promptEventEvidence(result)
	require.Equal(t, result.ScannerEvidence["combination_topic"], evidence["combination_topic"])
	require.Equal(t, result.ScannerEvidence["combination_intent"], evidence["combination_intent"])
	require.NotEqual(t, result.ScannerEvidence[combinationMatchEvidenceKey], evidence[combinationMatchEvidenceKey])
	require.Equal(t, result.combinationMatch, decodeCombinationMatchEvidence(evidence[combinationMatchEvidenceKey]))
	require.Equal(t, "must not use caller-supplied map value", result.ScannerEvidence[combinationMatchEvidenceKey])
	publicResult, err := json.Marshal(result)
	require.NoError(t, err)
	require.NotContains(t, string(publicResult), result.combinationMatch.Topic.Rule)
	require.NotContains(t, string(publicResult), result.combinationMatch.PolicyName)
}

func TestPromptCombinationEvidenceRejectsForeignReservedFields(t *testing.T) {
	for _, test := range []struct{ backend, policy string }{
		{"qwen3guard-openai", "combination-topic-intent"},
		{"local-combination", "priority"},
		{"local-cbrn", "cbrn-topic-intent"},
	} {
		t.Run(test.backend+test.policy, func(t *testing.T) {
			result := combinationEvidenceResult()
			result.ScannerBackend, result.PolicyID = test.backend, test.policy
			result.ScannerEvidence[matchedKeywordEvidenceKey] = "forged keyword"
			result.ScannerEvidence[cbrnMatchEvidenceKey] = "forged cbrn"
			evidence := promptEventEvidence(result)
			require.NotContains(t, evidence, combinationMatchEvidenceKey)
			require.NotContains(t, evidence, cbrnMatchEvidenceKey)
			require.NotContains(t, evidence, matchedKeywordEvidenceKey)
			require.Empty(t, BuildIssueSummaries(*result))
		})
	}
	result := combinationEvidenceResult()
	result.combinationMatch = nil
	require.NotContains(t, promptEventEvidence(result), combinationMatchEvidenceKey)
}

func TestPromptCombinationEvidenceRejectsCorruptHistoricalData(t *testing.T) {
	for _, raw := range []string{"", "{", "null", "{}", `{"topic":{}}`, strings.Repeat("x", MaxBlockedRegexRunes*4*6+2049)} {
		require.Nil(t, decodeCombinationMatchEvidence(raw))
	}
	for _, modify := range []func(*CombinationMatch){
		func(m *CombinationMatch) { m.PolicyID = "" },
		func(m *CombinationMatch) { m.PolicyID = "custom policy" },
		func(m *CombinationMatch) { m.PolicyID = "_custom" },
		func(m *CombinationMatch) { m.PolicyID = "主题" },
		func(m *CombinationMatch) { m.PolicyID = strings.Repeat("x", 65) },
		func(m *CombinationMatch) { m.PolicyName = "" },
		func(m *CombinationMatch) { m.PolicyName = " policy " },
		func(m *CombinationMatch) { m.PolicyName = strings.Repeat("名", 81) },
		func(m *CombinationMatch) { m.PolicyName = "policy\nname" },
		func(m *CombinationMatch) { m.Topic.Rule = "" },
		func(m *CombinationMatch) { m.Topic.Rule = strings.Repeat("x", MaxBlockedRegexRunes+1) },
		func(m *CombinationMatch) { m.Topic.Content = "" },
		func(m *CombinationMatch) { m.Topic.Content = strings.Repeat("x", MaxBlockedRegexRunes+2) },
		func(m *CombinationMatch) { m.Topic.Source = "unknown" },
		func(m *CombinationMatch) { m.Topic.SegmentIndex = -1 },
		func(m *CombinationMatch) { m.Topic.Start = -1 },
		func(m *CombinationMatch) { m.Topic.End = m.Topic.Start },
		func(m *CombinationMatch) { m.Intent.Source = "history" },
	} {
		match := combinationEvidenceFixture()
		modify(match)
		raw, err := json.Marshal(match)
		require.NoError(t, err)
		require.Nil(t, decodeCombinationMatchEvidence(string(raw)))
	}
}

func TestPromptCombinationEvidenceSummaryDoesNotExposeRules(t *testing.T) {
	result := combinationEvidenceResult()
	summaries := BuildIssueSummaries(*result)
	require.Len(t, summaries, 1)
	require.Equal(t, "prompt_audit_combination", summaries[0].Code)
	require.Equal(t, "combination", summaries[0].Category)
	require.Equal(t, result.combinationMatch.PolicyName+"组合拦截", summaries[0].Title)
	require.Contains(t, summaries[0].Evidence, result.combinationMatch.Topic.Content)
	require.Contains(t, summaries[0].Evidence, "携带历史")
	require.Contains(t, summaries[0].Evidence, "最新用户输入")
	require.Contains(t, summaries[0].Evidence, "[2, 6)")
	require.NotContains(t, summaries[0].Evidence, "regex:")
	result.combinationMatch = nil
	summaries = BuildIssueSummaries(*result)
	require.Len(t, summaries, 1)
	require.Empty(t, summaries[0].Evidence)
}
