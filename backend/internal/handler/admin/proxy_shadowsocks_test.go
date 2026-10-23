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

func TestShadowsocksProxyCreateAndBatchPreservePassword(t *testing.T) {
	for _, batch := range []bool{false, true} {
		t.Run(map[bool]string{false: "single", true: "batch"}[batch], func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			svc := newStubAdminService()
			h := NewProxyHandler(svc)
			router := gin.New()
			router.POST("/create", h.Create)
			router.POST("/batch", h.BatchCreate)
			item := map[string]any{"name": "ss-test", "protocol": "ss", "host": "::1", "port": 8388, "username": "aes-256-gcm", "password": " exact secret "}
			path, payload := "/create", any(item)
			if batch {
				path, payload = "/batch", map[string]any{"proxies": []any{item}}
			}
			body, err := json.Marshal(payload)
			require.NoError(t, err)
			req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)
			require.Equal(t, http.StatusOK, w.Code, w.Body.String())
			require.Len(t, svc.createdProxies, 1)
			require.Equal(t, "ss", svc.createdProxies[0].Protocol)
			require.Equal(t, " exact secret ", svc.createdProxies[0].Password)
		})
	}
}

func TestShadowsocksProxyExportAndImport(t *testing.T) {
	router, svc := setupProxyDataRouter()
	svc.proxies = []service.Proxy{{ID: 1, Name: "ss-test", Protocol: "ss", Host: "::1", Port: 8388, Username: "aes-256-gcm", Password: " exact secret ", Status: service.StatusActive}}
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/admin/proxies/data", nil))
	require.Equal(t, http.StatusOK, w.Code)
	var exported proxyDataResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &exported))
	require.Len(t, exported.Data.Proxies, 1)
	item := exported.Data.Proxies[0]
	require.Equal(t, " exact secret ", item.Password)
	require.NotContains(t, item.ProxyKey, item.Password)
	require.NotEqual(t, buildProxyKey("ss", "::1", 8388, item.Username, "exact secret"), item.ProxyKey)

	// Both account bundles and proxy-only bundles use the same SS validation.
	for _, path := range []string{"/api/v1/admin/proxies/data", "/api/v1/admin/accounts/data"} {
		t.Run(path, func(t *testing.T) {
			importRouter, importSvc := setupProxyDataRouter()
			if path == "/api/v1/admin/accounts/data" {
				importRouter, importSvc = setupAccountDataRouter()
			}
			payload, err := json.Marshal(map[string]any{"data": exported.Data})
			require.NoError(t, err)
			req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(payload))
			req.Header.Set("Content-Type", "application/json")
			res := httptest.NewRecorder()
			importRouter.ServeHTTP(res, req)
			require.Equal(t, http.StatusOK, res.Code, res.Body.String())
			require.Len(t, importSvc.createdProxies, 1)
			require.Equal(t, item.Username, importSvc.createdProxies[0].Username)
			require.Equal(t, item.Password, importSvc.createdProxies[0].Password)
		})
	}
}

func TestShadowsocksProxyImportErrorsDoNotExposePassword(t *testing.T) {
	router, svc := setupProxyDataRouter()
	item := DataProxy{Protocol: "ss", Host: "ss.example", Port: 8388, Username: "unsupported", Password: "secret-not-for-errors", ProxyKey: "ss|secret-not-for-errors"}
	body, err := json.Marshal(map[string]any{"data": DataPayload{Proxies: []DataProxy{item}, Accounts: []DataAccount{}}})
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/proxies/data", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Empty(t, svc.createdProxies)
	require.NotContains(t, w.Body.String(), item.Password)
	var result proxyImportResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &result))
	require.Equal(t, 1, result.Data.ProxyFailed)
	for _, protocol := range []string{"http", "https", "socks5", "socks5h"} {
		require.NoError(t, validateDataProxy(DataProxy{Protocol: protocol, Host: "proxy.example", Port: 8080}))
	}
}
