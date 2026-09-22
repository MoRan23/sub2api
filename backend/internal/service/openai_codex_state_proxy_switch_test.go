package service

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openaicookies"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type codexProxySwitchRepository struct {
	ProxyRepository
	proxies map[int64]*Proxy
}

func (r codexProxySwitchRepository) GetByID(_ context.Context, id int64) (*Proxy, error) {
	proxy := r.proxies[id]
	if proxy == nil {
		return nil, errors.New("fixture proxy not found")
	}
	copy := *proxy
	return &copy, nil
}

func codexProxySwitchSet(account *Account, enabled bool) {
	account.Extra[CodexTurnStateExtraKey].(map[string]any)["use_ticket_proxy"] = enabled
}

// Seed a complete, encrypted package, including a scoped Cookie. Tests then use
// the normal gateway and Cookie RoundTripper boundary without external traffic.
func codexProxySwitchSeed(t *testing.T, state *CodexTurnStateService, account *Account) string {
	t.Helper()
	token := seedCodexHTTPRouteBundle(t, state, account, "gpt-5.4", "lite")
	key := CodexTurnStateKey{OwnerAccountID: account.ID, Model: "gpt-5.4", Generation: CodexTurnStateGenerationForAccount(account)}
	record, err := state.repo.Get(context.Background(), key)
	require.NoError(t, err)
	require.NotNil(t, record)
	bundle := codexCookieAtomicTestBundle(record.IssuedAt, "synthetic-proxy-switch-cookie")
	bundle.ExpiresAt = record.ExpiresAt
	bundle.Entries[0].ExpiresAt = record.ExpiresAt
	publication, err := state.encryptCodexCookiePublication(key, record.AuthorizationGeneration, bundle, record.BundleBinding)
	require.NoError(t, err)
	applyCodexTurnStateCookiePublication(record, publication)
	updated, err := state.repo.SaveCAS(context.Background(), *record, record.Version)
	require.NoError(t, err)
	require.True(t, updated)
	return token
}

func codexProxySwitchForward(t *testing.T, gateway *OpenAIGatewayService, account *Account, path string) *gin.Context {
	t.Helper()
	body := codexStateHTTPIntegrationBody(path, true)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	url := "/v1/responses"
	if path == "chat" {
		url = "/v1/chat/completions"
	} else if path == "messages" {
		url = "/v1/messages"
	}
	c.Request = httptest.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	var err error
	switch path {
	case "chat":
		_, err = gateway.ForwardAsChatCompletions(context.Background(), c, account, body, "", "")
	case "messages":
		_, err = gateway.ForwardAsAnthropic(context.Background(), c, account, body, "", "")
	default:
		_, err = gateway.Forward(context.Background(), c, account, body)
	}
	require.NoError(t, err, recorder.Body.String())
	return c
}

func TestCodexTicketProxySwitchAllHTTPPathsPreserveBundleAndObserveActualRoute(t *testing.T) {
	for _, path := range []string{"responses", "passthrough", "chat", "messages"} {
		for _, accountProxy := range []bool{false, true} {
			name := path + "/direct"
			if accountProxy {
				name = path + "/account_proxy"
			}
			t.Run(name, func(t *testing.T) {
				isolateCodexHistory(t)
				isolateCodexTurnStateSummaryStore(t)
				state, repo, account := newCodexStateTestService(t)
				state.now = time.Now
				token := codexProxySwitchSeed(t, state, account)
				proxies := codexProxySwitchRepository{proxies: map[int64]*Proxy{
					2: {ID: 2, Protocol: "http", Host: "ticket.invalid", Port: 8080, Status: StatusActive, RouteGeneration: 1},
					7: {ID: 7, Protocol: "http", Host: "account.invalid", Port: 8081, Status: StatusActive, RouteGeneration: 3},
				}}
				if accountProxy {
					account.Proxy = proxies.proxies[7]
					account.ProxyID = &account.Proxy.ID
				}
				if path == "passthrough" {
					account.Extra["openai_passthrough"] = true
				}
				upstream := &codexStateCookieIntegrationUpstream{httpUpstreamRecorder: &httpUpstreamRecorder{}, manager: openaicookies.NewManager(), account: account}
				gateway := &OpenAIGatewayService{cfg: &config.Config{}, accountRepo: state.accounts, httpUpstream: upstream, codexTurnStateService: state, codexTurnStateProxyRepo: proxies}
				gateway.codexModelCapabilities.observeManifest(openAICodexModelCapabilitiesNamespace(account), []byte(`{"models":[{"slug":"gpt-5.4","use_responses_lite":true}]}`), time.Now())
				key := CodexTurnStateKey{OwnerAccountID: account.ID, Model: "gpt-5.4", Generation: CodexTurnStateGenerationForAccount(account)}
				before, err := repo.Get(context.Background(), key)
				require.NoError(t, err)
				for index, enabled := range []bool{false, true, false} {
					codexProxySwitchSet(account, enabled)
					upstream.resp = openAICompatSSECompletedResponse("resp_proxy_switch", "gpt-5.4")
					c := codexProxySwitchForward(t, gateway, account, path)
					require.Len(t, upstream.requests, index+1, "switching never replays the business request")
					require.Equal(t, token, upstream.lastReq.Header.Get(openAICodexTurnStateHeader))
					require.Contains(t, upstream.lastReq.Header.Get("Cookie"), "__oailb=synthetic-proxy-switch-cookie")
					wantProxy, wantURL, wantSource := int64(0), "", "account"
					if accountProxy {
						wantProxy, wantURL = 7, proxies.proxies[7].URL()
					}
					if enabled {
						wantProxy, wantURL, wantSource = 2, proxies.proxies[2].URL(), "bundle"
					}
					require.Equal(t, wantURL, upstream.lastProxyURL)
					observation := codexStateWireObservation(c)
					require.NotNil(t, observation)
					require.Equal(t, "injected", observation.value.Action)
					require.Equal(t, wantSource, observation.value.RouteSource)
					require.NotNil(t, observation.value.ActualProxyID)
					require.Equal(t, wantProxy, *observation.value.ActualProxyID)
					require.NotNil(t, observation.value.BundleProxyID)
					require.Equal(t, int64(2), *observation.value.BundleProxyID)
					require.NotNil(t, observation.value.CookieDiagnostic)
					require.Equal(t, "sent", observation.value.CookieDiagnostic.SendState)
					require.True(t, observation.value.CookieDiagnostic.Sent)
				}
				after, err := repo.Get(context.Background(), key)
				require.NoError(t, err)
				require.Equal(t, before.cacheIdentity(), after.cacheIdentity(), "the switch only controls use of the package's route")
			})
		}
	}
}

func TestCodexTicketProxySwitchRetryKeepsFrozenPolicyAndActualRoute(t *testing.T) {
	state, _, account := newCodexStateTestService(t)
	token := codexProxySwitchSeed(t, state, account)
	codexProxySwitchSet(account, false)
	baseline := CodexTurnStateBundleBinding{WireMode: "lite", EgressKind: "direct"}
	attempt, err := state.PrepareForHTTP(context.Background(), account, "gpt-5.4", baseline)
	require.NoError(t, err)
	require.Equal(t, token, attempt.Snapshot.Token)
	require.Equal(t, baseline, attempt.OutboundBinding)
	require.True(t, attempt.keepAccountProxy)
	codexProxySwitchSet(account, true)
	retry, err := state.RetryForHTTP(context.Background(), attempt)
	require.NoError(t, err)
	require.True(t, retry.keepAccountProxy)
	require.Equal(t, baseline, retry.OutboundBinding)
	require.Equal(t, attempt.Snapshot, retry.Snapshot)
	require.True(t, state.ValidateAttempt(context.Background(), retry))
	bundle, err := state.codexCookieBundleForSnapshot(retry)
	require.NoError(t, err)
	require.Len(t, bundle.Entries, 1)
	require.NoError(t, state.Finish(context.Background(), attempt, false))
	require.NoError(t, state.Finish(context.Background(), retry, false))
}

func TestCodexTicketProxySwitchOffDoesNotApplyLiteBundleToOrdinaryResponses(t *testing.T) {
	isolateCodexHistory(t)
	isolateCodexTurnStateSummaryStore(t)
	state, repo, account := newCodexStateTestService(t)
	state.now = time.Now
	codexProxySwitchSeed(t, state, account)
	codexProxySwitchSet(account, false)
	key := CodexTurnStateKey{OwnerAccountID: account.ID, Model: "gpt-5.4", Generation: CodexTurnStateGenerationForAccount(account)}
	before, err := repo.Get(context.Background(), key)
	require.NoError(t, err)
	upstream := &codexStateCookieIntegrationUpstream{httpUpstreamRecorder: &httpUpstreamRecorder{resp: openAICompatSSECompletedResponse("resp_standard_protocol", "gpt-5.4")}, manager: openaicookies.NewManager(), account: account}
	gateway := &OpenAIGatewayService{cfg: &config.Config{}, accountRepo: state.accounts, httpUpstream: upstream, codexTurnStateService: state}
	gateway.codexModelCapabilities.observeManifest(openAICodexModelCapabilitiesNamespace(account), []byte(`{"models":[{"slug":"gpt-5.4","use_responses_lite":false}]}`), time.Now())
	c := codexProxySwitchForward(t, gateway, account, "responses")
	require.Len(t, upstream.requests, 1)
	require.Empty(t, upstream.lastReq.Header.Get(openAICodexTurnStateHeader))
	require.Empty(t, upstream.lastReq.Header.Get("Cookie"))
	require.Empty(t, upstream.lastReq.Header.Get(responsesLiteHeader))
	require.Empty(t, upstream.lastProxyURL)
	observation := codexStateWireObservation(c)
	require.NotNil(t, observation)
	require.Equal(t, "bundle_protocol_mismatch", observation.value.MaintenanceReason)
	require.Equal(t, "account", observation.value.RouteSource)
	after, err := repo.Get(context.Background(), key)
	require.NoError(t, err)
	require.Equal(t, before.cacheIdentity(), after.cacheIdentity(), "a protocol bypass preserves the Lite package")
}

func TestCodexTicketProxySwitchSparkUsesMotherPolicy(t *testing.T) {
	state, _, accounts := newCodexStateOSService(t)
	owner, err := ResolveOpenAIOAuthCredentialAccount(context.Background(), accounts, accounts.owner, "windows")
	require.NoError(t, err)
	token := codexProxySwitchSeed(t, state, owner)
	codexProxySwitchSet(accounts.owner, false)
	shadow := &Account{ID: 9, Platform: PlatformOpenAI, Type: AccountTypeOAuth, ParentAccountID: &owner.ID, QuotaDimension: QuotaDimensionSpark}
	shadow, err = ResolveOpenAIOAuthCredentialAccount(context.Background(), accounts, shadow, "linux")
	require.NoError(t, err)
	// A stale or contradictory child projection must not override the owner.
	shadow.Extra = map[string]any{CodexTurnStateExtraKey: map[string]any{"enabled": true, "use_ticket_proxy": true}}
	baseline := CodexTurnStateBundleBinding{WireMode: "lite", EgressKind: "direct"}
	attempt, err := state.PrepareForHTTP(context.Background(), shadow, "gpt-5.4", baseline)
	require.NoError(t, err)
	require.NotNil(t, attempt)
	require.Equal(t, owner.ID, attempt.OwnerAccountID)
	require.Equal(t, "linux", attempt.OSFamily)
	require.True(t, attempt.keepAccountProxy)
	require.Equal(t, token, attempt.Snapshot.Token)
	require.Equal(t, baseline, attempt.OutboundBinding)
	require.NoError(t, state.Finish(context.Background(), attempt, false))
}

func TestCodexTicketProxySwitchNaturalNewTicketUsesActualAccountRoute(t *testing.T) {
	isolateCodexHistory(t)
	isolateCodexTurnStateSummaryStore(t)
	state, repo, account := newCodexStateTestService(t)
	now := time.Now().UTC().Truncate(time.Second).Add(-10 * time.Second)
	state.now = func() time.Time { return now }
	oldToken := codexProxySwitchSeed(t, state, account)
	codexProxySwitchSet(account, false)
	proxy := &Proxy{ID: 7, Protocol: "http", Host: "account.invalid", Port: 8081, Status: StatusActive, RouteGeneration: 3}
	account.Proxy, account.ProxyID = proxy, &proxy.ID
	proxies := codexProxySwitchRepository{proxies: map[int64]*Proxy{
		2: {ID: 2, Protocol: "http", Host: "ticket.invalid", Port: 8080, Status: StatusActive, RouteGeneration: 1},
		7: proxy,
	}}
	now = now.Add(time.Second)
	newToken := codexStateTestToken(10, now)
	response := codexStateHTTPIntegrationResponse(newToken, "header", false)
	response.Header.Add("Set-Cookie", "__oailb=synthetic-natural-cookie; Path=/; Secure; Max-Age=120")
	upstream := &codexStateCookieIntegrationUpstream{httpUpstreamRecorder: &httpUpstreamRecorder{resp: response}, manager: openaicookies.NewManager(), account: account}
	gateway := &OpenAIGatewayService{cfg: &config.Config{}, accountRepo: state.accounts, httpUpstream: upstream, codexTurnStateService: state, codexTurnStateProxyRepo: proxies}
	gateway.codexModelCapabilities.observeManifest(openAICodexModelCapabilitiesNamespace(account), []byte(`{"models":[{"slug":"gpt-5.4","use_responses_lite":true}]}`), time.Now())
	codexProxySwitchForward(t, gateway, account, "responses")
	require.Equal(t, oldToken, upstream.lastReq.Header.Get(openAICodexTurnStateHeader))
	require.Equal(t, proxy.URL(), upstream.lastProxyURL)
	key := CodexTurnStateKey{OwnerAccountID: account.ID, Model: "gpt-5.4", Generation: CodexTurnStateGenerationForAccount(account)}
	record, err := repo.Get(context.Background(), key)
	require.NoError(t, err)
	decoded, err := state.encryptor.Decrypt(record.EncryptedToken)
	require.NoError(t, err)
	require.Equal(t, newToken, decoded)
	wantBinding := CodexTurnStateBundleBinding{WireMode: "lite", EgressKind: "proxy", ProxyID: 7, ProxyRouteGeneration: 3}
	require.Equal(t, wantBinding, record.BundleBinding)
	envelope := decryptCodexCookieAtomicTestEnvelope(t, state, *record)
	require.Equal(t, wantBinding, envelope.Binding)
	require.Len(t, envelope.Bundle.Entries, 1)
	require.Equal(t, "synthetic-natural-cookie", envelope.Bundle.Entries[0].Value)
}
