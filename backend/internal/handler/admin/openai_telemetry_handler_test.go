package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestListTelemetryObservationsValidatesFilters(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := &OpenAIOAuthHandler{}
	for _, query := range []string{
		"page=0", "page=-1", "page=1000001", "page=abc", "page_size=0", "page_size=101",
		"account_id=-1", "account_id=0", "account_id=9223372036854775808",
		"status=success", "type=secret",
	} {
		t.Run(query, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodGet, "/?"+query, nil)
			h.ListTelemetryObservations(c)
			require.Equal(t, http.StatusBadRequest, rec.Code)
		})
	}
}

func TestListTelemetryObservationsRemainsIndependentOfFingerprintCollection(t *testing.T) {
	t.Setenv("CODEX_TELEMETRY_ENABLED", "false")
	telemetry := service.NewCodexTelemetryService(nil)
	t.Cleanup(telemetry.Stop)
	h := &OpenAIOAuthHandler{codexTelemetry: telemetry}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/?page=2&page_size=5&account_id=7&status=failed&type=metrics", nil)
	h.ListTelemetryObservations(c)
	require.Equal(t, http.StatusOK, rec.Code)
	var body struct {
		Data service.CodexTelemetryObservationSnapshot `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.True(t, body.Data.ConfiguredEnabled)
	require.False(t, body.Data.EffectiveEnabled)
	require.Equal(t, "CODEX_TELEMETRY_ENABLED", body.Data.ForcedOffReason)
	require.Equal(t, 2, body.Data.Page)
	require.Equal(t, 5, body.Data.PageSize)
	require.Empty(t, body.Data.Items)
	require.Zero(t, body.Data.Total)
}

func TestListTelemetryObservationsMissingRuntime(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/", nil)
	(&OpenAIOAuthHandler{}).ListTelemetryObservations(c)
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
}
