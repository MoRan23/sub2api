package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/securityaudit"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func TestCachesSecurityAuditCompletionSkipsWebSocketStages(t *testing.T) {
	require.True(t, cachesSecurityAuditCompletion("http"))
	require.True(t, cachesSecurityAuditCompletion(""))
	require.False(t, cachesSecurityAuditCompletion("first_turn"))
	require.False(t, cachesSecurityAuditCompletion("subsequent_turn"))
	require.True(t, isSecurityAuditWebSocketStage("first_turn"))
	require.True(t, isSecurityAuditWebSocketStage("subsequent_turn"))
	require.False(t, isSecurityAuditWebSocketStage("http"))
}

func TestRunSecurityAuditDoesNotSkipSubsequentWebSocketTurns(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := &turnCountingEngine{mode: securityaudit.ModeAsync}
	coordinator := securityaudit.NewCoordinator(nil, engine)

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)

	subject := middleware2.AuthSubject{UserID: 7, Concurrency: 1}
	first := runSecurityAudit(c, nil, coordinator, nil, nil, subject, "openai_responses", "gpt-test",
		[]byte(`{"type":"response.create","response":{"input":"benign"}}`), "first_turn")
	require.NotNil(t, first)
	require.True(t, first.AllowNextStage)
	require.Equal(t, int64(1), engine.enqueues.Load())
	_, cached := c.Get(securityAuditCompletedContextKey)
	require.False(t, cached, "WebSocket stages must not set the HTTP completion cache")

	// Even if an HTTP path previously cached completion on this Context, WS turns
	// must still audit every response.create payload.
	c.Set(securityAuditCompletedContextKey, true)

	second := runSecurityAudit(c, nil, coordinator, nil, nil, subject, "openai_responses", "gpt-test",
		[]byte(`{"type":"response.create","response":{"input":"malicious follow-up"}}`), "subsequent_turn")
	require.NotNil(t, second)
	require.Equal(t, int64(2), engine.enqueues.Load(), "subsequent WebSocket turns must be audited again")
}

func TestRunSecurityAuditDeduplicatesRepeatedPayloadWithinWebSocketTurn(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := &turnCountingEngine{mode: securityaudit.ModeBlocking}
	coordinator := securityaudit.NewCoordinator(nil, engine)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	payload := []byte(`{"type":"response.create","response":{"input":"same turn"}}`)
	c.Set(securityAuditWSTurnContextKey, 2)
	first := runSecurityAudit(c, nil, coordinator, nil, nil, middleware2.AuthSubject{UserID: 7}, "openai_responses", "gpt-test", payload, "subsequent_turn")
	second := runSecurityAudit(c, nil, coordinator, nil, nil, middleware2.AuthSubject{UserID: 7}, "openai_responses", "gpt-test", payload, "subsequent_turn")
	require.NotNil(t, first)
	require.NotNil(t, second)
	require.True(t, first.AllowNextStage)
	require.True(t, second.AllowNextStage)
	require.Equal(t, int64(1), engine.evaluates.Load())

	// The cache holds only one successful same-turn result.
	entry, exists := c.Get(securityAuditWSDedupeContextKey)
	require.True(t, exists)
	require.IsType(t, securityAuditWSDedupeEntry{}, entry)

	c.Set(securityAuditWSTurnContextKey, 3)
	runSecurityAudit(c, nil, coordinator, nil, nil, middleware2.AuthSubject{UserID: 7}, "openai_responses", "gpt-test", payload, "subsequent_turn")
	require.Equal(t, int64(2), engine.evaluates.Load())
}

func TestRunSecurityAuditDoesNotCacheFailedWebSocketDecision(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := &turnCountingEngine{
		mode: securityaudit.ModeBlocking,
		decisions: []*securityaudit.PromptDecision{
			{Kind: securityaudit.DecisionUnavailable, AllowNextStage: false},
			{Kind: securityaudit.DecisionAllow, AllowNextStage: true},
		},
	}
	coordinator := securityaudit.NewCoordinator(nil, engine)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	c.Set(securityAuditWSTurnContextKey, 2)
	payload := []byte(`{"type":"response.create","response":{"input":"retry me"}}`)

	first := runSecurityAudit(c, nil, coordinator, nil, nil, middleware2.AuthSubject{UserID: 7}, "openai_responses", "gpt-test", payload, "subsequent_turn")
	_, cachedAfterFailure := c.Get(securityAuditWSDedupeContextKey)
	second := runSecurityAudit(c, nil, coordinator, nil, nil, middleware2.AuthSubject{UserID: 7}, "openai_responses", "gpt-test", payload, "subsequent_turn")

	require.False(t, first.AllowNextStage)
	require.False(t, cachedAfterFailure)
	require.True(t, second.AllowNextStage)
	require.Equal(t, int64(2), engine.evaluates.Load())
}

func TestRunSecurityAuditDoesNotCacheFlaggedWebSocketDecision(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := &turnCountingEngine{
		mode: securityaudit.ModeBlocking,
		decisions: []*securityaudit.PromptDecision{
			{Kind: securityaudit.DecisionFlag, AllowNextStage: true},
			{Kind: securityaudit.DecisionAllow, AllowNextStage: true},
		},
	}
	coordinator := securityaudit.NewCoordinator(nil, engine)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	c.Set(securityAuditWSTurnContextKey, 2)
	payload := []byte(`{"type":"response.create","response":{"input":"retry flagged"}}`)

	first := runSecurityAudit(c, nil, coordinator, nil, nil, middleware2.AuthSubject{UserID: 7}, "openai_responses", "gpt-test", payload, "subsequent_turn")
	_, cachedAfterFlag := c.Get(securityAuditWSDedupeContextKey)
	second := runSecurityAudit(c, nil, coordinator, nil, nil, middleware2.AuthSubject{UserID: 7}, "openai_responses", "gpt-test", payload, "subsequent_turn")

	require.Equal(t, securityaudit.DecisionFlag, first.Kind)
	require.True(t, first.AllowNextStage)
	require.False(t, cachedAfterFlag)
	require.Equal(t, securityaudit.DecisionAllow, second.Kind)
	require.Equal(t, int64(2), engine.evaluates.Load())
}

func TestRunSecurityAuditLogsWebSocketChecksAndCacheHits(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := &turnCountingEngine{mode: securityaudit.ModeBlocking}
	coordinator := securityaudit.NewCoordinator(nil, engine)
	core, logs := observer.New(zap.InfoLevel)
	reqLog := zap.New(core)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	c.Set(securityAuditWSTurnContextKey, 2)
	payload := []byte(`{"type":"response.create","response":{"input":"same turn"}}`)

	runSecurityAudit(c, reqLog, coordinator, nil, nil, middleware2.AuthSubject{UserID: 7}, "openai_responses", "gpt-test", payload, "subsequent_turn")
	runSecurityAudit(c, reqLog, coordinator, nil, nil, middleware2.AuthSubject{UserID: 7}, "openai_responses", "gpt-test", payload, "subsequent_turn")

	startLogs := logs.FilterMessage("security_audit.gateway_check_start").All()
	require.Len(t, startLogs, 1)
	require.Equal(t, false, startLogs[0].ContextMap()["cached"])

	doneLogs := logs.FilterMessage("security_audit.gateway_check_done").All()
	require.Len(t, doneLogs, 2)
	require.Equal(t, false, doneLogs[0].ContextMap()["cached"])
	require.Equal(t, true, doneLogs[1].ContextMap()["cached"])
	require.Equal(t, "allow", doneLogs[1].ContextMap()["decision"])
	require.Equal(t, "subsequent_turn", doneLogs[1].ContextMap()["stage"])
	require.Equal(t, int64(1), engine.evaluates.Load())
}

type handlerLegacyEngine struct {
	decision *securityaudit.LegacyDecision
	calls    atomic.Int64
}

func (e *handlerLegacyEngine) Check(context.Context, securityaudit.Request) (*securityaudit.LegacyDecision, error) {
	e.calls.Add(1)
	return e.decision, nil
}

func TestRunSecurityAuditContentModerationWhitelistAllowsHTTPAndWebSocket(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, stage := range []string{"http", "first_turn", "subsequent_turn"} {
		t.Run(stage, func(t *testing.T) {
			legacy := &handlerLegacyEngine{decision: &securityaudit.LegacyDecision{
				Allowed: true, StatusCode: http.StatusOK, Action: service.ContentModerationActionWhitelistAllow,
			}}
			coordinator := securityaudit.NewCoordinator(legacy, nil)
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			if isSecurityAuditWebSocketStage(stage) {
				c.Set(securityAuditWSTurnContextKey, 1)
			}

			decision := runSecurityAudit(c, nil, coordinator, nil, nil, middleware2.AuthSubject{UserID: 7},
				service.ContentModerationProtocolOpenAIResponses, "gpt-test", []byte(`{"input":"review me"}`), stage)

			require.NotNil(t, decision)
			require.Equal(t, securityaudit.DecisionAllow, decision.Kind)
			require.True(t, decision.AllowNextStage)
			require.NotNil(t, decision.Legacy)
			require.True(t, decision.Legacy.Allowed)
			require.False(t, decision.Legacy.Blocked)
			require.Equal(t, service.ContentModerationActionWhitelistAllow, decision.Legacy.Action)
			require.Equal(t, int64(1), legacy.calls.Load())
			if stage == "http" {
				require.True(t, c.GetBool(securityAuditCompletedContextKey))
			} else {
				_, cached := c.Get(securityAuditWSDedupeContextKey)
				require.True(t, cached)
				require.False(t, c.GetBool(securityAuditCompletedContextKey))
			}
		})
	}
}

func TestRunSecurityAuditContentModerationWhitelistUsesAsyncAuditForHTTPAndWebSocket(t *testing.T) {
	gin.SetMode(gin.TestMode)
	auditStarted := make(chan struct{}, 2)
	releaseAudit := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseAudit) }) }
	defer release()
	moderationCalls := atomic.Int64{}
	moderationServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		moderationCalls.Add(1)
		auditStarted <- struct{}{}
		<-releaseAudit
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[{"category_scores":{"sexual":0.9}}]}`))
	}))
	defer moderationServer.Close()

	cfg := &service.ContentModerationConfig{
		Enabled:                           true,
		Mode:                              service.ContentModerationModePreBlock,
		BaseURL:                           moderationServer.URL,
		Model:                             "omni-moderation-latest",
		APIKeys:                           []string{"sk-test"},
		SampleRate:                        100,
		AllGroups:                         true,
		RecordNonHits:                     true,
		ContentModerationWhitelistUserIDs: []int64{7},
	}
	rawCfg, err := json.Marshal(cfg)
	require.NoError(t, err)
	repo := &contentModerationHandlerTestRepo{}
	moderationSvc := service.NewContentModerationService(
		&contentModerationHandlerSettingRepo{values: map[string]string{
			service.SettingKeyRiskControlEnabled:      "true",
			service.SettingKeyContentModerationConfig: string(rawCfg),
		}},
		repo, nil, nil, nil, nil, nil, nil,
	)
	coordinator := securityaudit.NewCoordinator(securityaudit.NewLegacyModerationAdapter(moderationSvc), nil)
	body := []byte(`{"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"review me"}]}]}`)

	for index, stage := range []string{"http", "first_turn"} {
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
		if isSecurityAuditWebSocketStage(stage) {
			c.Set(securityAuditWSTurnContextKey, index+1)
		}
		result := make(chan *securityaudit.Decision, 1)
		go func() {
			result <- runSecurityAudit(c, nil, coordinator, moderationSvc, nil,
				middleware2.AuthSubject{UserID: 7}, service.ContentModerationProtocolOpenAIResponses,
				"gpt-test", body, stage)
		}()

		select {
		case decision := <-result:
			require.NotNil(t, decision)
			require.True(t, decision.AllowNextStage)
			require.Equal(t, securityaudit.DecisionAllow, decision.Kind)
			require.NotNil(t, decision.Legacy)
			require.Equal(t, service.ContentModerationActionWhitelistAllow, decision.Legacy.Action)
		case <-time.After(time.Second):
			t.Fatalf("%s whitelist decision waited for the moderation API", stage)
		}
	}

	for range 2 {
		select {
		case <-auditStarted:
		case <-time.After(3 * time.Second):
			t.Fatal("whitelist request was allowed but its audit-only task did not reach the moderation API")
		}
	}
	require.Empty(t, repo.logSnapshot(), "audit log must not be produced before the asynchronous API returns")
	release()
	require.Eventually(t, func() bool {
		return len(repo.logSnapshot()) == 2
	}, 3*time.Second, 10*time.Millisecond)
	require.Equal(t, int64(2), moderationCalls.Load())
	for _, log := range repo.logSnapshot() {
		require.Equal(t, service.ContentModerationActionWhitelistAllow, log.Action)
		require.True(t, log.Flagged)
		require.Zero(t, log.ViolationCount)
		require.False(t, log.AutoBanned)
	}
}

func TestRunSecurityAuditPromptAuditStillBlocksContentModerationWhitelistUser(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, stage := range []string{"http", "subsequent_turn"} {
		t.Run(stage, func(t *testing.T) {
			legacy := &handlerLegacyEngine{decision: &securityaudit.LegacyDecision{
				Allowed: true, StatusCode: http.StatusOK, Action: service.ContentModerationActionWhitelistAllow,
			}}
			prompt := &turnCountingEngine{
				mode: securityaudit.ModeBlocking,
				decisions: []*securityaudit.PromptDecision{{
					Kind: securityaudit.DecisionBlock, ErrorCode: securityaudit.ErrorCodeBlocked, AllowNextStage: false,
				}},
			}
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			if isSecurityAuditWebSocketStage(stage) {
				c.Set(securityAuditWSTurnContextKey, 2)
			}

			decision := runSecurityAudit(c, nil, securityaudit.NewCoordinator(legacy, prompt), nil, nil,
				middleware2.AuthSubject{UserID: 7}, service.ContentModerationProtocolOpenAIResponses,
				"gpt-test", []byte(`{"input":"prompt audit block"}`), stage)

			require.NotNil(t, decision)
			require.Equal(t, securityaudit.DecisionBlock, decision.Kind)
			require.False(t, decision.AllowNextStage)
			require.Equal(t, securityaudit.ErrorCodeBlocked, decision.ErrorCode)
			require.NotNil(t, decision.Legacy)
			require.Equal(t, service.ContentModerationActionWhitelistAllow, decision.Legacy.Action)
			require.NotNil(t, decision.Prompt)
			require.Equal(t, securityaudit.DecisionBlock, decision.Prompt.Kind)
			require.Equal(t, int64(1), legacy.calls.Load())
			require.Equal(t, int64(1), prompt.evaluates.Load())
			_, cached := c.Get(securityAuditWSDedupeContextKey)
			require.False(t, cached, "blocked Prompt Audit decisions must not enter the WS allow cache")
		})
	}
}

func TestRunSecurityAuditNonWhitelistContentModerationBlockIsUnchanged(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, stage := range []string{"http", "subsequent_turn"} {
		t.Run(stage, func(t *testing.T) {
			legacy := &handlerLegacyEngine{decision: &securityaudit.LegacyDecision{
				Blocked: true, StatusCode: http.StatusForbidden, ErrorCode: "content_policy_violation",
				Message: "blocked by content moderation", Action: service.ContentModerationActionKeywordBlock,
			}}
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			if isSecurityAuditWebSocketStage(stage) {
				c.Set(securityAuditWSTurnContextKey, 3)
			}

			decision := runSecurityAudit(c, nil, securityaudit.NewCoordinator(legacy, nil), nil, nil,
				middleware2.AuthSubject{UserID: 8}, service.ContentModerationProtocolOpenAIResponses,
				"gpt-test", []byte(`{"input":"blocked content"}`), stage)

			require.NotNil(t, decision)
			require.Equal(t, securityaudit.DecisionBlock, decision.Kind)
			require.False(t, decision.AllowNextStage)
			require.Equal(t, "content_policy_violation", decision.ErrorCode)
			require.Equal(t, service.ContentModerationActionKeywordBlock, decision.Legacy.Action)
			require.Equal(t, int64(1), legacy.calls.Load())
			require.False(t, c.GetBool(securityAuditCompletedContextKey))
			_, cached := c.Get(securityAuditWSDedupeContextKey)
			require.False(t, cached)
		})
	}
}

type turnCountingEngine struct {
	mode      securityaudit.Mode
	enqueues  atomic.Int64
	evaluates atomic.Int64
	decisions []*securityaudit.PromptDecision
}

func (e *turnCountingEngine) EffectiveMode() securityaudit.Mode { return e.mode }
func (e *turnCountingEngine) CheckKeyword(context.Context, securityaudit.Request) (*securityaudit.PromptDecision, error) {
	return &securityaudit.PromptDecision{Kind: securityaudit.DecisionAllow, AllowNextStage: true}, nil
}
func (e *turnCountingEngine) Enqueue(context.Context, securityaudit.Request) error {
	e.enqueues.Add(1)
	return nil
}
func (e *turnCountingEngine) Evaluate(context.Context, securityaudit.Request) (*securityaudit.PromptDecision, error) {
	call := e.evaluates.Add(1)
	if int(call) <= len(e.decisions) {
		return e.decisions[call-1], nil
	}
	return &securityaudit.PromptDecision{Kind: securityaudit.DecisionAllow, AllowNextStage: true}, nil
}
