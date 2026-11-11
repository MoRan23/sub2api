package securityaudit

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

func cbrnServiceTestConfig(t *testing.T) ActiveConfig {
	t.Helper()
	cfg, err := ActiveFromStorage(storageConfig{
		CBRNBlockingEnabled: true, KeywordAllGroups: true,
		CBRNTopicRules: []string{`regex:(?i)subject-x`}, CBRNIntentRules: []string{`regex:(?i)prepare-x`},
	}, true, prefixEncryptor{})
	require.NoError(t, err)
	return cfg
}

func TestPromptCBRNServiceScopeAndPriority(t *testing.T) {
	group := int64(7)
	for _, tc := range []struct {
		name      string
		configure func(*ActiveConfig)
		body      string
		wantCode  string
	}{
		{name: "independent of keyword and Guard switches", wantCode: ErrorCodeCombinationBlocked},
		{name: "risk control disabled", configure: func(c *ActiveConfig) { c.RiskControlEnabled = false }},
		{name: "combination disabled", configure: func(c *ActiveConfig) { c.CombinationPolicies[0].Enabled = false }},
		{name: "different group", configure: func(c *ActiveConfig) { c.KeywordAllGroups = false; c.KeywordGroupIDs = []int64{8} }},
		{name: "selected group", configure: func(c *ActiveConfig) { c.KeywordAllGroups = false; c.KeywordGroupIDs = []int64{7} }, wantCode: ErrorCodeCombinationBlocked},
		{name: "exact Key bypass", configure: func(c *ActiveConfig) { c.KeywordWhitelistKeys = []KeywordWhitelistKey{{UserID: 1, APIKeyID: 2}} }},
		{name: "same user another Key", configure: func(c *ActiveConfig) { c.KeywordWhitelistKeys = []KeywordWhitelistKey{{UserID: 1, APIKeyID: 3}} }, wantCode: ErrorCodeCombinationBlocked},
		{name: "same Key ID wrong user", configure: func(c *ActiveConfig) { c.KeywordWhitelistKeys = []KeywordWhitelistKey{{UserID: 3, APIKeyID: 2}} }, wantCode: ErrorCodeCombinationBlocked},
		{name: "topic only", body: `{"input":"subject-x history"}`},
		{name: "intent only", body: `{"input":"prepare-x"}`},
		{name: "topic from carried history", body: `{"input":[{"role":"user","content":"subject-x"},{"role":"assistant","content":"history"},{"role":"user","content":"prepare-x"}]}`, wantCode: ErrorCodeCombinationBlocked},
		{name: "historical intent excluded", body: `{"input":[{"role":"user","content":"subject-x prepare-x"},{"role":"assistant","content":"history"},{"role":"user","content":"hello"}]}`},
		{name: "no implicit history lookup", body: `{"previous_response_id":"resp_subject-x","input":"prepare-x"}`},
		{name: "original keywords win", configure: func(c *ActiveConfig) {
			c.KeywordBlockingEnabled = true
			c.BlockedKeywords = []string{"subject-x"}
			var err error
			c.keywordMatcher, err = newPromptKeywordMatcher(c.BlockedKeywords)
			require.NoError(t, err)
		}, wantCode: ErrorCodeKeywordBlocked},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := cbrnServiceTestConfig(t)
			if tc.configure != nil {
				tc.configure(&cfg)
			}
			metrics := NewAtomicMetrics()
			service := &PromptService{config: &fakeConfigStore{active: true, cfg: cfg}, metrics: metrics}
			body := tc.body
			if body == "" {
				body = `{"input":"prepare-x SUBJECT-X"}`
			}
			req := Request{UserID: 1, APIKeyID: 2, GroupID: &group, Protocol: "openai_responses", Body: []byte(body)}
			decision, err := service.CheckKeyword(context.Background(), req)
			require.NoError(t, err)
			require.Equal(t, tc.wantCode, decision.ErrorCode)
			require.Equal(t, body, string(req.Body), "business body must remain unchanged")
			if tc.wantCode == "" {
				require.Equal(t, DecisionAllow, decision.Kind)
				require.Nil(t, decision.Result, "topic alone creates no new event")
			} else {
				require.Equal(t, DecisionBlock, decision.Kind)
				require.False(t, decision.AllowNextStage)
			}
			if tc.wantCode == ErrorCodeCombinationBlocked {
				require.Equal(t, int64(1), metrics.Snapshot().CombinationBlocked)
				require.Zero(t, metrics.Snapshot().KeywordBlocked)
				raw, err := json.Marshal(decision.Result)
				require.NoError(t, err)
				require.NotContains(t, string(raw), "regex:")
				require.NotContains(t, string(raw), "prepare-x")
			}
		})
	}
}

func TestPromptCBRNCoordinatorStopsBeforeOtherEngines(t *testing.T) {
	service := &PromptService{config: &fakeConfigStore{active: true, cfg: cbrnServiceTestConfig(t)}}
	legacy := &fakeLegacyEngine{}
	decision := NewCoordinator(legacy, service).Check(context.Background(), Request{
		Protocol: "openai_responses", Body: []byte(`{"input":"subject-x prepare-x"}`),
	})
	require.Equal(t, ErrorCodeCombinationBlocked, decision.ErrorCode)
	require.Equal(t, http.StatusForbidden, decision.HTTPStatus)
	require.Equal(t, int64(0), legacy.calls.Load())
	require.NotContains(t, decision.ClientMessage, "subject-x")
	require.NotContains(t, decision.ClientMessage, "prepare-x")
	require.Contains(t, decision.ClientMessage, "核、生物、化学、放射性危害")
	require.NotContains(t, decision.ClientMessage, "regex:")
}

func TestPromptCBRNClientEvidenceIsLocalAndBounded(t *testing.T) {
	result := &NormalizedResult{ScannerBackend: "local-cbrn", PolicyID: "cbrn-topic-intent", cbrnMatch: &CBRNMatch{
		Topic:  CBRNMatchEvidence{Rule: "regex:private-rule", Content: "subject\"\n" + strings.Repeat("中", 5000)},
		Intent: CBRNMatchEvidence{Rule: "regex:private-intent", Content: "prepare\t\\"},
	}}
	decision := prioritize(nil, &PromptDecision{Kind: DecisionBlock, ErrorCode: ErrorCodeCBRNBlocked, Result: result})
	require.NotContains(t, decision.ClientMessage, "subject")
	require.NotContains(t, decision.ClientMessage, "prepare")
	require.Less(t, len([]rune(decision.ClientMessage)), 450)
	require.NotContains(t, decision.ClientMessage, "private-rule")
	result.ScannerBackend = "external-guard"
	decision = prioritize(nil, &PromptDecision{Kind: DecisionBlock, ErrorCode: ErrorCodeCBRNBlocked, Result: result})
	require.NotContains(t, decision.ClientMessage, "subject")
}

func TestPromptCBRNServiceConcurrentSnapshot(t *testing.T) {
	service := &PromptService{config: &fakeConfigStore{active: true, cfg: cbrnServiceTestConfig(t)}, metrics: NewAtomicMetrics()}
	var wg sync.WaitGroup
	for range 24 {
		wg.Go(func() {
			for range 10 {
				decision, err := service.CheckKeyword(context.Background(), Request{Protocol: "openai_responses", Body: []byte(`{"input":"subject-x prepare-x"}`)})
				if err != nil || decision.ErrorCode != ErrorCodeCombinationBlocked {
					t.Errorf("unexpected decision: %+v, %v", decision, err)
					return
				}
			}
		})
	}
	wg.Wait()
	require.Equal(t, int64(240), service.metrics.Snapshot().CombinationBlocked)
}
