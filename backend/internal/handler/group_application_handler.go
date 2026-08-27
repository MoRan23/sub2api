package handler

import (
	"strconv"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

type GroupApplicationHandler struct {
	service *service.GroupApplicationService
}

func NewGroupApplicationHandler(service *service.GroupApplicationService) *GroupApplicationHandler {
	return &GroupApplicationHandler{service: service}
}

func groupApplicationUserID(c *gin.Context) (int64, bool) {
	subject, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok || subject.UserID <= 0 {
		response.Unauthorized(c, "User not authenticated")
		return 0, false
	}
	return subject.UserID, true
}

func (h *GroupApplicationHandler) Summary(c *gin.Context) {
	userID, ok := groupApplicationUserID(c)
	if !ok {
		return
	}
	options, err := h.service.ListOptions(c.Request.Context(), userID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	items, err := h.service.ListUserApplications(c.Request.Context(), userID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	active := 0
	available := 0
	for _, option := range options {
		if !option.HasActive && !option.AlreadyCompleted {
			available++
		}
	}
	for _, item := range items {
		if item.Status == service.GroupApplicationStatusPending || item.Status == service.GroupApplicationStatusAwaitingReply {
			active++
		}
	}
	response.Success(c, gin.H{"available_count": available, "active_count": active, "has_history": len(items) > 0})
}

func (h *GroupApplicationHandler) Options(c *gin.Context) {
	userID, ok := groupApplicationUserID(c)
	if !ok {
		return
	}
	items, err := h.service.ListOptions(c.Request.Context(), userID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, items)
}

func (h *GroupApplicationHandler) List(c *gin.Context) {
	userID, ok := groupApplicationUserID(c)
	if !ok {
		return
	}
	items, err := h.service.ListUserApplications(c.Request.Context(), userID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, items)
}

func (h *GroupApplicationHandler) Get(c *gin.Context) {
	userID, ok := groupApplicationUserID(c)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid application ID")
		return
	}
	item, err := h.service.GetUserApplication(c.Request.Context(), userID, id)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, item)
}

func (h *GroupApplicationHandler) Create(c *gin.Context) {
	userID, ok := groupApplicationUserID(c)
	if !ok {
		return
	}
	var input struct {
		GroupID      int64  `json:"group_id"`
		ContactEmail string `json:"contact_email"`
		Reason       string `json:"reason"`
		Locale       string `json:"locale"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		response.BadRequest(c, "Invalid request body")
		return
	}
	if input.Locale == "" {
		input.Locale = strings.Split(c.GetHeader("Accept-Language"), ",")[0]
	}
	item, err := h.service.Submit(c.Request.Context(), userID, input.GroupID, input.ContactEmail, input.Reason, input.Locale)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, item)
}
