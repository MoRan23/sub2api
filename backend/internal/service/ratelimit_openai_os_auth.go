package service

import (
	"context"
	"fmt"
	"log/slog"
	"reflect"
	"time"
)

// RecoverOpenAIOAuthOSAfterSuccessfulTest restores ordinary account runtime
// state only while the credentials used by the test are still current.
func (s *RateLimitService) RecoverOpenAIOAuthOSAfterSuccessfulTest(ctx context.Context, account *Account) (*SuccessfulTestRecoveryResult, error) {
	if s == nil || account == nil {
		return nil, fmt.Errorf("OpenAI OAuth test recovery requires a credential snapshot")
	}
	result, scoped, err := mutateOpenAIOAuthAccountState(ctx, s.accountRepo, account, OpenAIOAuthAccountStateChange{Kind: OpenAIOAuthAccountStateRecover})
	if !scoped {
		return s.RecoverAccountAfterSuccessfulTest(ctx, account.ID)
	}
	if err != nil {
		return nil, err
	}
	recovered := &SuccessfulTestRecoveryResult{}
	if result == nil || !result.Applied {
		return recovered, nil
	}
	recovered.ClearedError, recovered.ClearedRateLimit = result.ClearedError, result.ClearedRateLimit
	if recovered.ClearedRateLimit && s.tempUnschedCache != nil {
		if err := s.tempUnschedCache.DeleteTempUnsched(ctx, account.ID); err != nil {
			slog.Warn("temp_unsched_cache_delete_failed", "account_id", account.ID, "error", err)
		}
	}
	if recovered.ClearedError || recovered.ClearedRateLimit {
		s.ResetOpenAI403Counter(ctx, account.ID)
		s.notifyAccountSchedulingBlockCleared(account.ID)
	}
	return recovered, nil
}

// Freeze metadata only when an older caller supplied the exact credentials it
// used. Never attach a late response to current replacement credentials.
func (s *RateLimitService) openAIOAuthErrorAccount(ctx context.Context, account *Account) (*Account, bool) {
	if account == nil || !RequiresOpenAIOAuthOSAuthorization(account) {
		return account, true
	}
	reader, ok := s.accountRepo.(OpenAIOAuthOSCredentialsReader)
	if !ok {
		return account, true
	}
	if snapshot, scoped := openAIOAuthAccountStateSnapshot(account); scoped {
		current, err := reader.GetOpenAIOAuthOSCredential(ctx, snapshot.OwnerAccountID, account.OpenAIOAuthCredentialOS)
		return account, err == nil && current != nil && current.AuthorizationGeneration == snapshot.AuthorizationGeneration && current.Revision == snapshot.CredentialRevision
	}
	current, err := ResolveOpenAIOAuthCredentialAccount(ctx, s.accountRepo, account, OpenAIRequestOSFromContext(ctx).Family)
	if err != nil || !reflect.DeepEqual(openAIRefreshAuthIdentity(account.Credentials), openAIRefreshAuthIdentity(current.Credentials)) {
		return account, false
	}
	return current, true
}

func (s *RateLimitService) setAccountErrorForAttempt(ctx context.Context, account *Account, message, reason string, authFailure bool) (bool, error) {
	result, scoped, err := mutateOpenAIOAuthAccountState(ctx, s.accountRepo, account, OpenAIOAuthAccountStateChange{Kind: OpenAIOAuthAccountStateError, ErrorMessage: message, AuthFailure: authFailure})
	if scoped {
		if err != nil || result == nil || !result.Applied {
			return false, err
		}
	} else if err = s.accountRepo.SetError(ctx, account.ID, message); err != nil {
		return false, err
	}
	s.notifyAccountSchedulingBlocked(account, time.Time{}, reason)
	return true, nil
}

func (s *RateLimitService) setAccountCooldownForAttempt(ctx context.Context, account *Account, until time.Time, message, reason string) (bool, error) {
	result, scoped, err := mutateOpenAIOAuthAccountState(ctx, s.accountRepo, account, OpenAIOAuthAccountStateChange{Kind: OpenAIOAuthAccountStateCooldown, Until: until, Reason: message})
	if scoped {
		if err != nil || result == nil || !result.Applied {
			return false, err
		}
	} else if err = s.accountRepo.SetTempUnschedulable(ctx, account.ID, until, message); err != nil {
		return false, err
	}
	s.notifyAccountSchedulingBlocked(account, until, reason)
	return true, nil
}
