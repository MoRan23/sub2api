package service

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/stretchr/testify/require"
)

type auxiliaryProxyFailureRepo struct {
	ProxyRepository
	err error
}

func (r *auxiliaryProxyFailureRepo) GetByID(context.Context, int64) (*Proxy, error) {
	return nil, r.err
}

type auxiliaryProxyUpstream struct{ calls int }

func (u *auxiliaryProxyUpstream) Do(*http.Request, string, int64, int) (*http.Response, error) {
	u.calls++
	return nil, errors.New("unexpected upstream call")
}

func (u *auxiliaryProxyUpstream) DoWithTLS(req *http.Request, proxyURL string, accountID int64, concurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, proxyURL, accountID, concurrency)
}

func TestCNAuxiliaryRequestsRejectUnavailableBoundProxy(t *testing.T) {
	ctx := context.Background()
	proxyID := int64(7)
	for _, lookupErr := range []error{nil, errors.New("proxy repository unavailable")} {
		t.Run("lookup_"+map[bool]string{true: "failure", false: "missing"}[lookupErr != nil], func(t *testing.T) {
			for _, operation := range []string{"balance", "quota", "cline_balance", "cline_quota", "command_code_balance", "command_code_quota"} {
				t.Run(operation, func(t *testing.T) {
					upstream := &auxiliaryProxyUpstream{}
					repo := &auxiliaryProxyFailureRepo{err: lookupErr}
					balance := &CNProviderBalanceService{proxyRepo: repo, httpUpstream: upstream}
					quota := &CNProviderQuotaService{proxyRepo: repo, httpUpstream: upstream}
					account := &Account{ID: 1, ProxyID: &proxyID, Platform: PlatformDeepseek, Type: AccountTypeAPIKey,
						Credentials: map[string]any{"api_key": "private-account-key", "base_url": "https://relay.example.com"}}
					var err error
					switch operation {
					case "balance":
						_, err = balance.queryBalanceForAccount(ctx, account)
					case "quota":
						account.Platform = PlatformKimi
						account.Credentials["account_mode"] = AccountModeCoding
						account.Credentials["base_url"] = "https://api.kimi.com/coding/v1"
						_, err = quota.queryUsageForAccount(ctx, account)
					case "cline_balance":
						_, err = balance.queryClineBalance(ctx, account)
					case "cline_quota":
						_, err = quota.queryClineUsage(ctx, account)
					case "command_code_balance":
						_, err = balance.queryCommandCodeBalance(ctx, account)
					case "command_code_quota":
						_, err = quota.queryCommandCodeUsage(ctx, account)
					}
					require.ErrorIs(t, err, ErrAccountProxyUnavailable)
					require.Zero(t, upstream.calls, "bound proxy failures must not become direct requests")
				})
			}
		})
	}
}

func TestGrokAuxiliaryRequestsRejectUnavailableBoundProxy(t *testing.T) {
	proxyID := int64(7)
	account := &Account{ID: 1, ProxyID: &proxyID, Platform: PlatformGrok, Type: AccountTypeOAuth,
		Credentials: map[string]any{"access_token": "private-access-token"}}
	upstream := &auxiliaryProxyUpstream{}
	svc := &GrokQuotaService{
		accountRepo:   &cnBalanceProbeRepo{account: account},
		proxyRepo:     &auxiliaryProxyFailureRepo{err: errors.New("lookup failed")},
		tokenProvider: &GrokTokenProvider{}, httpUpstream: upstream,
	}
	_, _, _, err := svc.prepareProbe(context.Background(), account.ID)
	require.ErrorIs(t, err, ErrAccountProxyUnavailable)
	require.ErrorIs(t, svc.syncGrokObservedModels(context.Background(), account), ErrAccountProxyUnavailable)
	require.Zero(t, upstream.calls)
}

func TestUpstreamModelsRejectUnavailableBoundProxy(t *testing.T) {
	proxyID := int64(7)
	account := &Account{ID: 1, ProxyID: &proxyID}
	upstream := &auxiliaryProxyUpstream{}
	svc := &AccountTestService{httpUpstream: upstream}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://catalog.example.com/models", nil)
	require.NoError(t, err)
	_, err = svc.readUpstreamModelsResponse(req, account)
	require.ErrorIs(t, err, ErrAccountProxyUnavailable)
	_, err = svc.fetchModelsDevRegistry(context.Background(), account)
	require.ErrorIs(t, err, ErrAccountProxyUnavailable)
	require.Zero(t, upstream.calls)
}

func TestOpenAIModelsRejectUnavailableBoundProxy(t *testing.T) {
	upstream := &auxiliaryProxyUpstream{}
	svc := newCodexModelsAPIKeyTestService(upstream)
	account := newCodexModelsAPIKeyTestAccount("https://catalog.example.com/v1")
	proxyID := int64(7)
	account.ProxyID = &proxyID
	_, err := svc.FetchOpenAIModelsList(context.Background(), account)
	require.ErrorIs(t, err, ErrAccountProxyUnavailable)
	require.Zero(t, upstream.calls)
}

func TestVertexTokenExchangeRejectsUnavailableBoundProxy(t *testing.T) {
	proxyID := int64(7)
	account := &Account{ID: 1, ProxyID: &proxyID, Platform: PlatformGemini, Type: AccountTypeServiceAccount,
		Credentials: map[string]any{"service_account_json": `{"type":"service_account","project_id":"project","client_email":"test@example.com","private_key":"not-a-real-private-key"}`}}
	_, err := getVertexServiceAccountAccessToken(context.Background(), nil, account)
	// The invalid PEM would fail during exchange. A routing error must stop first.
	require.ErrorIs(t, err, ErrAccountProxyUnavailable)
}

func TestWebSearchRejectsUnavailableBoundProxy(t *testing.T) {
	proxyID := int64(7)
	account := &Account{ID: 1, ProxyID: &proxyID}
	_, _, err := doWebSearch(context.Background(), account, "test")
	require.ErrorIs(t, err, ErrAccountProxyUnavailable)
	_, err = writeEmulatedWebSearch(context.Background(), nil, account,
		[]byte(`{"messages":[{"role":"user","content":"test"}]}`), "model", false)
	var failover *UpstreamFailoverError
	require.ErrorAs(t, err, &failover)
	require.Equal(t, http.StatusBadGateway, failover.StatusCode)
}

func TestCodexAuxiliaryRejectsUnavailableBoundProxy(t *testing.T) {
	account, repo := oauthOSCredentialFixture(t)
	proxyID := int64(7)
	account.ProxyID = &proxyID
	account.Status, account.Schedulable = StatusActive, true
	upstream := &auxiliaryProxyUpstream{}
	svc := &OpenAIGatewayService{accountRepo: repo, httpUpstream: upstream}
	_, err := svc.doCodexAuxiliaryRequest(context.Background(), nil, account,
		"/alpha/notes/v2/thread_hint", []byte(`{"context":{"session_id":"test"}}`))
	require.ErrorIs(t, err, ErrAccountProxyUnavailable)
	require.Zero(t, upstream.calls)
}

func TestLiveAuxiliaryRejectsUnavailableBoundProxy(t *testing.T) {
	account, repo := oauthOSCredentialFixture(t)
	proxyID := int64(7)
	account.ProxyID = &proxyID
	upstream := &auxiliaryProxyUpstream{}
	svc := &OpenAIGatewayService{accountRepo: repo, httpUpstream: upstream}
	_, err := svc.createUpstreamLiveCall(context.Background(), account, &LiveCallRequest{SDP: "test"}, "test")
	require.ErrorIs(t, err, ErrAccountProxyUnavailable)
	_, err = svc.dialLiveSideband(context.Background(), &LiveCallRecord{
		AccountID: account.ID, CredentialOS: OpenAIOSLinux, CredentialOwnerID: account.ID,
		AuthorizationGeneration: "shared-generation", CallID: "test-call",
	})
	require.ErrorIs(t, err, ErrAccountProxyUnavailable)
	require.Zero(t, upstream.calls)
}

func TestPluginIdentityRejectsUnavailableBoundProxy(t *testing.T) {
	account, repo := oauthOSCredentialFixture(t)
	proxyID := int64(7)
	account.ProxyID = &proxyID
	account.Status = StatusActive
	svc := &OpenAIGatewayService{accountRepo: repo}
	identity, err := svc.ResolvePluginOutboundIdentity(context.Background(), pluginOAuthTestScope(), account.ID)
	require.ErrorIs(t, err, ErrAccountProxyUnavailable)
	require.Nil(t, identity, "a configured proxy failure must not issue a direct outbound identity")
}

func TestCodexModelsRejectUnavailableBoundProxyBeforeTokenLookup(t *testing.T) {
	proxyID := int64(7)
	account := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth, ProxyID: &proxyID}
	upstream := &auxiliaryProxyUpstream{}
	svc := &OpenAIGatewayService{httpUpstream: upstream}
	for _, auxiliary := range []bool{false, true} {
		_, err := svc.fetchCodexModelsManifest(context.Background(), account, "", "", auxiliary)
		require.ErrorIs(t, err, ErrAccountProxyUnavailable)
	}
	require.Zero(t, upstream.calls)
	require.Empty(t, account.ErrorMessage)
}

func TestGeminiModelsRejectUnavailableBoundProxyBeforeTokenLookup(t *testing.T) {
	proxyID := int64(7)
	account := &Account{ID: 1, Platform: PlatformGemini, Type: AccountTypeOAuth, ProxyID: &proxyID}
	upstream := &auxiliaryProxyUpstream{}
	svc := &GeminiMessagesCompatService{httpUpstream: upstream}
	_, err := svc.forwardGeminiModelsGET(context.Background(), account, "/v1beta/models")
	require.ErrorIs(t, err, ErrAccountProxyUnavailable)
	_, _, err = svc.listVertexGeminiModels(context.Background(), account, "global")
	require.ErrorIs(t, err, ErrAccountProxyUnavailable)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://catalog.example.com/v1beta/models", nil)
	require.NoError(t, err)
	_, err = svc.doGeminiModelsGET(req, account)
	require.ErrorIs(t, err, ErrAccountProxyUnavailable)
	require.Zero(t, upstream.calls)
	require.Empty(t, account.ErrorMessage)
}
