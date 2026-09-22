package admin

import (
	"context"
	"strconv"
	"strings"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

type codexTurnStateStatusService interface {
	GetStatus(context.Context, int64) (*service.CodexTurnStateStatus, error)
	GetStatuses(context.Context, []int64) (*service.CodexTurnStateBatchStatus, error)
}

type codexTurnStateOSStatusService interface {
	GetStatusForOS(context.Context, int64, string) (*service.CodexTurnStateStatus, error)
	GetStatusesForOS(context.Context, []int64, string) (*service.CodexTurnStateBatchStatus, error)
}

func codexTurnStateQueryOS(c *gin.Context) (string, bool) {
	values, exists := c.GetQueryArray("os")
	if !exists {
		return "", true
	}
	if len(values) != 1 || values[0] == "" || service.NormalizeOpenAIOSFamily(values[0]) != values[0] {
		response.BadRequest(c, "os must be windows, macos, or linux")
		return "", false
	}
	return values[0], true
}

func (h *AccountHandler) SetCodexTurnStateService(state codexTurnStateStatusService) {
	h.codexTurnState = state
}

func (h *AccountHandler) GetCodexTurnStates(c *gin.Context) {
	os, valid := codexTurnStateQueryOS(c)
	if !valid {
		return
	}
	const maxAccountIDs = 200
	const maxAccountIDsQueryBytes = 8192
	queries, present := c.GetQueryArray("account_ids")
	if !present || len(queries) != 1 || queries[0] == "" || len(queries[0]) > maxAccountIDsQueryBytes {
		response.BadRequest(c, "account_ids must contain 1 to 200 unique positive account IDs")
		return
	}
	ids := make([]int64, 0, maxAccountIDs)
	seen := make(map[int64]struct{}, maxAccountIDs)
	for _, raw := range strings.Split(queries[0], ",") {
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || id <= 0 || raw[0] < '0' || raw[0] > '9' {
			response.BadRequest(c, "Invalid account ID in account_ids")
			return
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
		if len(ids) > maxAccountIDs {
			response.BadRequest(c, "account_ids must contain at most 200 unique account IDs")
			return
		}
	}
	if h.codexTurnState == nil {
		response.ErrorFrom(c, infraerrors.New(503, "CODEX_TURN_STATE_UNAVAILABLE", "turn-state storage is unavailable"))
		return
	}
	var status *service.CodexTurnStateBatchStatus
	var err error
	if scoped, ok := h.codexTurnState.(codexTurnStateOSStatusService); ok {
		status, err = scoped.GetStatusesForOS(c.Request.Context(), ids, os)
	} else if os == "" {
		status, err = h.codexTurnState.GetStatuses(c.Request.Context(), ids)
	} else {
		response.BadRequest(c, "OS-scoped turn-state status is unavailable")
		return
	}
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, status)
}

func (h *AccountHandler) GetCodexTurnState(c *gin.Context) {
	os, valid := codexTurnStateQueryOS(c)
	if !valid {
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		response.BadRequest(c, "Invalid account ID")
		return
	}
	if h.codexTurnState == nil {
		response.ErrorFrom(c, infraerrors.New(503, "CODEX_TURN_STATE_UNAVAILABLE", "turn-state storage is unavailable"))
		return
	}
	var status *service.CodexTurnStateStatus
	if scoped, ok := h.codexTurnState.(codexTurnStateOSStatusService); ok {
		status, err = scoped.GetStatusForOS(c.Request.Context(), id, os)
	} else if os == "" {
		status, err = h.codexTurnState.GetStatus(c.Request.Context(), id)
	} else {
		response.BadRequest(c, "OS-scoped turn-state status is unavailable")
		return
	}
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, status)
}
