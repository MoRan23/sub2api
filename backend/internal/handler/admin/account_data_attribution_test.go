package admin

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestAPIKeyAutoAttributionAccountDataRoundTrip(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		router, adminSvc := setupAccountDataRouter()
		adminSvc.accounts = []service.Account{{ID: 21, Name: "api-key-attribution", Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey,
			Credentials: map[string]any{"api_key": "synthetic-key", "base_url": "https://synthetic.invalid/v1"}, Extra: map[string]any{service.OpenAIAPIKeyAutoAttributionEnabledExtraKey: enabled}}}
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/admin/accounts/data", nil))
		require.Equal(t, http.StatusOK, rec.Code)
		var exported dataResponse
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &exported))
		require.Equal(t, enabled, exported.Data.Accounts[0].Extra[service.OpenAIAPIKeyAutoAttributionEnabledExtraKey])
		payload, err := json.Marshal(map[string]any{"data": exported.Data, "skip_default_group_bind": true})
		require.NoError(t, err)
		rec = httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/data", bytes.NewReader(payload))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(rec, req)
		require.Equal(t, http.StatusOK, rec.Code)
		require.Len(t, adminSvc.createdAccounts, 1)
		require.Equal(t, enabled, adminSvc.createdAccounts[0].Extra[service.OpenAIAPIKeyAutoAttributionEnabledExtraKey])
	}
}
