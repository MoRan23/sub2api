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

func TestCodexContextObservationAdminPageAndDisable(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service.SetFingerprintObservationEnabled(false)
	service.SetFingerprintObservationEnabled(true)
	t.Cleanup(func() { service.SetFingerprintObservationEnabled(false) })

	for _, status := range []string{"delivered", "rejected"} {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodGet, "/v1/user-auth-credential/whoami", nil)
		service.BeginCodexContextManagementObservation(c, "whoami", c.Request.URL.Path)
		code, errKind := http.StatusOK, ""
		if status == "rejected" {
			code, errKind = http.StatusUnauthorized, "authentication_error"
		}
		service.RecordCodexContextManagementResult(c, "whoami", c.Request.URL.Path, status, code, 0, errKind)
	}

	type envelope struct {
		Code int `json:"code"`
		Data struct {
			Enabled bool `json:"enabled"`
			service.CodexContextManagementObservationPage
		} `json:"data"`
	}
	read := func(query string) envelope {
		t.Helper()
		r := invokeFingerprintObservationHandler(t,
			"/api/v1/admin/openai/fingerprint-observations/context-management"+query,
			(&OpenAIOAuthHandler{}).ListCodexContextManagementObservations)
		require.Equal(t, http.StatusOK, r.Code)
		var response envelope
		require.NoError(t, json.Unmarshal(r.Body.Bytes(), &response))
		require.Zero(t, response.Code)
		return response
	}

	first := read("?page_size=1")
	require.True(t, first.Data.Enabled)
	require.Equal(t, 2, first.Data.Total)
	require.Len(t, first.Data.Items, 1)
	require.Equal(t, "rejected", first.Data.Items[0].Status, "newest event comes first")
	require.Equal(t, 1, first.Data.Summary.Successes)
	require.Equal(t, 1, first.Data.Summary.Failures)
	last := read("?page=99&page_size=1")
	require.Equal(t, 2, last.Data.Page)
	require.Len(t, last.Data.Items, 1)
	require.Equal(t, "delivered", last.Data.Items[0].Status)

	service.SetFingerprintObservationEnabled(false)
	off := read("?page=99&page_size=1000")
	require.False(t, off.Data.Enabled)
	require.NotNil(t, off.Data.Items)
	require.Empty(t, off.Data.Items)
	require.Zero(t, off.Data.Total)
	require.Equal(t, service.CodexContextManagementObservationSummary{}, off.Data.Summary)
	require.Equal(t, 1, off.Data.Page)
	require.Equal(t, 100, off.Data.PageSize)

	service.SetFingerprintObservationEnabled(true)
	reopened := read("")
	require.True(t, reopened.Data.Enabled)
	require.Empty(t, reopened.Data.Items, "re-enabling starts an empty observation period")
}
