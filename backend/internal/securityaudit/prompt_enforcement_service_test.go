package securityaudit

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type enforcementStoreProbe struct {
	inputs  []EnforcementInput
	outcome *EnforcementOutcome
	err     error
}

func (p *enforcementStoreProbe) RecordEnforcedBlocking(_ context.Context, input EnforcementInput) (*EnforcementOutcome, error) {
	p.inputs = append(p.inputs, input)
	return p.outcome, p.err
}

type enforcementInvalidatorProbe struct{ users []int64 }

func (p *enforcementInvalidatorProbe) InvalidateAuthCacheByUserID(_ context.Context, id int64) {
	p.users = append(p.users, id)
}

func enforcementServiceConfig(t *testing.T) ActiveConfig {
	t.Helper()
	cfg := DefaultStorageConfig()
	cfg.KeywordBlockingEnabled, cfg.KeywordAllGroups = true, true
	cfg.JailbreakKeywords = []string{"jailbreak-secret"}
	cfg.OtherKeywords = []string{"ordinary-secret"}
	cfg.CombinationPolicies = []CombinationPolicy{{ID: "garden", Name: "Garden policy", Enabled: true, TopicRules: []string{"orchid"}, IntentRules: []string{"prune"}}}
	active, err := ActiveFromStorage(cfg, true, prefixEncryptor{})
	require.NoError(t, err)
	return active
}

func TestPromptEnforcementPriorityAndPrivateClientResponse(t *testing.T) {
	for _, tc := range []struct {
		name, input, kind, code string
		count                   int
	}{
		{"jailbreak precedes other and combination", "jailbreak-secret ordinary-secret orchid prune", "jailbreak", ErrorCodeKeywordBlocked, 1},
		{"combination precedes other", "ordinary-secret orchid prune", "combination", ErrorCodeCombinationBlocked, 1},
		{"ordinary keyword remains uncounted", "ordinary-secret", "other", ErrorCodeKeywordBlocked, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &enforcementStoreProbe{outcome: &EnforcementOutcome{Recorded: true, Counted: true, ViolationCount: 3, WindowHours: 24, BanThreshold: 10}}
			service := &PromptService{config: &fakeConfigStore{cfg: enforcementServiceConfig(t), active: true}}
			service.SetEnforcementDependencies(store, nil)
			started := time.Now().Add(-time.Second).UTC()
			body, err := json.Marshal(map[string]string{"input": tc.input})
			require.NoError(t, err)
			decision := NewCoordinator(nil, service).Check(context.Background(), Request{RequestID: "untrusted-client-id", EnforcementID: "gateway-turn-id", EnforcementStartedAt: started, UserID: 42, Protocol: "responses", Body: body})
			require.Equal(t, http.StatusForbidden, decision.HTTPStatus)
			require.Equal(t, tc.code, decision.ErrorCode)
			require.Len(t, store.inputs, tc.count, "one request is recorded once regardless of overlapping rule matches")
			if tc.count > 0 {
				require.Equal(t, tc.kind, store.inputs[0].Kind)
				require.Equal(t, "gateway-turn-id", store.inputs[0].RequestKey)
				require.Equal(t, started, store.inputs[0].StartedAt)
				require.Equal(t, EnforcementPolicy{AutoBanEnabled: true, ViolationWindowHours: 24, BanThreshold: 10, EmailOnHit: true, EmailMergeMinutes: 5}, store.inputs[0].Policy)
				require.Contains(t, decision.ClientMessage, "24 小时已记录 3 次")
				for _, secret := range []string{"jailbreak-secret", "ordinary-secret", "orchid", "prune", "regex:"} {
					require.NotContains(t, decision.ClientMessage, secret)
				}
				if tc.kind == "combination" {
					require.Contains(t, decision.ClientMessage, "Garden policy")
					require.Contains(t, decision.ClientMessage, "请停止相关违规请求")
				} else {
					require.Contains(t, decision.ClientMessage, "请立即停止使用破甲词，否则将封号")
				}
			} else {
				require.Contains(t, decision.ClientMessage, "ordinary-secret")
				require.Nil(t, decision.Prompt.Enforcement)
			}
		})
	}
}

func TestPromptEnforcementCombinationOnlyDoesNotEnableOtherKeywords(t *testing.T) {
	cfg := enforcementServiceConfig(t)
	cfg.KeywordBlockingEnabled = false
	store := &enforcementStoreProbe{}
	service := &PromptService{config: &fakeConfigStore{cfg: cfg, active: true}}
	service.SetEnforcementDependencies(store, nil)
	decision, err := service.CheckKeyword(context.Background(), Request{UserID: 42, EnforcementID: "turn", Protocol: "responses", Body: []byte(`{"input":"ordinary-secret"}`)})
	require.NoError(t, err)
	require.Equal(t, DecisionAllow, decision.Kind)
	require.Empty(t, store.inputs)
}

func TestPromptEnforcementRecordFailureDoesNotInventCountOrDispatch(t *testing.T) {
	store := &enforcementStoreProbe{err: errors.New("database unavailable")}
	service := &PromptService{config: &fakeConfigStore{cfg: enforcementServiceConfig(t), active: true}, metrics: NewAtomicMetrics()}
	service.SetEnforcementDependencies(store, nil)
	legacy := &fakeLegacyEngine{}
	decision := NewCoordinator(legacy, service).Check(context.Background(), Request{UserID: 42, EnforcementID: "turn", Protocol: "responses", Body: []byte(`{"input":"jailbreak-secret"}`)})
	require.Equal(t, http.StatusForbidden, decision.HTTPStatus)
	require.Contains(t, decision.ClientMessage, "禁止破甲")
	require.Contains(t, decision.ClientMessage, "次数暂不可用")
	require.NotContains(t, decision.ClientMessage, "jailbreak-secret")
	require.NotContains(t, decision.ClientMessage, "已记录")
	require.Nil(t, decision.Prompt.Enforcement)
	require.Zero(t, legacy.calls.Load())
	require.EqualValues(t, 1, service.metrics.Snapshot().RecordFailed)
}

func TestPromptEnforcementInvalidatesOnlyCommittedUserChange(t *testing.T) {
	for _, tc := range []struct {
		name    string
		changed bool
		err     error
		want    int
	}{
		{"no change", false, nil, 0}, {"committed disable", true, nil, 1}, {"rolled back", true, errors.New("rollback"), 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &enforcementStoreProbe{outcome: &EnforcementOutcome{Recorded: tc.err == nil, Counted: true, UserChanged: tc.changed}, err: tc.err}
			invalidator := &enforcementInvalidatorProbe{}
			service := &PromptService{config: &fakeConfigStore{cfg: enforcementServiceConfig(t), active: true}}
			service.SetEnforcementDependencies(store, invalidator)
			_, err := service.CheckKeyword(context.Background(), Request{UserID: 42, EnforcementID: "turn", Protocol: "responses", Body: []byte(`{"input":"jailbreak-secret"}`)})
			require.NoError(t, err)
			require.Len(t, invalidator.users, tc.want)
		})
	}
}

func TestPromptEnforcementDuplicateHistoryDoesNotReportAnActiveUserBanned(t *testing.T) {
	outcome := &EnforcementOutcome{Recorded: true, Counted: true, ViolationCount: 10, WindowHours: 24, BanThreshold: 10, AutoBanned: true, UserStatus: "active"}
	require.Contains(t, enforcementCountMessage(outcome), "已记录 10 次")
	require.NotContains(t, enforcementCountMessage(outcome), "账号已自动停用")
	outcome.UserStatus = "disabled"
	require.Contains(t, enforcementCountMessage(outcome), "账号已自动停用")
}

func TestPromptKeywordClassificationEvidenceIsLocalOnly(t *testing.T) {
	result := &NormalizedResult{ScannerBackend: "external-guard", PolicyID: "keyword-substring", matchedKeyword: "secret", keywordKind: "jailbreak", ScannerEvidence: map[string]string{keywordKindEvidenceKey: "jailbreak", matchedKeywordEvidenceKey: "secret"}}
	evidence := promptEventEvidence(result)
	require.NotContains(t, evidence, keywordKindEvidenceKey)
	require.NotContains(t, evidence, matchedKeywordEvidenceKey)
	result.ScannerBackend = "local-keyword"
	require.Equal(t, "jailbreak", promptEventEvidence(result)[keywordKindEvidenceKey])
}

func TestPromptAdminUnbanOwnershipAndResult(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		status int
	}{
		{"success", nil, http.StatusOK}, {"other ban", ErrEnforcementUnbanOwnership, http.StatusConflict}, {"missing user", ErrEnforcementUserNotFound, http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := &fakePromptAdminService{unban: func(_ context.Context, id int64) (*EnforcementOutcome, error) {
				require.EqualValues(t, 42, id)
				return &EnforcementOutcome{Epoch: 2, UserStatus: "active"}, tc.err
			}}
			response := promptAdminRequest(t, promptAdminRouter(svc), http.MethodPost, "/admin/prompt-audit/users/42/unban", nil)
			require.Equal(t, tc.status, response.Code)
			if tc.err == nil {
				require.Contains(t, response.Body.String(), `"epoch":2`)
			}
		})
	}
}
