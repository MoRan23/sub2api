package admin

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

func (h *AccountHandler) SetAttributionService(s *service.ModelAttributionService) {
	h.attributionService = s
}
func (h *AccountHandler) attributionReady(c *gin.Context) bool {
	c.Header("Cache-Control", "private, no-store")
	if h.attributionService == nil {
		response.Error(c, http.StatusServiceUnavailable, "Model attribution unavailable")
		return false
	}
	return true
}
func attributionError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrAttributionInvalid):
		response.BadRequest(c, "Invalid attribution configuration or request")
	case errors.Is(err, service.ErrAttributionDisabled):
		response.BadRequest(c, "Enable model attribution after configuring the service and both whitelists")
	case errors.Is(err, service.ErrAttributionConflict):
		response.Error(c, http.StatusConflict, "Configuration changed; reload before saving")
	case errors.Is(err, service.ErrAttributionNotFound):
		response.NotFound(c, "Attribution record not found")
	default:
		response.Error(c, http.StatusInternalServerError, "Model attribution operation failed")
	}
}
func (h *AccountHandler) AttributionConfig(c *gin.Context) {
	if !h.attributionReady(c) {
		return
	}
	v, err := h.attributionService.Config(c.Request.Context())
	if err != nil {
		attributionError(c, err)
		return
	}
	response.Success(c, v)
}
func (h *AccountHandler) SaveAttributionConfig(c *gin.Context) {
	if !h.attributionReady(c) {
		return
	}
	var v service.AttributionConfig
	if c.ShouldBindJSON(&v) != nil {
		response.BadRequest(c, "Invalid configuration")
		return
	}
	out, err := h.attributionService.SaveConfig(c.Request.Context(), v)
	if err != nil {
		attributionError(c, err)
		return
	}
	response.Success(c, out)
}
func (h *AccountHandler) AttributionModels(c *gin.Context) {
	if !h.attributionReady(c) {
		return
	}
	var v struct {
		BaseURL string `json:"base_url"`
	}
	if c.ShouldBindJSON(&v) != nil {
		response.BadRequest(c, "Invalid service address")
		return
	}
	if _, err := service.AttributionBaseURL(v.BaseURL); err != nil {
		attributionError(c, err)
		return
	}
	models, err := h.attributionService.Models(c.Request.Context(), v.BaseURL)
	if err != nil {
		response.Error(c, http.StatusBadGateway, "ModelTrace unavailable or invalid model bank")
		return
	}
	response.Success(c, gin.H{"models": models})
}
func (h *AccountHandler) CreateAttributionJobs(c *gin.Context) {
	if !h.attributionReady(c) {
		return
	}
	var v struct {
		AccountIDs []int64 `json:"account_ids"`
		Model      string  `json:"model"`
	}
	if c.ShouldBindJSON(&v) != nil {
		response.BadRequest(c, "Invalid account IDs")
		return
	}
	jobs, err := h.attributionService.Create(c.Request.Context(), v.AccountIDs, v.Model)
	if err != nil {
		attributionError(c, err)
		return
	}
	response.Success(c, gin.H{"items": jobs})
}
func (h *AccountHandler) ListAttributionJobs(c *gin.Context) {
	if !h.attributionReady(c) {
		return
	}
	page, size := response.ParsePagination(c)
	var id int64
	if raw := c.Query("account_id"); raw != "" {
		var err error
		id, err = strconv.ParseInt(raw, 10, 64)
		if err != nil || id < 1 {
			response.BadRequest(c, "Invalid account ID")
			return
		}
	}
	out, err := h.attributionService.List(c.Request.Context(), id, page, size)
	if err != nil {
		attributionError(c, err)
		return
	}
	response.Success(c, out)
}
func (h *AccountHandler) GetAttributionJob(c *gin.Context) {
	if !h.attributionReady(c) {
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id < 1 {
		response.BadRequest(c, "Invalid record ID")
		return
	}
	j, err := h.attributionService.Get(c.Request.Context(), id)
	if err != nil {
		attributionError(c, err)
		return
	}
	response.Success(c, j)
}
