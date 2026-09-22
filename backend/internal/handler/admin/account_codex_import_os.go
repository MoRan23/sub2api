package admin

import (
	"context"
	"errors"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

func (h *AccountHandler) authorizeCodexImportedOS(ctx context.Context, existing *service.Account, requestedOS string, item *codexImportAccount, credentials map[string]any) (*service.Account, error) {
	validationError := errors.New("unable to update account OAuth credentials")
	fresh, err := h.adminService.GetAccount(ctx, existing.ID)
	if err != nil || fresh == nil || !service.IsOpenAIOAuthOSProfileOwner(fresh) {
		return nil, validationError
	}
	binder, ok := h.adminService.(service.OpenAIOAuthCredentialsAdmin)
	if !ok {
		return nil, validationError
	}
	// Import already provides the credential tuple. Preserve the established
	// access-token-only merge behavior without an extra refresh-token exchange.
	merged := mergeCodexImportCredentials(fresh.Credentials, credentials, item)
	return binder.BindOpenAIOAuthCredentials(ctx, fresh.ID, requestedOS, merged)
}
