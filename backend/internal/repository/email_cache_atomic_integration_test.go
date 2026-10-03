//go:build integration

package repository

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestEmailCacheAtomicOperationsRealRedis(t *testing.T) {
	ctx := context.Background()
	cache := NewEmailCache(integrationRedis)
	email := fmt.Sprintf("atomic-%d@example.invalid", time.Now().UnixNano())
	t.Cleanup(func() {
		_ = cache.DeleteVerificationCode(ctx, email)
		_ = cache.DeleteNotifyVerifyCode(ctx, email)
		_ = cache.DeletePasswordResetToken(ctx, email)
	})
	for _, tc := range []struct {
		name string
		set  func(context.Context, string, *service.VerificationCodeData, time.Duration) error
		incr func(context.Context, string) (int, error)
		get  func(context.Context, string) (*service.VerificationCodeData, error)
	}{
		{"registration", cache.SetVerificationCode, cache.IncrVerificationCodeAttempts, cache.GetVerificationCode},
		{"notification", cache.SetNotifyVerifyCode, cache.IncrNotifyVerifyCodeAttempts, cache.GetNotifyVerifyCode},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.NoError(t, tc.set(ctx, email, &service.VerificationCodeData{Code: "123456"}, time.Minute))
			const workers = 64
			results := make(chan int, workers)
			var wg sync.WaitGroup
			for range workers {
				wg.Add(1)
				go func() {
					defer wg.Done()
					n, err := tc.incr(ctx, email)
					if !assertNoAtomicCacheError(t, err) {
						return
					}
					results <- n
				}()
			}
			wg.Wait()
			close(results)
			seen := map[int]bool{}
			for n := range results {
				require.False(t, seen[n], "duplicate attempt number")
				seen[n] = true
			}
			require.Len(t, seen, workers)
			data, err := tc.get(ctx, email)
			require.NoError(t, err)
			require.Equal(t, workers, data.Attempts)
			require.NoError(t, tc.set(ctx, email, &service.VerificationCodeData{Code: "654321"}, time.Minute))
			data, err = tc.get(ctx, email)
			require.NoError(t, err)
			require.Zero(t, data.Attempts)
		})
	}
	require.NoError(t, cache.SetPasswordResetToken(ctx, email,
		&service.PasswordResetTokenData{Token: "synthetic-hash"}, time.Minute))
	ok, err := cache.ConsumePasswordResetToken(ctx, email, "wrong-hash")
	require.NoError(t, err)
	require.False(t, ok)
	var consumed atomic.Int32
	var wg sync.WaitGroup
	for range 64 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, err := cache.ConsumePasswordResetToken(ctx, email, "synthetic-hash")
			if assertNoAtomicCacheError(t, err) && ok {
				consumed.Add(1)
			}
		}()
	}
	wg.Wait()
	require.Equal(t, int32(1), consumed.Load())
}

func assertNoAtomicCacheError(t *testing.T, err error) bool {
	t.Helper()
	if err != nil {
		t.Errorf("Redis operation failed: %v", err)
		return false
	}
	return true
}
