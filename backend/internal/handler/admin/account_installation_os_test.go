package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type osInstallationAdminStub struct {
	stubAdminService
	selectedOS string
	profiles   *service.OpenAIOAuthOSProfiles
}

func (s *osInstallationAdminStub) RegenerateOpenAIInstallationIDForOS(_ context.Context, _ int64, osFamily string) (*service.OpenAIOAuthOSProfiles, error) {
	s.selectedOS = osFamily
	return s.profiles, nil
}

func TestRegenerateInstallationIDSelectsOneOSAndReturnsProfiles(t *testing.T) {
	gin.SetMode(gin.TestMode)
	profiles := &service.OpenAIOAuthOSProfiles{DefaultOS: "windows", Profiles: map[string]service.OpenAIOAuthOSProfile{
		"macos": {OSFamily: "macos", InstallationID: "11111111-2222-4333-8444-555555555555"},
	}}
	admin := &osInstallationAdminStub{profiles: profiles}
	handler := &AccountHandler{adminService: admin}
	router := gin.New()
	router.POST("/accounts/:id/installation-id/regenerate", handler.RegenerateInstallationID)
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/accounts/71/installation-id/regenerate", strings.NewReader(`{"os":"macos"}`))
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code)
	require.Equal(t, "macos", admin.selectedOS)
	var payload struct {
		Data struct {
			OS             string                         `json:"os"`
			InstallationID string                         `json:"installation_id"`
			Profiles       *service.OpenAIOAuthOSProfiles `json:"openai_oauth_os_profiles"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &payload))
	require.Equal(t, "macos", payload.Data.OS)
	require.Equal(t, profiles.Profiles["macos"].InstallationID, payload.Data.InstallationID)
	require.Equal(t, profiles, payload.Data.Profiles)
}

func TestRegenerateInstallationIDRejectsUnknownOSAndKeepsLegacyEmptyBody(t *testing.T) {
	gin.SetMode(gin.TestMode)
	admin := &osInstallationAdminStub{}
	router := gin.New()
	router.POST("/accounts/:id/installation-id/regenerate", (&AccountHandler{adminService: admin}).RegenerateInstallationID)
	for _, body := range []string{`{"os":"android"}`, `{"os":""}`, `{`} {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/accounts/71/installation-id/regenerate", strings.NewReader(body)))
		require.Equal(t, http.StatusBadRequest, response.Code, body)
	}
	require.Empty(t, admin.selectedOS)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/accounts/71/installation-id/regenerate", nil))
	require.Equal(t, http.StatusOK, response.Code)
	require.Contains(t, response.Body.String(), "00000000-0000-4000-8000-000000000000")
}
