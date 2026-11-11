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

func combinationServiceTestConfig(t *testing.T) ActiveConfig {
	t.Helper()
	cfg, err := ActiveFromStorage(storageConfig{
		KeywordAllGroups: true,
		CombinationPolicies: []CombinationPolicy{{
			ID: "custom-government", Name: "自定义政府目标策略", Enabled: true,
			TopicRules: defaultGovernmentTopicRules(), IntentRules: defaultGovernmentIntentRules(),
		}},
	}, true, prefixEncryptor{})
	require.NoError(t, err)
	return cfg
}

func TestPromptCombinationServiceScopeAndPriority(t *testing.T) {
	group := int64(7)
	for _, tc := range []struct {
		name       string
		configure  func(*ActiveConfig)
		body       string
		wantCode   string
		wantPolicy string
	}{
		{name: "independent of keyword and Guard", wantCode: ErrorCodeCombinationBlocked},
		{name: "risk control off", configure: func(c *ActiveConfig) { c.RiskControlEnabled = false }},
		{name: "policy disabled", configure: func(c *ActiveConfig) { c.CombinationPolicies[0].Enabled = false }},
		{name: "wrong group", configure: func(c *ActiveConfig) { c.KeywordAllGroups = false; c.KeywordGroupIDs = []int64{8} }},
		{name: "selected group", configure: func(c *ActiveConfig) { c.KeywordAllGroups = false; c.KeywordGroupIDs = []int64{7} }, wantCode: ErrorCodeCombinationBlocked},
		{name: "exact Key exemption", configure: func(c *ActiveConfig) { c.KeywordWhitelistKeys = []KeywordWhitelistKey{{UserID: 1, APIKeyID: 2}} }},
		{name: "same user different Key", configure: func(c *ActiveConfig) { c.KeywordWhitelistKeys = []KeywordWhitelistKey{{UserID: 1, APIKeyID: 3}} }, wantCode: ErrorCodeCombinationBlocked},
		{name: "wrong user same Key ID", configure: func(c *ActiveConfig) { c.KeywordWhitelistKeys = []KeywordWhitelistKey{{UserID: 3, APIKeyID: 2}} }, wantCode: ErrorCodeCombinationBlocked},
		{name: "government information", body: `{"input":"请查询 example.gov.cn 的公开办事信息"}`},
		{name: "attack without government target", body: `{"input":"渗透 example.com"}`},
		{name: "carried target", body: `{"input":[{"role":"user","content":"example.gov.cn"},{"role":"assistant","content":"收到"},{"role":"user","content":"渗透它"}]}`, wantCode: ErrorCodeCombinationBlocked},
		{name: "old intent ignored", body: `{"input":[{"role":"user","content":"渗透 example.gov.cn"},{"role":"assistant","content":"历史"},{"role":"user","content":"你好"}]}`},
		{name: "no remote history", body: `{"previous_response_id":"resp_example.gov.cn","input":"渗透它"}`},
		{name: "image only newest user", body: `{"input":[{"role":"user","content":"渗透 example.gov.cn"},{"role":"user","content":[{"type":"input_image","image_url":"https://images.example/x.png"}]}]}`},
		{name: "original keywords first", configure: func(c *ActiveConfig) {
			c.KeywordBlockingEnabled = true
			c.BlockedKeywords = []string{"渗透"}
			var err error
			c.keywordMatcher, err = newPromptKeywordMatcher(c.BlockedKeywords)
			require.NoError(t, err)
		}, wantCode: ErrorCodeKeywordBlocked},
		{name: "first matching policy wins", body: `{"input":"subject-x prepare-x 渗透 example.gov.cn"}`, configure: func(c *ActiveConfig) {
			c.CombinationPolicies = append(cbrnServiceTestConfig(t).CombinationPolicies, c.CombinationPolicies...)
		}, wantCode: ErrorCodeCombinationBlocked, wantPolicy: "cbrn"},
		{name: "reordering changes priority", body: `{"input":"subject-x prepare-x 渗透 example.gov.cn"}`, configure: func(c *ActiveConfig) {
			c.CombinationPolicies = append(c.CombinationPolicies, cbrnServiceTestConfig(t).CombinationPolicies...)
		}, wantCode: ErrorCodeCombinationBlocked, wantPolicy: "custom-government"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := combinationServiceTestConfig(t)
			if tc.configure != nil {
				tc.configure(&cfg)
			}
			service := &PromptService{config: &fakeConfigStore{active: true, cfg: cfg}, metrics: NewAtomicMetrics()}
			body := tc.body
			if body == "" {
				body = `{"input":"请渗透 example.gov.cn"}`
			}
			req := Request{Protocol: "openai_responses", UserID: 1, APIKeyID: 2, GroupID: &group, Body: []byte(body)}
			decision, err := service.CheckKeyword(context.Background(), req)
			require.NoError(t, err)
			require.Equal(t, tc.wantCode, decision.ErrorCode)
			require.Equal(t, body, string(req.Body))
			if tc.wantCode == "" {
				require.Equal(t, DecisionAllow, decision.Kind)
				require.Nil(t, decision.Result)
			} else {
				require.Equal(t, DecisionBlock, decision.Kind)
				require.False(t, decision.AllowNextStage)
			}
			if tc.wantCode == ErrorCodeCombinationBlocked {
				require.EqualValues(t, 1, service.metrics.Snapshot().CombinationBlocked)
				require.NotNil(t, decision.Result.combinationMatch)
				if tc.wantPolicy != "" {
					require.Equal(t, tc.wantPolicy, decision.Result.combinationMatch.PolicyID)
				}
				require.Zero(t, service.metrics.Snapshot().CBRNBlocked)
				require.Zero(t, service.metrics.Snapshot().KeywordBlocked)
				raw, err := json.Marshal(decision.Result)
				require.NoError(t, err)
				require.NotContains(t, string(raw), "regex:")
				require.NotContains(t, string(raw), "example.gov.cn")
			}
		})
	}
}

func TestPromptCombinationCoordinatorLocalEvidence(t *testing.T) {
	service := &PromptService{config: &fakeConfigStore{active: true, cfg: combinationServiceTestConfig(t)}}
	legacy := &fakeLegacyEngine{}
	decision := NewCoordinator(legacy, service).Check(context.Background(), Request{Protocol: "openai_responses", Body: []byte(`{"input":"渗透 example.gov.cn"}`)})
	require.Equal(t, http.StatusForbidden, decision.HTTPStatus)
	require.Equal(t, ErrorCodeCombinationBlocked, decision.ErrorCode)
	require.Zero(t, legacy.calls.Load())
	require.Contains(t, decision.ClientMessage, "example.gov.cn")
	require.Contains(t, decision.ClientMessage, "渗透")
	require.Contains(t, decision.ClientMessage, "自定义政府目标策略")
	require.NotContains(t, decision.ClientMessage, "regex:")

	result := &NormalizedResult{ScannerBackend: "local-combination", PolicyID: "combination-topic-intent", combinationMatch: &CombinationMatch{
		PolicyID: "custom", PolicyName: "用户自定义",
		CBRNMatch: CBRNMatch{
			Topic:  CBRNMatchEvidence{Rule: "regex:private-domain", Content: "example\"\n" + strings.Repeat("中", 5000)},
			Intent: CBRNMatchEvidence{Rule: "regex:private-intent", Content: "attack\t\\"},
		},
	}}
	decision = prioritize(nil, &PromptDecision{Kind: DecisionBlock, ErrorCode: ErrorCodeCombinationBlocked, Result: result})
	require.Contains(t, decision.ClientMessage, `example\"\n`)
	require.Contains(t, decision.ClientMessage, `attack\t\\`)
	require.NotContains(t, decision.ClientMessage, "private-domain")
	require.Less(t, len([]rune(decision.ClientMessage)), 450)
	result.ScannerBackend = "external-guard"
	decision = prioritize(nil, &PromptDecision{Kind: DecisionBlock, ErrorCode: ErrorCodeCombinationBlocked, Result: result})
	require.NotContains(t, decision.ClientMessage, "example")
}

func TestPromptCombinationConcurrentSnapshot(t *testing.T) {
	service := &PromptService{config: &fakeConfigStore{active: true, cfg: combinationServiceTestConfig(t)}, metrics: NewAtomicMetrics()}
	var wg sync.WaitGroup
	for range 12 {
		wg.Go(func() {
			for range 10 {
				decision, err := service.CheckKeyword(context.Background(), Request{Protocol: "openai_responses", Body: []byte(`{"input":"渗透 example.gov.cn"}`)})
				if err != nil || decision.ErrorCode != ErrorCodeCombinationBlocked {
					t.Errorf("unexpected result: %+v, %v", decision, err)
					return
				}
			}
		})
	}
	wg.Wait()
	require.EqualValues(t, 120, service.metrics.Snapshot().CombinationBlocked)
}
