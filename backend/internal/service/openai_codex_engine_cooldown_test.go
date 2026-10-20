package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestCodexEngineDeploymentCooldownDuration(t *testing.T) {
	now := time.Date(2026, time.October, 11, 6, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name, retryAfter string
		want             time.Duration
	}{
		{"missing", "", 5 * time.Second},
		{"invalid", "later", 5 * time.Second},
		{"fraction", "1.5", 5 * time.Second},
		{"integer overflow", "9999999999999999999999999", 5 * time.Second},
		{"seconds", "7", 7 * time.Second},
		{"whitespace", " 7 ", 7 * time.Second},
		{"minimum", "0", time.Second},
		{"negative", "-2", time.Second},
		{"maximum", "90", 30 * time.Second},
		{"duration overflow", "9223372036854775807", 30 * time.Second},
		{"date", now.Add(12 * time.Second).Format(http.TimeFormat), 12 * time.Second},
		{"past date", now.Add(-time.Hour).Format(http.TimeFormat), time.Second},
		{"distant date", now.Add(time.Hour).Format(http.TimeFormat), 30 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, codexEngineDeploymentCooldown(tc.retryAfter, now))
		})
	}
}

func TestCodexEngineDeploymentCooldownRequiresExplicitTrustedContract(t *testing.T) {
	now := time.Date(2026, time.October, 11, 6, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name, code, scope string
		status            int
		changeAccount     func(*Account)
		want              bool
	}{
		{name: "deployment maintenance", code: "deployment_maintenance", scope: "deployment", status: 503, want: true},
		{name: "deployment capacity", code: "capacity_unavailable", scope: "deployment", status: 503, want: true},
		{name: "scope outer whitespace", code: "capacity_unavailable", scope: " deployment ", status: 503, want: true},
		{name: "no scope", code: "capacity_unavailable", status: 503},
		{name: "group scope", code: "capacity_unavailable", scope: "group", status: 503},
		{name: "unknown scope", code: "capacity_unavailable", scope: "future_scope", status: 503},
		{name: "scope case differs", code: "capacity_unavailable", scope: "Deployment", status: 503},
		{name: "combined scopes", code: "capacity_unavailable", scope: "deployment,group", status: 503},
		{name: "unconfirmed task", code: "task_state_unconfirmed", scope: "deployment", status: 503},
		{name: "native error", code: "native_error", scope: "deployment", status: 503},
		{name: "unknown code", code: "deployment_unavailable", scope: "deployment", status: 503},
		{name: "no code despite maintenance message", scope: "deployment", status: 503},
		{name: "code case differs", code: "CAPACITY_UNAVAILABLE", scope: "deployment", status: 503},
		{name: "code whitespace", code: " capacity_unavailable ", scope: "deployment", status: 503},
		{name: "rate limit", code: "capacity_unavailable", scope: "deployment", status: 429},
		{name: "bad gateway", code: "capacity_unavailable", scope: "deployment", status: 502},
		{name: "successful HTTP with semantic failure", code: "capacity_unavailable", scope: "deployment", status: 200},
		{name: "no account ID", code: "capacity_unavailable", scope: "deployment", status: 503, changeAccount: func(a *Account) { a.ID = 0 }},
		{name: "generic API key", code: "capacity_unavailable", scope: "deployment", status: 503, changeAccount: func(a *Account) { delete(a.Extra, OpenAIAPIKeyModeExtraKey) }},
		{name: "OAuth", code: "capacity_unavailable", scope: "deployment", status: 503, changeAccount: func(a *Account) { a.Type = AccountTypeOAuth }},
		{name: "other platform", code: "capacity_unavailable", scope: "deployment", status: 503, changeAccount: func(a *Account) { a.Platform = PlatformAnthropic }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			account := engineAccount()
			account.ID = 42
			account.Schedulable = true
			if tc.changeAccount != nil {
				tc.changeAccount(account)
			}
			svc := &OpenAIGatewayService{}
			resp := &http.Response{StatusCode: tc.status, Header: make(http.Header)}
			resp.Header.Set("X-Codex-Engine-Unavailable-Scope", tc.scope)
			resp.Header.Set("Retry-After", "5")
			failure := &codexEngineResponseError{status: 503, reportedStatus: 503, code: tc.code, message: "Deployment maintenance is in progress"}
			svc.observeCodexEngineDeploymentUnavailable(account, resp, failure, now)
			require.Equal(t, tc.want, svc.isCodexEngineDeploymentUnavailable(account, now))
			require.False(t, svc.isCodexEngineDeploymentUnavailable(account, now.Add(5*time.Second)))
			require.True(t, account.Schedulable, "temporary capacity must not overwrite admin scheduling intent")
			require.Nil(t, account.TempUnschedulableUntil)
		})
	}
}

func TestCodexEngineDeploymentCooldownForwarding(t *testing.T) {
	for _, tc := range []struct {
		name, contentType, wire, scope string
		status                         int
		wantCooldown                   bool
	}{
		{"JSON maintenance", "application/json", `{"error":{"code":"deployment_maintenance","message":"Deployment maintenance is in progress","type":"gateway_error"}}`, "deployment", 503, true},
		{"JSON capacity", "application/json", `{"error":{"code":"capacity_unavailable","message":"No available accounts in this group","type":"gateway_error"}}`, "deployment", 503, true},
		{"SSE HTTP 503", "text/event-stream", "event: response.failed\ndata: {\"type\":\"response.failed\",\"response\":{\"error\":{\"code\":\"capacity_unavailable\",\"message\":\"No available accounts in this group\"}}}\n\n", "deployment", 503, true},
		{"SSE accepted before failure", "text/event-stream", "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_started\"}}\n\nevent: response.failed\ndata: {\"type\":\"response.failed\",\"response\":{\"error\":{\"status\":503,\"code\":\"capacity_unavailable\",\"message\":\"No available accounts in this group\"}}}\n\n", "deployment", 200, false},
		{"unscoped capacity with spoofed client scope", "application/json", `{"error":{"code":"capacity_unavailable","message":"No available accounts in this group"}}`, "", 503, false},
		{"group capacity", "application/json", `{"error":{"code":"capacity_unavailable","message":"No available accounts in this group"}}`, "group", 503, false},
		{"code must not be sanitized into match", "application/json", `{"error":{"code":" capacity_unavailable ","message":"No available accounts in this group"}}`, "deployment", 503, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			c.Request.Header.Set("X-Codex-Engine-Unavailable-Scope", "deployment")
			c.Request.Header.Set("Retry-After", "30")
			resp := &http.Response{StatusCode: tc.status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(tc.wire))}
			resp.Header.Set("Content-Type", tc.contentType)
			resp.Header.Set("Retry-After", "5")
			resp.Header.Set("X-Request-ID", "req_maintenance_test")
			resp.Header.Set("X-Codex-Engine-Unavailable-Scope", tc.scope)
			upstream := &httpUpstreamRecorder{resp: resp}
			healthSettings, err := json.Marshal(OpenAIAPIKeyHealthBreakerSettings{Enabled: true, WindowMinutes: 1, FailureThreshold: 1, CooldownMinutes: 5})
			require.NoError(t, err)
			healthCache := &openAIAPIKeyHealthCacheStub{tripped: true}
			healthRepo := &openAIAPIKeyHealthAccountRepo{}
			healthBlocker := &openAIAPIKeyHealthRuntimeBlocker{}
			rateLimits := NewRateLimitService(healthRepo, nil, &config.Config{}, nil, healthCache)
			rateLimits.SetSettingService(NewSettingService(&openAIAPIKeyHealthSettingRepo{value: string(healthSettings)}, &config.Config{}))
			rateLimits.SetOpenAIAPIKeyHealthCache(healthCache)
			rateLimits.SetAccountRuntimeBlocker(healthBlocker)
			svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream, rateLimitService: rateLimits}
			account := engineAccount()
			account.ID, account.Schedulable = 42, true
			account.Credentials["pool_mode"] = true
			result, err := svc.Forward(context.Background(), c, account, []byte(`{"model":"alias","stream":true}`))
			require.True(t, isCodexEngineResponseError(err))
			var failover *UpstreamFailoverError
			require.NotErrorAs(t, err, &failover, "the current failed request must not be replayed")
			require.Nil(t, result, "maintenance without usage must not enter accounting")
			require.Len(t, upstream.requests, 1)
			require.Equal(t, tc.status, rec.Code)
			require.Equal(t, tc.wire, rec.Body.String())
			require.Equal(t, "5", rec.Header().Get("Retry-After"))
			require.Equal(t, tc.scope, rec.Header().Get("X-Codex-Engine-Unavailable-Scope"))
			require.Equal(t, "req_maintenance_test", rec.Header().Get("X-Request-ID"))
			require.Empty(t, upstream.lastReq.Header.Get("X-Codex-Engine-Unavailable-Scope"), "clients cannot promote a local capacity failure to deployment scope")
			require.Empty(t, upstream.lastReq.Header.Get("Retry-After"))
			require.True(t, CodexEngineResponseWritten(c))
			require.Equal(t, tc.wantCooldown, svc.isCodexEngineDeploymentUnavailable(account, time.Now()))
			require.False(t, svc.ReportOpenAIAccountScheduleResult(account, "native", false, nil, err))
			require.False(t, svc.ObserveOpenAIAccountHealthFailure(context.Background(), account, err))
			require.Zero(t, healthCache.recordCalls)
			require.Zero(t, healthCache.setCalls)
			require.Zero(t, healthRepo.setCalls)
			require.Zero(t, healthBlocker.calls)
			require.True(t, account.Schedulable)
			require.Nil(t, account.TempUnschedulableUntil)
		})
	}
}

func TestCodexEngineDeploymentCooldownDiagnosticDoesNotChangeState(t *testing.T) {
	now := time.Date(2026, time.October, 20, 6, 0, 0, 0, time.UTC)
	for _, code := range []string{"deployment_maintenance", "capacity_unavailable"} {
		for _, tc := range []struct {
			name       string
			existing   time.Duration
			retryAfter string
		}{
			{name: "does not create", retryAfter: "30"},
			{name: "does not extend", existing: 5 * time.Second, retryAfter: "30"},
			{name: "does not shorten or clear", existing: 30 * time.Second, retryAfter: "1"},
		} {
			t.Run(code+"/"+tc.name, func(t *testing.T) {
				svc := &OpenAIGatewayService{}
				account := engineAccount()
				account.ID, account.Schedulable = 42, true
				failure := &codexEngineResponseError{status: 503, code: code}
				resp := &http.Response{StatusCode: 503, Header: make(http.Header)}
				resp.Header.Set(codexEngineUnavailableScopeHeader, "deployment")
				if tc.existing > 0 {
					resp.Header.Set("Retry-After", fmt.Sprint(int(tc.existing/time.Second)))
					svc.observeCodexEngineDeploymentUnavailable(account, resp, failure, now)
				}
				diagnostic := *account
				diagnostic.openAICandyTest = true
				resp.Header.Set("Retry-After", tc.retryAfter)
				svc.observeCodexEngineDeploymentUnavailable(&diagnostic, resp, failure, now.Add(time.Second))

				if tc.existing == 0 {
					require.Empty(t, svc.codexEngineDeploymentCooldowns.until)
					require.False(t, svc.isCodexEngineDeploymentUnavailable(account, now.Add(time.Second)))
				} else {
					require.Equal(t, now.Add(tc.existing), svc.codexEngineDeploymentCooldowns.until[account.ID])
					require.True(t, svc.isCodexEngineDeploymentUnavailable(account, now.Add(tc.existing-time.Nanosecond)))
					require.False(t, svc.isCodexEngineDeploymentUnavailable(account, now.Add(tc.existing)))
				}
				require.False(t, account.openAICandyTest, "the diagnostic marker belongs to an account copy")
				require.True(t, account.Schedulable)
				require.Nil(t, account.TempUnschedulableUntil)
			})
		}
	}
}

func TestCodexEngineDeploymentCooldownDiagnosticForwarding(t *testing.T) {
	for _, code := range []string{"deployment_maintenance", "capacity_unavailable"} {
		for _, streaming := range []bool{false, true} {
			for _, diagnostic := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/stream=%t/diagnostic=%t", code, streaming, diagnostic), func(t *testing.T) {
					payload := fmt.Sprintf(`{"model":"native","error":{"code":%q,"type":"gateway_error","message":"Deployment temporarily unavailable","status_code":503},"usage":{"input_tokens":17,"output_tokens":3,"input_tokens_details":{"cached_tokens":11}}}`, code)
					wire, contentType := payload, "application/json"
					if streaming {
						wire = "event: response.failed\ndata: {\"type\":\"response.failed\",\"response\":" + payload + "}\n\n"
						contentType = "text/event-stream"
					}
					resp := &http.Response{StatusCode: 503, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(wire))}
					resp.Header.Set("Content-Type", contentType)
					resp.Header.Set("Retry-After", "30")
					resp.Header.Set("X-Request-ID", "req_diagnostic_maintenance")
					resp.Header.Set(codexEngineUnavailableScopeHeader, "deployment")
					upstream := &httpUpstreamRecorder{resp: resp}
					svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
					account := engineAccount()
					account.ID, account.Schedulable = 42, true
					ctx := context.Background()
					var attempt *openAICandyTestAttempt
					if diagnostic {
						account = snapshotOAuthRefreshAccount(account)
						account.openAICandyTest = true
						attempt = &openAICandyTestAttempt{}
						ctx = withOpenAICandyTest(ctx, attempt)
					}
					rec := httptest.NewRecorder()
					c, _ := gin.CreateTestContext(rec)
					c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(ctx)
					// Public headers cannot grant the internal diagnostic exemption.
					c.Request.Header.Set("X-Codex-Engine-Diagnostic", "true")
					result, err := svc.Forward(ctx, c, account, []byte(`{"model":"native","stream":true}`))
					var failure *codexEngineResponseError
					require.ErrorAs(t, err, &failure)
					require.Equal(t, 503, failure.status)
					require.Equal(t, code, failure.code)
					var failover *UpstreamFailoverError
					require.NotErrorAs(t, err, &failover)
					require.NotNil(t, result, "actual upstream usage must remain available")
					require.EqualValues(t, 17, result.Usage.InputTokens)
					require.EqualValues(t, 3, result.Usage.OutputTokens)
					require.EqualValues(t, 11, result.Usage.CacheReadInputTokens)
					require.Equal(t, "native", result.UpstreamResponseModel)
					require.Equal(t, 503, rec.Code)
					require.Equal(t, wire, rec.Body.String())
					require.Equal(t, "30", rec.Header().Get("Retry-After"))
					require.Equal(t, "req_diagnostic_maintenance", rec.Header().Get("X-Request-ID"))
					require.Equal(t, "deployment", rec.Header().Get(codexEngineUnavailableScopeHeader))
					require.Len(t, upstream.requests, 1, "a diagnostic error must not replay the inference")
					require.Empty(t, upstream.lastReq.Header.Get("X-Codex-Engine-Diagnostic"))
					require.Equal(t, !diagnostic, svc.isCodexEngineDeploymentUnavailable(account, time.Now()))
					if attempt != nil {
						require.Equal(t, 1, attempt.sends)
					}
					require.True(t, account.Schedulable)
					require.Nil(t, account.TempUnschedulableUntil)
				})
			}
		}
	}
}

func TestCodexEngineDeploymentCooldownDiagnosticSuccessDoesNotClear(t *testing.T) {
	now := time.Date(2026, time.October, 20, 6, 0, 0, 0, time.UTC)
	account := engineAccount()
	account.ID = 42
	wire := `{"model":"native","output":[],"usage":{"input_tokens":7,"output_tokens":2}}`
	upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200,
		Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(wire))}}
	svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
	maintenance := &http.Response{StatusCode: 503, Header: make(http.Header)}
	maintenance.Header.Set(codexEngineUnavailableScopeHeader, "deployment")
	maintenance.Header.Set("Retry-After", "30")
	svc.observeCodexEngineDeploymentUnavailable(account, maintenance, &codexEngineResponseError{status: 503, code: "deployment_maintenance"}, now)
	diagnostic := *account
	diagnostic.openAICandyTest = true
	ctx := withOpenAICandyTest(context.Background(), &openAICandyTestAttempt{})
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(ctx)
	result, err := svc.Forward(ctx, c, &diagnostic, []byte(`{"model":"native"}`))
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, wire, rec.Body.String())
	require.EqualValues(t, 7, result.Usage.InputTokens)
	require.EqualValues(t, 2, result.Usage.OutputTokens)
	require.Len(t, upstream.requests, 1)
	require.Equal(t, now.Add(30*time.Second), svc.codexEngineDeploymentCooldowns.until[account.ID])
	require.True(t, svc.isCodexEngineDeploymentUnavailable(account, now.Add(29*time.Second)))
	require.False(t, svc.isCodexEngineDeploymentUnavailable(account, now.Add(30*time.Second)))
}

func TestCodexEngineDeploymentCooldownConcurrentExtensions(t *testing.T) {
	now := time.Date(2026, time.October, 11, 6, 0, 0, 0, time.UTC)
	account, otherAccount := engineAccount(), engineAccount()
	account.ID, otherAccount.ID = 42, 43
	svc := &OpenAIGatewayService{}
	failure := &codexEngineResponseError{status: 503, code: "capacity_unavailable"}
	response := func(retryAfter string) *http.Response {
		resp := &http.Response{StatusCode: 503, Header: make(http.Header)}
		resp.Header.Set("X-Codex-Engine-Unavailable-Scope", "deployment")
		resp.Header.Set("Retry-After", retryAfter)
		return resp
	}
	short, long := response("1"), response("30")
	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			resp := short
			if i%2 == 0 {
				resp = long
			}
			svc.observeCodexEngineDeploymentUnavailable(account, resp, failure, now)
			_ = svc.isCodexEngineDeploymentUnavailable(account, now)
		}(i)
	}
	wg.Wait()
	require.True(t, svc.isCodexEngineDeploymentUnavailable(account, now.Add(29*time.Second)), "late short cooldowns cannot shorten a newer or longer deployment pause")
	require.False(t, svc.isCodexEngineDeploymentUnavailable(otherAccount, now), "independent Engine accounts must remain eligible")
	require.False(t, svc.isCodexEngineDeploymentUnavailable(account, now.Add(30*time.Second)))

	// A subsequent deployment has its own deadline; expiry checks for the old
	// deadline cannot clear it.
	svc.observeCodexEngineDeploymentUnavailable(account, short, failure, now.Add(31*time.Second))
	require.True(t, svc.isCodexEngineDeploymentUnavailable(account, now.Add(30*time.Second)))
	require.False(t, svc.isCodexEngineDeploymentUnavailable(account, now.Add(32*time.Second)))

	// Either expiry wins first and the new observation inserts its deadline, or
	// the extension wins first and the expiry check sees the new deadline.
	for i := 0; i < 64; i++ {
		racingSvc := &OpenAIGatewayService{}
		racingSvc.observeCodexEngineDeploymentUnavailable(account, short, failure, now)
		start := make(chan struct{})
		var race sync.WaitGroup
		race.Add(2)
		go func() {
			defer race.Done()
			<-start
			_ = racingSvc.isCodexEngineDeploymentUnavailable(account, now.Add(2*time.Second))
		}()
		go func() {
			defer race.Done()
			<-start
			racingSvc.observeCodexEngineDeploymentUnavailable(account, long, failure, now.Add(2*time.Second))
		}()
		close(start)
		race.Wait()
		require.True(t, racingSvc.isCodexEngineDeploymentUnavailable(account, now.Add(2*time.Second)))
		require.True(t, racingSvc.isCodexEngineDeploymentUnavailable(account, now.Add(31*time.Second)))
	}
}

type codexEngineCooldownPendingBody struct {
	io.Reader
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (b *codexEngineCooldownPendingBody) Read(p []byte) (int, error) {
	b.once.Do(func() { close(b.started) })
	<-b.release
	return b.Reader.Read(p)
}

func (*codexEngineCooldownPendingBody) Close() error { return nil }

func TestCodexEngineDeploymentCooldownOldSuccessDoesNotClearNewPause(t *testing.T) {
	body := &codexEngineCooldownPendingBody{Reader: strings.NewReader(`{"id":"resp_older","output":[],"usage":{"input_tokens":1,"output_tokens":1}}`), started: make(chan struct{}), release: make(chan struct{})}
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(body.release) }) }
	defer release()
	upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: body}}
	svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
	account := engineAccount()
	account.ID = 42
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	done := make(chan error, 1)
	go func() {
		_, err := svc.Forward(context.Background(), c, account, []byte(`{"model":"alias"}`))
		done <- err
	}()
	select {
	case <-body.started:
	case err := <-done:
		t.Fatalf("forwarding finished before reaching the pending response: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("forwarding did not reach the pending response")
	}
	now := time.Now()
	maintenance := &http.Response{StatusCode: 503, Header: make(http.Header)}
	maintenance.Header.Set("X-Codex-Engine-Unavailable-Scope", "deployment")
	maintenance.Header.Set("Retry-After", "30")
	svc.observeCodexEngineDeploymentUnavailable(account, maintenance, &codexEngineResponseError{status: 503, code: "capacity_unavailable"}, now)
	release()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("forwarding did not finish after releasing its response")
	}
	svc.ReportOpenAIAccountScheduleResult(account, "native", true, nil)
	require.Equal(t, 200, rec.Code)
	require.True(t, svc.isCodexEngineDeploymentUnavailable(account, now.Add(29*time.Second)))
	require.False(t, svc.isCodexEngineDeploymentUnavailable(account, now.Add(30*time.Second)))
}
