package service

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestOpenAIAuxiliaryResidencyAPIKeyRequests(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, enabled := range []bool{true, false} {
		name := "enabled"
		want := "us"
		if !enabled {
			name, want = "disabled", "eu"
		}
		t.Run(name, func(t *testing.T) {
			seen := make(chan http.Header, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				seen <- r.Header.Clone()
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{}`)
			}))
			defer server.Close()
			cfg := &config.Config{}
			cfg.Security.URLAllowlist.AllowInsecureHTTP = true
			svc := &OpenAIGatewayService{cfg: cfg}
			account := &Account{ID: 7, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{
				"api_key": "test-key", "base_url": server.URL,
				"header_override_enabled": true,
				"header_overrides":        map[string]any{openai.CodexResidencyHeaderName: "eu"},
			}}
			policy := openai.DefaultRequestPolicy()
			policy.CodexResidencyUS = enabled
			ctx := openai.WithRequestPolicy(context.Background(), policy)
			body := []byte(`{"model":"gpt-image-2","prompt":"a tree"}`)
			builders := map[string]func() (*http.Request, error){
				"images": func() (*http.Request, error) {
					c, _ := gin.CreateTestContext(httptest.NewRecorder())
					c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body))
					return svc.buildOpenAIImagesRequest(ctx, c, account, body, "application/json", "test-key", openAIImagesGenerationsEndpoint)
				},
				"alpha_search": func() (*http.Request, error) {
					return svc.buildOpenAIAlphaSearchRequest(ctx, nil, account, []byte(`{"model":"gpt-5"}`), "test-key")
				},
				"models": func() (*http.Request, error) {
					return buildOpenAIAPIKeyModelsRequest(ctx, account, svc.validateUpstreamBaseURL)
				},
			}
			for route, build := range builders {
				t.Run(route, func(t *testing.T) {
					request, err := build()
					require.NoError(t, err)
					response, err := server.Client().Do(request)
					require.NoError(t, err)
					defer response.Body.Close()
					require.Equal(t, []string{want}, (<-seen).Values(openai.CodexResidencyHeaderName))
				})
			}
		})
	}
}

func TestOpenAIModelsResidencyFrozenAcrossFetchAndRedirect(t *testing.T) {
	seen := make(chan http.Header, 2)
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r.Header.Clone()
		_, _ = io.WriteString(w, `{"data":[]}`)
	}))
	defer destination.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r.Header.Clone()
		http.Redirect(w, r, destination.URL, http.StatusFound)
	}))
	defer origin.Close()
	svc := &OpenAIGatewayService{}
	policy := openai.DefaultRequestPolicy()
	request := openAIModelsRequest{url: origin.URL, headers: http.Header{openai.CodexResidencyHeaderName: {"eu", "ap"}}, requestPolicy: &policy}
	changedPolicy := policy
	changedPolicy.CodexResidencyUS = false
	response, err := svc.fetchOpenAIModelsUpstream(openai.WithRequestPolicy(context.Background(), changedPolicy), request, "")
	require.NoError(t, err)
	require.JSONEq(t, `{"data":[]}`, string(response.Body))
	require.Equal(t, []string{"us"}, (<-seen).Values(openai.CodexResidencyHeaderName))
	require.Empty(t, (<-seen).Values(openai.CodexResidencyHeaderName), "a cross-origin redirect must not forward residency")
	require.Equal(t, []string{"eu", "ap"}, request.headers[openai.CodexResidencyHeaderName], "fetch must not mutate reusable input headers")
	otherRequest := request
	otherRequest.requestPolicy = &changedPolicy
	require.NotEqual(t, buildOpenAIModelsCacheKey(request), buildOpenAIModelsCacheKey(otherRequest))
}

func TestOpenAIModelsResidencyDoesNotApplyToCNProvider(t *testing.T) {
	seen := make(chan http.Header, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r.Header.Clone()
		_, _ = io.WriteString(w, `{"data":[]}`)
	}))
	defer server.Close()
	account := &Account{ID: 8, Platform: PlatformDeepseek, Type: AccountTypeAPIKey, Credentials: map[string]any{
		"api_key": "test-key", "base_url": "https://api.deepseek.com",
	}}
	svc := &OpenAIGatewayService{}
	request, err := buildOpenAIAPIKeyModelsRequest(context.Background(), account, svc.validateUpstreamBaseURL)
	require.NoError(t, err)
	require.Empty(t, request.Header.Get(openai.CodexResidencyHeaderName))
	_, err = svc.fetchOpenAIModelsUpstream(context.Background(), openAIModelsRequest{
		url: server.URL, headers: request.Header, credentialAccount: account,
	}, "")
	require.NoError(t, err)
	require.Empty(t, (<-seen).Values(openai.CodexResidencyHeaderName))
}

func TestOpenAILiveResidencyRequestAndHandshake(t *testing.T) {
	cipher := newLiveAttestationCipher(&config.Config{JWT: config.JWTConfig{Secret: "live-residency-test-secret"}})
	ciphertext, err := cipher.Encrypt(`{"v":1,"t":"test"}`)
	require.NoError(t, err)
	account := &Account{ID: 7, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{
		"access_token": "test-token", "chatgpt_account_id": "test-account",
	}}
	for _, enabled := range []bool{true, false} {
		policy := openai.DefaultRequestPolicy()
		policy.CodexResidencyUS = enabled
		ctx := openai.WithRequestPolicy(context.Background(), policy)
		upstream := &liveHTTPUpstreamStub{}
		svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream, liveAttestationCipher: cipher}
		_, err := svc.createUpstreamLiveCall(ctx, account, &LiveCallRequest{SDP: "v=0\r\n", Session: []byte(`{"model":"gpt-live"}`)}, "test-attestation")
		require.NoError(t, err)
		headers, err := svc.liveSidebandHeaders(ctx, account, &LiveCallRecord{AttestationCiphertext: ciphertext})
		require.NoError(t, err)
		want := ""
		if enabled {
			want = "us"
		}
		require.Equal(t, want, upstream.request.Header.Get(openai.CodexResidencyHeaderName))
		require.Equal(t, want, headers.Get(openai.CodexResidencyHeaderName))
		require.False(t, strings.Contains(string(upstream.body), openai.CodexResidencyHeaderName))
	}
}
