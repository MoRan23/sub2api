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

func TestGroupApplicationEmailConfigSensitiveRoutesRequireStepUp(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tests := []struct {
		method string
		path   string
	}{
		{method: http.MethodPut, path: "/admin/group-applications/email-config"},
		{method: http.MethodPost, path: "/admin/group-applications/email-config/test-smtp"},
		{method: http.MethodPost, path: "/admin/group-applications/email-config/send-test"},
		{method: http.MethodPost, path: "/admin/group-applications/email-config/test-imap"},
	}

	for _, test := range tests {
		t.Run(test.method+" "+test.path, func(t *testing.T) {
			stepUpCalls := 0
			stepUp := middleware.StepUpAuthMiddleware(func(c *gin.Context) {
				stepUpCalls++
				c.AbortWithStatus(http.StatusPreconditionRequired)
			})
			handlers := &handler.Handlers{Admin: &handler.AdminHandlers{
				GroupApplication: adminhandler.NewGroupApplicationHandler(nil, nil),
			}}
			router := gin.New()
			registerGroupApplicationRoutes(router.Group("/admin"), handlers, stepUp)

			request := httptest.NewRequest(test.method, test.path, nil)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)

			require.Equal(t, http.StatusPreconditionRequired, response.Code)
			require.Equal(t, 1, stepUpCalls)
		})
	}
}
