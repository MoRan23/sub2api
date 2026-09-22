package service

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/stretchr/testify/require"
)

func TestLiveCreateUsesRequestedOSAuthorization(t *testing.T) {
	account, repo := oauthOSCredentialFixture(t)
	upstream := &liveHTTPUpstreamStub{}
	gateway := &OpenAIGatewayService{accountRepo: repo, httpUpstream: upstream}
	ctx := ContextWithOpenAIRequestOS(context.Background(), OpenAIRequestOS{Family: OpenAIOSLinux, Source: "user_agent", Captured: true})
	created, err := gateway.createUpstreamLiveCall(ctx, account, &LiveCallRequest{SDP: "v=0", Session: json.RawMessage(`{"model":"gpt-live"}`)}, "test-attestation")
	require.NoError(t, err)
	require.Equal(t, "Bearer linux-token", upstream.request.Header.Get("Authorization"))
	require.Equal(t, OpenAIOSLinux, created.Account.OpenAIOAuthCredentialOS)
	require.Equal(t, OpenAIOSLinux, openai.DetectOSFamilyFromUserAgent(upstream.request.Header.Get("User-Agent")))
	upstream.request = nil
	repo.slots[OpenAIOSLinux].Status = OpenAIOAuthAuthorizationUnauthorized
	_, err = gateway.createUpstreamLiveCall(ctx, account, &LiveCallRequest{SDP: "v=0", Session: json.RawMessage(`{"model":"gpt-live"}`)}, "test-attestation")
	require.ErrorIs(t, err, ErrOpenAIOAuthOSUnauthorized)
	require.Nil(t, upstream.request, "an unauthorized OS must not send using the default token")
}

func TestLiveSidebandPreservesAuthorizationAcrossDefaultChanges(t *testing.T) {
	account, repo := oauthOSCredentialFixture(t)
	gateway := &OpenAIGatewayService{accountRepo: repo}
	record := &LiveCallRecord{AccountID: account.ID, CredentialOS: OpenAIOSLinux, CredentialOwnerID: account.ID, AuthorizationGeneration: "linux-generation"}
	account.OpenAIOAuthOSProfiles.DefaultOS = OpenAIOSWindows
	resolved, err := gateway.resolveLiveCallAccount(context.Background(), record)
	require.NoError(t, err)
	require.Equal(t, "linux-token", resolved.GetOpenAIAccessToken())
	repo.slots[OpenAIOSLinux].AuthorizationGeneration = "rebound"
	_, err = gateway.resolveLiveCallAccount(context.Background(), record)
	require.ErrorIs(t, err, ErrOpenAIOAuthOSAuthorizationChanged)
	require.True(t, liveSessionEnded(err))
	_, err = gateway.resolveLiveCallAccount(context.Background(), &LiveCallRecord{AccountID: account.ID})
	require.ErrorIs(t, err, ErrOpenAIOAuthOSAuthorizationChanged)
}

type auxiliaryOSModelsRepository struct {
	*oauthOSCredentialTestRepository
}

func (r auxiliaryOSModelsRepository) ListByGroup(context.Context, int64) ([]Account, error) {
	var accounts []Account
	for _, account := range r.accounts {
		accounts = append(accounts, *account)
	}
	return accounts, nil
}

func TestPinnedModelsEnforcesRequestedOSAuthorization(t *testing.T) {
	account, repo := oauthOSCredentialFixture(t)
	account.Status, account.Schedulable = StatusActive, true
	gateway := &OpenAIGatewayService{accountRepo: auxiliaryOSModelsRepository{repo}}
	group := &Group{ID: 1, Platform: PlatformOpenAI}
	group.CodexModelsManifestConfig.Enabled = true
	group.CodexModelsManifestConfig.AccountIDs = []int64{account.ID}
	fetched := 0
	fetch := func(_ context.Context, selected *Account) (*OpenAIModelsResponse, error) {
		fetched++
		require.Equal(t, OpenAIOSLinux, selected.OpenAIOAuthCredentialOS)
		require.Equal(t, "linux-token", selected.GetOpenAIAccessToken())
		return &OpenAIModelsResponse{Body: []byte(`{"models":[]}`)}, nil
	}
	ctx := ContextWithOpenAIRequestOS(context.Background(), OpenAIRequestOS{Family: OpenAIOSMacOS, Captured: true})
	_, err := gateway.fetchPinnedOpenAIModels(ctx, group, fetch)
	require.Equal(t, http.StatusServiceUnavailable, infraerrors.Code(err))
	require.Zero(t, fetched)
	ctx = ContextWithOpenAIRequestOS(context.Background(), OpenAIRequestOS{Family: OpenAIOSLinux, Captured: true})
	_, err = gateway.fetchPinnedOpenAIModels(ctx, group, fetch)
	require.NoError(t, err)
	require.Equal(t, 1, fetched)
}

func TestModelsBackgroundRefreshRejectsReboundAuthorization(t *testing.T) {
	account, repo := oauthOSCredentialFixture(t)
	scoped, err := ResolveOpenAIOAuthCredentialAccount(context.Background(), repo, account, OpenAIOSLinux)
	require.NoError(t, err)
	request := openAIModelsRequest{credentialAccount: scoped, headers: make(http.Header), url: "https://must-not-be-requested.invalid/models"}
	firstKey := buildOpenAIModelsCacheKey(request)
	copy := *scoped
	copy.OpenAIOAuthAuthorizationGeneration = "next-generation"
	request.credentialAccount = &copy
	require.NotEqual(t, firstKey, buildOpenAIModelsCacheKey(request))
	request.credentialAccount = scoped
	repo.slots[OpenAIOSLinux].AuthorizationGeneration = "next-generation"
	gateway := &OpenAIGatewayService{accountRepo: repo}
	_, err = gateway.fetchOpenAIModelsUpstream(context.Background(), request, "")
	require.Equal(t, http.StatusServiceUnavailable, infraerrors.Code(err))
}

func TestModelsCapabilitiesAreIsolatedByOSAndAuthorization(t *testing.T) {
	account, repo := oauthOSCredentialFixture(t)
	linux, err := ResolveOpenAIOAuthCredentialAccount(context.Background(), repo, account, OpenAIOSLinux)
	require.NoError(t, err)
	windows, err := ResolveOpenAIOAuthCredentialAccount(context.Background(), repo, account, OpenAIOSWindows)
	require.NoError(t, err)
	namespace := openAICodexModelCapabilitiesNamespace(linux)
	plan := OpenAIOAuthIdentityPlan{OSOwnerID: account.ID, CredentialOS: linux.OpenAIOAuthCredentialOS, AuthorizationGeneration: linux.OpenAIOAuthAuthorizationGeneration}
	require.Equal(t, namespace, openAICodexModelCapabilitiesPlanNamespace(plan))
	require.NotEqual(t, namespace, openAICodexModelCapabilitiesNamespace(windows))
	gateway := &OpenAIGatewayService{}
	gateway.codexModelCapabilities.observeManifest(namespace, []byte(`{"models":[{"slug":"model","use_responses_lite":true}]}`), time.Now())
	require.True(t, gateway.openAICodexModelCapabilities(namespace, "model").Known)
	require.False(t, gateway.openAICodexModelCapabilities(openAICodexModelCapabilitiesNamespace(windows), "model").Known)
	plan.AuthorizationGeneration = "new-generation"
	require.False(t, gateway.openAICodexModelCapabilities(openAICodexModelCapabilitiesPlanNamespace(plan), "model").Known)
}

func TestQuotaHeadersFreezeCredentialOSAndRejectRebind(t *testing.T) {
	account, repo := oauthOSCredentialFixture(t)
	scoped, err := ResolveOpenAIOAuthCredentialAccount(context.Background(), repo, account, OpenAIOSLinux)
	require.NoError(t, err)
	ctx := context.WithValue(context.Background(), openAIQuotaCredentialContextKey{}, scoped)
	service := &OpenAIQuotaService{accountRepo: repo}
	headers, _, err := service.buildCodexQuotaHeaders(ctx, account.ID, "linux-token", "test-account", false)
	require.NoError(t, err)
	require.Equal(t, "Bearer linux-token", headers["authorization"])
	require.Equal(t, OpenAIOSLinux, openai.DetectOSFamilyFromUserAgent(headers["user-agent"]))
	repo.slots[OpenAIOSLinux].AuthorizationGeneration = "replacement"
	_, _, err = service.buildCodexQuotaHeaders(ctx, account.ID, "linux-token", "test-account", false)
	require.ErrorIs(t, err, ErrOpenAIOAuthOSAuthorizationChanged)
}

func TestUsageProbeSkipsUnauthorizedDefaultWithoutCrossOSFallback(t *testing.T) {
	account, repo := oauthOSCredentialFixture(t)
	repo.slots[OpenAIOSWindows].Status = OpenAIOAuthAuthorizationUnauthorized
	service := &AccountUsageService{accountRepo: repo}
	updates, err := service.probeOpenAICodexSnapshot(context.Background(), account)
	require.Nil(t, updates)
	require.ErrorIs(t, err, ErrOpenAIOAuthOSUnauthorized)
	usage := &UsageInfo{}
	setOpenAIUsageAuthorizationError(usage, err)
	require.Equal(t, "openai_os_authorization_unavailable", usage.ErrorCode)
}
