//go:build unit

package service

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func (r *openAIOSRuntimeRepo) MutateOpenAIOAuthAccountStateIfUnchanged(_ context.Context, accountID int64, snapshot OpenAIOAuthAccountStateSnapshot, change OpenAIOAuthAccountStateChange) (*OpenAIOAuthAccountStateResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	result := &OpenAIOAuthAccountStateResult{}
	grant := r.slots[OpenAIOSWindows]
	if accountID != r.account.ID || snapshot.OwnerAccountID != r.account.ID || snapshot.AuthorizationGeneration != grant.AuthorizationGeneration || snapshot.CredentialRevision != grant.Revision {
		return result, nil
	}
	result.Applied = true
	switch change.Kind {
	case OpenAIOAuthAccountStateError:
		r.account.Status, r.account.ErrorMessage, r.account.Schedulable = StatusError, change.ErrorMessage, false
	case OpenAIOAuthAccountStateCooldown:
		if r.account.TempUnschedulableUntil != nil && !change.Until.After(*r.account.TempUnschedulableUntil) {
			result.Applied = false
			return result, nil
		}
		r.account.TempUnschedulableUntil, r.account.TempUnschedulableReason = &change.Until, change.Reason
	case OpenAIOAuthAccountStateModelCooldown:
		setAccountModelRateLimitSnapshot(r.account, change.ModelKey, change.Until, change.Reason, time.Now())
	case OpenAIOAuthAccountStateRecover:
		result.ClearedError = r.account.Status == StatusError
		result.ClearedRateLimit = hasRecoverableRuntimeState(r.account)
		if result.ClearedError {
			r.account.Status, r.account.ErrorMessage = StatusActive, ""
		}
		if result.ClearedRateLimit {
			r.account.RateLimitedAt, r.account.RateLimitResetAt, r.account.OverloadUntil = nil, nil, nil
			r.account.TempUnschedulableUntil, r.account.TempUnschedulableReason = nil, ""
			delete(r.account.Extra, "model_rate_limits")
			delete(r.account.Extra, "antigravity_quota_scopes")
		}
		result.Applied = result.ClearedError || result.ClearedRateLimit
	case OpenAIOAuthAccountStateClearTemp:
		r.account.TempUnschedulableUntil, r.account.TempUnschedulableReason = nil, ""
		delete(r.account.Extra, "model_rate_limits")
	}
	return result, nil
}

func (r *rateLimitOAuthOwnerReader) MutateOpenAIOAuthAccountStateIfUnchanged(ctx context.Context, accountID int64, snapshot OpenAIOAuthAccountStateSnapshot, change OpenAIOAuthAccountStateChange) (*OpenAIOAuthAccountStateResult, error) {
	result := &OpenAIOAuthAccountStateResult{}
	grant, err := r.GetOpenAIOAuthOSCredential(ctx, snapshot.OwnerAccountID, OpenAIOSWindows)
	if err != nil || grant.AuthorizationGeneration != snapshot.AuthorizationGeneration || grant.Revision != snapshot.CredentialRevision {
		return result, err
	}
	switch change.Kind {
	case OpenAIOAuthAccountStateError:
		err = r.SetError(ctx, accountID, change.ErrorMessage)
	case OpenAIOAuthAccountStateCooldown:
		err = r.SetTempUnschedulable(ctx, accountID, change.Until, change.Reason)
	}
	result.Applied = err == nil
	return result, err
}

func TestOpenAIOAuthRuntime401UsesAccountCooldownWithoutChangingCredentials(t *testing.T) {
	r := newOpenAIOSRuntimeRepo()
	snapshot, err := ResolveOpenAIOAuthCredentialAccount(context.Background(), r, r.account, OpenAIOSLinux)
	require.NoError(t, err)
	expiry := snapshot.GetCredential("expires_at")
	cfg := &config.Config{}
	cfg.RateLimit.OAuth401CooldownMinutes = 3
	s := NewRateLimitService(r, nil, cfg, nil, nil)
	invalidator := &tokenCacheInvalidatorRecorder{}
	blocker := &runtimeBlockRecorder{}
	s.SetTokenCacheInvalidator(invalidator)
	s.SetAccountRuntimeBlocker(blocker)
	require.True(t, s.HandleUpstreamError(context.Background(), snapshot, 401, nil, []byte(`{"error":{"message":"expired"}}`)))
	require.Equal(t, StatusActive, r.account.Status)
	require.WithinDuration(t, time.Now().Add(3*time.Minute), *r.account.TempUnschedulableUntil, time.Second)
	require.Contains(t, r.account.TempUnschedulableReason, "OAuth 401: expired")
	require.Equal(t, expiry, r.account.GetCredential("expires_at"))
	require.Equal(t, snapshot.OpenAIOAuthCredentialRevision, r.slots[OpenAIOSWindows].Revision)
	require.Nil(t, r.slots[OpenAIOSWindows].RefreshRetryAfter)
	require.Len(t, invalidator.accounts, 1)
	require.Equal(t, []string{"oauth_401"}, blocker.reasons)
}

func TestOpenAIOAuthRuntime403PreservesEscalationAndHTMLExemption(t *testing.T) {
	r := newOpenAIOSRuntimeRepo()
	snapshot, err := ResolveOpenAIOAuthCredentialAccount(context.Background(), r, r.account, OpenAIOSLinux)
	require.NoError(t, err)
	s := NewRateLimitService(r, nil, nil, nil, nil)
	counter := &openAI403CounterCacheStub{counts: []int64{1, 2, 3}}
	s.SetOpenAI403CounterCache(counter)
	require.False(t, s.HandleUpstreamError(context.Background(), snapshot, 403, nil, []byte("<!doctype html><html>blocked</html>")))
	require.Len(t, counter.counts, 3)
	for i := 1; i <= 3; i++ {
		require.True(t, s.HandleUpstreamError(context.Background(), snapshot, 403, nil, []byte(`{"error":{"message":"workspace forbidden"}}`)))
		if i < 3 {
			require.Equal(t, StatusActive, r.account.Status)
			require.WithinDuration(t, time.Now().Add(10*time.Minute), *r.account.TempUnschedulableUntil, time.Second)
		}
	}
	require.Equal(t, StatusError, r.account.Status)
	require.False(t, r.account.Schedulable)
	require.Contains(t, r.account.ErrorMessage, "consecutive_403=3/3")
	require.Equal(t, OpenAIOAuthAuthorizationAuthorized, r.slots[OpenAIOSWindows].Status)
}

func TestOpenAIOAuthRuntimePoliciesRemainEffective(t *testing.T) {
	for _, model := range []string{"", "gpt-test"} {
		r := newOpenAIOSRuntimeRepo()
		r.account.Credentials["temp_unschedulable_enabled"] = true
		r.account.Credentials["temp_unschedulable_rules"] = []any{map[string]any{"error_code": http.StatusForbidden, "keywords": []any{"forbidden"}, "duration_minutes": 7}}
		snapshot, err := ResolveOpenAIOAuthCredentialAccount(context.Background(), r, r.account, OpenAIOSLinux)
		require.NoError(t, err)
		s := NewRateLimitService(r, nil, nil, nil, nil)
		require.Equal(t, ErrorPolicyTempUnscheduled, s.CheckErrorPolicy(context.Background(), snapshot, 403, []byte(`{"error":{"message":"forbidden"}}`), model))
		require.Equal(t, StatusActive, r.account.Status)
		if model == "" {
			require.WithinDuration(t, time.Now().Add(7*time.Minute), *r.account.TempUnschedulableUntil, time.Second)
		} else {
			require.Nil(t, r.account.TempUnschedulableUntil)
			require.True(t, hasNonEmptyMapValue(r.account.Extra, "model_rate_limits"))
		}
	}
}

func TestOpenAIOAuthRuntimeSuccessfulTestRestoresAccountAndRejectsLateResults(t *testing.T) {
	for _, stale := range []bool{false, true} {
		r := newOpenAIOSRuntimeRepo()
		snapshot, err := ResolveOpenAIOAuthCredentialAccount(context.Background(), r, r.account, OpenAIOSLinux)
		require.NoError(t, err)
		until := time.Now().Add(time.Hour)
		r.account.Status, r.account.ErrorMessage, r.account.Schedulable = StatusError, "old error", false
		r.account.RateLimitedAt, r.account.RateLimitResetAt, r.account.OverloadUntil, r.account.TempUnschedulableUntil = &until, &until, &until, &until
		r.account.Extra = map[string]any{"model_rate_limits": map[string]any{"model": "limited"}, "antigravity_quota_scopes": map[string]any{"scope": "limited"}}
		// Legacy metadata must not impose a second authorization gate on recovery.
		r.slots[OpenAIOSWindows].Status = OpenAIOAuthAuthorizationReauthRequired
		if stale {
			r.slots[OpenAIOSWindows].Revision++
		}
		s := NewRateLimitService(r, nil, nil, nil, nil)
		counter, blocker := &openAI403CounterCacheStub{}, &runtimeBlockRecorder{}
		s.SetOpenAI403CounterCache(counter)
		s.SetAccountRuntimeBlocker(blocker)
		result, err := s.RecoverOpenAIOAuthOSAfterSuccessfulTest(context.Background(), snapshot)
		require.NoError(t, err)
		require.Equal(t, !stale, result.ClearedError)
		require.Equal(t, !stale, result.ClearedRateLimit)
		if stale {
			require.Equal(t, StatusError, r.account.Status)
			require.NotNil(t, r.account.TempUnschedulableUntil)
			require.Empty(t, blocker.clearedIDs)
			require.Empty(t, counter.resetCalls)
		} else {
			require.Equal(t, StatusActive, r.account.Status)
			require.False(t, r.account.Schedulable, "baseline recovery does not change the scheduling toggle")
			require.False(t, hasRecoverableRuntimeState(r.account))
			require.Equal(t, []int64{r.account.ID}, blocker.clearedIDs)
			require.Equal(t, []int64{r.account.ID}, counter.resetCalls)
		}
	}
}

type openAIOAuthRuntimeChangeRecorder struct {
	*openAIOSRuntimeRepo
	change OpenAIOAuthAccountStateChange
}

func (r *openAIOAuthRuntimeChangeRecorder) MutateOpenAIOAuthAccountStateIfUnchanged(ctx context.Context, id int64, snapshot OpenAIOAuthAccountStateSnapshot, change OpenAIOAuthAccountStateChange) (*OpenAIOAuthAccountStateResult, error) {
	r.change = change
	return r.openAIOSRuntimeRepo.MutateOpenAIOAuthAccountStateIfUnchanged(ctx, id, snapshot, change)
}

func TestOpenAIOAuthRuntimeReauthorizationOwnsOnlyAuthenticationErrors(t *testing.T) {
	for _, tc := range []struct {
		status      int
		body        string
		authFailure bool
	}{
		{401, `{"error":{"code":"token_revoked"}}`, true},
		{403, `{"error":{"message":"forbidden"}}`, true},
		{402, `{"error":{"message":"billing problem"}}`, false},
	} {
		r := &openAIOAuthRuntimeChangeRecorder{openAIOSRuntimeRepo: newOpenAIOSRuntimeRepo()}
		account, err := ResolveOpenAIOAuthCredentialAccount(context.Background(), r, r.account, OpenAIOSLinux)
		require.NoError(t, err)
		s := NewRateLimitService(r, nil, nil, nil, nil)
		require.True(t, s.HandleUpstreamError(context.Background(), account, tc.status, nil, []byte(tc.body)))
		require.Equal(t, tc.authFailure, r.change.AuthFailure)
	}
}
