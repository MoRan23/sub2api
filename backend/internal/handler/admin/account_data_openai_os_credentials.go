package admin

import (
	"context"
	"errors"
	"maps"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

func portableOpenAIOAuthCredentials(account *service.Account, credentials map[string]any) map[string]any {
	if !service.IsOpenAIOAuthOSProfileOwner(account) {
		return credentials
	}
	out := maps.Clone(credentials)
	for _, key := range []string{"_token_version", "user_agent", "installation_id", "sync_session_id", "openai_oauth_os_profiles"} {
		delete(out, key)
	}
	return out
}

func portableOpenAIOAuthExtra(account *service.Account) map[string]any {
	out := service.StripCodexTurnStateManagedExtra(account.Extra)
	if !service.IsOpenAIOAuthOSProfileOwner(account) {
		return out
	}
	out = maps.Clone(out)
	for _, key := range []string{"openai_pinned_installation_id", "openai_installation_rotate_enabled", "openai_environment_fingerprint", "openai_oauth_os_profiles", "sync_session_id"} {
		delete(out, key)
	}
	return out
}

func (h *AccountHandler) exportOpenAIOAuthAuthorizations(ctx context.Context, account *service.Account) (string, map[string]DataOpenAIOAuthAuthorization, error) {
	if !service.IsOpenAIOAuthOSProfileOwner(account) {
		return "", nil, nil
	}
	reader, ok := h.adminService.(interface {
		ListOpenAIOAuthOSCredentials(context.Context, int64) ([]*service.OpenAIOAuthOSCredential, error)
	})
	if !ok || account.OpenAIOAuthOSProfiles == nil {
		return "", nil, errors.New("OpenAI OAuth authorization storage is unavailable")
	}
	defaultOS := account.OpenAIOAuthOSProfiles.DefaultOS
	slots, err := reader.ListOpenAIOAuthOSCredentials(ctx, account.ID)
	if err != nil {
		return "", nil, errors.New("unable to read OpenAI OAuth authorization")
	}
	// New exports contain one shared credential tuple. The legacy OS map remains
	// an import-only format and must not duplicate the same refresh token.
	account.Credentials = service.PreserveOpenAIOAuthProviderCredentials(nil, account.Credentials)
	for _, slot := range slots {
		if slot == nil || slot.OwnerAccountID != account.ID || service.NormalizeOpenAIOSFamily(slot.OSFamily) == "" {
			continue
		}
		credentials := service.OpenAIOAuthProviderCredentials(slot.Credentials)
		delete(credentials, "_token_version")
		if strings.TrimSpace(codexCredentialString(credentials, "access_token")) == "" && strings.TrimSpace(codexCredentialString(credentials, "refresh_token")) == "" {
			continue
		}
		account.Credentials = service.PreserveOpenAIOAuthProviderCredentials(credentials, account.Credentials)
		break
	}
	return defaultOS, nil, nil
}

// Old multi-OS backups collapse to one complete provider credential tuple. Prefer
// the saved default identity; otherwise use the first populated known OS. Never
// exchange unused refresh tokens or combine credentials from different grants.
func (h *AccountHandler) prepareOpenAIOAuthBackupImport(_ context.Context, item *DataAccount, _ *int64) (string, map[string]map[string]any, error) {
	account := &service.Account{Platform: item.Platform, Type: item.Type, Credentials: item.Credentials, Extra: item.Extra}
	if item.OpenAIOAuthDefaultOS == "" && len(item.OpenAIOAuthAuthorizations) == 0 {
		if service.IsOpenAIOAuthOSProfileOwner(account) {
			item.Credentials = portableOpenAIOAuthCredentials(account, item.Credentials)
			item.Extra = portableOpenAIOAuthExtra(account)
		}
		return "", nil, nil
	}
	if !service.IsOpenAIOAuthOSProfileOwner(account) {
		return "", nil, errors.New("OAuth authorizations require a regular OpenAI OAuth account")
	}
	defaultOS := service.NormalizeOpenAIOSFamily(item.OpenAIOAuthDefaultOS)
	if defaultOS == "" {
		return "", nil, errors.New("openai_oauth_default_os must be windows, macos, or linux")
	}
	for os, slot := range item.OpenAIOAuthAuthorizations {
		if os == "" || service.NormalizeOpenAIOSFamily(os) != os || len(slot.Credentials) == 0 {
			return "", nil, errors.New("invalid OpenAI OS authorization mapping")
		}
		if !service.IsOpenAIOAuthOSProfileOwner(&service.Account{Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Credentials: slot.Credentials}) {
			return "", nil, errors.New("OS authorization mappings do not support PAT or Agent Identity credentials")
		}
	}
	item.Credentials = portableOpenAIOAuthCredentials(account, item.Credentials)
	item.Extra = portableOpenAIOAuthExtra(account)
	if len(item.OpenAIOAuthAuthorizations) == 0 {
		return defaultOS, nil, nil
	}
	order := append([]string{defaultOS}, service.OpenAIOAuthOSFamilies()...)
	for _, os := range order {
		slot, exists := item.OpenAIOAuthAuthorizations[os]
		if !exists {
			continue
		}
		credentials := service.OpenAIOAuthProviderCredentials(slot.Credentials)
		if strings.TrimSpace(codexCredentialString(credentials, "access_token")) == "" && strings.TrimSpace(codexCredentialString(credentials, "refresh_token")) == "" {
			continue
		}
		delete(credentials, "_token_version")
		item.Credentials = service.PreserveOpenAIOAuthProviderCredentials(credentials, item.Credentials)
		return defaultOS, nil, nil
	}
	item.Credentials = service.PreserveOpenAIOAuthProviderCredentials(nil, item.Credentials)
	return defaultOS, nil, nil
}
