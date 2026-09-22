package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestApplyOpenAIOAuthCredentialsRejectsUnvalidatedBrowserClaims(t *testing.T) {
	gin.SetMode(gin.TestMode)
	account := &service.Account{ID: 42, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth}
	admin := &adminOpenAIRefreshPersistence{initial: account}
	client := &adminOpenAIRefreshClient{}
	oauth := service.NewOpenAIOAuthService(nil, client)
	defer oauth.Stop()
	h := &AccountHandler{adminService: admin, openaiOAuthService: oauth}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/accounts/42/apply-oauth-credentials", strings.NewReader(`{"type":"oauth","os":"linux","credentials":{"access_token":"fabricated-jwt","id_token":"fabricated-id","chatgpt_account_id":"same-workspace","chatgpt_user_id":"same-user"}}`))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Params = gin.Params{{Key: "id", Value: "42"}}
	h.ApplyOAuthCredentials(c)
	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Contains(t, w.Body.String(), "refresh token is required")
	require.Zero(t, admin.fullUpdates)
	require.Zero(t, client.calls)
}

type unavailableOSAuthorizationAdmin struct {
	*adminOpenAIRefreshPersistence
	os string
}

func (a *unavailableOSAuthorizationAdmin) ResolveOpenAIOAuthCredentialAccount(_ context.Context, _ int64, os string) (*service.Account, error) {
	a.os = os
	return nil, service.ErrOpenAIOAuthOSUnauthorized
}

func TestOpenAIOAuthManualRefreshMissingSelectedAuthorizationMakesNoCalls(t *testing.T) {
	gin.SetMode(gin.TestMode)
	account := &service.Account{ID: 42, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Credentials: map[string]any{"refresh_token": "default-rt"}}
	admin := &unavailableOSAuthorizationAdmin{adminOpenAIRefreshPersistence: &adminOpenAIRefreshPersistence{initial: account}}
	client := &adminOpenAIRefreshClient{}
	oauth := service.NewOpenAIOAuthService(nil, client)
	defer oauth.Stop()
	h := NewOpenAIOAuthHandler(oauth, admin, nil, nil)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/openai/accounts/42/refresh?os=linux", nil)
	c.Params = gin.Params{{Key: "id", Value: "42"}}
	h.RefreshAccountToken(c)
	require.NotEqual(t, http.StatusOK, w.Code)
	require.Equal(t, "linux", admin.os)
	require.Zero(t, client.calls)
	require.Zero(t, admin.persistCalls)
}

func TestOpenAIOAuthBoundResponseDoesNotExposeProviderCredentials(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/openai/exchange-code", nil)
	respondOpenAIOAuthAuthorization(c, &service.OpenAITokenInfo{OS: service.OpenAIOSLinux, Account: &service.Account{
		ID: 42, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth,
		Credentials: map[string]any{"access_token": "secret-access", "refresh_token": "secret-refresh", "id_token": "secret-id"},
	}})
	require.Equal(t, http.StatusOK, w.Code)
	require.Contains(t, w.Body.String(), `"os":"linux"`)
	require.Contains(t, w.Body.String(), `"account":`)
	require.NotContains(t, w.Body.String(), "secret-")
}
