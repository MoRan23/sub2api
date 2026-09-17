//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestAccountRepositorySetRateLimitedOnlyExtendsUntilCleared(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	repo := newAccountRepositoryWithSQL(tx.Client(), tx, nil)
	account := mustCreateAccount(t, tx.Client(), &service.Account{
		Name: "ordinary-rate-limit-monotonic", Platform: service.PlatformAnthropic, Type: service.AccountTypeOAuth,
	})
	initialReset := time.Now().UTC().Truncate(time.Second).Add(30 * time.Minute)
	require.NoError(t, repo.SetRateLimited(ctx, account.ID, initialReset))
	initial, err := repo.GetByID(ctx, account.ID)
	require.NoError(t, err)
	require.NotNil(t, initial.RateLimitedAt)
	require.NotNil(t, initial.RateLimitResetAt)

	for _, reset := range []time.Time{initialReset.Add(-20 * time.Minute), initialReset} {
		require.NoError(t, repo.SetRateLimited(ctx, account.ID, reset))
		got, err := repo.GetByID(ctx, account.ID)
		require.NoError(t, err)
		require.Equal(t, initial.RateLimitResetAt, got.RateLimitResetAt)
		require.Equal(t, initial.RateLimitedAt, got.RateLimitedAt, "no-op must not replace the observed generation")
		require.Equal(t, initial.UpdatedAt, got.UpdatedAt, "no-op must not touch the row version")
	}

	extendedReset := initialReset.Add(time.Hour)
	require.NoError(t, repo.SetRateLimited(ctx, account.ID, extendedReset))
	extended, err := repo.GetByID(ctx, account.ID)
	require.NoError(t, err)
	require.NotNil(t, extended.RateLimitResetAt)
	require.True(t, extendedReset.Equal(*extended.RateLimitResetAt))

	require.NoError(t, repo.ClearRateLimit(ctx, account.ID))
	cleared, err := repo.GetByID(ctx, account.ID)
	require.NoError(t, err)
	require.Nil(t, cleared.RateLimitedAt)
	require.Nil(t, cleared.RateLimitResetAt)

	rearmedReset := initialReset.Add(-20 * time.Minute)
	require.NoError(t, repo.SetRateLimited(ctx, account.ID, rearmedReset))
	rearmed, err := repo.GetByID(ctx, account.ID)
	require.NoError(t, err)
	require.NotNil(t, rearmed.RateLimitResetAt)
	require.True(t, rearmedReset.Equal(*rearmed.RateLimitResetAt), "explicit clearing allows a fresh shorter generation")
}
