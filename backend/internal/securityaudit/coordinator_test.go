package securityaudit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

type fakeLegacyEngine struct {
	decision *LegacyDecision
	err      error
	calls    atomic.Int64
}

func (f *fakeLegacyEngine) Check(context.Context, Request) (*LegacyDecision, error) {
	f.calls.Add(1)
	return f.decision, f.err
}

type fakePromptEngine struct {
	mode       Mode
	keyword    *PromptDecision
	decision   *PromptDecision
	keywordErr error
	err        error
	enqueues   atomic.Int64
	evaluates  atomic.Int64
}

func (f *fakePromptEngine) EffectiveMode() Mode { return f.mode }
func (f *fakePromptEngine) CheckKeyword(context.Context, Request) (*PromptDecision, error) {
	if f.keywordErr != nil {
		return nil, f.keywordErr
	}
	if f.keyword != nil {
		return f.keyword, nil
	}
	return &PromptDecision{Kind: DecisionAllow, AllowNextStage: true}, nil
}
func (f *fakePromptEngine) Enqueue(context.Context, Request) error {
	f.enqueues.Add(1)
	return f.err
}
func (f *fakePromptEngine) Evaluate(context.Context, Request) (*PromptDecision, error) {
	f.evaluates.Add(1)
	return f.decision, f.err
}

func TestCoordinatorModesAndPriority(t *testing.T) {
	tests := []struct {
		name           string
		mode           Mode
		legacy         *LegacyDecision
		prompt         *PromptDecision
		promptErr      error
		wantKind       DecisionKind
		wantCode       string
		wantEnqueue    int64
		wantEvaluation int64
	}{
		{name: "off", mode: ModeOff, wantKind: DecisionAllow},
		{name: "async only enqueues", mode: ModeAsync, wantKind: DecisionAllow, wantEnqueue: 1},
		{name: "prompt block", mode: ModeBlocking, prompt: &PromptDecision{Kind: DecisionBlock}, wantKind: DecisionBlock, wantCode: ErrorCodeBlocked, wantEvaluation: 1},
		{name: "prompt unavailable", mode: ModeBlocking, promptErr: errors.New("down"), wantKind: DecisionUnavailable, wantCode: ErrorCodeUnavailable, wantEvaluation: 1},
		{name: "legacy wins both block", mode: ModeBlocking,
			legacy: &LegacyDecision{Blocked: true, StatusCode: http.StatusForbidden, ErrorCode: "content_policy_violation", Message: "legacy"},
			prompt: &PromptDecision{Kind: DecisionBlock}, wantKind: DecisionBlock, wantCode: "content_policy_violation", wantEvaluation: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			legacy := &fakeLegacyEngine{decision: tt.legacy}
			prompt := &fakePromptEngine{mode: tt.mode, decision: tt.prompt, err: tt.promptErr}
			decision := NewCoordinator(legacy, prompt).Check(context.Background(), Request{Body: []byte(`{}`)})
			require.Equal(t, tt.wantKind, decision.Kind)
			require.Equal(t, tt.wantCode, decision.ErrorCode)
			require.Equal(t, int64(1), legacy.calls.Load())
			require.Equal(t, tt.wantEnqueue, prompt.enqueues.Load())
			require.Equal(t, tt.wantEvaluation, prompt.evaluates.Load())
		})
	}
}

func TestCoordinatorDoesNotMutateRequestBody(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":"hello"}]}`)
	original := append([]byte(nil), body...)
	prompt := &fakePromptEngine{mode: ModeAsync}
	decision := NewCoordinator(&fakeLegacyEngine{}, prompt).Check(context.Background(), Request{Body: body})
	require.True(t, decision.AllowNextStage)
	require.Equal(t, original, body)
}

func TestCoordinatorKeywordBlockShortCircuitsLegacyQueueAndGuard(t *testing.T) {
	legacy := &fakeLegacyEngine{}
	prompt := &fakePromptEngine{
		mode:     ModeAsync,
		keyword:  &PromptDecision{Kind: DecisionBlock, ErrorCode: ErrorCodeKeywordBlocked, AllowNextStage: false},
		decision: &PromptDecision{Kind: DecisionBlock},
	}
	decision := NewCoordinator(legacy, prompt).Check(context.Background(), Request{Body: []byte(`{"messages":[{"role":"user","content":"blocked"}]}`)})
	require.Equal(t, DecisionBlock, decision.Kind)
	require.Equal(t, http.StatusForbidden, decision.HTTPStatus)
	require.Equal(t, ErrorCodeKeywordBlocked, decision.ErrorCode)
	require.Equal(t, "提示词包含禁止内容，请调整输入后重试", decision.ClientMessage)
	require.Zero(t, legacy.calls.Load())
	require.Zero(t, prompt.enqueues.Load())
	require.Zero(t, prompt.evaluates.Load())
}

func TestCoordinatorKeywordBlockReturnsActualMatch(t *testing.T) {
	active, err := ActiveFromStorage(storageConfig{
		KeywordBlockingEnabled: true,
		BlockedKeywords:        []string{".gov.", "Other Rule", "unmatched-private-rule"},
		KeywordAllGroups:       true,
	}, true, prefixEncryptor{})
	require.NoError(t, err)
	legacy := &fakeLegacyEngine{}
	prompt := &PromptService{config: &fakeConfigStore{active: true, cfg: active}}
	decision := NewCoordinator(legacy, prompt).Check(context.Background(), Request{
		Protocol: "openai_responses",
		Body:     []byte(`{"input":"private prompt: OTHER RULE appears before example.GOV.cn"}`),
	})
	require.Equal(t, DecisionBlock, decision.Kind)
	require.False(t, decision.AllowNextStage)
	require.Equal(t, http.StatusForbidden, decision.HTTPStatus)
	require.Equal(t, ErrorCodeKeywordBlocked, decision.ErrorCode)
	require.Equal(t, `提示词包含禁止内容，请调整输入后重试（命中关键词：".gov."）`, decision.ClientMessage)
	require.Zero(t, legacy.calls.Load())
	require.NotContains(t, decision.ClientMessage, "private prompt")
	require.NotContains(t, decision.ClientMessage, "Other Rule")
	require.NotContains(t, decision.ClientMessage, "unmatched-private-rule")
	require.Equal(t, ".gov.", decision.Prompt.Result.matchedKeyword)
	require.Contains(t, decision.Prompt.Result.ScannerEvidence["keyword"], "sha256:")
}

func TestCoordinatorKeywordMessageRequiresLocalMatch(t *testing.T) {
	for _, tc := range []struct {
		name   string
		result *NormalizedResult
	}{
		{name: "missing result"},
		{name: "missing match", result: &NormalizedResult{ScannerBackend: "local-keyword", PolicyID: "keyword-substring"}},
		{name: "guard evidence", result: &NormalizedResult{ScannerBackend: "qwen3guard-openai", PolicyID: "keyword-substring", matchedKeyword: "private guard text", matchedContent: "private guard content"}},
		{name: "different policy", result: &NormalizedResult{ScannerBackend: "local-keyword", PolicyID: "other-policy", matchedKeyword: "private guard text", matchedContent: "private guard content"}},
		{name: "rule without content", result: &NormalizedResult{ScannerBackend: "local-keyword", PolicyID: "keyword-substring", matchedKeyword: `regex:private-rule`}},
		{name: "content without local rule", result: &NormalizedResult{ScannerBackend: "local-keyword", PolicyID: "keyword-substring", matchedContent: "private guard text"}},
		{name: "evidence is not a match", result: &NormalizedResult{ScannerBackend: "local-keyword", PolicyID: "keyword-substring", ScannerEvidence: map[string]string{"keyword": "private guard text"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			decision := prioritize(nil, &PromptDecision{Kind: DecisionBlock, ErrorCode: ErrorCodeKeywordBlocked, Result: tc.result})
			require.Equal(t, `提示词包含禁止内容，请调整输入后重试`, decision.ClientMessage)
		})
	}
	result := &NormalizedResult{ScannerBackend: "local-keyword", PolicyID: "keyword-substring", matchedKeyword: "private rule", matchedContent: "private content"}
	decision := prioritize(nil, &PromptDecision{Kind: DecisionBlock, ErrorCode: ErrorCodeBlocked, Result: result})
	require.Equal(t, "提示词安全审计拒绝了该请求，请调整输入后重试", decision.ClientMessage)
}

func TestCoordinatorKeywordMessageQuotesMatchedContent(t *testing.T) {
	decision := prioritize(nil, &PromptDecision{Kind: DecisionBlock, ErrorCode: ErrorCodeKeywordBlocked, Result: &NormalizedResult{
		ScannerBackend: "local-keyword", PolicyID: "keyword-substring", matchedKeyword: "regex:(?s).*", matchedContent: "中文\"规则\\\n\t",
	}})
	require.Equal(t, `提示词包含禁止内容，请调整输入后重试（命中关键词："中文\"规则\\\n\t"）`, decision.ClientMessage)
	require.NotContains(t, decision.ClientMessage, "\n")
	require.NotContains(t, decision.ClientMessage, "regex:")
}

func TestCoordinatorRegexBlockReturnsMatchedContentAcrossProtocols(t *testing.T) {
	const rule = `regex:(?i)blocked-[0-9]+|never-match-private-branch`
	active, err := ActiveFromStorage(storageConfig{
		KeywordBlockingEnabled: true, KeywordAllGroups: true, BlockedKeywords: []string{rule},
	}, true, prefixEncryptor{})
	require.NoError(t, err)
	for _, tc := range []struct {
		protocol, body string
	}{
		{"openai_responses", `{"input":"unrelated prefix BLOCKED-42 unrelated suffix"}`},
		{"openai_chat_completions", `{"messages":[{"role":"user","content":"unrelated prefix BLOCKED-42 unrelated suffix"}]}`},
		{"anthropic_messages", `{"messages":[{"role":"user","content":"unrelated prefix BLOCKED-42 unrelated suffix"}]}`},
		{"gemini", `{"contents":[{"role":"user","parts":[{"text":"unrelated prefix BLOCKED-42 unrelated suffix"}]}]}`},
		{"responses_websocket", `{"type":"response.create","response":{"input":"unrelated prefix BLOCKED-42 unrelated suffix"}}`},
	} {
		t.Run(tc.protocol, func(t *testing.T) {
			prompt := &PromptService{config: &fakeConfigStore{active: true, cfg: active}}
			decision := NewCoordinator(nil, prompt).Check(context.Background(), Request{Protocol: tc.protocol, Body: []byte(tc.body)})
			require.Equal(t, DecisionBlock, decision.Kind)
			require.Equal(t, http.StatusForbidden, decision.HTTPStatus)
			require.Equal(t, ErrorCodeKeywordBlocked, decision.ErrorCode)
			require.Equal(t, `提示词包含禁止内容，请调整输入后重试（命中关键词："BLOCKED-42"）`, decision.ClientMessage)
			require.Equal(t, rule, decision.Prompt.Result.matchedKeyword, "administrator audit keeps the full rule")
			encoded, err := json.Marshal(decision)
			require.NoError(t, err)
			require.NotContains(t, string(encoded), "regex:")
			require.NotContains(t, string(encoded), "never-match-private-branch")
			require.NotContains(t, string(encoded), "unrelated")
		})
	}
}

func TestCoordinatorRegexEmptyMatchKeepsGenericBlockMessage(t *testing.T) {
	active, err := ActiveFromStorage(storageConfig{
		KeywordBlockingEnabled: true, KeywordAllGroups: true, BlockedKeywords: []string{`regex:^`},
	}, true, prefixEncryptor{})
	require.NoError(t, err)
	prompt := &PromptService{config: &fakeConfigStore{active: true, cfg: active}}
	decision := NewCoordinator(nil, prompt).Check(context.Background(), Request{Protocol: "openai_responses", Body: []byte(`{"input":"hello"}`)})
	require.Equal(t, DecisionBlock, decision.Kind)
	require.Equal(t, `提示词包含禁止内容，请调整输入后重试`, decision.ClientMessage)
	require.Equal(t, `regex:^`, decision.Prompt.Result.matchedKeyword)
}

func TestCoordinatorBlockingPriorityCoversBothEngineDecisionMatrix(t *testing.T) {
	legacyCases := []struct {
		name     string
		decision *LegacyDecision
	}{
		{name: "allow", decision: &LegacyDecision{Allowed: true, StatusCode: http.StatusOK, Action: "allow"}},
		{name: "flag", decision: &LegacyDecision{Allowed: true, Flagged: true, StatusCode: http.StatusOK, Action: "flag"}},
		{name: "block", decision: &LegacyDecision{Blocked: true, StatusCode: http.StatusForbidden, ErrorCode: "legacy_exact_code", Message: "legacy exact message", Action: "block"}},
	}
	promptCases := []struct {
		name     string
		decision *PromptDecision
		wantKind DecisionKind
		wantCode string
	}{
		{name: "allow", decision: &PromptDecision{Kind: DecisionAllow, AllowNextStage: true}, wantKind: DecisionAllow},
		{name: "flag", decision: &PromptDecision{Kind: DecisionFlag, AllowNextStage: true}, wantKind: DecisionFlag},
		{name: "block", decision: &PromptDecision{Kind: DecisionBlock}, wantKind: DecisionBlock, wantCode: ErrorCodeBlocked},
		{name: "unavailable", decision: &PromptDecision{Kind: DecisionUnavailable, ErrorCode: ErrorCodeUnavailable}, wantKind: DecisionUnavailable, wantCode: ErrorCodeUnavailable},
		{name: "invalid", decision: &PromptDecision{Kind: DecisionInvalid, ErrorCode: ErrorCodeInvalidResponse}, wantKind: DecisionInvalid, wantCode: ErrorCodeInvalidResponse},
	}

	for _, legacyCase := range legacyCases {
		for _, promptCase := range promptCases {
			t.Run(fmt.Sprintf("legacy_%s_prompt_%s", legacyCase.name, promptCase.name), func(t *testing.T) {
				legacy := &fakeLegacyEngine{decision: legacyCase.decision}
				prompt := &fakePromptEngine{mode: ModeBlocking, decision: promptCase.decision}
				decision := NewCoordinator(legacy, prompt).Check(context.Background(), Request{})

				require.Same(t, legacyCase.decision, decision.Legacy)
				require.Same(t, promptCase.decision, decision.Prompt)
				require.Equal(t, int64(1), legacy.calls.Load())
				require.Equal(t, int64(1), prompt.evaluates.Load())
				if legacyCase.name == "block" {
					require.Equal(t, DecisionBlock, decision.Kind)
					require.Equal(t, "legacy_exact_code", decision.ErrorCode)
					require.Equal(t, "legacy exact message", decision.ClientMessage)
					require.False(t, decision.AllowNextStage)
					return
				}
				require.Equal(t, promptCase.wantKind, decision.Kind)
				require.Equal(t, promptCase.wantCode, decision.ErrorCode)
				require.Equal(t, promptCase.decision.AllowNextStage, decision.AllowNextStage)
			})
		}
	}
}

func TestCoordinatorPreservesIndependentEngineFactsAndMapsOnlyGatewayOutcome(t *testing.T) {
	legacyDecision := &LegacyDecision{
		Allowed: true, Flagged: true, Message: "legacy finding", StatusCode: http.StatusAccepted,
		ErrorCode: "legacy_observation", Action: "legacy_action",
	}
	promptResult := &NormalizedResult{
		Decision: EventCritical, RiskLevel: RiskCritical, Action: ActionBlock,
		Categories: []string{"pii"}, ScannerScores: map[string]float64{"pii": 1},
	}
	promptDecision := &PromptDecision{Kind: DecisionBlock, Result: promptResult}
	decision := NewCoordinator(
		&fakeLegacyEngine{decision: legacyDecision},
		&fakePromptEngine{mode: ModeBlocking, decision: promptDecision},
	).Check(context.Background(), Request{})

	require.Same(t, legacyDecision, decision.Legacy)
	require.Same(t, promptDecision, decision.Prompt)
	require.Same(t, promptResult, decision.Prompt.Result)
	require.Equal(t, "legacy finding", decision.Legacy.Message)
	require.Equal(t, []string{"pii"}, decision.Prompt.Result.Categories)
	require.Equal(t, ErrorCodeBlocked, decision.ErrorCode)
}

func TestCoordinatorAsyncEnqueueFailuresNeverChangeResponseOrDownstreamDispatch(t *testing.T) {
	for _, enqueueErr := range []error{ErrQueueFull, ErrQueueAdmissionBusy, errors.New("redis unavailable"), errors.New("publish failed")} {
		prompt := &fakePromptEngine{mode: ModeAsync, err: enqueueErr}
		decision := NewCoordinator(&fakeLegacyEngine{decision: &LegacyDecision{Allowed: true}}, prompt).Check(context.Background(), Request{})
		downstreamDispatches := 0
		status := http.StatusOK
		responseBody := "unchanged-upstream-response"
		if decision.AllowNextStage {
			downstreamDispatches++
		} else {
			status = decision.HTTPStatus
			responseBody = decision.ClientMessage
		}
		require.Equal(t, http.StatusOK, status)
		require.Equal(t, "unchanged-upstream-response", responseBody)
		require.Equal(t, 1, downstreamDispatches)
		require.Equal(t, int64(1), prompt.enqueues.Load())
		require.Zero(t, prompt.evaluates.Load())
	}
}

func TestCoordinatorContentModerationWhitelistDoesNotBypassPromptAuditOrCyberPolicy(t *testing.T) {
	t.Run("prompt audit block wins over content moderation whitelist", func(t *testing.T) {
		legacyDecision := &LegacyDecision{
			Allowed: true, StatusCode: http.StatusOK, Action: service.ContentModerationActionWhitelistAllow,
		}
		promptDecision := &PromptDecision{
			Kind: DecisionBlock, ErrorCode: ErrorCodeBlocked, AllowNextStage: false,
		}
		legacy := &fakeLegacyEngine{decision: legacyDecision}
		prompt := &fakePromptEngine{mode: ModeBlocking, decision: promptDecision}

		decision := NewCoordinator(legacy, prompt).Check(context.Background(), Request{UserID: 7})

		require.Equal(t, DecisionBlock, decision.Kind)
		require.False(t, decision.AllowNextStage)
		require.Equal(t, ErrorCodeBlocked, decision.ErrorCode)
		require.Same(t, legacyDecision, decision.Legacy)
		require.Equal(t, service.ContentModerationActionWhitelistAllow, decision.Legacy.Action)
		require.Same(t, promptDecision, decision.Prompt)
		require.Equal(t, int64(1), legacy.calls.Load())
		require.Equal(t, int64(1), prompt.evaluates.Load())
	})

	t.Run("cyber policy remains blocking", func(t *testing.T) {
		legacyDecision := &LegacyDecision{
			Blocked: true, StatusCode: http.StatusForbidden, ErrorCode: "content_policy_violation",
			Message: "cyber policy", Action: service.ContentModerationActionCyberPolicy,
		}
		legacy := &fakeLegacyEngine{decision: legacyDecision}

		decision := NewCoordinator(legacy, nil).Check(context.Background(), Request{UserID: 7})

		require.Equal(t, DecisionBlock, decision.Kind)
		require.False(t, decision.AllowNextStage)
		require.Equal(t, "content_policy_violation", decision.ErrorCode)
		require.Same(t, legacyDecision, decision.Legacy)
		require.Equal(t, service.ContentModerationActionCyberPolicy, decision.Legacy.Action)
		require.Equal(t, int64(1), legacy.calls.Load())
	})
}
