package admin

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type codexAuthExportAdminStub struct {
	service.AdminService
	account *service.Account
	err     error
	readIDs []int64
	slots   map[string]*service.OpenAIOAuthOSCredential
	readOS  []string
}

func (s *codexAuthExportAdminStub) GetOpenAIOAuthOSCredential(_ context.Context, id int64, os string) (*service.OpenAIOAuthOSCredential, error) {
	s.readOS = append(s.readOS, os)
	if s.slots != nil {
		return s.slots[os], nil
	}
	return &service.OpenAIOAuthOSCredential{OwnerAccountID: id, OSFamily: os, Credentials: s.account.Credentials}, nil
}

func (s *codexAuthExportAdminStub) GetAccount(_ context.Context, id int64) (*service.Account, error) {
	s.readIDs = append(s.readIDs, id)
	return s.account, s.err
}

func TestExportCodexAuthFreshSnapshotAndImportRoundTrip(t *testing.T) {
	gin.SetMode(gin.TestMode)
	idToken := "header." + base64.RawURLEncoding.EncodeToString([]byte(`{"https://api.openai.com/auth":{"chatgpt_account_id":"jwt-workspace"}}`)) + ".signature"
	stub := &codexAuthExportAdminStub{account: &service.Account{ID: 42, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Credentials: map[string]any{
		"access_token": "first-token", "id_token": idToken, "refresh_token": "refresh-token", "chatgpt_account_id": "selected-workspace",
	}}}
	stub.account.OpenAIOAuthOSProfiles = &service.OpenAIOAuthOSProfiles{DefaultOS: "windows"}
	h := &AccountHandler{adminService: stub}
	router := gin.New()
	router.GET("/accounts/:id/codex-auth", h.ExportCodexAuth)
	for _, token := range []string{"first-token", "updated-token"} {
		stub.account.Credentials["access_token"] = token
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/accounts/42/codex-auth", nil))
		require.Equal(t, http.StatusOK, rec.Code)
		require.Equal(t, "private, no-store", rec.Header().Get("Cache-Control"))
		require.Equal(t, "nosniff", rec.Header().Get("X-Content-Type-Options"))
		require.Equal(t, token, gjson.GetBytes(rec.Body.Bytes(), "data.auth.tokens.access_token").String())
		require.Equal(t, "[]", gjson.GetBytes(rec.Body.Bytes(), "data.warnings").Raw)
		var raw map[string]any
		require.NoError(t, json.Unmarshal([]byte(gjson.GetBytes(rec.Body.Bytes(), "data.auth").Raw), &raw))
		imported, err := normalizeCodexImportEntry(codexImportEntry{Index: 1, Value: raw})
		require.NoError(t, err)
		require.Equal(t, "selected-workspace", imported.AccountID, "explicit tokens.account_id must precede JWT claims")
		require.Equal(t, token, imported.AccessToken)
		require.Equal(t, "refresh-token", imported.RefreshToken)
		require.Equal(t, idToken, imported.IDToken)
	}
	require.Equal(t, []int64{42, 42}, stub.readIDs)
	require.Equal(t, []string{"windows", "windows"}, stub.readOS)
}

func TestExportCodexAuthErrorsAreNonCacheableAndDoNotExposeTokens(t *testing.T) {
	gin.SetMode(gin.TestMode)
	stub := &codexAuthExportAdminStub{account: &service.Account{ID: 42, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Credentials: map[string]any{"access_token": "secret-access", "id_token": "secret-invalid-id"}, OpenAIOAuthOSProfiles: &service.OpenAIOAuthOSProfiles{DefaultOS: "windows"}}}
	h := &AccountHandler{adminService: stub}
	router := gin.New()
	router.GET("/accounts/:id/codex-auth", h.ExportCodexAuth)
	for _, id := range []string{"0", "-1", "invalid", "42"} {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/accounts/"+id+"/codex-auth", nil))
		require.Equal(t, http.StatusBadRequest, rec.Code)
		require.Equal(t, "private, no-store", rec.Header().Get("Cache-Control"))
		require.NotContains(t, rec.Body.String(), "secret-")
		if id == "42" {
			require.Equal(t, "OPENAI_CODEX_AUTH_EXPORT_INCOMPLETE", gjson.GetBytes(rec.Body.Bytes(), "reason").String())
		}
	}
	require.Equal(t, []int64{42}, stub.readIDs)
	stub.err = service.ErrAccountNotFound
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/accounts/43/codex-auth", nil))
	require.Equal(t, http.StatusNotFound, rec.Code)
}

func TestExportCodexAuthUsesSamePrivateGrantForEveryLegacyOS(t *testing.T) {
	idToken := "header." + base64.RawURLEncoding.EncodeToString([]byte(`{}`)) + ".signature"
	stub := &codexAuthExportAdminStub{account: &service.Account{ID: 42, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth,
		Credentials: map[string]any{"access_token": "default-secret"}, OpenAIOAuthOSProfiles: &service.OpenAIOAuthOSProfiles{DefaultOS: "windows"}},
		slots: map[string]*service.OpenAIOAuthOSCredential{"windows": {OwnerAccountID: 42, OSFamily: "windows", Credentials: map[string]any{"access_token": "shared-token", "id_token": idToken}}}}
	router := gin.New()
	router.GET("/accounts/:id/codex-auth", (&AccountHandler{adminService: stub}).ExportCodexAuth)
	for _, query := range []string{"", "?os=macos", "?os=linux", "?os=windows"} {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/accounts/42/codex-auth"+query, nil))
		require.Equal(t, http.StatusOK, response.Code)
		require.Equal(t, "shared-token", gjson.GetBytes(response.Body.Bytes(), "data.auth.tokens.access_token").String())
		require.False(t, gjson.GetBytes(response.Body.Bytes(), "data.os").Exists())
		require.NotContains(t, response.Body.String(), "default-secret")
	}
	require.Equal(t, []string{"windows", "windows", "windows", "windows"}, stub.readOS)
}
