package service

import (
	"context"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/codexnative"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openaicookies"
	"github.com/google/uuid"
)

// OpenAIOAuthAuthorizationTarget binds the account before PKCE authorization
// starts. OS selects the flow's client identity, never a separate authorization.
type OpenAIOAuthAuthorizationTarget struct {
	AccountID int64
	OS        string
	Purpose   string
}

type openAIOAuthAuthUAKey struct{}

// New and renewed authorizations never inherit cookies from the old grant or
// caller. One ephemeral scope spans this flow's token and enrichment requests.
func (s *OpenAIOAuthService) beginCookieFlow(ctx context.Context, forceNew bool) (context.Context, func()) {
	if ctx == nil {
		ctx = context.Background()
	}
	if _, exists := openaicookies.ScopeFromContext(ctx); exists && !forceNew {
		return ctx, func() {}
	}
	id := uuid.NewString()
	return openaicookies.WithScope(ctx, openaicookies.Scope{EphemeralID: id}), func() {
		if s.cookieManager != nil {
			s.cookieManager.ClearEphemeral(id)
		}
	}
}

func (s *OpenAIOAuthService) SetAccountRepository(repo AccountRepository) { s.accountRepo = repo }

func withOpenAIOAuthAuthUserAgent(ctx context.Context, ua string) context.Context {
	if strings.TrimSpace(ua) == "" {
		return ctx
	}
	ctx = context.WithValue(ctx, openAIOAuthAuthUAKey{}, ua)
	ctx = WithOpenAINativeHTTPScope(ctx, nil, ua)
	// A caller may already carry the account's default native scope. Both the
	// application header and transport hint must use this explicitly bound OS.
	scope, _ := codexnative.ScopeFromContext(ctx)
	scope.AccountUserAgent = ua
	return codexnative.WithScope(ctx, scope)
}

// OpenAIOAuthAuthIdentity uses only the server-bound installation identity.
func OpenAIOAuthAuthIdentity(ctx context.Context) (string, string) {
	ua, originator := CodexCanonicalAuthIdentity()
	if ctx != nil {
		if bound, _ := ctx.Value(openAIOAuthAuthUAKey{}).(string); bound != "" {
			return bound, originator
		}
		if scope, ok := codexnative.ScopeFromContext(ctx); ok && scope.AccountUserAgent != "" {
			return scope.AccountUserAgent, originator
		}
	}
	return ua, originator
}

func (s *OpenAIOAuthService) prepareAuthorizationTarget(ctx context.Context, target OpenAIOAuthAuthorizationTarget) (*openai.OAuthSession, error) {
	os := NormalizeOpenAIOSFamily(target.OS)
	if strings.TrimSpace(target.OS) != "" && os == "" {
		return nil, infraerrors.BadRequest("OPENAI_OAUTH_OS_REQUIRED", "a valid client identity OS is required")
	}
	if target.AccountID < 0 {
		return nil, infraerrors.BadRequest("OPENAI_OAUTH_INVALID_ACCOUNT", "invalid account ID")
	}
	purpose := strings.TrimSpace(target.Purpose)
	if purpose == "" {
		if target.AccountID > 0 {
			purpose = "authorize"
		} else {
			purpose = "create"
		}
	}
	if (target.AccountID == 0 && purpose != "create") || (target.AccountID > 0 && purpose != "authorize") {
		return nil, infraerrors.BadRequest("OPENAI_OAUTH_INVALID_PURPOSE", "authorization purpose does not match its target")
	}
	binding := &openai.OAuthSession{AccountID: target.AccountID, OS: os, Purpose: purpose}
	if target.AccountID == 0 {
		if os == "" {
			os = OpenAIOSWindows
			binding.OS = os
		}
		ua, err := BuildOpenAIUserAgentWithEnvironment(CodexCanonicalUserAgent(), defaultOpenAIOAuthEnvironment(os))
		binding.UserAgent = ua
		return binding, err
	}
	if s.accountRepo == nil {
		return nil, infraerrors.ServiceUnavailable("OPENAI_OAUTH_STORAGE_UNAVAILABLE", "authorization storage is unavailable")
	}
	account, err := s.accountRepo.GetByID(ctx, target.AccountID)
	if err != nil {
		return nil, err
	}
	if !IsOpenAIOAuthOSProfileOwner(account) {
		return nil, infraerrors.BadRequest("OPENAI_OAUTH_INVALID_ACCOUNT", "account does not support shared OpenAI OAuth authorization")
	}
	if os == "" && account.OpenAIOAuthOSProfiles != nil {
		os = account.OpenAIOAuthOSProfiles.DefaultOS
	}
	if os == "" {
		os = OpenAIOSWindows
	}
	binding.OS = os
	profile, err := ResolveOpenAIOAuthOSProfile(ctx, s.accountRepo, account, os)
	if err != nil {
		return nil, err
	}
	binding.UserAgent = profile.UserAgent
	store, ok := s.accountRepo.(OpenAIOAuthOSCredentialsRepository)
	if !ok {
		return nil, infraerrors.ServiceUnavailable("OPENAI_OAUTH_STORAGE_UNAVAILABLE", "authorization storage is unavailable")
	}
	slot, err := store.GetOpenAIOAuthOSCredential(ctx, target.AccountID, os)
	if err != nil {
		return nil, err
	}
	if slot != nil {
		binding.AuthorizationGeneration = slot.AuthorizationGeneration
	}
	return binding, nil
}

func (s *OpenAIOAuthService) completeBoundAuthorization(ctx context.Context, session *openai.OAuthSession, info *OpenAITokenInfo, source string) (*OpenAITokenInfo, error) {
	// These values come from the HTTPS token exchange response, never callback
	// parameters or a caller-provided JWT. Missing identity cannot establish a slot.
	if strings.TrimSpace(info.ChatGPTAccountID) == "" || strings.TrimSpace(info.ChatGPTUserID) == "" {
		return nil, infraerrors.BadRequest("OPENAI_OAUTH_IDENTITY_REQUIRED", "the token response did not identify the ChatGPT account and user")
	}
	store, ok := s.accountRepo.(OpenAIOAuthOSCredentialsRepository)
	if !ok {
		return nil, infraerrors.ServiceUnavailable("OPENAI_OAUTH_STORAGE_UNAVAILABLE", "authorization storage is unavailable")
	}
	if _, err := store.BindOpenAIOAuthOSCredentialsIfGeneration(ctx, session.AccountID, session.OS, session.AuthorizationGeneration, s.BuildAccountCredentials(info), source); err != nil {
		return nil, err
	}
	account, err := s.accountRepo.GetByID(ctx, session.AccountID)
	if err != nil {
		return nil, err
	}
	return &OpenAITokenInfo{OS: session.OS, Account: account}, nil
}

// RefreshTokenForOS validates an imported refresh token with the chosen OS identity.
func (s *OpenAIOAuthService) RefreshTokenForOS(ctx context.Context, refreshToken, proxyURL, clientID, os string) (*OpenAITokenInfo, error) {
	ctx, releaseCookies := s.beginCookieFlow(ctx, true)
	defer releaseCookies()
	binding, err := s.prepareAuthorizationTarget(ctx, OpenAIOAuthAuthorizationTarget{OS: os, Purpose: "create"})
	if err != nil {
		return nil, err
	}
	// This is a validation primitive for imports: the caller must finish subject
	// checks for all grants before performing account enrichment or mutations.
	info, err := s.refreshTokenWithClientID(withOpenAIOAuthAuthUserAgent(ctx, binding.UserAgent), refreshToken, proxyURL, clientID, false)
	if err != nil {
		return nil, err
	}
	info.OS = binding.OS
	if info.RefreshToken == "" {
		info.RefreshToken = strings.TrimSpace(refreshToken)
	}
	return info, nil
}

// AuthorizeAccountWithRefreshToken exchanges on the server, then CAS-binds the
// account's shared authorization. Browser supplied subject claims are ignored.
func (s *OpenAIOAuthService) AuthorizeAccountWithRefreshToken(ctx context.Context, accountID int64, os, refreshToken, clientID string) (*OpenAITokenInfo, error) {
	ctx, releaseCookies := s.beginCookieFlow(ctx, true)
	defer releaseCookies()
	if strings.TrimSpace(refreshToken) == "" {
		return nil, infraerrors.BadRequest("OPENAI_OAUTH_REFRESH_TOKEN_REQUIRED", "refresh token is required to validate imported credentials")
	}
	binding, err := s.prepareAuthorizationTarget(ctx, OpenAIOAuthAuthorizationTarget{AccountID: accountID, OS: os, Purpose: "authorize"})
	if err != nil {
		return nil, err
	}
	account, err := s.accountRepo.GetByID(ctx, accountID)
	if err != nil {
		return nil, err
	}
	var proxyURL string
	if account.ProxyID != nil && s.proxyRepo != nil {
		proxy, proxyErr := s.proxyRepo.GetByID(ctx, *account.ProxyID)
		if proxyErr != nil {
			return nil, proxyErr
		}
		if proxy != nil {
			proxyURL = proxy.URL()
		}
	}
	// Subject verification precedes privacy/account enrichment, so importing an
	// unrelated login cannot mutate that unrelated account's preferences.
	info, err := s.refreshTokenWithClientID(withOpenAIOAuthAuthUserAgent(ctx, binding.UserAgent), refreshToken, proxyURL, clientID, false)
	if err != nil {
		return nil, err
	}
	if info.RefreshToken == "" {
		info.RefreshToken = strings.TrimSpace(refreshToken)
	}
	return s.completeBoundAuthorization(ctx, binding, info, "refresh_token_import")
}

func credentialString(credentials map[string]any, key string) string {
	value, _ := credentials[key].(string)
	return value
}
