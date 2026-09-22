package admin

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type completedOpenAIOAuthAdmin struct {
	*adminOpenAIRefreshPersistence
	bindCalls  int
	clearCalls int
	boundOS    string
	bound      map[string]any
	bindErr    error
}

func (a *completedOpenAIOAuthAdmin) BindOpenAIOAuthCredentials(_ context.Context, _ int64, os string, credentials map[string]any) (*service.Account, error) {
	a.bindCalls++
	a.boundOS, a.bound = os, credentials
	if a.bindErr != nil {
		return nil, a.bindErr
	}
	copy := *a.initial
	copy.Credentials = service.PreserveOpenAIOAuthProviderCredentials(credentials, copy.Credentials)
	a.initial = &copy
	return &copy, nil
}

func (a *completedOpenAIOAuthAdmin) ClearAccountError(context.Context, int64) (*service.Account, error) {
	a.clearCalls++
	return a.initial, nil
}

func (a *completedOpenAIOAuthAdmin) UpdateAccountExtra(_ context.Context, _ int64, extra map[string]any) error {
	if a.initial.Extra == nil {
		a.initial.Extra = make(map[string]any)
	}
	for key, value := range extra {
		a.initial.Extra[key] = value
	}
	return nil
}

func TestApplyOpenAIOAuthCredentialsBindsCompletedFlowWithoutAnotherExchange(t *testing.T) {
	gin.SetMode(gin.TestMode)
	account := &service.Account{ID: 42, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Status: service.StatusDisabled,
		Credentials: map[string]any{"model_mapping": map[string]any{"a": "b"}}, Extra: map[string]any{"base_rpm": 17}}
	admin := &completedOpenAIOAuthAdmin{adminOpenAIRefreshPersistence: &adminOpenAIRefreshPersistence{initial: account}}
	client := &adminOpenAIRefreshClient{}
	oauth := service.NewOpenAIOAuthService(nil, client)
	defer oauth.Stop()
	h := &AccountHandler{adminService: admin, openaiOAuthService: oauth}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/accounts/42/apply-oauth-credentials", strings.NewReader(`{"type":"oauth","credentials":{"access_token":"completed-access","id_token":"completed-id","chatgpt_account_id":"same-workspace","chatgpt_user_id":"same-user"},"extra":{"privacy_mode":"enabled"}}`))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Params = gin.Params{{Key: "id", Value: "42"}}
	h.ApplyOAuthCredentials(c)
	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, 1, admin.bindCalls)
	require.Empty(t, admin.boundOS, "OS is optional for account authorization")
	require.Equal(t, "completed-access", admin.bound["access_token"])
	require.Equal(t, "completed-id", admin.bound["id_token"])
	require.Equal(t, service.StatusDisabled, admin.initial.Status, "handler must not clear an administrator's pause")
	require.Equal(t, 17, admin.initial.Extra["base_rpm"])
	require.Equal(t, "enabled", admin.initial.Extra["privacy_mode"])
	require.Equal(t, map[string]any{"a": "b"}, admin.initial.Credentials["model_mapping"])
	require.Zero(t, admin.clearCalls)
	require.Zero(t, admin.fullUpdates)
	require.Zero(t, client.calls)
}

func TestApplyOpenAIOAuthCredentialsDoesNotClearErrorsAfterRejectedBind(t *testing.T) {
	gin.SetMode(gin.TestMode)
	admin := &completedOpenAIOAuthAdmin{adminOpenAIRefreshPersistence: &adminOpenAIRefreshPersistence{initial: &service.Account{
		ID: 42, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth,
	}}, bindErr: errors.New("synthetic binding rejection")}
	router := gin.New()
	router.POST("/accounts/:id/apply-oauth-credentials", (&AccountHandler{adminService: admin}).ApplyOAuthCredentials)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/accounts/42/apply-oauth-credentials", strings.NewReader(`{"type":"oauth","os":"linux","credentials":{"access_token":"completed-access"}}`))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)
	require.NotEqual(t, http.StatusOK, w.Code)
	require.Equal(t, "linux", admin.boundOS)
	require.Equal(t, 1, admin.bindCalls)
	require.Zero(t, admin.clearCalls)
	require.Zero(t, admin.fullUpdates)
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

type sharedAuthorizationMutationAdmin struct {
	*codexAuthExportAdminStub
	revokedOS []string
	defaultOS string
}

func (a *sharedAuthorizationMutationAdmin) ResolveOpenAIOAuthCredentialAccount(context.Context, int64, string) (*service.Account, error) {
	return a.account, nil
}
func (a *sharedAuthorizationMutationAdmin) RevokeOpenAIOAuthOSCredentials(_ context.Context, _ int64, os string) error {
	a.revokedOS = append(a.revokedOS, os)
	return nil
}
func (a *sharedAuthorizationMutationAdmin) SetDefaultOpenAIOAuthOS(_ context.Context, _ int64, os string) (*service.OpenAIOAuthOSProfiles, error) {
	a.defaultOS = os
	return a.account.OpenAIOAuthOSProfiles, nil
}

func TestOpenAIOAuthSharedRevokeAndLegacyRouteUseSameAccountMutation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	admin := &sharedAuthorizationMutationAdmin{codexAuthExportAdminStub: &codexAuthExportAdminStub{account: &service.Account{
		ID: 42, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth,
		OpenAIOAuthOSProfiles: &service.OpenAIOAuthOSProfiles{DefaultOS: service.OpenAIOSWindows},
	}}}
	h := &AccountHandler{adminService: admin}
	router := gin.New()
	router.DELETE("/accounts/:id/openai-oauth-authorization", h.RevokeOpenAIOAuthAuthorization)
	router.DELETE("/accounts/:id/openai/os-auth/:os", h.RevokeOpenAIOAuthOSAuthorization)
	for _, path := range []string{"/accounts/42/openai-oauth-authorization", "/accounts/42/openai/os-auth/linux"} {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, path, nil))
		require.Equal(t, http.StatusOK, rec.Code)
	}
	require.Equal(t, []string{"", service.OpenAIOSLinux}, admin.revokedOS)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/accounts/42/openai/os-auth/unknown", nil))
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Len(t, admin.revokedOS, 2)
}

type accountStateIntentAdmin struct {
	*stubAdminService
	ctx context.Context
}

func (a *accountStateIntentAdmin) UpdateAccount(ctx context.Context, id int64, input *service.UpdateAccountInput) (*service.Account, error) {
	a.ctx = ctx
	return a.stubAdminService.UpdateAccount(ctx, id, input)
}

func (a *accountStateIntentAdmin) BulkUpdateAccounts(ctx context.Context, input *service.BulkUpdateAccountsInput) (*service.BulkUpdateAccountsResult, error) {
	a.ctx = ctx
	return a.stubAdminService.BulkUpdateAccounts(ctx, input)
}

func TestOpenAIOAuthAdminHandlersMarkOnlyExplicitAccountStateIntent(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name, path, body string
		bulk, intent     bool
	}{
		{"single status", "/accounts/42", `{"status":"active"}`, false, true},
		{"single config", "/accounts/42", `{"name":"renamed"}`, false, false},
		{"bulk status", "/accounts/bulk-update", `{"account_ids":[42],"status":"active"}`, true, true},
		{"bulk schedulable", "/accounts/bulk-update", `{"account_ids":[42],"schedulable":false}`, true, true},
		{"bulk config", "/accounts/bulk-update", `{"account_ids":[42],"name":"renamed"}`, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			admin := &accountStateIntentAdmin{stubAdminService: newStubAdminService()}
			h := &AccountHandler{adminService: admin}
			router := gin.New()
			handler := h.Update
			if tc.bulk {
				handler = h.BulkUpdate
			}
			router.POST("/accounts/:id", handler)
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(w, req)
			require.Equal(t, http.StatusOK, w.Code, w.Body.String())
			require.NotNil(t, admin.ctx)
			require.Equal(t, tc.intent, service.OpenAIOAuthAccountStateIntentAllowed(admin.ctx, 42))
			require.False(t, service.OpenAIOAuthAccountStateIntentAllowed(admin.ctx, 43))
		})
	}
}
