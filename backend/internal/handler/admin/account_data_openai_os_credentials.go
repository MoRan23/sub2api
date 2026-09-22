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

func (h *AccountHandler) exportOpenAIOAuthAuthorizations(_ context.Context, account *service.Account) (string, map[string]DataOpenAIOAuthAuthorization, error) {
	if !service.IsOpenAIOAuthOSProfileOwner(account) {
		return "", nil, nil
	}
	// The account row is the only credential source. OS metadata is an optional
	// identity hint; an empty or unavailable legacy slot cannot erase this tuple.
	defaultOS := ""
	if account.OpenAIOAuthOSProfiles != nil {
		defaultOS = account.OpenAIOAuthOSProfiles.DefaultOS
	}
	account.Credentials = portableOpenAIOAuthCredentials(account, account.Credentials)
	return defaultOS, nil, nil
}

// Old multi-OS backups retain only the saved default identity's complete tuple. Never
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
	if slot, exists := item.OpenAIOAuthAuthorizations[defaultOS]; exists {
		credentials := service.OpenAIOAuthProviderCredentials(slot.Credentials)
		if strings.TrimSpace(codexCredentialString(credentials, "access_token")) != "" || strings.TrimSpace(codexCredentialString(credentials, "refresh_token")) != "" {
			delete(credentials, "_token_version")
			item.Credentials = service.PreserveOpenAIOAuthProviderCredentials(credentials, item.Credentials)
			return defaultOS, nil, nil
		}
	}
	item.Credentials = service.PreserveOpenAIOAuthProviderCredentials(nil, item.Credentials)
	return defaultOS, nil, nil
}
