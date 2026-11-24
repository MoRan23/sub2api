package admin

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func setupUpstreamBillingProbeRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	handler := NewAccountHandler(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	handler.SetUpstreamBillingProbeService(service.NewUpstreamBillingProbeService(nil, nil, nil))

	router := gin.New()
	router.GET("/admin/accounts/upstream-billing-probe/settings", handler.GetUpstreamBillingProbeSettings)
	router.POST("/admin/accounts/upstream-billing-probe/batch", handler.ProbeUpstreamBillingBatch)
	router.POST("/admin/accounts/upstream-billing-probe/group-preview", handler.PreviewUpstreamGroup)
	router.PUT("/admin/accounts/:id/upstream-billing-probe", handler.SetUpstreamBillingProbeEnabled)
	return router
}

func TestAccountHandlerUpstreamBillingGroupPreviewKeepsStoredCredentialsBound(t *testing.T) {
	router := setupUpstreamBillingProbeRouter()
	for _, body := range []string{
		`{"account_id":7,"base_url":"https://different.example"}`,
		`{"account_id":7,"proxy_id":8}`,
	} {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/admin/accounts/upstream-billing-probe/group-preview", bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(w, req)
		require.Equal(t, http.StatusBadRequest, w.Code)
	}
}

func TestAccountHandlerBatchCreateForwardsBillingSyncSettings(t *testing.T) {
	gin.SetMode(gin.TestMode)
	adminService := newStubAdminService()
	handler := NewAccountHandler(adminService, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	router := gin.New()
	router.POST("/admin/accounts/batch", handler.BatchCreate)
	request := httptest.NewRequest(http.MethodPost, "/admin/accounts/batch", bytes.NewBufferString(`{"accounts":[
		{"name":"enabled","platform":"openai","type":"apikey","credentials":{"api_key":"test"},"upstream_billing_probe_enabled":true,"upstream_billing_group_sync_enabled":true},
		{"name":"disabled","platform":"openai","type":"apikey","credentials":{"api_key":"test"},"upstream_billing_probe_enabled":false,"upstream_billing_group_sync_enabled":false},
		{"name":"group-only","platform":"openai","type":"apikey","credentials":{"api_key":"test"},"upstream_billing_group_sync_enabled":true},
		{"name":"omitted","platform":"openai","type":"apikey","credentials":{"api_key":"test"}}
	]}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	require.Len(t, adminService.createdAccounts, 4)
	for index, expected := range []bool{true, false} {
		input := adminService.createdAccounts[index]
		require.NotNil(t, input.ProbeEnabled)
		require.Equal(t, expected, *input.ProbeEnabled)
		require.NotNil(t, input.GroupSyncEnabled)
		require.Equal(t, expected, *input.GroupSyncEnabled)
	}
	require.Nil(t, adminService.createdAccounts[2].ProbeEnabled)
	require.NotNil(t, adminService.createdAccounts[2].GroupSyncEnabled)
	require.True(t, *adminService.createdAccounts[2].GroupSyncEnabled)
	require.Nil(t, adminService.createdAccounts[3].ProbeEnabled)
	require.Nil(t, adminService.createdAccounts[3].GroupSyncEnabled)
}

func TestAccountHandlerGetUpstreamBillingProbeSettingsReturnsDefaults(t *testing.T) {
	router := setupUpstreamBillingProbeRouter()
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/admin/accounts/upstream-billing-probe/settings", nil))

	require.Equal(t, http.StatusOK, recorder.Code)
	var response struct {
		Data service.UpstreamBillingProbeSettings `json:"data"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	require.True(t, response.Data.Enabled)
	require.Equal(t, 30, response.Data.IntervalMinutes)
}

func TestAccountHandlerProbeUpstreamBillingBatchValidatesIDs(t *testing.T) {
	router := setupUpstreamBillingProbeRouter()

	for _, body := range []string{`{"account_ids":[]}`, `{"account_ids":[0]}`} {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/admin/accounts/upstream-billing-probe/batch", bytes.NewBufferString(body))
		request.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(recorder, request)
		require.Equal(t, http.StatusBadRequest, recorder.Code)
	}
}

func TestAccountHandlerSetUpstreamBillingProbeEnabledRejectsInvalidID(t *testing.T) {
	router := setupUpstreamBillingProbeRouter()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPut, "/admin/accounts/not-an-id/upstream-billing-probe", bytes.NewBufferString(`{"enabled":true}`))
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusBadRequest, recorder.Code)
}

func TestAccountHandlerSetUpstreamBillingProbeEnabledRequiresValue(t *testing.T) {
	router := setupUpstreamBillingProbeRouter()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPut, "/admin/accounts/1/upstream-billing-probe", bytes.NewBufferString(`{}`))
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusBadRequest, recorder.Code)
}
