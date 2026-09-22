package service

import (
	"context"
	"net/http"
	"strings"
)

// OpenAIGatewayService implements service.PluginAccountDirectory for the OpenAI
// OAuth outbound transport capability. The directory is intentionally scoped to
// OpenAI OAuth-like, non-shadow accounts regardless of the requested filter, so a
// plugin can never enumerate or resolve credentials outside that set. The host
// additionally only wires this directory into plugins whose manifest declares the
// matching capability (see PluginManager.buildHostServices).

func pluginAccountDirectoryEligible(account *Account) bool {
	return account != nil && account.Status == StatusActive && account.IsOpenAIOAuthLike() &&
		account.Type == AccountTypeOAuth && !account.IsShadow()
}

// ListPluginAccounts returns the ids of active OpenAI OAuth-like accounts.
func (s *OpenAIGatewayService) ListPluginAccounts(ctx context.Context, platform, accountType string) ([]int64, error) {
	if s == nil || s.accountRepo == nil {
		return nil, nil
	}
	if p := strings.TrimSpace(platform); p != "" && p != PlatformOpenAI {
		return nil, nil
	}
	if at := strings.TrimSpace(accountType); at != "" && at != AccountTypeOAuth {
		// Setup-token style accounts are OAuth-like but not AccountTypeOAuth; the
		// current binding only routes AccountTypeOAuth, so honour that filter.
		return nil, nil
	}
	accounts, err := s.accountRepo.ListByPlatform(ctx, PlatformOpenAI)
	if err != nil {
		return nil, err
	}
	ids := make([]int64, 0, len(accounts))
	for i := range accounts {
		account := &accounts[i]
		if pluginAccountDirectoryEligible(account) && OpenAIOAuthOSAuthorizationAvailable(account, OpenAIRequestOSFromContext(ctx).Family) {
			ids = append(ids, account.ID)
		}
	}
	return ids, nil
}

// ResolvePluginOutboundIdentity resolves the access token, account client headers,
// and business proxy. Request/turn identity and model-specific turn-state remain
// the responsibility of the gateway's physical request path. It returns (nil, nil)
// for out-of-scope accounts or when no token can be resolved.
func (s *OpenAIGatewayService) ResolvePluginOutboundIdentity(ctx context.Context, accountID int64) (*PluginOutboundIdentity, error) {
	if s == nil || s.accountRepo == nil || accountID <= 0 {
		return nil, nil
	}
	account, err := s.accountRepo.GetByID(ctx, accountID)
	if err != nil {
		return nil, err
	}
	if !pluginAccountDirectoryEligible(account) {
		return nil, nil
	}
	// Freeze the selected authorization before resolving its token. The token
	// provider may refresh this request-owned snapshot, never the directory entry.
	account, err = ResolveOpenAIOAuthCredentialAccount(ctx, s.accountRepo, account, OpenAIRequestOSFromContext(ctx).Family)
	if err != nil {
		return nil, err
	}
	token, _, err := s.GetAccessToken(ctx, account)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(token) == "" {
		return nil, nil
	}
	headers := http.Header{}
	if err := resolveAndSetOpenAIChatGPTAccountHeaders(ctx, s.accountRepo, headers, account); err != nil {
		return nil, err
	}
	// A directory lookup has no model or logical turn. Resolve only the account's
	// client identity, without selecting request roots or touching turn-state runtime.
	userAgent := account.GetOpenAIUserAgent()
	_, profilesAvailable := s.accountRepo.(OpenAIOAuthOSProfilesEnsurer)
	if IsOpenAIOAuthOSProfileOwner(account) && (profilesAvailable || OpenAIOAuthOSProfilesComplete(account.OpenAIOAuthOSProfiles)) {
		profile, err := ResolveOpenAIOAuthOSProfile(ctx, s.accountRepo, account, "")
		if err != nil {
			return nil, err
		}
		userAgent = profile.UserAgent
	}
	identity := resolveCodexClientIdentityPlan(CodexClientIdentityNormalize, userAgent)
	ensureCodexIdentityHeadersFromPlan(headers, identity)
	return &PluginOutboundIdentity{
		AccountID:   account.ID,
		Platform:    account.Platform,
		AccountType: account.Type,
		ProxyURL:    resolveAccountProxyURL(account),
		Token:       token,
		Headers:     headers,
	}, nil
}
