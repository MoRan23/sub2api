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
	"github.com/tidwall/gjson"
)

type attributionProbeModelsAccounts struct {
	service.AccountRepository
}

func (*attributionProbeModelsAccounts) GetByIDs(context.Context, []int64) ([]*service.Account, error) {
	return []*service.Account{{ID: 7, Name: "configured account", Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey,
		Credentials: map[string]any{"model_mapping": map[string]any{"public-alias": "real-target"}}}}, nil
}

func TestAttributionProbeModelsHandler(t *testing.T) {
	h := &AccountHandler{attributionService: service.NewModelAttributionService(nil, &attributionProbeModelsAccounts{}, nil)}
	router := gin.New()
	router.POST("/probe-models", h.AttributionProbeModels)
	for _, tc := range []struct {
		body string
		code int
	}{
		{`{"account_ids":[7]}`, http.StatusOK},
		{`{"account_ids":[]}`, http.StatusBadRequest},
		{`{"account_ids":[0]}`, http.StatusBadRequest},
		{`{"account_ids":"7"}`, http.StatusBadRequest},
	} {
		t.Run(tc.body, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/probe-models", strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(recorder, req)
			require.Equal(t, tc.code, recorder.Code)
			require.Equal(t, "private, no-store", recorder.Header().Get("Cache-Control"))
			if tc.code == http.StatusOK {
				require.Equal(t, "real-target", gjson.GetBytes(recorder.Body.Bytes(), "data.models.0").String())
				require.Equal(t, "partial", gjson.GetBytes(recorder.Body.Bytes(), "data.accounts.0.status").String())
				require.Equal(t, "model_catalog_failed", gjson.GetBytes(recorder.Body.Bytes(), "data.accounts.0.error").String())
			}
		})
	}
}
