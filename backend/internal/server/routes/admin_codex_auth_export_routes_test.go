package routes

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/handler"
	adminhandler "github.com/Wei-Shaw/sub2api/internal/handler/admin"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestCodexAuthExportRouteRequiresStepUp(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	handlers := &handler.Handlers{Admin: &handler.AdminHandlers{Account: &adminhandler.AccountHandler{}}}
	calls := 0
	stepUp := middleware.StepUpAuthMiddleware(func(c *gin.Context) {
		calls++
		c.AbortWithStatus(http.StatusPreconditionRequired)
	})
	registerAccountRoutes(router.Group("/admin"), handlers, stepUp)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/admin/accounts/42/codex-auth", nil))
	require.Equal(t, http.StatusPreconditionRequired, rec.Code)
	require.Equal(t, 1, calls, "step-up must run before any credential read")
	require.Empty(t, rec.Body.String())
}
