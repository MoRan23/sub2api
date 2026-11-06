package admin

import (
	"errors"
	"net/http"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/pkg/runtimeobs"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

func (h *OpsHandler) requireRuntimeMemory(c *gin.Context) *runtimeobs.Collector {
	if h.opsService == nil || h.opsService.RuntimeMemory() == nil {
		response.Error(c, http.StatusServiceUnavailable, "Runtime memory monitoring is unavailable")
		return nil
	}
	if err := h.opsService.RequireMonitoringEnabled(c.Request.Context()); err != nil {
		response.ErrorFrom(c, err)
		return nil
	}
	return h.opsService.RuntimeMemory()
}
func (h *OpsHandler) GetRuntimeMemory(c *gin.Context) {
	if h.requireRuntimeMemory(c) == nil {
		return
	}
	c.Header("Cache-Control", "no-store")
	instance := c.Query("instance_id")
	if instance != "" {
		if _, err := uuid.Parse(instance); err != nil {
			response.Error(c, http.StatusBadRequest, "Invalid instance_id")
			return
		}
	}
	response.Success(c, h.opsService.GetRuntimeMemory(c.Request.Context(), instance))
}
func (h *OpsHandler) setHeapProfile(c *gin.Context, enabled bool) {
	collector := h.requireRuntimeMemory(c)
	if collector == nil {
		return
	}
	var req struct {
		InstanceID string `json:"instance_id" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, "instance_id is required")
		return
	}
	if req.InstanceID != runtimeobs.InstanceID() {
		response.Error(c, http.StatusConflict, runtimeobs.ErrInstanceMismatch.Error())
		return
	}
	if enabled {
		response.Success(c, collector.Profile().Enable())
	} else {
		response.Success(c, collector.Profile().Disable())
	}
}
func (h *OpsHandler) EnableHeapProfile(c *gin.Context)  { h.setHeapProfile(c, true) }
func (h *OpsHandler) DisableHeapProfile(c *gin.Context) { h.setHeapProfile(c, false) }
func (h *OpsHandler) DownloadHeapProfile(c *gin.Context) {
	collector := h.requireRuntimeMemory(c)
	if collector == nil {
		return
	}
	release, err := collector.Profile().Acquire(c.Query("instance_id"))
	if err != nil {
		status := http.StatusConflict
		if errors.Is(err, runtimeobs.ErrProfileDisabled) {
			status = http.StatusForbidden
		}
		response.Error(c, status, err.Error())
		return
	}
	defer release()
	c.Header("Content-Type", "application/octet-stream")
	c.Header("Content-Disposition", `attachment; filename="heap-`+runtimeobs.InstanceID()+`.pprof"`)
	c.Header("Cache-Control", "no-store")
	// Direct writer: no temporary file, forced GC or MemProfileRate changes.
	if err := runtimeobs.WriteHeap(c.Writer); err != nil {
		_ = c.Error(err)
	}
}
