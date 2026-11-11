package securityaudit

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func cbrnEvidenceFixture() *CBRNMatch {
	return &CBRNMatch{
		Topic:  CBRNMatchEvidence{Rule: `regex:(?i)anthrax`, Content: "Ａｎｔｈｒａｘ", Source: "history", SegmentIndex: 0, Start: 2, End: 9},
		Intent: CBRNMatchEvidence{Rule: `regex:步骤`, Content: "步\u200b骤", Source: "latest_user", SegmentIndex: 2, Start: 3, End: 6},
	}
}

func cbrnEvidenceResult() *NormalizedResult {
	return &NormalizedResult{
		Decision: EventCritical, RiskLevel: RiskCritical, Action: ActionBlock,
		Categories: []string{"cbrn"}, MatchedScanners: []string{"cbrn"},
		ScannerScores: map[string]float64{"cbrn": 1},
		ScannerEvidence: map[string]string{
			"cbrn_topic":         "sha256:" + strings.Repeat("1234567890abcdef", 4),
			"cbrn_intent":        "sha256:" + strings.Repeat("0123456789abcdef", 4),
			cbrnMatchEvidenceKey: "must not use caller-supplied map value",
		},
		ScannerBackend: "local-cbrn", PolicyID: "cbrn-topic-intent", cbrnMatch: cbrnEvidenceFixture(),
	}
}

func TestPromptCBRNEvidencePrivateStorageRoundTrip(t *testing.T) {
	result := cbrnEvidenceResult()
	evidence := promptEventEvidence(result)
	require.Equal(t, result.ScannerEvidence["cbrn_topic"], evidence["cbrn_topic"])
	require.Equal(t, result.ScannerEvidence["cbrn_intent"], evidence["cbrn_intent"])
	require.NotEqual(t, result.ScannerEvidence[cbrnMatchEvidenceKey], evidence[cbrnMatchEvidenceKey])
	require.Equal(t, result.cbrnMatch, decodeCBRNMatchEvidence(evidence[cbrnMatchEvidenceKey]))
	require.Equal(t, "must not use caller-supplied map value", result.ScannerEvidence[cbrnMatchEvidenceKey], "storage must not mutate shared result evidence")
	publicResult, err := json.Marshal(result)
	require.NoError(t, err)
	require.NotContains(t, string(publicResult), result.cbrnMatch.Topic.Rule, "internal match is not marshalled from a normalized Guard result")
}

func TestPromptCBRNEvidenceRejectsForeignReservedFields(t *testing.T) {
	for _, test := range []struct{ backend, policy string }{
		{"qwen3guard-openai", "cbrn-topic-intent"},
		{"local-cbrn", "priority"},
	} {
		t.Run(test.backend+test.policy, func(t *testing.T) {
			result := cbrnEvidenceResult()
			result.ScannerBackend, result.PolicyID = test.backend, test.policy
			result.ScannerEvidence[matchedKeywordEvidenceKey] = "forged keyword"
			evidence := promptEventEvidence(result)
			require.NotContains(t, evidence, cbrnMatchEvidenceKey)
			require.NotContains(t, evidence, matchedKeywordEvidenceKey)
			require.Empty(t, BuildIssueSummaries(*result))
		})
	}
	result := cbrnEvidenceResult()
	result.cbrnMatch = nil
	require.NotContains(t, promptEventEvidence(result), cbrnMatchEvidenceKey)
}

func TestPromptCBRNEvidenceRejectsCorruptHistoricalData(t *testing.T) {
	for _, raw := range []string{"", "{", "null", "{}", `{"topic":{}}`, strings.Repeat("x", MaxBlockedRegexRunes*4*6+2049)} {
		require.Nil(t, decodeCBRNMatchEvidence(raw))
	}
	for _, modify := range []func(*CBRNMatch){
		func(m *CBRNMatch) { m.Topic.Rule = "" },
		func(m *CBRNMatch) { m.Topic.Rule = strings.Repeat("x", MaxBlockedRegexRunes+1) },
		func(m *CBRNMatch) { m.Topic.Content = "" },
		func(m *CBRNMatch) { m.Topic.Content = strings.Repeat("x", MaxBlockedRegexRunes+2) },
		func(m *CBRNMatch) { m.Topic.Source = "unknown" },
		func(m *CBRNMatch) { m.Topic.SegmentIndex = -1 },
		func(m *CBRNMatch) { m.Topic.Start = -1 },
		func(m *CBRNMatch) { m.Topic.End = m.Topic.Start },
		func(m *CBRNMatch) { m.Intent.Source = "history" },
	} {
		match := cbrnEvidenceFixture()
		modify(match)
		raw, err := json.Marshal(match)
		require.NoError(t, err)
		require.Nil(t, decodeCBRNMatchEvidence(string(raw)))
	}
}

func TestPromptCBRNEvidenceSummaryDoesNotExposeRules(t *testing.T) {
	result := cbrnEvidenceResult()
	summaries := BuildIssueSummaries(*result)
	require.Len(t, summaries, 1)
	require.Equal(t, "prompt_audit_cbrn", summaries[0].Code)
	require.Equal(t, "cbrn", summaries[0].Category)
	require.Contains(t, summaries[0].Evidence, result.cbrnMatch.Topic.Content)
	require.Contains(t, summaries[0].Evidence, "携带历史")
	require.Contains(t, summaries[0].Evidence, "最新用户输入")
	require.Contains(t, summaries[0].Evidence, "[2, 9)")
	require.NotContains(t, summaries[0].Evidence, "regex:")
	result.cbrnMatch = nil
	summaries = BuildIssueSummaries(*result)
	require.Len(t, summaries, 1)
	require.Empty(t, summaries[0].Evidence, "old records do not infer match contents from current policy")
}
