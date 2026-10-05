package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type daybreakCapabilityAdminStub struct{ service.AdminService }

func (s *daybreakCapabilityAdminStub) GetAccountDaybreakCapabilities(context.Context, int64) (*service.OpenAIDaybreakCapabilities, error) {
	return &service.OpenAIDaybreakCapabilities{BlueAvailable: true, CredentialOwnerID: 777, CredentialOS: "linux", AuthorizationGeneration: "private-grant", Models: []service.OpenAIDaybreakModel{{Model: "gpt-6-sol", RequiredTier: "blue", Cyber: "daybreak_blue"}}}, nil
}

func TestDaybreakCapabilityEndpointDoesNotExposeGrant(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	h := &AccountHandler{adminService: &daybreakCapabilityAdminStub{}}
	router.GET("/accounts/:id/daybreak-capabilities", h.GetDaybreakCapabilities)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/accounts/15/daybreak-capabilities", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"blue_available":true`)
	require.Contains(t, rec.Body.String(), `"model":"gpt-6-sol"`)
	require.NotContains(t, rec.Body.String(), "private-grant")
	require.NotContains(t, rec.Body.String(), "credential_owner")
	require.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
}

func TestDaybreakImportDisabledAndReportsPerAccountWarning(t *testing.T) {
	router, adminSvc := setupAccountDataRouter()
	data := DataImportRequest{Data: DataPayload{Proxies: []DataProxy{}, Accounts: []DataAccount{{Name: "Daybreak backup", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth,
		Credentials: map[string]any{"access_token": "test-token"}, Extra: map[string]any{service.OpenAIDaybreakBlueEnabledKey: true, service.OpenAIDaybreakRedEnabledKey: true}}}}}
	body, err := json.Marshal(data)
	require.NoError(t, err)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/data", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Len(t, adminSvc.createdAccounts, 1)
	require.NotContains(t, adminSvc.createdAccounts[0].Extra, service.OpenAIDaybreakBlueEnabledKey)
	require.NotContains(t, adminSvc.createdAccounts[0].Extra, service.OpenAIDaybreakRedEnabledKey)
	var reply struct {
		Data DataImportResult `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &reply))
	require.Equal(t, 1, reply.Data.AccountCreated)
	require.Len(t, reply.Data.Warnings, 1)
	require.Equal(t, "Daybreak backup", reply.Data.Warnings[0].Name)
}

func TestDaybreakCodexSessionImportDefaultsOff(t *testing.T) {
	svc := newCodexImportMemoryAdminService(nil)
	handler := NewAccountHandler(svc, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	req := CodexSessionImportRequest{SkipDefaultGroupBind: boolPtr(true), Extra: map[string]any{service.OpenAIDaybreakBlueEnabledKey: true, service.OpenAIDaybreakRedEnabledKey: true}}
	entries := []codexImportEntry{{Index: 1, Value: buildCodexAccessOnlyImportValue(t, "workspace-daybreak", "user-daybreak")}}
	result, err := handler.importCodexSessions(context.Background(), req, entries)
	require.NoError(t, err)
	require.Equal(t, 1, result.Created)
	require.Zero(t, result.Failed)
	require.NotEmpty(t, result.Warnings)
	require.NotContains(t, svc.createdAccounts[0].Extra, service.OpenAIDaybreakBlueEnabledKey)
	require.NotContains(t, svc.createdAccounts[0].Extra, service.OpenAIDaybreakRedEnabledKey)
}
