package service

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/codexnative"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openaicookies"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type codexHTTPRouteProxyRepo struct {
	ProxyRepository
	proxy    Proxy
	reads    int
	changeAt int
}

type codexHTTPRetryUnavailableRepo struct {
	CodexTurnStateRepository
	nilRecord bool
}

func (r codexHTTPRetryUnavailableRepo) BeginBusiness(context.Context, CodexTurnStateKey, string, time.Time, time.Time) (*CodexTurnStateRecord, error) {
	if r.nilRecord {
		return nil, nil
	}
	return nil, errors.New("retry store unavailable")
}

func (r *codexHTTPRouteProxyRepo) GetByID(context.Context, int64) (*Proxy, error) {
	r.reads++
	if r.changeAt > 0 && r.reads >= r.changeAt {
		r.proxy.RouteGeneration = 2
		r.proxy.Host = "changed.invalid"
	}
	copy := r.proxy
	return &copy, nil
}

func seedCodexHTTPRouteBundle(t *testing.T, state *CodexTurnStateService, account *Account, model, mode string) string {
	t.Helper()
	a, err := state.PrepareForHTTP(context.Background(), account, model, CodexTurnStateBundleBinding{WireMode: mode, EgressKind: "proxy", ProxyID: 2, ProxyRouteGeneration: 1})
	require.NoError(t, err)
	markCodexStateTestBusinessSent(t, state, a)
	token := codexStateTestToken(10, state.now())
	state.Observe(a, token)
	require.NoError(t, state.Finish(context.Background(), a, true))
	return token
}

func TestCodexHTTPBundleRouteAllHTTPPaths(t *testing.T) {
	for _, path := range []string{"responses", "passthrough", "chat", "messages"} {
		t.Run(path, func(t *testing.T) {
			state, _, account := newCodexStateTestService(t)
			state.now = time.Now
			token := seedCodexHTTPRouteBundle(t, state, account, "gpt-5.4", "lite")
			if path == "passthrough" {
				account.Extra["openai_passthrough"] = true
			}
			proxies := &codexHTTPRouteProxyRepo{proxy: Proxy{ID: 2, Protocol: "http", Host: "bundle.invalid", Port: 8080, Status: StatusActive, RouteGeneration: 1}}
			upstream := &codexStateCookieIntegrationUpstream{httpUpstreamRecorder: &httpUpstreamRecorder{resp: openAICompatSSECompletedResponse("resp_route", "gpt-5.4")}, manager: openaicookies.NewManager(), account: account}
			gateway := &OpenAIGatewayService{cfg: &config.Config{}, accountRepo: state.accounts, httpUpstream: upstream, codexTurnStateService: state, codexTurnStateProxyRepo: proxies}
			gateway.codexModelCapabilities.observeManifest(openAICodexModelCapabilitiesNamespace(account), []byte(`{"models":[{"slug":"gpt-5.4","use_responses_lite":true}]}`), time.Now())
			body := codexStateHTTPIntegrationBody(path, true)
			if path == "messages" {
				body = []byte(strings.TrimSuffix(string(body), "}") + `,"tools":[{"name":"lookup","description":"Look up a city","input_schema":{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}}]}`)
			}
			if path != "passthrough" {
				account.Credentials["model_mapping"] = map[string]any{"client-alias": "gpt-5.4"}
				body = []byte(strings.ReplaceAll(string(body), "gpt-5.4", "client-alias"))
			}
			result, recorder, err := codexStateHTTPIntegrationForward(t, gateway, account, path, body)
			require.NoError(t, err, recorder.Body.String())
			require.NotNil(t, result)
			require.Len(t, upstream.requests, 1)
			require.Equal(t, proxies.proxy.URL(), upstream.lastProxyURL)
			require.Equal(t, token, upstream.lastReq.Header.Get(openAICodexTurnStateHeader))
			require.Equal(t, "true", upstream.lastReq.Header.Get(responsesLiteHeader))
			if path == "messages" {
				require.Equal(t, "lookup", gjson.GetBytes(upstream.lastBody, "tools.0.name").String())
				require.Equal(t, "string", gjson.GetBytes(upstream.lastBody, "tools.0.parameters.properties.city.type").String())
			}
			require.Nil(t, account.ProxyID, "the shared account route is never edited")
		})
	}
}

func TestCodexHTTPBundleInvalidFinalToolsNeverSend(t *testing.T) {
	state, _, account := newCodexStateTestService(t)
	state.now = time.Now
	seedCodexHTTPRouteBundle(t, state, account, "gpt-5.4", "lite")
	proxies := &codexHTTPRouteProxyRepo{proxy: Proxy{ID: 2, Protocol: "http", Host: "bundle.invalid", Port: 8080, Status: StatusActive, RouteGeneration: 1}}
	upstream := &codexStateCookieIntegrationUpstream{httpUpstreamRecorder: &httpUpstreamRecorder{}, manager: openaicookies.NewManager(), account: account}
	gateway := &OpenAIGatewayService{cfg: &config.Config{}, accountRepo: state.accounts, httpUpstream: upstream, codexTurnStateService: state, codexTurnStateProxyRepo: proxies}
	gateway.codexModelCapabilities.observeManifest(openAICodexModelCapabilitiesNamespace(account), []byte(`{"models":[{"slug":"gpt-5.4","use_responses_lite":true}]}`), time.Now())
	body := []byte(`{"model":"gpt-5.4","instructions":"Reply briefly.","input":[{"role":"user","content":"hello"}],"tools":[{"type":"web_search"}],"stream":true}`)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(string(body)))
	_, err := gateway.Forward(context.Background(), c, account, body)
	require.ErrorContains(t, err, `top-level tool type "web_search"`)
	require.Empty(t, upstream.requests)
	selected := codexHTTPRouteSelectionFromContext(c, account)
	require.NotNil(t, selected)
	require.NotNil(t, selected.attempt)
	require.Equal(t, int64(2), selected.route.ProxyID, "valid route selection precedes final wire validation")
	require.False(t, selected.physical, "invalid final tools never reach the send lifecycle")
	require.True(t, selected.attempt.finished, "the wrapper releases the unused attempt")
	require.True(t, selected.attempt.businessSentAt.IsZero(), "rejected final tools cannot count as activity")
}

func TestCodexHTTPBundleStrictRouteChangeRebuildsBeforeSend(t *testing.T) {
	state, _, account := newCodexStateTestService(t)
	state.now = time.Now
	seedCodexHTTPRouteBundle(t, state, account, "gpt-5.4", "lite")
	proxies := &codexHTTPRouteProxyRepo{proxy: Proxy{ID: 2, Protocol: "http", Host: "bundle.invalid", Port: 8080, Status: StatusActive, RouteGeneration: 1}, changeAt: 3}
	upstream := &codexStateCookieIntegrationUpstream{httpUpstreamRecorder: &httpUpstreamRecorder{resp: openAICompatSSECompletedResponse("resp_baseline", "gpt-5.4")}, manager: openaicookies.NewManager(), account: account}
	gateway := &OpenAIGatewayService{cfg: &config.Config{}, accountRepo: state.accounts, httpUpstream: upstream, codexTurnStateService: state, codexTurnStateProxyRepo: proxies}
	gateway.codexModelCapabilities.observeManifest(openAICodexModelCapabilitiesNamespace(account), []byte(`{"models":[{"slug":"gpt-5.4","use_responses_lite":true}]}`), time.Now())
	_, recorder, err := codexStateHTTPIntegrationForward(t, gateway, account, "responses", codexStateHTTPIntegrationBody("responses", true))
	require.NoError(t, err, recorder.Body.String())
	require.Len(t, upstream.requests, 1, "rejection precedes the only physical send")
	require.Empty(t, upstream.lastProxyURL)
	require.Empty(t, upstream.lastReq.Header.Get(openAICodexTurnStateHeader))
	require.Equal(t, "true", upstream.lastReq.Header.Get(responsesLiteHeader), "bundle fallback cannot alter the existing wire protocol")
}

func TestCodexHTTPBundleRouteSelectionFreezeAndNativeWSIsolation(t *testing.T) {
	state, _, account := newCodexStateTestService(t)
	seedCodexHTTPRouteBundle(t, state, account, "gpt-5", "lite")
	proxies := &codexHTTPRouteProxyRepo{proxy: Proxy{ID: 2, Protocol: "http", Host: "bundle.invalid", Port: 8080, Status: StatusActive, RouteGeneration: 1}}
	s := &OpenAIGatewayService{codexTurnStateService: state, codexTurnStateProxyRepo: proxies}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	c.Request.Header.Set(responsesLiteHeader, "true")
	body := []byte(`{"model":"gpt-5","input":"hello"}`)
	first := s.selectOpenAIHTTPBundleRoute(context.Background(), c, account, "gpt-5", body)
	require.Equal(t, int64(2), FreezeOpenAIOutboundRoute(c, account).ProxyID)
	proxies.proxy.Host = "later.invalid"
	proxies.proxy.RouteGeneration++
	require.Same(t, first, s.selectOpenAIHTTPBundleRoute(context.Background(), c, account, "gpt-5", body))
	require.Equal(t, "http://bundle.invalid:8080", OpenAIOutboundRouteForAccount(c, account).ProxyURL)
	require.False(t, s.validateOpenAIHTTPBundleRoute(context.Background(), c, account, first.attempt))
	next := s.selectOpenAIHTTPBundleRoute(context.Background(), c, account, "other-model", body)
	require.NotSame(t, first, next)
	ClearOpenAIOutboundRoute(c)
	require.Zero(t, OpenAIOutboundRouteForAccount(c, account).ProxyID)
	SetOpenAIClientTransport(c, OpenAIClientTransportWS)
	require.Nil(t, s.selectOpenAIHTTPBundleRoute(context.Background(), c, account, "gpt-5", body))
	ClearOpenAIOutboundRoute(c)
	require.Empty(t, OpenAIOutboundRouteForAccount(c, account).ProxyURL)
	finishCodexTurnStateHTTPAttempt(state, next.attempt, false)
}

func TestCodexHTTPBundleBaselineWrapperNeverReplaysNetworkFailure(t *testing.T) {
	s := &OpenAIGatewayService{}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	account := &Account{ID: 1}
	c.Set(codexHTTPRouteSelectionKey, &codexHTTPRouteSelection{accountID: account.ID, model: "gpt-5"})
	calls := 0
	networkErr := errors.New("network failure")
	_, err := s.withOpenAIHTTPBundleBaseline(c, account, func() (*OpenAIForwardResult, error) { calls++; return nil, networkErr })
	require.ErrorIs(t, err, networkErr)
	require.Equal(t, 1, calls)
}

func TestCodexHTTPBundleBaselineWrapperNeverReplaysSentAttempt(t *testing.T) {
	s := &OpenAIGatewayService{}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	account := &Account{ID: 1}
	attempt := &CodexTurnStateAttempt{}
	deferCodexTurnStateHTTPActivity(attempt)
	c.Set(codexHTTPRouteSelectionKey, &codexHTTPRouteSelection{accountID: account.ID, model: "gpt-5", attempt: attempt, physical: true})
	calls := 0
	_, err := s.withOpenAIHTTPBundleBaseline(c, account, func() (*OpenAIForwardResult, error) {
		calls++
		observeCodexCookies(attempt, openaicookies.Diagnostic{SendState: "sent"})
		observeCodexCookies(attempt, openaicookies.Diagnostic{SendState: "not_sent"})
		return nil, openaicookies.ErrBundleSendRejected
	})
	require.ErrorIs(t, err, openaicookies.ErrBundleSendRejected)
	require.Equal(t, 1, calls, "a rejected redirect cannot replay the original business request")
}

func TestCodexHTTPBundleOrdinaryResponsesNeverSwitchConfiguredRoute(t *testing.T) {
	state, _, account := newCodexStateTestService(t)
	seedCodexHTTPRouteBundle(t, state, account, "gpt-5", "responses")
	proxies := &codexHTTPRouteProxyRepo{proxy: Proxy{ID: 2, Protocol: "http", Host: "collector.invalid", Port: 8080, Status: StatusActive, RouteGeneration: 1}}
	s := &OpenAIGatewayService{codexTurnStateService: state, codexTurnStateProxyRepo: proxies}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	selected := s.selectOpenAIHTTPBundleRoute(context.Background(), c, account, "gpt-5", []byte(`{"model":"gpt-5","input":"hello"}`))
	require.Equal(t, "responses", selected.attempt.WireMode)
	require.False(t, selected.attempt.CollectionEligible)
	require.Empty(t, selected.attempt.Snapshot.Token)
	require.Empty(t, selected.route.ProxyURL)
	require.Equal(t, "direct", selected.attempt.OutboundBinding.EgressKind)
	finishCodexTurnStateHTTPAttempt(state, selected.attempt, false)
}

func TestCodexHTTPBundleDirectSnapshotCannotBypassNewAccountProxy(t *testing.T) {
	state, _, account := newCodexStateTestService(t)
	a, err := state.PrepareForHTTP(context.Background(), account, "gpt-5", codexStateTestBinding())
	require.NoError(t, err)
	state.Observe(a, codexStateTestToken(10, state.now()))
	require.NoError(t, state.Finish(context.Background(), a, true))
	proxy := &Proxy{ID: 5, Protocol: "http", Host: "account.invalid", Port: 8080, Status: StatusActive, RouteGeneration: 1}
	account.ProxyID, account.Proxy = &proxy.ID, proxy
	s := &OpenAIGatewayService{codexTurnStateService: state}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	c.Request.Header.Set(responsesLiteHeader, "true")
	selected := s.selectOpenAIHTTPBundleRoute(context.Background(), c, account, "gpt-5", []byte(`{"model":"gpt-5","input":"hello"}`))
	require.Empty(t, selected.attempt.Snapshot.Token)
	require.Equal(t, proxy.URL(), selected.route.ProxyURL)
	require.Equal(t, int64(5), selected.attempt.OutboundBinding.ProxyID)
	finishCodexTurnStateHTTPAttempt(state, selected.attempt, false)
}

func TestCodexHTTPBundleRetryLeaseFailureRebuildsBaseline(t *testing.T) {
	for _, nilRecord := range []bool{false, true} {
		t.Run(map[bool]string{false: "store_failure", true: "generation_changed"}[nilRecord], func(t *testing.T) {
			state, _, account := newCodexStateTestService(t)
			seedCodexHTTPRouteBundle(t, state, account, "gpt-5", "lite")
			proxies := &codexHTTPRouteProxyRepo{proxy: Proxy{ID: 2, Protocol: "http", Host: "bundle.invalid", Port: 8080, Status: StatusActive, RouteGeneration: 1}}
			s := &OpenAIGatewayService{codexTurnStateService: state, codexTurnStateProxyRepo: proxies}
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			c.Request.Header.Set(responsesLiteHeader, "true")
			body := []byte(`{"model":"gpt-5","input":"hello"}`)
			s.selectOpenAIHTTPBundleRoute(context.Background(), c, account, "gpt-5", body)
			firstInput := codexStateHTTPRequest(t, string(body))
			firstInput.Header.Set(responsesLiteHeader, "true")
			first := s.prepareOpenAICodexStateHTTPRequest(c, account, firstInput)
			require.NotEmpty(t, first.Header.Get(openAICodexTurnStateHeader))
			observeCodexTurnStateHTTPResponse(first, nil, errors.New("completed rejected-field attempt"))
			state.repo = codexHTTPRetryUnavailableRepo{CodexTurnStateRepository: state.repo, nilRecord: nilRecord}
			builds := 0
			_, err := s.withOpenAIHTTPBundleBaseline(c, account, func() (*OpenAIForwardResult, error) {
				builds++
				s.selectOpenAIHTTPBundleRoute(context.Background(), c, account, "gpt-5", body)
				request := s.prepareOpenAICodexStateHTTPRequest(c, account, codexStateHTTPRequest(t, string(body)))
				if rejected, _ := request.Context().Value(codexHTTPBundleRejectedKey{}).(bool); rejected {
					return nil, openaicookies.ErrBundleSendRejected
				}
				require.Empty(t, OpenAIOutboundRouteForAccount(c, account).ProxyURL)
				require.Empty(t, request.Header.Get(openAICodexTurnStateHeader))
				return &OpenAIForwardResult{}, nil
			})
			require.NoError(t, err)
			require.Equal(t, 2, builds)
		})
	}
}

func TestCodexHTTPBundleTimezoneUsesSelectedRouteAndRebuildSource(t *testing.T) {
	for _, reject := range []bool{false, true} {
		t.Run(map[bool]string{false: "bundle", true: "baseline_rebuild"}[reject], func(t *testing.T) {
			state, _, account := newCodexStateTestService(t)
			state.now = time.Now
			seedCodexHTTPRouteBundle(t, state, account, "gpt-5.4", "lite")
			proxies := &codexHTTPRouteProxyRepo{proxy: Proxy{ID: 2, Protocol: "http", Host: "bundle.invalid", Port: 8080, Status: StatusActive, RouteGeneration: 1}}
			if reject {
				proxies.changeAt = 3
			}
			resolver := NewOpenAIEgressLocationService(nil)
			t.Cleanup(resolver.Stop)
			resolver.ObserveResult(OpenAIEgressRoute{ProxyID: 2, ProxyURL: proxies.proxy.URL()}, &ProxyExitInfo{IP: "203.0.113.7", Country: "Japan", CountryCode: "JP", Region: "Tokyo", City: "Tokyo", Timezone: "Asia/Tokyo", GeoStatus: "success", GeoCheckedAt: time.Now()}, nil)
			upstream := &codexStateCookieIntegrationUpstream{httpUpstreamRecorder: &httpUpstreamRecorder{resp: openAICompatSSECompletedResponse("resp_timezone", "gpt-5.4")}, manager: openaicookies.NewManager(), account: account}
			s := &OpenAIGatewayService{cfg: &config.Config{}, accountRepo: state.accounts, httpUpstream: upstream, codexTurnStateService: state, codexTurnStateProxyRepo: proxies, egressLocationService: resolver}
			body := timezoneTestBody(t, map[string]any{"model": "gpt-5.4", "instructions": "Reply briefly.", "input": timezoneTestInput(timezoneTestEnvironment("Asia/Shanghai", "2026-09-10")), "stream": true})
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			c.Request.Header.Set(responsesLiteHeader, "true")
			c.Request = c.Request.WithContext(openai.WithRequestPolicy(c.Request.Context(), timezoneTestPolicy()))
			s.CaptureOpenAIRequestTimezone(c, body)
			capture, _ := c.Get(openAIRequestTimezoneCaptureKey)
			capture.(*openAIRequestTimezoneCapture).acceptedAt = timezoneTestAcceptedAt()
			_, err := s.Forward(context.Background(), c, account, body)
			require.NoError(t, err)
			require.Len(t, upstream.requests, 1)
			scan := ScanOpenAIRequestTimezones(upstream.lastBody)
			require.NotEmpty(t, scan.Items)
			expected := "Asia/Tokyo"
			if reject {
				expected = "America/Los_Angeles"
			}
			require.Equal(t, expected, scan.Items[0].Value)
			timezone, ok := RequestTimezoneStateFromContext(c)
			require.True(t, ok)
			require.Equal(t, expected, timezone.EgressLocation.Timezone)
		})
	}
}

func TestCodexHTTPBundleProxyPreservesThreeOSIdentityAndTelemetry(t *testing.T) {
	for _, family := range []string{"windows", "macos", "linux"} {
		t.Run(family, func(t *testing.T) {
			state, _, accounts := newCodexStateOSService(t)
			state.now = time.Now
			issuer, err := ResolveOpenAIOAuthCredentialAccount(context.Background(), accounts, accounts.owner, "windows")
			require.NoError(t, err)
			token := seedCodexHTTPRouteBundle(t, state, issuer, "gpt-5.4", "lite")
			account, err := ResolveOpenAIOAuthCredentialAccount(context.Background(), accounts, accounts.owner, family)
			require.NoError(t, err)
			proxies := &codexHTTPRouteProxyRepo{proxy: Proxy{ID: 2, Protocol: "http", Host: "bundle.invalid", Port: 8080, Status: StatusActive, RouteGeneration: 1}}
			upstream := &codexStateCookieIntegrationUpstream{httpUpstreamRecorder: &httpUpstreamRecorder{resp: openAICompatSSECompletedResponse("resp_os", "gpt-5.4")}, manager: openaicookies.NewManager(), account: account}
			s := &OpenAIGatewayService{cfg: &config.Config{}, accountRepo: accounts, httpUpstream: upstream, codexTurnStateService: state, codexTurnStateProxyRepo: proxies}
			body := codexStateHTTPIntegrationBody("responses", true)
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			c.Request.Header.Set(responsesLiteHeader, "true")
			c.Request.Header.Set("User-Agent", account.GetOpenAIUserAgent())
			_, err = s.Forward(context.Background(), c, account, body)
			require.NoError(t, err)
			require.Equal(t, proxies.proxy.URL(), upstream.lastProxyURL)
			require.Equal(t, token, upstream.lastReq.Header.Get(openAICodexTurnStateHeader))
			require.Equal(t, family, openai.DetectOSFamilyFromUserAgent(upstream.lastReq.UserAgent()))
			scope, enabled := codexnative.ScopeFromContext(upstream.lastReq.Context())
			require.True(t, enabled)
			require.Equal(t, codexnative.Platform(family), codexnative.Resolve(upstream.lastReq.UserAgent(), scope).Platform)
			caller, ok := upstream.lastReq.Context().Value(codexTelemetryHTTPContextKey{}).(context.Context)
			require.True(t, ok)
			telemetry, ok := caller.Value(codexTelemetryGatewayContextKey{}).(codexTelemetryGatewaySnapshot)
			require.True(t, ok)
			require.Equal(t, OpenAIEgressRoute{ProxyID: 2, ProxyURL: proxies.proxy.URL()}, telemetry.route)
			require.Equal(t, family, telemetry.os)
			require.Equal(t, family, telemetry.credentialOS)
			require.Nil(t, accounts.owner.ProxyID)
		})
	}
}

func TestCodexHTTPBundleSparkUsesOwnerBundleAndAuthorization(t *testing.T) {
	state, _, accounts := newCodexStateOSService(t)
	owner, err := ResolveOpenAIOAuthCredentialAccount(context.Background(), accounts, accounts.owner, "windows")
	require.NoError(t, err)
	token := seedCodexHTTPRouteBundle(t, state, owner, "gpt-5", "lite")
	shadow := &Account{ID: 9, Platform: PlatformOpenAI, Type: AccountTypeOAuth, ParentAccountID: &owner.ID, QuotaDimension: QuotaDimensionSpark}
	shadow, err = ResolveOpenAIOAuthCredentialAccount(context.Background(), accounts, shadow, "linux")
	require.NoError(t, err)
	proxies := &codexHTTPRouteProxyRepo{proxy: Proxy{ID: 2, Protocol: "http", Host: "bundle.invalid", Port: 8080, Status: StatusActive, RouteGeneration: 1}}
	s := &OpenAIGatewayService{accountRepo: accounts, codexTurnStateService: state, codexTurnStateProxyRepo: proxies}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	c.Request.Header.Set(responsesLiteHeader, "true")
	selected := s.selectOpenAIHTTPBundleRoute(context.Background(), c, shadow, "gpt-5", []byte(`{"model":"gpt-5","input":"hello"}`))
	require.Equal(t, owner.ID, selected.attempt.OwnerAccountID)
	require.Equal(t, "linux", selected.attempt.OSFamily)
	require.Equal(t, token, selected.attempt.Snapshot.Token)
	require.Equal(t, proxies.proxy.URL(), selected.route.ProxyURL)
	headers := http.Header{"Authorization": {"Bearer " + owner.GetCredential("access_token")}}
	require.True(t, state.ValidateCredentialHeaders(context.Background(), selected.attempt, headers))
	headers.Set("Authorization", "Bearer stale-shadow-token")
	require.False(t, state.ValidateCredentialHeaders(context.Background(), selected.attempt, headers))
	finishCodexTurnStateHTTPAttempt(state, selected.attempt, false)
}

func TestCodexHTTPBundleBaselineKeepsFrozenLiteDecisionAcrossManifestChange(t *testing.T) {
	state, _, account := newCodexStateTestService(t)
	s := &OpenAIGatewayService{codexTurnStateService: state}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	modelNamespace := openAICodexModelCapabilitiesNamespace(account)
	s.codexModelCapabilities.observeManifest(modelNamespace, []byte(`{"models":[{"slug":"gpt-5","use_responses_lite":true}]}`), time.Now())
	builds := 0
	_, err := s.withOpenAIHTTPBundleBaseline(c, account, func() (*OpenAIForwardResult, error) {
		builds++
		selected := s.selectOpenAIHTTPBundleRoute(context.Background(), c, account, "gpt-5", []byte(`{"model":"gpt-5","input":"hello"}`))
		require.True(t, selected.capabilities.UseResponsesLite)
		if builds == 1 {
			s.codexModelCapabilities.observeManifest(modelNamespace, []byte(`{"models":[{"slug":"gpt-5","use_responses_lite":false}]}`), time.Now())
			return nil, openaicookies.ErrBundleSendRejected
		}
		require.False(t, s.openAICodexModelCapabilities(modelNamespace, "gpt-5").UseResponsesLite)
		require.Nil(t, selected.attempt)
		return &OpenAIForwardResult{}, nil
	})
	require.NoError(t, err)
	require.Equal(t, 2, builds)
}

func TestCodexHTTPBundleBridgeCreatesIndependentFrameSnapshots(t *testing.T) {
	state, _, account := newCodexStateTestService(t)
	seedCodexHTTPRouteBundle(t, state, account, "gpt-5", "lite")
	proxies := &codexHTTPRouteProxyRepo{proxy: Proxy{ID: 2, Protocol: "http", Host: "bundle.invalid", Port: 8080, Status: StatusActive, RouteGeneration: 1}}
	s := &OpenAIGatewayService{codexTurnStateService: state, codexTurnStateProxyRepo: proxies}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
	c.Request.Header.Set(responsesLiteHeader, "true")
	SetOpenAIClientTransport(c, OpenAIClientTransportWS)
	firstBody := []byte(`{"type":"response.create","model":"gpt-5","input":"first"}`)
	s.prepareOpenAIHTTPBridgeBundleRoute(context.Background(), c, account, firstBody, true)
	first := codexHTTPRouteSelectionFromContext(c, account)
	require.NotEmpty(t, first.attempt.Snapshot.Token)
	require.Equal(t, int64(2), OpenAIOutboundRouteForAccount(c, account).ProxyID)
	request := codexStateHTTPRequest(t, string(firstBody))
	request.Header.Set(responsesLiteHeader, "true")
	request = s.prepareOpenAICodexStateHTTPRequest(c, account, request)
	observeCodexTurnStateHTTPResponse(request, nil, errors.New("finished frame"))
	secondBody := []byte(`{"type":"response.create","model":"other-model","input":"second"}`)
	s.prepareOpenAIHTTPBridgeBundleRoute(context.Background(), c, account, secondBody, true)
	second := codexHTTPRouteSelectionFromContext(c, account)
	require.NotSame(t, first, second)
	require.NotEqual(t, first.attempt.id, second.attempt.id)
	require.Empty(t, second.attempt.Snapshot.Token)
	require.Empty(t, OpenAIOutboundRouteForAccount(c, account).ProxyURL)
	require.Equal(t, "other-model", second.attempt.Model)
	finishCodexTurnStateHTTPAttempt(state, second.attempt, false)
}
