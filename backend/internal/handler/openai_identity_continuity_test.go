package handler

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

const openAIIdentityContinuityInstallationID = "11111111-2222-4333-8444-555555555555"

type openAIIdentityContinuityAccountRepo struct {
	service.AccountRepository
	account service.Account
}

func (r *openAIIdentityContinuityAccountRepo) GetByID(_ context.Context, id int64) (*service.Account, error) {
	if id != r.account.ID {
		return nil, service.ErrNoAvailableAccounts
	}
	account := r.account
	return &account, nil
}

func (r *openAIIdentityContinuityAccountRepo) ListSchedulableByGroupIDAndPlatform(_ context.Context, _ int64, platform string) ([]service.Account, error) {
	return r.accountsForPlatform(platform), nil
}

func (r *openAIIdentityContinuityAccountRepo) ListSchedulableByPlatform(_ context.Context, platform string) ([]service.Account, error) {
	return r.accountsForPlatform(platform), nil
}

func (r *openAIIdentityContinuityAccountRepo) ListSchedulableUngroupedByPlatform(_ context.Context, platform string) ([]service.Account, error) {
	return r.accountsForPlatform(platform), nil
}

func (r *openAIIdentityContinuityAccountRepo) accountsForPlatform(platform string) []service.Account {
	if platform != r.account.Platform {
		return nil
	}
	account := r.account
	return []service.Account{account}
}

type openAIIdentityContinuityCapture struct {
	Header http.Header
	Body   []byte
}

type openAIIdentityContinuityUpstream struct {
	service.HTTPUpstream
	mu       sync.Mutex
	requests []openAIIdentityContinuityCapture
}

func (u *openAIIdentityContinuityUpstream) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	body, _ := io.ReadAll(req.Body)
	_ = req.Body.Close()
	req.Body = io.NopCloser(bytes.NewReader(body))
	u.mu.Lock()
	u.requests = append(u.requests, openAIIdentityContinuityCapture{
		Header: req.Header.Clone(),
		Body:   append([]byte(nil), body...),
	})
	u.mu.Unlock()
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(bytes.NewBufferString(`{"id":"resp_continuity","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1}}`)),
	}, nil
}

func (u *openAIIdentityContinuityUpstream) DoWithTLS(req *http.Request, proxyURL string, accountID int64, accountConcurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, proxyURL, accountID, accountConcurrency)
}

func (u *openAIIdentityContinuityUpstream) snapshot() []openAIIdentityContinuityCapture {
	u.mu.Lock()
	defer u.mu.Unlock()
	out := make([]openAIIdentityContinuityCapture, len(u.requests))
	copy(out, u.requests)
	return out
}

func newOpenAIIdentityContinuityHandler(t *testing.T) (*OpenAIGatewayHandler, *openAIIdentityContinuityUpstream) {
	t.Helper()
	repo := &openAIIdentityContinuityAccountRepo{account: service.Account{
		ID:          910074,
		Name:        "identity-continuity",
		Platform:    service.PlatformOpenAI,
		Type:        service.AccountTypeOAuth,
		Status:      service.StatusActive,
		Schedulable: true,
		Concurrency: 0,
		Credentials: map[string]any{
			"access_token":       "oauth-token",
			"chatgpt_account_id": "identity-continuity-account",
		},
		Extra: map[string]any{
			"openai_compact_supported":      true,
			"openai_pinned_installation_id": openAIIdentityContinuityInstallationID,
		},
	}}
	upstream := &openAIIdentityContinuityUpstream{}
	cfg := &config.Config{
		RunMode: config.RunModeSimple,
		JWT:     config.JWTConfig{Secret: "identity-continuity-secret"},
	}
	gateway := service.NewOpenAIGatewayService(
		repo, nil, nil, nil, nil, nil, nil, cfg, nil, nil, nil, nil, nil,
		upstream, nil, nil, nil, nil, nil, nil, nil, nil,
	)
	billingCache := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil)
	t.Cleanup(billingCache.Stop)
	handler := NewOpenAIGatewayHandler(
		gateway,
		service.NewConcurrencyService(nil),
		billingCache,
		service.NewAPIKeyService(nil, nil, nil, nil, nil, nil, cfg),
		nil, nil, nil, nil, cfg,
	)
	return handler, upstream
}

func newOpenAIIdentityContinuityContext(t *testing.T, path string, body []byte, setup func(http.Header)) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	if setup != nil {
		setup(c.Request.Header)
	}
	groupID := int64(7400)
	user := &service.User{ID: 7401}
	c.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{
		ID:      7402,
		GroupID: &groupID,
		Group: &service.Group{
			ID:       groupID,
			Platform: service.PlatformOpenAI,
		},
		User: user,
	})
	c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: user.ID})
	return c, recorder
}

func TestOpenAIResponsesHandlerPreservesIdentityAcrossResponsesAndCompact(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tests := []struct {
		name          string
		responsesBody []byte
		compactBody   []byte
		setupHeaders  func(http.Header)
	}{
		{
			name:          "canonical tuple",
			responsesBody: []byte(`{"model":"gpt-5.4","stream":false,"input":"hello","client_metadata":{"session_id":"handler-canonical","thread_id":"handler-canonical"}}`),
			compactBody:   []byte(`{"model":"gpt-5.4","stream":false,"input":"hello","client_metadata":{"session_id":"handler-canonical","thread_id":"handler-canonical"}}`),
		},
		{
			name:          "prompt cache fallback",
			responsesBody: []byte(`{"model":"gpt-5.4","stream":false,"input":"hello","prompt_cache_key":"handler-prompt"}`),
			compactBody:   []byte(`{"model":"gpt-5.4","stream":false,"input":"hello","prompt_cache_key":"handler-prompt"}`),
		},
		{
			name:          "legacy session_id alias",
			responsesBody: []byte(`{"model":"gpt-5.4","stream":false,"input":"hello"}`),
			compactBody:   []byte(`{"model":"gpt-5.4","stream":false,"input":"hello"}`),
			setupHeaders: func(headers http.Header) {
				headers.Set("session_id", "handler-legacy")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler, upstream := newOpenAIIdentityContinuityHandler(t)
			responsesContext, responsesRecorder := newOpenAIIdentityContinuityContext(t, "/v1/responses", tt.responsesBody, tt.setupHeaders)
			handler.Responses(responsesContext)
			require.Equal(t, http.StatusOK, responsesRecorder.Code, responsesRecorder.Body.String())

			compactContext, compactRecorder := newOpenAIIdentityContinuityContext(t, "/v1/responses/compact", tt.compactBody, tt.setupHeaders)
			handler.Responses(compactContext)
			require.Equal(t, http.StatusOK, compactRecorder.Code, compactRecorder.Body.String())

			requests := upstream.snapshot()
			require.Len(t, requests, 2)
			responsesSession := requests[0].Header.Get("session-id")
			responsesThread := requests[0].Header.Get("thread-id")
			require.NotEmpty(t, responsesSession)
			require.NotEmpty(t, responsesThread)
			require.True(t, service.ValidateFingerprintObservationUUIDv7(responsesSession))
			require.True(t, service.ValidateFingerprintObservationUUIDv7(responsesThread))
			require.Equal(t, responsesSession, responsesThread)
			require.Equal(t, responsesSession, gjson.GetBytes(requests[0].Body, "client_metadata.session_id").String())
			require.Equal(t, responsesThread, gjson.GetBytes(requests[0].Body, "client_metadata.thread_id").String())
			require.Equal(t, responsesSession, requests[1].Header.Get("session-id"))
			require.Equal(t, responsesThread, requests[1].Header.Get("thread-id"))
			require.Equal(t, responsesThread, requests[0].Header.Get("x-client-request-id"))
			require.Empty(t, requests[1].Header.Get("x-client-request-id"))
			require.False(t, gjson.GetBytes(requests[1].Body, "client_metadata").Exists())
			require.Equal(t, openAIIdentityContinuityInstallationID, requests[0].Header.Get("x-codex-installation-id"))
			require.Equal(t, openAIIdentityContinuityInstallationID, requests[1].Header.Get("x-codex-installation-id"))
			require.Equal(t, requests[0].Header.Get("user-agent"), requests[1].Header.Get("user-agent"))
			require.Equal(t, requests[0].Header.Get("version"), requests[1].Header.Get("version"))
		})
	}
}
