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
		return "", nil, errors.New("OpenAI OS authorization storage is unavailable")
	}
	defaultOS := account.OpenAIOAuthOSProfiles.DefaultOS
	slots, err := reader.ListOpenAIOAuthOSCredentials(ctx, account.ID)
	if err != nil {
		return "", nil, errors.New("unable to read OpenAI OS authorizations")
	}
	out := make(map[string]DataOpenAIOAuthAuthorization)
	// Clear the compatibility mirror before projecting the actual default slot.
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
		out[slot.OSFamily] = DataOpenAIOAuthAuthorization{Credentials: credentials}
		if slot.OSFamily == defaultOS {
			account.Credentials = service.PreserveOpenAIOAuthProviderCredentials(credentials, account.Credentials)
		}
	}
	return defaultOS, out, nil
}

// Explicit multi-OS backup imports verify each refresh token before creating the
// account. The repository then inserts every verified slot in one transaction.
// Legacy single-credential backups still establish only the default OS, including
// older access-token-only accounts that cannot renew their credentials.
func (h *AccountHandler) prepareOpenAIOAuthBackupImport(ctx context.Context, item *DataAccount, proxyID *int64) (string, map[string]map[string]any, error) {
	account := &service.Account{Platform: item.Platform, Type: item.Type, Credentials: item.Credentials, Extra: item.Extra}
	if item.OpenAIOAuthDefaultOS == "" && len(item.OpenAIOAuthAuthorizations) == 0 {
		if service.IsOpenAIOAuthOSProfileOwner(account) {
			item.Credentials = portableOpenAIOAuthCredentials(account, item.Credentials)
			item.Extra = portableOpenAIOAuthExtra(account)
		}
		return "", nil, nil
	}
	if !service.IsOpenAIOAuthOSProfileOwner(account) {
		return "", nil, errors.New("OS authorizations require a regular OpenAI OAuth account")
	}
	defaultOS := service.NormalizeOpenAIOSFamily(item.OpenAIOAuthDefaultOS)
	if defaultOS == "" {
		return "", nil, errors.New("openai_oauth_default_os must be windows, macos, or linux")
	}
	seenRefresh := make(map[string]struct{})
	_, containsDefault := item.OpenAIOAuthAuthorizations[defaultOS]
	requiresVerification := len(item.OpenAIOAuthAuthorizations) > 1 || len(item.OpenAIOAuthAuthorizations) == 1 && !containsDefault
	for os, slot := range item.OpenAIOAuthAuthorizations {
		if os == "" || service.NormalizeOpenAIOSFamily(os) != os || len(slot.Credentials) == 0 {
			return "", nil, errors.New("invalid OpenAI OS authorization mapping")
		}
		if !service.IsOpenAIOAuthOSProfileOwner(&service.Account{Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Credentials: slot.Credentials}) {
			return "", nil, errors.New("OS authorization mappings do not support PAT or Agent Identity credentials")
		}
		refresh := strings.TrimSpace(codexCredentialString(slot.Credentials, "refresh_token"))
		if requiresVerification && refresh == "" {
			return "", nil, errors.New("importing additional OS authorizations requires refresh_token for every saved OS; reauthorize missing systems")
		}
		if refresh != "" {
			if _, exists := seenRefresh[refresh]; exists {
				return "", nil, errors.New("each OS requires an independent refresh token; start a new OAuth login")
			}
			seenRefresh[refresh] = struct{}{}
		}
	}
	item.Credentials = portableOpenAIOAuthCredentials(account, item.Credentials)
	item.Extra = portableOpenAIOAuthExtra(account)
	if len(item.OpenAIOAuthAuthorizations) == 0 {
		return defaultOS, nil, nil
	}
	if len(item.OpenAIOAuthAuthorizations) == 1 {
		if slot, exists := item.OpenAIOAuthAuthorizations[defaultOS]; exists {
			item.Credentials = service.PreserveOpenAIOAuthProviderCredentials(slot.Credentials, item.Credentials)
			delete(item.Credentials, "_token_version")
			return defaultOS, nil, nil
		}
	}
	if h.openaiOAuthService == nil {
		return "", nil, errors.New("importing additional OS authorizations requires refresh_token verification or OAuth reauthorization")
	}
	var proxyURL string
	if proxyID != nil {
		proxy, err := h.adminService.GetProxy(ctx, *proxyID)
		if err != nil || proxy == nil {
			return "", nil, errors.New("unable to resolve authorization proxy")
		}
		proxyURL = proxy.URL()
	}
	verified := make(map[string]map[string]any, len(item.OpenAIOAuthAuthorizations))
	var accountID, userID string
	for _, os := range service.OpenAIOAuthOSFamilies() {
		slot, exists := item.OpenAIOAuthAuthorizations[os]
		if !exists {
			continue
		}
		refresh := strings.TrimSpace(codexCredentialString(slot.Credentials, "refresh_token"))
		if refresh == "" {
			return "", nil, errors.New("importing additional OS authorizations requires refresh_token for every saved OS; reauthorize missing systems")
		}
		info, err := h.openaiOAuthService.RefreshTokenForOS(ctx, refresh, proxyURL, codexCredentialString(slot.Credentials, "client_id"), os)
		if err != nil || info == nil || info.ChatGPTAccountID == "" || info.ChatGPTUserID == "" {
			return "", nil, errors.New("unable to verify an imported OS authorization; reauthorize that OS")
		}
		if accountID != "" && (accountID != info.ChatGPTAccountID || userID != info.ChatGPTUserID) {
			return "", nil, errors.New("all OS authorizations must belong to the same ChatGPT account and user")
		}
		accountID, userID = info.ChatGPTAccountID, info.ChatGPTUserID
		verified[os] = h.openaiOAuthService.BuildAccountCredentials(info)
	}
	// Never seed an unverified compatibility copy while importing verified slots.
	item.Credentials = service.PreserveOpenAIOAuthProviderCredentials(verified[defaultOS], item.Credentials)
	return defaultOS, verified, nil
}
