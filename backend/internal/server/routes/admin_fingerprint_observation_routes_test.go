package routes

import (
	"net/http"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/handler"
	adminhandler "github.com/Wei-Shaw/sub2api/internal/handler/admin"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestFingerprintObservationAdminRoutesAreRegistered(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	handlers := &handler.Handlers{Admin: &handler.AdminHandlers{
		OpenAIOAuth: &adminhandler.OpenAIOAuthHandler{},
	}}
	registerOpenAIOAuthRoutes(router.Group("/api/v1/admin"), handlers)

	registered := make(map[string]struct{})
	for _, route := range router.Routes() {
		registered[route.Method+" "+route.Path] = struct{}{}
	}

	for _, path := range []string{
		"/api/v1/admin/openai/fingerprint-observations",
		"/api/v1/admin/openai/fingerprint-observations/api-keys",
		"/api/v1/admin/openai/fingerprint-observations/sessions",
		"/api/v1/admin/openai/fingerprint-observations/threads",
		"/api/v1/admin/openai/fingerprint-observations/entries",
	} {
		_, ok := registered[http.MethodGet+" "+path]
		require.Truef(t, ok, "missing admin fingerprint observation route %s", path)
	}
}
