package routes

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/handler"
	adminhandler "github.com/Wei-Shaw/sub2api/internal/handler/admin"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestGroupApplicationRoutesRejectSimpleMode(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := &config.Config{RunMode: config.RunModeSimple}
	handlers := &handler.Handlers{
		GroupApplication: handler.NewGroupApplicationHandler(nil),
		Admin: &handler.AdminHandlers{
			Group:            adminhandler.NewGroupHandlerWithConfig(nil, nil, nil, cfg),
			GroupApplication: adminhandler.NewGroupApplicationHandler(nil, nil),
		},
	}
	router := gin.New()
	v1 := router.Group("/api/v1")
	jwtAuth := middleware.JWTAuthMiddleware(func(c *gin.Context) {
		c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 1})
		c.Set(string(middleware.ContextKeyUserRole), "user")
		c.Next()
	})
	adminAuth := middleware.AdminAuthMiddleware(func(c *gin.Context) {
		c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 1})
		c.Set(string(middleware.ContextKeyUserRole), "admin")
		c.Next()
	})
	auditLog := middleware.AuditLogMiddleware(func(c *gin.Context) { c.Next() })
	stepUpCalls := 0
	stepUp := middleware.StepUpAuthMiddleware(func(c *gin.Context) {
		stepUpCalls++
		c.AbortWithStatus(http.StatusPreconditionRequired)
	})
	RegisterUserRoutes(v1, handlers, jwtAuth, auditLog, nil, nil, cfg)
	RegisterAdminRoutes(v1, handlers, adminAuth, auditLog, stepUp, nil, nil, cfg)

	checked := 0
	for _, route := range router.Routes() {
		if !strings.Contains(route.Path, "/group-applications") && !strings.Contains(route.Path, "/group-application-policies") {
			continue
		}
		checked++
		t.Run(route.Method+" "+route.Path, func(t *testing.T) {
			path := strings.NewReplacer(":id", "1", ":outbox_id", "1", ":group_id", "1", ":attachment_id", "1").Replace(route.Path)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(route.Method, path, nil))
			require.Equal(t, http.StatusForbidden, response.Code)
			require.Contains(t, response.Body.String(), `"reason":"SIMPLE_MODE_OPERATION_UNSUPPORTED"`)
		})
	}
	require.Equal(t, 23, checked, "all user, admin, policy, and attachment routes must be covered")
	require.Zero(t, stepUpCalls, "mode rejection precedes sensitive operation checks")

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/admin/groups/invalid", nil))
	require.Equal(t, http.StatusBadRequest, response.Code, "ordinary group routes still reach their handler")
}

func TestGroupApplicationModeGuardAllowsStandardMode(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name string
		cfg  *config.Config
	}{
		{name: "legacy nil config"},
		{name: "default config", cfg: &config.Config{}},
		{name: "standard config", cfg: &config.Config{RunMode: config.RunModeStandard}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			router := gin.New()
			router.GET("/probe", groupApplicationModeGuard(tc.cfg), func(c *gin.Context) {
				c.Status(http.StatusNoContent)
			})
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/probe", nil))
			require.Equal(t, http.StatusNoContent, response.Code)
		})
	}
}

func TestUserGroupApplicationRoutesRetainAuthenticationInStandardMode(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	handlers := &handler.Handlers{GroupApplication: handler.NewGroupApplicationHandler(nil)}
	registerUserGroupApplicationRoutes(router.Group("/api/v1"), handlers, &config.Config{RunMode: config.RunModeStandard})
	for _, route := range router.Routes() {
		t.Run(route.Method+" "+route.Path, func(t *testing.T) {
			response := httptest.NewRecorder()
			path := strings.ReplaceAll(route.Path, ":id", "1")
			router.ServeHTTP(response, httptest.NewRequest(route.Method, path, nil))
			require.Equal(t, http.StatusUnauthorized, response.Code)
			require.NotContains(t, response.Body.String(), "SIMPLE_MODE_OPERATION_UNSUPPORTED")
		})
	}
}
