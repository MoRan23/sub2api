package admin

import (
	"context"
	"strconv"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

func (h *AccountHandler) GetDaybreakCapabilities(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		response.BadRequest(c, "invalid account ID")
		return
	}
	reader, ok := h.adminService.(interface {
		GetAccountDaybreakCapabilities(context.Context, int64) (*service.OpenAIDaybreakCapabilities, error)
	})
	if !ok {
		response.Error(c, 503, "Daybreak capability lookup is unavailable")
		return
	}
	result, err := reader.GetAccountDaybreakCapabilities(c.Request.Context(), id)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}
