package admin

import (
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

func (h *GroupApplicationHandler) SaveEmailConfigSecure(c *gin.Context) {
	var input service.GroupApplicationEmailConfigInput
	if c.ShouldBindJSON(&input) != nil {
		response.BadRequest(c, "Invalid request body")
		return
	}
	item, err := h.service.SaveEmailConfigInput(c.Request.Context(), input)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	h.worker.RefreshConfiguration(c.Request.Context())
	response.Success(c, item)
}

func (h *GroupApplicationHandler) TestSMTPSecure(c *gin.Context) {
	var input service.GroupApplicationEmailConfigInput
	if c.ShouldBindJSON(&input) != nil {
		response.BadRequest(c, "Invalid request body")
		return
	}
	config, err := h.service.ResolveEmailConfigInputForTest(c.Request.Context(), input, "smtp")
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	if err := h.worker.TestSMTP(c.Request.Context(), *config); err != nil {
		response.ErrorFrom(c, groupApplicationSMTPTestError(err))
		return
	}
	response.Success(c, gin.H{"ok": true})
}

func (h *GroupApplicationHandler) SendTestEmailSecure(c *gin.Context) {
	var input struct {
		Config    service.GroupApplicationEmailConfigInput `json:"config"`
		Recipient string                                   `json:"recipient"`
	}
	if c.ShouldBindJSON(&input) != nil {
		response.BadRequest(c, "Invalid request body")
		return
	}
	config, err := h.service.ResolveEmailConfigInputForTest(c.Request.Context(), input.Config, "smtp")
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	if err := h.worker.SendTestEmail(c.Request.Context(), *config, input.Recipient); err != nil {
		response.ErrorFrom(c, groupApplicationSMTPSendTestError(err))
		return
	}
	response.Success(c, gin.H{"ok": true})
}

func (h *GroupApplicationHandler) TestIMAPSecure(c *gin.Context) {
	var input service.GroupApplicationEmailConfigInput
	if c.ShouldBindJSON(&input) != nil {
		response.BadRequest(c, "Invalid request body")
		return
	}
	config, err := h.service.ResolveEmailConfigInputForTest(c.Request.Context(), input, "imap")
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	mailboxes, err := h.worker.TestIMAP(c.Request.Context(), *config)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"ok": true, "mailboxes": mailboxes})
}
