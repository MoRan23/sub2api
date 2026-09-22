package admin

import (
	"context"
	"strconv"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// ExportCodexAuth returns a fresh credential snapshot without refreshing it.
// GET /api/v1/admin/accounts/:id/codex-auth
func (h *AccountHandler) ExportCodexAuth(c *gin.Context) {
	c.Header("Cache-Control", "private, no-store")
	c.Header("X-Content-Type-Options", "nosniff")
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		response.BadRequest(c, "Invalid account ID")
		return
	}
	// AdminService.GetAccount delegates directly to AccountRepository.GetByID;
	// do not use a scheduler snapshot or follow a credential shadow here.
	account, err := h.adminService.GetAccount(c.Request.Context(), id)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	if !service.IsOpenAIOAuthOSProfileOwner(account) {
		_, err := service.BuildOpenAICodexAuthExport(account, time.Now())
		response.ErrorFrom(c, err)
		return
	}
	os := service.NormalizeOpenAIOSFamily(c.Query("os"))
	if c.Query("os") != "" && os == "" {
		response.BadRequest(c, "os must be windows, macos, or linux")
		return
	}
	if os == "" && account.OpenAIOAuthOSProfiles != nil {
		os = account.OpenAIOAuthOSProfiles.DefaultOS
	}
	reader, ok := h.adminService.(interface {
		GetOpenAIOAuthOSCredential(context.Context, int64, string) (*service.OpenAIOAuthOSCredential, error)
	})
	if !ok || os == "" {
		response.BadRequest(c, "selected OS has no saved OAuth authorization")
		return
	}
	slot, err := reader.GetOpenAIOAuthOSCredential(c.Request.Context(), id, os)
	if err != nil {
		response.BadRequest(c, "selected OS has no saved OAuth authorization")
		return
	}
	exported, err := service.BuildOpenAICodexAuthExportForOS(account, slot, time.Now())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, exported)
}
