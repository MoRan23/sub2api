package routes

import (
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

func groupApplicationModeGuard(cfg *config.Config) gin.HandlerFunc {
	return func(c *gin.Context) {
		if err := service.ValidateSimpleModeGroupOperation(cfg, "group_application"); err != nil {
			response.ErrorFrom(c, err)
			c.Abort()
			return
		}
		c.Next()
	}
}

func registerUserGroupApplicationRoutes(authenticated *gin.RouterGroup, h *handler.Handlers, cfg *config.Config) {
	applications := authenticated.Group("/group-applications", groupApplicationModeGuard(cfg))
	applications.GET("/summary", h.GroupApplication.Summary)
	applications.GET("/options", h.GroupApplication.Options)
	applications.GET("", h.GroupApplication.List)
	applications.POST("", h.GroupApplication.Create)
	applications.GET("/:id", h.GroupApplication.Get)
}
