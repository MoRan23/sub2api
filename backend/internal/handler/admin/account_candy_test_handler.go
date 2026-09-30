package admin

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

func (h *AccountHandler) SetCandyTestService(s *service.AccountCandyTestService) {
	h.candyTestService = s
}

func (h *AccountHandler) candyTestsReady(c *gin.Context) bool {
	c.Header("Cache-Control", "private, no-store")
	if h.candyTestService == nil {
		response.Error(c, http.StatusServiceUnavailable, "Pelican test service unavailable")
		return false
	}
	return true
}

func candyTestHTTPError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrCandyTestNotFound):
		response.NotFound(c, "Pelican test not found")
	case errors.Is(err, service.ErrCandyTestInvalidRequest):
		response.BadRequest(c, "Invalid pelican test request")
	case errors.Is(err, service.ErrCandyTestIdempotencyConflict):
		response.Error(c, http.StatusConflict, "Idempotency key already used with a different request")
	default:
		// Database and provider errors may contain credentials or arbitrary upstream
		// text; only return a fixed diagnostic to the administrator.
		response.Error(c, http.StatusInternalServerError, "Pelican test operation failed")
	}
}

func (h *AccountHandler) CandyTestOptions(c *gin.Context) {
	if !h.candyTestsReady(c) {
		return
	}
	var request struct {
		AccountIDs []int64 `json:"account_ids"`
	}
	if c.ShouldBindJSON(&request) != nil {
		response.BadRequest(c, "Invalid pelican test request")
		return
	}
	options, err := h.candyTestService.Options(c.Request.Context(), request.AccountIDs)
	if err != nil {
		candyTestHTTPError(c, err)
		return
	}
	response.Success(c, options)
}

func (h *AccountHandler) CreateCandyTests(c *gin.Context) {
	if !h.candyTestsReady(c) {
		return
	}
	var request service.CandyTestCreateRequest
	if c.ShouldBindJSON(&request) != nil {
		response.BadRequest(c, "Invalid pelican test request")
		return
	}
	batch, err := h.candyTestService.Create(c.Request.Context(), &request)
	if err != nil {
		candyTestHTTPError(c, err)
		return
	}
	response.Success(c, batch)
}

func candyBatchID(c *gin.Context) (string, bool) {
	id, err := uuid.Parse(c.Param("batch_id"))
	if err != nil {
		response.BadRequest(c, "Invalid batch ID")
		return "", false
	}
	return id.String(), true
}

func (h *AccountHandler) GetCandyTestBatch(c *gin.Context) {
	if !h.candyTestsReady(c) {
		return
	}
	id, ok := candyBatchID(c)
	if !ok {
		return
	}
	page, size := response.ParsePagination(c)
	batch, err := h.candyTestService.Batch(c.Request.Context(), id, page, size)
	if err != nil {
		candyTestHTTPError(c, err)
		return
	}
	response.Success(c, batch)
}

func (h *AccountHandler) CancelCandyTests(c *gin.Context) {
	if !h.candyTestsReady(c) {
		return
	}
	id, ok := candyBatchID(c)
	if !ok {
		return
	}
	var request struct {
		ItemIDs []int64 `json:"item_ids"`
	}
	if c.ShouldBindJSON(&request) != nil {
		response.BadRequest(c, "Invalid cancellation request")
		return
	}
	if len(request.ItemIDs) > 1000 {
		response.BadRequest(c, "Too many test items")
		return
	}
	for _, itemID := range request.ItemIDs {
		if itemID <= 0 {
			response.BadRequest(c, "Invalid test item ID")
			return
		}
	}
	if err := h.candyTestService.Cancel(c.Request.Context(), id, request.ItemIDs); err != nil {
		candyTestHTTPError(c, err)
		return
	}
	response.Success(c, gin.H{"cancelled": true})
}

func (h *AccountHandler) GetCandyTestHistory(c *gin.Context) {
	if !h.candyTestsReady(c) {
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		response.BadRequest(c, "Invalid account ID")
		return
	}
	items, err := h.candyTestService.History(c.Request.Context(), id)
	if err != nil {
		candyTestHTTPError(c, err)
		return
	}
	summaries, err := h.candyTestService.Summaries(c.Request.Context(), []int64{id})
	if err != nil {
		candyTestHTTPError(c, err)
		return
	}
	response.Success(c, gin.H{"items": items, "summary": summaries[id]})
}
