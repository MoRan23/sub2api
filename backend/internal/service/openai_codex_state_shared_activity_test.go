package service

import (
	"context"
	"encoding/base64"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCodexTurnStateSharedActivityStartsOnSendAndRefreshesAfterCompletion(t *testing.T) {
	s, repo, account := newCodexStateTestService(t)
	isolateCodexHistory(t)
	ctx := context.Background()
	now := s.now()
	s.now = func() time.Time { return now }
	prepared, err := s.Prepare(ctx, account, "gpt-5")
	require.NoError(t, err)
	calls := 0
	s.collector = codexStateTestCollector(func(_ context.Context, request CodexTurnStateCollectRequest) (CodexTurnStateCollectResult, error) {
		calls++
		require.Equal(t, account.OpenAIOAuthOSProfiles.DefaultOS, request.Account.OpenAIOAuthCredentialOS)
		now = now.Add(9 * time.Second)
		return CodexTurnStateCollectResult{StatusCode: http.StatusOK, completed: true, Tokens: []string{codexStateTestToken(10, now)}}, nil
	})
	s.collect(ctx, prepared.key)
	require.Zero(t, calls, "preparation is not actual business activity")
	sentAt := now
	markCodexStateTestBusinessSent(t, s, prepared)
	s.completeBusinessSent(prepared)
	row, err := repo.Get(ctx, prepared.key)
	require.NoError(t, err)
	require.Equal(t, "business_active", row.DemandReason)
	require.False(t, prepared.finished, "collection may run before business finishes")
	s.collect(ctx, prepared.key)
	require.Equal(t, 1, calls)
	row, err = repo.Get(ctx, prepared.key)
	require.NoError(t, err)
	require.Equal(t, sentAt, row.LastBusinessAt, "collection never advances real business activity")
	require.Equal(t, now.Add(30*time.Second), row.NextCollectAt, "interval starts after result completion")
	require.Equal(t, "refresh", row.DemandReason)
	require.Equal(t, "scheduled", row.CollectionStatus)
	require.NotEmpty(t, row.EncryptedToken)
	require.NotEmpty(t, row.EncryptedCookieBundle)
	now = row.NextCollectAt.Add(-time.Second)
	s.collect(ctx, prepared.key)
	require.Equal(t, 1, calls)
	now = row.NextCollectAt
	s.collect(ctx, prepared.key)
	require.Equal(t, 2, calls)
	now = sentAt.Add(CodexTurnStateActiveWindow + time.Second)
	s.collect(ctx, prepared.key)
	require.Equal(t, 2, calls, "an idle account stops even if earlier collection succeeded")
	row, err = repo.Get(ctx, prepared.key)
	require.NoError(t, err)
	require.Empty(t, row.DemandReason)
}

func TestCodexTurnStateSharedActivityCollectorPreservesFrozenSourceOS(t *testing.T) {
	s, repo, account := newCodexStateTestService(t)
	isolateCodexHistory(t)
	ctx := context.Background()
	a := seedCodexStateTestDemand(t, s, account, "gpt-5")
	s.collector = codexStateTestCollector(func(_ context.Context, input CodexTurnStateCollectRequest) (CodexTurnStateCollectResult, error) {
		require.Equal(t, "windows", input.Account.OpenAIOAuthCredentialOS)
		accounts := s.accounts.(*codexStateTestAccounts)
		accounts.mu.Lock()
		profiles := CloneOpenAIOAuthOSProfiles(accounts.account.OpenAIOAuthOSProfiles)
		profiles.DefaultOS = "macos"
		accounts.account.OpenAIOAuthOSProfiles = profiles
		accounts.mu.Unlock()
		return CodexTurnStateCollectResult{StatusCode: http.StatusOK, Tokens: []string{codexStateTestToken(10, s.now())}}, nil
	})
	s.collect(ctx, a.key)
	row, err := repo.Get(ctx, a.key)
	require.NoError(t, err)
	require.NotEmpty(t, row.EncryptedToken)
	require.Equal(t, "windows", row.OSFamily, "source is the physical request's frozen OS, not the changed default at completion")
	require.Empty(t, row.Key().OSFamily, "source OS must never partition the shared cache key")
}

func TestCodexTurnStateSharedActivitySameTokenDoesNotExtendLifetime(t *testing.T) {
	s, repo, account := newCodexStateTestService(t)
	isolateCodexHistory(t)
	ctx := context.Background()
	now := s.now()
	s.now = func() time.Time { return now }
	a, err := s.Prepare(ctx, account, "gpt-5")
	require.NoError(t, err)
	markCodexStateTestBusinessSent(t, s, a)
	token := codexStateTestToken(10, now)
	s.Observe(a, token)
	require.NoError(t, s.Finish(ctx, a, true))
	before, err := repo.Get(ctx, a.key)
	require.NoError(t, err)
	s.collector = codexStateTestCollector(func(context.Context, CodexTurnStateCollectRequest) (CodexTurnStateCollectResult, error) {
		return CodexTurnStateCollectResult{StatusCode: http.StatusOK, completed: true, Tokens: []string{token}}, nil
	})
	now = before.NextCollectAt
	s.collect(ctx, a.key)
	after, err := repo.Get(ctx, a.key)
	require.NoError(t, err)
	require.Equal(t, before.IssuedAt, after.IssuedAt)
	require.Equal(t, before.ExpiresAt, after.ExpiresAt)
	require.Equal(t, now.Add(CodexTurnStateCollectInterval), after.NextCollectAt)
	now = before.ExpiresAt.Add(-10 * time.Second)
	s.collect(ctx, a.key)
	after, err = repo.Get(ctx, a.key)
	require.NoError(t, err)
	require.Equal(t, before.ExpiresAt, after.ExpiresAt)
	require.Equal(t, "target_still_expiring", after.LastError)
	require.Equal(t, now.Add(CodexTurnStateRetryInterval), after.NextCollectAt)
}

func TestCodexTurnStateSharedActivityUsesOneBundleAcrossOperatingSystems(t *testing.T) {
	s, repo, account := newCodexStateTestService(t)
	isolateCodexHistory(t)
	ctx := context.Background()
	windows, err := s.Prepare(ctx, account, "gpt-5")
	require.NoError(t, err)
	markCodexStateTestBusinessSent(t, s, windows)
	token := codexStateTestToken(10, s.now())
	s.Observe(windows, token)
	require.NoError(t, s.Finish(ctx, windows, true))
	for _, os := range []string{"windows", "macos", "linux"} {
		requestAccount := *account
		requestAccount.OpenAIOAuthCredentialOS = os
		attempt, err := s.Prepare(ctx, &requestAccount, "gpt-5")
		require.NoError(t, err)
		require.Equal(t, os, attempt.OSFamily)
		require.Equal(t, windows.key, attempt.key)
		require.Empty(t, attempt.key.OSFamily)
		require.Equal(t, token, attempt.Snapshot.Token)
		require.NotEmpty(t, attempt.Snapshot.EncryptedCookieBundle)
		require.True(t, s.ValidateAttempt(ctx, attempt))
		require.NoError(t, s.Finish(ctx, attempt, false))
	}
	rows, err := repo.ListByAccount(ctx, account.ID)
	require.NoError(t, err)
	require.Len(t, rows, 1)
}

func TestCodexTurnStateSharedActivitySameIssuedDifferentTokenCannotMixBundle(t *testing.T) {
	for _, source := range []string{"business", "collector"} {
		t.Run(source, func(t *testing.T) {
			s, repo, account := newCodexStateTestService(t)
			isolateCodexHistory(t)
			ctx := context.Background()
			now := s.now()
			s.now = func() time.Time { return now }
			a, err := s.Prepare(ctx, account, "gpt-5")
			require.NoError(t, err)
			markCodexStateTestBusinessSent(t, s, a)
			token := codexStateTestToken(10, now)
			s.Observe(a, token)
			require.NoError(t, s.Finish(ctx, a, true))
			before, err := repo.Get(ctx, a.key)
			require.NoError(t, err)
			bytes, err := base64.URLEncoding.DecodeString(token)
			require.NoError(t, err)
			bytes[len(bytes)-1] ^= 1
			other := base64.URLEncoding.EncodeToString(bytes)
			require.NotEqual(t, token, other)
			if source == "business" {
				attempt, err := s.Prepare(ctx, account, "gpt-5")
				require.NoError(t, err)
				markCodexStateTestBusinessSent(t, s, attempt)
				s.Observe(attempt, other)
				require.NoError(t, s.Finish(ctx, attempt, true))
			} else {
				now = before.NextCollectAt
				s.collector = codexStateTestCollector(func(context.Context, CodexTurnStateCollectRequest) (CodexTurnStateCollectResult, error) {
					return CodexTurnStateCollectResult{StatusCode: http.StatusOK, completed: true, Tokens: []string{other}}, nil
				})
				s.collect(ctx, a.key)
			}
			after, err := repo.Get(ctx, a.key)
			require.NoError(t, err)
			require.Equal(t, before.EncryptedToken, after.EncryptedToken)
			require.Equal(t, before.EncryptedCookieBundle, after.EncryptedCookieBundle)
			require.Equal(t, before.CookieBundleExpiresAt, after.CookieBundleExpiresAt)
		})
	}
}

func TestCodexTurnStateSharedActivityDuplicateBusinessDoesNotPostponeRefresh(t *testing.T) {
	s, repo, account := newCodexStateTestService(t)
	isolateCodexHistory(t)
	ctx := context.Background()
	now := s.now()
	s.now = func() time.Time { return now }
	seed, err := s.Prepare(ctx, account, "gpt-5")
	require.NoError(t, err)
	markCodexStateTestBusinessSent(t, s, seed)
	token := codexStateTestToken(10, now)
	s.Observe(seed, token)
	require.NoError(t, s.Finish(ctx, seed, true))
	before, err := repo.Get(ctx, seed.key)
	require.NoError(t, err)
	now = now.Add(20 * time.Second)
	next, err := s.Prepare(ctx, account, "gpt-5")
	require.NoError(t, err)
	markCodexStateTestBusinessSent(t, s, next)
	s.Observe(next, token)
	require.NoError(t, s.Finish(ctx, next, true))
	after, err := repo.Get(ctx, next.key)
	require.NoError(t, err)
	require.Equal(t, before.NextCollectAt, after.NextCollectAt)
	require.Equal(t, before.ExpiresAt, after.ExpiresAt)
}
