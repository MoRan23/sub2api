package service

import (
	"context"
	"strings"
	"time"
)

// Benchmarks use the ordinary refresh CAS, but never its stale-token fallback
// policy and never account-health mutation. Credentials are read before sending.
func (p *OpenAITokenProvider) getCandyTestAccessToken(ctx context.Context, original *Account) (string, *Account, error) {
	account := original
	var err error
	if p.accountRepo != nil {
		if original.OpenAIOAuthAuthorizationGeneration != "" {
			account, err = ReloadOpenAIOAuthCredentialAccount(ctx, p.accountRepo, original)
		} else {
			account, err = p.accountRepo.GetByID(ctx, original.ID)
		}
		if err != nil || !sameCandyAuthorization(original, account) {
			return "", nil, candyTestError("authorization_changed")
		}
	}
	expires := account.GetCredentialAsTime("expires_at")
	needsRefresh := !account.IsOpenAIPersonalAccessToken() && (expires == nil || time.Until(*expires) <= openAITokenRefreshSkew)
	if needsRefresh && strings.TrimSpace(account.GetOpenAIRefreshToken()) == "" {
		if expires != nil && !time.Now().Before(*expires) {
			return "", nil, candyTestError("authorization_expired")
		}
		needsRefresh = false
	}
	if needsRefresh {
		if p.refreshAPI == nil || p.executor == nil {
			return "", nil, candyTestError("refresh_unavailable")
		}
		result, refreshErr := p.refreshAPI.RefreshIfNeeded(ctx, account, p.executor, openAITokenRefreshSkew)
		if refreshErr != nil {
			return "", nil, safeCandyTestError(ctx, candyTestError("refresh_failed"))
		}
		if result == nil {
			return "", nil, candyTestError("refresh_failed")
		}
		if result.LockHeld {
			if p.tokenCache == nil {
				return "", nil, candyTestError("refresh_busy")
			}
			token, winner, waitErr := p.waitForTokenAfterLockRaceWithAccount(ctx, OpenAITokenCacheKey(account), account)
			if waitErr != nil {
				return "", nil, safeCandyTestError(ctx, waitErr)
			}
			if strings.TrimSpace(token) == "" || !sameCandyAuthorization(original, winner) {
				return "", nil, candyTestError("refresh_busy")
			}
			// Re-read the durable winner rather than trusting a cached old token.
			if winner.OpenAIOAuthAuthorizationGeneration != "" {
				winner, err = ReloadOpenAIOAuthCredentialAccount(ctx, p.accountRepo, winner)
			} else {
				winner, err = p.accountRepo.GetByID(ctx, winner.ID)
			}
			if err != nil || !sameCandyAuthorization(original, winner) {
				return "", nil, candyTestError("authorization_changed")
			}
			if winner.GetOpenAIAccessToken() == account.GetOpenAIAccessToken() && winner.GetOpenAIRefreshToken() == account.GetOpenAIRefreshToken() {
				return "", nil, candyTestError("refresh_busy")
			}
			account = winner
		} else {
			if !sameCandyAuthorization(original, result.Account) {
				return "", nil, candyTestError("authorization_changed")
			}
			account = result.Account
		}
	}
	if err := ctx.Err(); err != nil {
		return "", nil, err
	}
	if expires = account.GetCredentialAsTime("expires_at"); expires != nil && !time.Now().Before(*expires) {
		return "", nil, candyTestError("authorization_expired")
	}
	token := strings.TrimSpace(account.GetOpenAIAccessToken())
	if token == "" {
		return "", nil, candyTestError("authorization_missing")
	}
	return token, snapshotOAuthRefreshAccount(account), nil
}

func sameCandyAuthorization(before, after *Account) bool {
	if before == nil || after == nil || before.ID != after.ID || before.Type != after.Type || before.Platform != after.Platform {
		return false
	}
	if before.OpenAIOAuthAuthorizationGeneration != after.OpenAIOAuthAuthorizationGeneration || before.OpenAIOAuthCredentialOwnerID != after.OpenAIOAuthCredentialOwnerID {
		return false
	}
	for _, key := range []string{"chatgpt_account_id", "chatgpt_user_id", "organization_id", openAIAuthModeCredentialKey} {
		if before.GetCredential(key) != after.GetCredential(key) {
			return false
		}
	}
	return true
}
