package routes

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/handler"
	adminhandler "github.com/Wei-Shaw/sub2api/internal/handler/admin"
	servermiddleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestRuntimeMemoryRoutesRequireAdmin(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	h := &handler.Handlers{Admin: &handler.AdminHandlers{Ops: adminhandler.NewOpsHandler(nil)}}
	auth := servermiddleware.AdminAuthMiddleware(func(c *gin.Context) {
		if c.GetHeader("Authorization") == "" {
			c.AbortWithStatus(http.StatusUnauthorized)
		} else {
			c.AbortWithStatus(http.StatusForbidden)
		}
	})
	RegisterAdminRoutes(router.Group("/api/v1"), h, auth, servermiddleware.AuditLogMiddleware(func(c *gin.Context) { c.Next() }), servermiddleware.StepUpAuthMiddleware(func(c *gin.Context) { c.Next() }), nil, nil, nil)
	for _, tc := range []struct{ method, path string }{{"GET", "memory"}, {"POST", "heap-profile/enable"}, {"POST", "heap-profile/disable"}, {"GET", "heap-profile/download"}} {
		for _, token := range []string{"", "Bearer user"} {
			req := httptest.NewRequest(tc.method, "/api/v1/admin/ops/runtime/"+tc.path, nil)
			req.Header.Set("Authorization", token)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)
			expected := http.StatusUnauthorized
			if token != "" {
				expected = http.StatusForbidden
			}
			require.Equal(t, expected, w.Code, tc.path)
		}
	}
}
