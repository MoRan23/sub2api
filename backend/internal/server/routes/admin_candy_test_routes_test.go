package routes

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/handler"
	adminhandler "github.com/Wei-Shaw/sub2api/internal/handler/admin"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestAccountCandyRoutesAndRetiredCacheRoutes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	account := &adminhandler.AccountHandler{}
	handlers := &handler.Handlers{Admin: &handler.AdminHandlers{Account: account}}
	group := router.Group("/admin", func(c *gin.Context) {
		if c.GetHeader("X-Test-Admin") != "yes" {
			c.AbortWithStatus(http.StatusUnauthorized)
		}
	})
	registerAccountRoutes(group, handlers, middleware.StepUpAuthMiddleware(func(c *gin.Context) { c.AbortWithStatus(http.StatusPreconditionRequired) }))
	for _, route := range router.Routes() {
		require.NotContains(t, route.Path, "codex-turn-state")
	}
	for _, tc := range []struct{ method, path string }{
		{"POST", "/admin/accounts/candy-test-options"},
		{"POST", "/admin/accounts/candy-tests"},
		{"GET", "/admin/accounts/candy-tests/00000000-0000-4000-8000-000000000001"},
		{"POST", "/admin/accounts/candy-tests/00000000-0000-4000-8000-000000000001/cancel"},
		{"GET", "/admin/accounts/8/candy-tests"},
	} {
		request := httptest.NewRequest(tc.method, tc.path, strings.NewReader(`{}`))
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, request)
		require.Equal(t, http.StatusUnauthorized, recorder.Code, tc.path)
		request = httptest.NewRequest(tc.method, tc.path, strings.NewReader(`{}`))
		request.Header.Set("X-Test-Admin", "yes")
		recorder = httptest.NewRecorder()
		router.ServeHTTP(recorder, request)
		require.Equal(t, http.StatusServiceUnavailable, recorder.Code, tc.path)
		require.Equal(t, "private, no-store", recorder.Header().Get("Cache-Control"))
	}
}
