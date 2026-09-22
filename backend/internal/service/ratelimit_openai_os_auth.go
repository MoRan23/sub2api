package service

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/tidwall/gjson"
)

// RecoverOpenAIOAuthOSAfterSuccessfulTest clears only the exact authorization
// that was tested. A late result after replacement or refresh loses its CAS.
func (s *RateLimitService) RecoverOpenAIOAuthOSAfterSuccessfulTest(ctx context.Context, account *Account) (*SuccessfulTestRecoveryResult, error) {
	if s == nil || account == nil || account.OpenAIOAuthCredentialOS == "" {
		return nil, fmt.Errorf("OpenAI OAuth test recovery requires a scoped credential snapshot")
	}
	repo, ok := s.accountRepo.(OpenAIOAuthOSCredentialsRepository)
	if !ok {
		return nil, fmt.Errorf("OpenAI OAuth OS credential repository is not configured")
	}
	applied, err := repo.PatchOpenAIOAuthOSCredentialsIfUnchanged(ctx, account.OpenAIOAuthCredentialOwnerID,
		account.OpenAIOAuthCredentialOS, account.OpenAIOAuthAuthorizationGeneration, account.OpenAIOAuthCredentialRevision,
		account.ProxyID, nil, nil)
	if err != nil {
		return nil, err
	}
	return &SuccessfulTestRecoveryResult{ClearedError: applied}, nil
}

// Authentication failures belong to the slot that made the request. Quota,
// overload, account concurrency and other account limits retain shared handling.
func (s *RateLimitService) handleOpenAIOAuthOSAuthFailure(ctx context.Context, account *Account, status int, body []byte) (bool, bool) {
	if s == nil || !RequiresOpenAIOAuthOSAuthorization(account) || (status != http.StatusUnauthorized && status != http.StatusForbidden) {
		return false, false
	}
	repo, supported := s.accountRepo.(OpenAIOAuthOSCredentialsRepository)
	if !supported {
		return false, false
	}
	if status == http.StatusForbidden && isHTMLResponse(body) {
		return true, false
	}
	// Preserve a frozen attempt's revision. Re-resolving a scoped snapshot here
	// would incorrectly attach its late 401 to a newly refreshed token.
	if account.OpenAIOAuthCredentialOS == "" {
		var err error
		account, err = ResolveOpenAIOAuthCredentialAccount(ctx, s.accountRepo, account, OpenAIRequestOSFromContext(ctx).Family)
		if err != nil {
			return true, true
		}
	}
	if s.tokenCacheInvalidator != nil {
		_ = s.tokenCacheInvalidator.InvalidateToken(ctx, account)
	}
	code := extractUpstreamErrorCode(body)
	permanent := status == http.StatusUnauthorized && (code == "token_invalidated" || code == "token_revoked" ||
		gjson.GetBytes(body, "detail").String() == "Unauthorized" || strings.TrimSpace(account.GetOpenAIRefreshToken()) == "")
	if permanent {
		_, _, err := persistOpenAIOAuthCredentialError(ctx, s.accountRepo, account, "OpenAI OAuth authorization requires sign-in")
		if err != nil {
			slog.Warn("openai_oauth_slot_auth_error_failed", "account_id", account.ID, "os", account.OpenAIOAuthCredentialOS, "error", err)
		}
		return true, true
	}
	if status == http.StatusUnauthorized {
		// Force a real refresh once the retry delay expires without ever replacing
		// the whole credential document or touching another slot's token.
		applied, err := repo.PatchOpenAIOAuthOSCredentialsIfUnchanged(ctx, account.OpenAIOAuthCredentialOwnerID,
			account.OpenAIOAuthCredentialOS, account.OpenAIOAuthAuthorizationGeneration, account.OpenAIOAuthCredentialRevision,
			account.ProxyID, map[string]any{"expires_at": time.Now().Add(-time.Minute).Format(time.RFC3339)}, nil)
		if err != nil || !applied {
			return true, true
		}
		account, err = ReloadOpenAIOAuthCredentialAccount(ctx, s.accountRepo, account)
		if err != nil {
			return true, true
		}
	}
	cooldown := openAI403CooldownMinutesDefault
	if status == http.StatusUnauthorized && s.cfg != nil && s.cfg.RateLimit.OAuth401CooldownMinutes > 0 {
		cooldown = s.cfg.RateLimit.OAuth401CooldownMinutes
	}
	_, err := repo.SetOpenAIOAuthOSCredentialCooldownIfUnchanged(ctx, account.OpenAIOAuthCredentialOwnerID,
		account.OpenAIOAuthCredentialOS, account.OpenAIOAuthAuthorizationGeneration, account.OpenAIOAuthCredentialRevision,
		time.Now().Add(time.Duration(cooldown)*time.Minute), "OpenAI OAuth authorization temporarily unavailable")
	if err != nil {
		slog.Warn("openai_oauth_slot_auth_cooldown_failed", "account_id", account.ID, "os", account.OpenAIOAuthCredentialOS, "error", err)
	}
	return true, true
}
