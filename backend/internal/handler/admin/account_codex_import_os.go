package admin

import (
	"context"
	"errors"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

func (h *AccountHandler) authorizeCodexImportedOS(ctx context.Context, existing *service.Account, requestedOS string, item *codexImportAccount, credentials map[string]any) (*service.Account, error) {
	validationError := errors.New("updating an existing OS authorization requires refresh_token verification or OAuth reauthorization")
	fresh, err := h.adminService.GetAccount(ctx, existing.ID)
	if err != nil || fresh == nil || fresh.OpenAIOAuthOSProfiles == nil || !service.IsOpenAIOAuthOSProfileOwner(fresh) {
		return nil, validationError
	}
	os := service.NormalizeOpenAIOSFamily(requestedOS)
	if os == "" {
		os = fresh.OpenAIOAuthOSProfiles.DefaultOS
	}
	if strings.TrimSpace(item.RefreshToken) == "" {
		// Reimporting the exact default access token is a configuration-only edit.
		// Any new token or another OS requires a server-verified authorization.
		reader, ok := h.adminService.(interface {
			GetOpenAIOAuthOSCredential(context.Context, int64, string) (*service.OpenAIOAuthOSCredential, error)
		})
		if !ok || os != fresh.OpenAIOAuthOSProfiles.DefaultOS || item.AccessToken == "" {
			return nil, validationError
		}
		slot, readErr := reader.GetOpenAIOAuthOSCredential(ctx, fresh.ID, os)
		if readErr != nil || slot == nil || slot.OwnerAccountID != fresh.ID || slot.OSFamily != os || slot.Status == service.OpenAIOAuthAuthorizationUnauthorized || codexCredentialString(slot.Credentials, "access_token") != item.AccessToken {
			return nil, validationError
		}
		copy := *fresh
		copy.Credentials = service.PreserveOpenAIOAuthProviderCredentials(slot.Credentials, fresh.Credentials)
		return &copy, nil
	}
	if h.openaiOAuthService == nil {
		return nil, validationError
	}
	info, err := h.openaiOAuthService.AuthorizeAccountWithRefreshToken(ctx, fresh.ID, os, item.RefreshToken, codexCredentialString(credentials, "client_id"))
	if err != nil || info == nil || info.Account == nil {
		return nil, errors.New("unable to verify OAuth authorization for the selected OS; reauthorize that OS")
	}
	return info.Account, nil
}
