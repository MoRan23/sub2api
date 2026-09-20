package service

import (
	"context"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/codexnative"
	"github.com/stretchr/testify/require"
)

type codexCollectorTransportAccounts struct {
	AccountRepository
	account *Account
}

func (r codexCollectorTransportAccounts) GetByID(context.Context, int64) (*Account, error) {
	return r.account, nil
}

type codexCollectorTransportProxies struct {
	ProxyRepository
	proxy *Proxy
}

func (r codexCollectorTransportProxies) GetByID(context.Context, int64) (*Proxy, error) {
	return r.proxy, nil
}

type codexCollectorTransportUpstream struct {
	HTTPUpstream
	request   *http.Request
	proxyURL  string
	accountID int64
	calls     int
}

func (u *codexCollectorTransportUpstream) Do(request *http.Request, proxyURL string, accountID int64, _ int) (*http.Response, error) {
	u.request, u.proxyURL, u.accountID = request, proxyURL, accountID
	u.calls++
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(""))}, nil
}

func codexCollectorTransportFixture() (*Account, *Proxy) {
	businessProxyID := int64(99)
	account := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, ProxyID: &businessProxyID,
		Credentials: map[string]any{"access_token": "test-only-access", "chatgpt_account_id": "test-owner", "plan_type": "plus"},
		Extra:       map[string]any{CodexTurnStateExtraKey: map[string]any{"enabled": true, "account_type": "personal", "collector_proxy_id": float64(2)}, CodexTurnStateGenerationExtraKey: "generation-1"}}
	proxy := &Proxy{ID: 2, Protocol: "http", Host: "collector.invalid", Port: 8080, Status: StatusActive}
	return account, proxy
}

func allowCodexCollectorTestModelPolicy(context.Context) bool { return true }

func TestCodexTurnStateCollectorTransportIsolatedExplicitProxy(t *testing.T) {
	account, proxy := codexCollectorTransportFixture()
	upstream := &codexCollectorTransportUpstream{}
	do := ProvideCodexTurnStateCollectorHTTPDo(codexCollectorTransportAccounts{account: account}, codexCollectorTransportProxies{proxy: proxy}, upstream)
	request, err := http.NewRequest(http.MethodPost, chatgptCodexURL, strings.NewReader("{}"))
	require.NoError(t, err)
	request.Header["x-Codex-Turn-state"] = []string{"must-not-be-sent"}
	var sentAt time.Time
	response, err := do(context.Background(), CodexTurnStateCollectRequest{Account: account, Model: "gpt-5.4", ProxyID: 2, validateModelPolicy: allowCodexCollectorTestModelPolicy,
		onSend: func(at time.Time) { sentAt = at }}, request)
	require.NoError(t, err)
	require.False(t, sentAt.IsZero())
	require.NoError(t, response.Body.Close())
	require.Equal(t, 1, upstream.calls)
	require.Equal(t, proxy.URL(), upstream.proxyURL)
	require.Equal(t, account.ID, upstream.accountID)
	require.True(t, HTTPUpstreamRedirectsDisabled(upstream.request.Context()))
	require.Equal(t, HTTPUpstreamProfileCodexAuxiliary, HTTPUpstreamProfileFromContext(upstream.request.Context()))
	scope, ok := codexnative.ScopeFromContext(upstream.request.Context())
	require.True(t, ok)
	require.Equal(t, "turn_state_collector", scope.Purpose)
	require.Equal(t, account.ID, scope.AccountID)
	require.Equal(t, "Bearer test-only-access", upstream.request.Header.Get("Authorization"))
	require.Equal(t, "test-owner", upstream.request.Header.Get("ChatGPT-Account-Id"))
	require.NotEmpty(t, upstream.request.Header.Get("User-Agent"))
	require.NotEmpty(t, upstream.request.Header.Get("Version"))
	for key := range upstream.request.Header {
		require.False(t, strings.EqualFold(key, "x-codex-turn-state"))
	}
	// Cloning must not mutate a caller-owned request or its original headers.
	require.Equal(t, []string{"must-not-be-sent"}, request.Header["x-Codex-Turn-state"])
}

func TestCodexTurnStateCollectorTransportRejectsUnavailableProxyWithoutFallback(t *testing.T) {
	for _, mode := range []string{"missing", "inactive", "expired"} {
		t.Run(mode, func(t *testing.T) {
			account, proxy := codexCollectorTransportFixture()
			proxy.FallbackMode = FallbackModeDirect
			switch mode {
			case "missing":
				proxy = nil
			case "inactive":
				proxy.Status = "disabled"
			case "expired":
				expired := time.Now().Add(-time.Minute)
				proxy.ExpiresAt = &expired
			}
			upstream := &codexCollectorTransportUpstream{}
			do := ProvideCodexTurnStateCollectorHTTPDo(codexCollectorTransportAccounts{account: account}, codexCollectorTransportProxies{proxy: proxy}, upstream)
			request, err := http.NewRequest(http.MethodPost, chatgptCodexURL, nil)
			require.NoError(t, err)
			_, err = do(context.Background(), CodexTurnStateCollectRequest{Account: account, ProxyID: 2, validateModelPolicy: allowCodexCollectorTestModelPolicy,
				onSend: func(time.Time) { t.Fatal("unavailable proxy must not record a send") }}, request)
			require.ErrorIs(t, err, ErrCodexTurnStateCollectorProxyUnavailable)
			require.Zero(t, upstream.calls)
		})
	}
}

func TestCodexTurnStateCollectorTransportRechecksGenerationAndDestination(t *testing.T) {
	account, proxy := codexCollectorTransportFixture()
	old := *account
	old.Extra = map[string]any{CodexTurnStateGenerationExtraKey: "previous-generation"}
	upstream := &codexCollectorTransportUpstream{}
	do := ProvideCodexTurnStateCollectorHTTPDo(codexCollectorTransportAccounts{account: account}, codexCollectorTransportProxies{proxy: proxy}, upstream)
	request, err := http.NewRequest(http.MethodPost, chatgptCodexURL, nil)
	require.NoError(t, err)
	_, err = do(context.Background(), CodexTurnStateCollectRequest{Account: &old, ProxyID: 2, validateModelPolicy: allowCodexCollectorTestModelPolicy}, request)
	require.ErrorContains(t, err, "configuration_changed")
	request.URL.Host = "unrelated.invalid"
	_, err = do(context.Background(), CodexTurnStateCollectRequest{Account: account, ProxyID: 2, validateModelPolicy: allowCodexCollectorTestModelPolicy}, request)
	require.ErrorContains(t, err, "invalid_request")
	require.Zero(t, upstream.calls)
}

func TestCodexTurnStateCollectorTransportRequiresFinalModelPolicyCheck(t *testing.T) {
	for _, mode := range []string{"missing", "excluded"} {
		t.Run(mode, func(t *testing.T) {
			account, proxy := codexCollectorTransportFixture()
			upstream := &codexCollectorTransportUpstream{}
			do := ProvideCodexTurnStateCollectorHTTPDo(codexCollectorTransportAccounts{account: account}, codexCollectorTransportProxies{proxy: proxy}, upstream)
			request, err := http.NewRequest(http.MethodPost, chatgptCodexURL, nil)
			require.NoError(t, err)
			input := CodexTurnStateCollectRequest{Account: account, Model: "gpt-5.4", ProxyID: proxy.ID}
			checked := false
			if mode == "excluded" {
				input.validateModelPolicy = func(context.Context) bool { checked = true; return false }
			}
			_, err = do(context.Background(), input, request)
			require.Error(t, err)
			require.Equal(t, mode == "excluded", checked)
			require.Zero(t, upstream.calls, "final policy rejection prevents actual upstream transport")
		})
	}
}

func TestCodexTurnStateCollectorTransportRechecksLiveAccountEligibility(t *testing.T) {
	for _, mode := range []string{"inactive", "scheduling_disabled", "expired"} {
		t.Run(mode, func(t *testing.T) {
			account, proxy := codexCollectorTransportFixture()
			prepared := *account
			// The account was eligible when this task was selected. A later admin
			// update or expiry must be honored before starting the physical request.
			switch mode {
			case "inactive":
				account.Status = "disabled"
			case "scheduling_disabled":
				account.Schedulable = false
			case "expired":
				expired := time.Now().Add(-time.Minute)
				account.ExpiresAt = &expired
			}
			upstream := &codexCollectorTransportUpstream{}
			do := ProvideCodexTurnStateCollectorHTTPDo(codexCollectorTransportAccounts{account: account}, codexCollectorTransportProxies{proxy: proxy}, upstream)
			request, err := http.NewRequest(http.MethodPost, chatgptCodexURL, nil)
			require.NoError(t, err)
			_, err = do(context.Background(), CodexTurnStateCollectRequest{Account: &prepared, Model: "gpt-5.4", ProxyID: proxy.ID,
				validateModelPolicy: allowCodexCollectorTestModelPolicy,
				onSend: func(time.Time) { t.Error("an ineligible account must not record a send") }}, request)
			require.ErrorContains(t, err, "collector_account_unavailable")
			require.Zero(t, upstream.calls, "a stale eligible account cannot authorize collection")
		})
	}
}

func TestCodexTurnStateCollectorTransportUsesSelectedListMember(t *testing.T) {
	for _, id := range []int64{3, 4} {
		t.Run(strconv.FormatInt(id, 10), func(t *testing.T) {
			account, proxy := codexCollectorTransportFixture()
			account.Extra[CodexTurnStateExtraKey].(map[string]any)["collector_proxy_ids"] = []any{float64(2), float64(3)}
			proxy.ID = id
			upstream := &codexCollectorTransportUpstream{}
			do := ProvideCodexTurnStateCollectorHTTPDo(codexCollectorTransportAccounts{account: account}, codexCollectorTransportProxies{proxy: proxy}, upstream)
			request, err := http.NewRequest(http.MethodPost, chatgptCodexURL, nil)
			require.NoError(t, err)
			response, err := do(context.Background(), CodexTurnStateCollectRequest{Account: account, ProxyID: id, validateModelPolicy: allowCodexCollectorTestModelPolicy}, request)
			if id == 3 {
				require.NoError(t, err)
				require.NoError(t, response.Body.Close())
				require.Equal(t, 1, upstream.calls)
			} else {
				require.ErrorContains(t, err, "configuration_changed")
				require.Zero(t, upstream.calls)
			}
		})
	}
}
