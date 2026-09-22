package admin

import (
	"context"
	"strconv"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/handler/dto"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

func respondOpenAIOAuthAuthorization(c *gin.Context, info *service.OpenAITokenInfo) {
	if info.Account != nil {
		response.Success(c, gin.H{"account": dto.AccountFromService(info.Account), "os": info.OS})
		return
	}
	response.Success(c, info)
}

func resolveAdminOpenAIOAuthCredentialAccount(ctx context.Context, admin service.AdminService, account *service.Account, os string) (*service.Account, error) {
	if !service.IsOpenAIOAuthOSProfileOwner(account) {
		return account, nil
	}
	if strings.TrimSpace(os) != "" && service.NormalizeOpenAIOSFamily(os) == "" {
		return nil, infraerrors.BadRequest("OPENAI_OAUTH_OS_REQUIRED", "invalid authorization OS")
	}
	resolver, ok := admin.(interface {
		ResolveOpenAIOAuthCredentialAccount(context.Context, int64, string) (*service.Account, error)
	})
	if !ok {
		return nil, infraerrors.ServiceUnavailable("OPENAI_OAUTH_STORAGE_UNAVAILABLE", "authorization storage is unavailable")
	}
	return resolver.ResolveOpenAIOAuthCredentialAccount(ctx, account.ID, os)
}

func (h *AccountHandler) RevokeOpenAIOAuthOSAuthorization(c *gin.Context) {
	h.mutateOpenAIOAuthOSAuthorization(c, false)
}
func (h *AccountHandler) SetDefaultOpenAIOAuthOS(c *gin.Context) {
	h.mutateOpenAIOAuthOSAuthorization(c, true)
}

func (h *AccountHandler) mutateOpenAIOAuthOSAuthorization(c *gin.Context, setDefault bool) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		response.BadRequest(c, "invalid account ID")
		return
	}
	os := service.NormalizeOpenAIOSFamily(c.Param("os"))
	if os == "" {
		response.BadRequest(c, "a valid authorization OS is required")
		return
	}
	admin, ok := h.adminService.(service.OpenAIOAuthOSAuthorizationAdmin)
	if !ok {
		response.ErrorFrom(c, infraerrors.ServiceUnavailable("OPENAI_OAUTH_STORAGE_UNAVAILABLE", "authorization storage is unavailable"))
		return
	}
	ctx := c.Request.Context()
	if setDefault {
		_, err = admin.SetDefaultOpenAIOAuthOS(ctx, id, os)
	} else {
		err = admin.RevokeOpenAIOAuthOSCredentials(ctx, id, os)
	}
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	account, err := h.adminService.GetAccount(ctx, id)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, h.buildAccountResponseWithRuntime(ctx, account))
}
