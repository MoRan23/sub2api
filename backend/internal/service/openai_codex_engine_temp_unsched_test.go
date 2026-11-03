package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

const engineTempRuleRejection = `{"error":{"code":"capacity_unavailable","message":"No available accounts in this group","type":"gateway_error"}}`

type engineTempRuleRepo struct {
	AccountRepository
	account                  *Account
	accountCalls, modelCalls int
	model, reason            string
	until                    time.Time
	contextErr               error
}

func (r *engineTempRuleRepo) GetByID(context.Context, int64) (*Account, error) { return r.account, nil }
func (r *engineTempRuleRepo) SetTempUnschedulable(ctx context.Context, _ int64, until time.Time, reason string) error {
	r.accountCalls++
	r.until, r.reason, r.contextErr = until, reason, ctx.Err()
	return r.contextErr
}
func (r *engineTempRuleRepo) SetModelRateLimit(ctx context.Context, _ int64, model string, until time.Time, reasons ...string) error {
	r.modelCalls++
	r.model, r.until, r.reason, r.contextErr = model, until, strings.Join(reasons, ""), ctx.Err()
	return r.contextErr
}

func engineTempRuleAccount(scope string) *Account {
	a := engineAccount()
	a.ID, a.Status, a.Schedulable = 1707, StatusActive, true
	a.Credentials["temp_unschedulable_enabled"] = true
	a.Credentials["temp_unschedulable_rules"] = []any{map[string]any{
		"error_code": float64(503), "scope": scope, "duration_minutes": float64(30),
		"keywords": []any{"unavailable", "maintenance", "account"},
	}}
	return a
}

func TestCodexEngineExplicitTempRulesForwarding(t *testing.T) {
	for _, tc := range []struct {
		name, scope, contentType, wire                          string
		status                                                  int
		pool, manualPause, diagnosticContext, diagnosticAccount bool
		configure                                               func(*Account)
		want                                                    bool
	}{
		{name: "account HTTP503", scope: "account", status: 503, wire: engineTempRuleRejection, want: true},
		{name: "pool account", scope: "account", status: 503, wire: engineTempRuleRejection, pool: true, want: true},
		{name: "pool model", scope: "model", status: 503, wire: engineTempRuleRejection, pool: true, want: true},
		{name: "manual pause preserved", scope: "account", status: 503, wire: engineTempRuleRejection, manualPause: true, want: true},
		{name: "HTTP status precedes body", scope: "account", status: 503, wire: `{"error":{"code":"capacity_unavailable","status_code":429}}`, want: true},
		{name: "SSE HTTP503", scope: "account", status: 503, contentType: "text/event-stream", wire: "event: response.failed\ndata: " + engineTempRuleRejection + "\n\n", want: true},
		{name: "SSE semantic503", scope: "account", status: 200, contentType: "text/event-stream", wire: "event: response.failed\ndata: {\"response\":{\"error\":{\"code\":\"capacity_unavailable\",\"status_code\":503},\"usage\":{\"input_tokens\":17,\"output_tokens\":3}}}\n\n", want: true},
		{name: "bare and terminal only once", scope: "account", status: 200, contentType: "text/event-stream", wire: "event: error\ndata: {\"code\":\"capacity_unavailable\",\"status_code\":503}\n\nevent: response.failed\ndata: {\"response\":{\"status\":\"failed\"}}\n\n", want: true},
		{name: "disabled", scope: "account", status: 503, wire: engineTempRuleRejection, configure: func(a *Account) { a.Credentials["temp_unschedulable_enabled"] = false }},
		{name: "missing rules", scope: "account", status: 503, wire: engineTempRuleRejection, configure: func(a *Account) { delete(a.Credentials, "temp_unschedulable_rules") }},
		{name: "different status", scope: "account", status: 429, wire: engineTempRuleRejection},
		{name: "different keyword", scope: "account", status: 503, wire: `{"error":{"code":"invalid_input","message":"bad request"}}`},
		{name: "custom error policy overrides", scope: "account", status: 503, wire: engineTempRuleRejection, configure: func(a *Account) {
			a.Credentials["custom_error_codes_enabled"] = true
			a.Credentials["custom_error_codes"] = []any{float64(503)}
		}},
		{name: "diagnostic context", scope: "account", status: 503, wire: engineTempRuleRejection, diagnosticContext: true},
		{name: "diagnostic account", scope: "account", status: 503, wire: engineTempRuleRejection, diagnosticAccount: true},
		{name: "no real status no synthetic502", scope: "account", status: 200, contentType: "text/event-stream", wire: "event: response.failed\ndata: " + engineTempRuleRejection + "\n\n", configure: func(a *Account) {
			a.Credentials["temp_unschedulable_rules"].([]any)[0].(map[string]any)["error_code"] = float64(502)
		}},
		{name: "output is not error evidence", scope: "account", status: 503, wire: `{"output":[{"text":"capacity_unavailable account"}],"error":{"code":"other","message":"failed"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := engineTempRuleAccount(tc.scope)
			a.Credentials["pool_mode"] = tc.pool
			a.Schedulable = !tc.manualPause
			a.openAICandyTest = tc.diagnosticAccount
			if tc.configure != nil {
				tc.configure(a)
			}
			ctx := context.Background()
			if tc.diagnosticContext {
				ctx = withOpenAICandyTest(ctx, &openAICandyTestAttempt{})
			}
			repo := &engineTempRuleRepo{account: a}
			cache := &openAIAPIKeyHealthCacheStub{}
			rates := NewRateLimitService(repo, nil, &config.Config{}, nil, cache)
			headers := http.Header{"Content-Type": {tc.contentType}, "Retry-After": {"5"}}
			if tc.contentType == "" {
				headers.Set("Content-Type", "application/json")
			}
			upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: tc.status, Header: headers, Body: io.NopCloser(strings.NewReader(tc.wire))}}
			svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream, rateLimitService: rates}
			rates.SetAccountRuntimeBlocker(svc)
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(ctx)
			c.Request.Header.Set("X-Codex-Engine-Diagnostic", "true")
			started := time.Now()
			result, err := svc.Forward(ctx, c, a, []byte(`{"model":"alias","stream":true}`))
			require.True(t, isCodexEngineResponseError(err))
			var failover *UpstreamFailoverError
			require.NotErrorAs(t, err, &failover)
			require.Len(t, upstream.requests, 1)
			require.Equal(t, tc.status, rec.Code)
			require.Equal(t, tc.wire, rec.Body.String())
			require.Equal(t, "5", rec.Header().Get("Retry-After"))
			require.Equal(t, !tc.manualPause, a.Schedulable)
			require.Equal(t, StatusActive, a.Status)
			require.Zero(t, cache.recordCalls, "no automatic health-breaker penalty")
			wantAccount := tc.want && tc.scope == "account"
			wantModel := tc.want && tc.scope == "model"
			wantAccountCalls, wantModelCalls := 0, 0
			if wantAccount {
				wantAccountCalls = 1
			}
			if wantModel {
				wantModelCalls = 1
			}
			require.Equal(t, wantAccountCalls, repo.accountCalls)
			require.Equal(t, wantModelCalls, repo.modelCalls)
			require.Equal(t, wantAccountCalls, cache.setCalls)
			require.Equal(t, wantAccount, svc.peekOpenAIAccountRuntimeBlock(a).blocked)
			if tc.want {
				require.NoError(t, repo.contextErr)
				require.WithinDuration(t, started.Add(30*time.Minute), repo.until, 3*time.Second)
				var state TempUnschedState
				require.NoError(t, json.Unmarshal([]byte(repo.reason), &state))
				require.Equal(t, 503, state.StatusCode)
				require.Equal(t, tc.scope, state.Scope)
				if wantModel {
					require.Equal(t, "native", repo.model)
					require.Contains(t, a.modelRateLimitKeysForRequest(ctx, "alias"), repo.model)
				}
			}
			if tc.name == "SSE semantic503" {
				require.NotNil(t, result)
				require.EqualValues(t, 17, result.Usage.InputTokens)
				require.EqualValues(t, 3, result.Usage.OutputTokens)
			}
		})
	}
}

func TestCodexEngineTempRuleUsesSchedulingModelForCompact(t *testing.T) {
	a := engineTempRuleAccount("model")
	a.Credentials["compact_model_mapping"] = map[string]any{"alias": "compact-native"}
	repo := &engineTempRuleRepo{account: a}
	upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 503, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(engineTempRuleRejection))}}
	svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream, rateLimitService: NewRateLimitService(repo, nil, &config.Config{}, nil, nil)}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses/compact", nil)
	_, err := svc.forwardCodexEngine(context.Background(), c, a, []byte(`{"model":"alias","input":[]}`), "/v1/responses/compact", "")
	require.True(t, isCodexEngineResponseError(err))
	require.Equal(t, 1, repo.modelCalls)
	require.Equal(t, "native", repo.model)
	require.Contains(t, a.modelRateLimitKeysForRequest(context.Background(), "alias"), repo.model)
	require.Contains(t, string(upstream.lastBody), "compact-native")
}

func TestCodexEngineTempRulePersistsAfterCancellation(t *testing.T) {
	a := engineTempRuleAccount("account")
	a.Schedulable = false
	repo := &engineTempRuleRepo{account: a}
	svc := &OpenAIGatewayService{rateLimitService: NewRateLimitService(repo, nil, &config.Config{}, nil, nil)}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	svc.observeCodexEngineTempUnschedulable(ctx, a, &http.Response{StatusCode: 503}, newCodexEngineResponseError(503, []byte(engineTempRuleRejection), ""), "native")
	require.Equal(t, 1, repo.accountCalls)
	require.NoError(t, repo.contextErr)
	require.False(t, a.Schedulable)
}
