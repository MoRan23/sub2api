package admin

import (
	"context"
	"strconv"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

type codexTurnStateStatusService interface {
	GetStatus(context.Context, int64) (*service.CodexTurnStateStatus, error)
}

func (h *AccountHandler) SetCodexTurnStateService(state codexTurnStateStatusService) {
	h.codexTurnState = state
}

func (h *AccountHandler) GetCodexTurnState(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		response.BadRequest(c, "Invalid account ID")
		return
	}
	if h.codexTurnState == nil {
		response.ErrorFrom(c, infraerrors.New(503, "CODEX_TURN_STATE_UNAVAILABLE", "turn-state storage is unavailable"))
		return
	}
	status, err := h.codexTurnState.GetStatus(c.Request.Context(), id)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, status)
}
