package handler

import (
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

type OfficialStatusHandler struct {
	service *service.OfficialStatusService
}

func NewOfficialStatusHandler(svc *service.OfficialStatusService) *OfficialStatusHandler {
	return &OfficialStatusHandler{service: svc}
}

func (h *OfficialStatusHandler) Get(c *gin.Context) {
	c.Header("Cache-Control", "private, no-store")
	response.Success(c, h.service.Get())
}
