package service

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/codexnative"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/stretchr/testify/require"
)

type nativeScopeHTTPUpstream struct {
	mu          sync.Mutex
	requests    []*http.Request
	accountIDs  []int64
	concurrency []int
}

func (u *nativeScopeHTTPUpstream) Do(req *http.Request, _ string, accountID int64, concurrency int) (*http.Response, error) {
	u.mu.Lock()
	u.requests = append(u.requests, req)
	u.accountIDs = append(u.accountIDs, accountID)
	u.concurrency = append(u.concurrency, concurrency)
	u.mu.Unlock()
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("{}"))}, nil
}

func (u *nativeScopeHTTPUpstream) DoWithTLS(req *http.Request, proxy string, accountID int64, concurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, proxy, accountID, concurrency)
}

func TestWithOpenAINativeHTTPScopeFreezesHints(t *testing.T) {
	ctx := codexnative.WithScope(context.Background(), codexnative.Scope{SourceUserAgent: "source (Linux)", CanonicalUserAgent: "canonical (Windows)", Purpose: "quota"})
	account := &Account{ID: 10, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"user_agent": "account (Mac OS 26.4.1; arm64)"}}
	gotCtx := WithOpenAINativeHTTPScope(ctx, account, "")
	account.Credentials["user_agent"] = "changed (Windows)"
	scope, ok := codexnative.ScopeFromContext(gotCtx)
	require.True(t, ok)
	require.Equal(t, int64(10), scope.AccountID)
	require.Equal(t, "account (Mac OS 26.4.1; arm64)", scope.AccountUserAgent)
	require.Equal(t, "source (Linux)", scope.SourceUserAgent)
	require.Equal(t, "canonical (Windows)", scope.CanonicalUserAgent)
	require.Equal(t, "quota", scope.Purpose)
	// Explicit authentication before an account exists retains source identity.
	authScope, ok := codexnative.ScopeFromContext(WithOpenAINativeHTTPScope(gotCtx, nil, ""))
	require.True(t, ok)
	require.Equal(t, scope, authScope)
	for _, account := range []*Account{
		{Platform: PlatformOpenAI, Type: AccountTypeAPIKey},
		{Platform: PlatformAnthropic, Type: AccountTypeOAuth},
	} {
		_, ok := codexnative.ScopeFromContext(WithOpenAINativeHTTPScope(context.Background(), account, "Linux"))
		require.False(t, ok)
		_, ok = codexnative.ScopeFromContext(WithOpenAINativeHTTPScope(gotCtx, account, "Linux"))
		require.False(t, ok, "switching to API key or another provider must clear inherited OAuth scope")
	}
}

func TestOpenAINativeHTTPGatewayKeepsFinalUAAndAccountLimits(t *testing.T) {
	for _, accountType := range []string{AccountTypeOAuth, AccountTypeSetupToken, AccountTypeAPIKey} {
		t.Run(accountType, func(t *testing.T) {
			upstream := &nativeScopeHTTPUpstream{}
			gateway := &OpenAIGatewayService{httpUpstream: upstream}
			account := &Account{ID: 17, Platform: PlatformOpenAI, Type: accountType, Concurrency: 7, Credentials: map[string]any{"user_agent": "account (Windows)"}}
			req, err := http.NewRequest(http.MethodPost, "https://chatgpt.com/backend-api/codex/responses", strings.NewReader(`{"input":"unchanged"}`))
			require.NoError(t, err)
			req.Header.Set("User-Agent", "custom (Mac OS 26.6.2; arm64)")
			req.Header.Set("Authorization", "Bearer test-token")
			req.Header.Set("Session-Id", "actual-session")
			if accountType == AccountTypeAPIKey {
				req = req.WithContext(codexnative.WithScope(req.Context(), codexnative.Scope{AccountID: 99, Purpose: "previous-oauth-attempt"}))
			}
			resp, err := gateway.doOpenAIUpstream(req, "", account)
			require.NoError(t, err)
			require.NoError(t, resp.Body.Close())
			require.Len(t, upstream.requests, 1)
			outbound := upstream.requests[0]
			scope, ok := codexnative.ScopeFromContext(outbound.Context())
			require.Equal(t, accountType != AccountTypeAPIKey, ok)
			if ok {
				require.Equal(t, "account (Windows)", scope.AccountUserAgent)
				require.Equal(t, codexnative.MacOS, codexnative.Resolve(outbound.UserAgent(), scope).Platform)
			}
			require.Equal(t, req.Header.Get("User-Agent"), outbound.Header.Get("User-Agent"))
			require.Equal(t, "Bearer test-token", outbound.Header.Get("Authorization"))
			require.Equal(t, "actual-session", outbound.Header.Get("Session-Id"))
			body, err := io.ReadAll(outbound.Body)
			require.NoError(t, err)
			require.Equal(t, `{"input":"unchanged"}`, string(body))
			require.Equal(t, []int64{17}, upstream.accountIDs)
			require.Equal(t, []int{7}, upstream.concurrency)
			_, originalTagged := codexnative.ScopeFromContext(req.Context())
			require.Equal(t, accountType == AccountTypeAPIKey, originalTagged)
		})
	}
}

func TestOpenAINativeHTTPScopeUsesCredentialOwner(t *testing.T) {
	owner := &Account{ID: 20, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"user_agent": "owner (Windows)"}}
	shadow := &Account{ID: 21, Platform: PlatformOpenAI, Type: AccountTypeOAuth, ParentAccountID: &owner.ID, Credentials: map[string]any{"user_agent": "wrong shadow (Linux)"}}
	req, err := http.NewRequest(http.MethodGet, "https://chatgpt.com/backend-api/codex/models", nil)
	require.NoError(t, err)
	out := withOpenAINativeHTTPRequestScope(req, shadow, &openAIEnvironmentAdminRepoStub{account: owner}, "models")
	scope, ok := codexnative.ScopeFromContext(out.Context())
	require.True(t, ok)
	require.Equal(t, owner.ID, scope.AccountID)
	require.Equal(t, "owner (Windows)", scope.AccountUserAgent)
	owner.Credentials["user_agent"] = "new (Mac OS)"
	require.Equal(t, "owner (Windows)", scope.AccountUserAgent)
}

func TestOpenAINativeHTTPConcurrentAccountScopesRemainSeparate(t *testing.T) {
	upstream := &nativeScopeHTTPUpstream{}
	gateway := &OpenAIGatewayService{httpUpstream: upstream}
	var wg sync.WaitGroup
	for i := int64(1); i <= 24; i++ {
		wg.Add(1)
		go func(id int64) {
			defer wg.Done()
			account := &Account{ID: id, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
			req, err := http.NewRequest(http.MethodGet, "https://chatgpt.com/backend-api/codex/models", nil)
			if err != nil {
				t.Error(err)
				return
			}
			resp, err := gateway.doOpenAIUpstream(req, "", account)
			if err != nil {
				t.Error(err)
				return
			}
			_ = resp.Body.Close()
		}(i)
	}
	wg.Wait()
	require.Len(t, upstream.requests, 24)
	for i, req := range upstream.requests {
		scope, ok := codexnative.ScopeFromContext(req.Context())
		require.True(t, ok)
		require.Equal(t, upstream.accountIDs[i], scope.AccountID)
	}
}

func TestCodexTelemetryNativeHTTPPreservesExporterUAAndFrozenSource(t *testing.T) {
	svc := &CodexTelemetryService{analyticsURL: "https://chatgpt.com/analytics", metricsURL: "https://ab.chatgpt.com/metrics"}
	sourceUA := "codex-tui/0.154.0 (Mac OS 26.4.1; arm64)"
	scope := codexnative.Scope{AccountID: 42, SourceUserAgent: sourceUA, AccountUserAgent: "account (Windows)", CanonicalUserAgent: "canonical (Linux)", Purpose: "telemetry"}
	profile := codexTelemetryProfile{client: codexTelemetryClient{localID: 42, userAgent: sourceUA, nativeHTTPScope: scope, accessToken: "test-token"}}
	for _, metrics := range []bool{false, true} {
		code, reason, cancelled := svc.send(context.Background(), func(ctx context.Context, req *http.Request, _ CodexTelemetryInput, isMetrics bool) (*http.Response, error) {
			gotScope, ok := codexnative.ScopeFromContext(req.Context())
			require.True(t, ok)
			require.Equal(t, scope, gotScope)
			require.Equal(t, codexnative.MacOS, codexnative.Resolve(req.UserAgent(), gotScope).Platform)
			require.True(t, HTTPUpstreamRedirectsDisabled(ctx))
			if isMetrics {
				require.Equal(t, "OTel-OTLP-Exporter-Rust/0.31.0", req.UserAgent())
				require.Empty(t, req.Header.Get("Authorization"))
			} else {
				require.Equal(t, sourceUA, req.UserAgent())
				require.Equal(t, "Bearer test-token", req.Header.Get("Authorization"))
			}
			body, err := io.ReadAll(req.Body)
			require.NoError(t, err)
			require.Equal(t, `{"events":[]}`, string(body))
			return &http.Response{StatusCode: 204, Body: http.NoBody}, nil
		}, codexTelemetryJob{profile: profile, body: []byte(`{"events":[]}`), metrics: metrics})
		require.Equal(t, 204, code)
		require.Empty(t, reason)
		require.False(t, cancelled)
	}
	other := profile
	other.client.nativeHTTPScope.AccountUserAgent = "other (Linux)"
	require.NotEqual(t, codexMetricClientKey(profile), codexMetricClientKey(other))
}

func TestCodexTelemetryNativeHTTPFreezesAccountAtInference(t *testing.T) {
	t.Setenv("CODEX_TELEMETRY_ENABLED", "true")
	telemetry := NewCodexTelemetryService(nil)
	t.Cleanup(telemetry.Stop)
	gateway := &OpenAIGatewayService{codexTelemetry: telemetry}
	account := &Account{ID: 51, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"user_agent": "codex-tui/0.154.0 (Mac OS 26.4.1; arm64)"}}
	headers := make(http.Header)
	headers.Set("User-Agent", "custom-client/1.0")
	headers.Set("Authorization", "Bearer test-token")
	headers.Set("Chatgpt-Account-Id", "chatgpt-account")
	ctx := codexnative.WithScope(context.Background(), codexnative.Scope{CanonicalUserAgent: "canonical (Linux)"})
	attempt := gateway.beginCodexTelemetryFromWire(ctx, account, headers, []byte(`{"model":"gpt-6-astra","input":[]}`), "", false)
	require.NotNil(t, attempt)
	account.Credentials["user_agent"] = "new-account (Windows)"
	headers.Set("User-Agent", "changed-client (Windows)")
	scope := attempt.profile.client.nativeHTTPScope
	require.Equal(t, int64(51), scope.AccountID)
	require.Equal(t, "custom-client/1.0", scope.SourceUserAgent)
	require.Equal(t, "canonical (Linux)", scope.CanonicalUserAgent)
	require.Equal(t, codexnative.MacOS, codexnative.Resolve("OTel-OTLP-Exporter-Rust/0.31.0", scope).Platform)
	attempt.Finish(CodexTelemetryResult{Status: "failed"})
}
