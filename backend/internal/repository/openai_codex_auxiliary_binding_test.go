package repository

import (
	"context"
	"math"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func newCodexAuxiliaryAccountBindingTest(t *testing.T) (*miniredis.Miniredis, *redis.Client, service.CodexAuxiliaryAccountBindingStore) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr(), MaxRetries: -1})
	t.Cleanup(func() { _ = rdb.Close() })
	store, ok := NewGatewayCache(rdb).(service.CodexAuxiliaryAccountBindingStore)
	require.True(t, ok)
	return mr, rdb, store
}

func TestCodexAuxiliaryAccountBindingRedisKeepsEligibleAccountWithoutTTL(t *testing.T) {
	mr, rdb, store := newCodexAuxiliaryAccountBindingTest(t)
	ctx := context.Background()
	key := strings.Repeat("a", 64)
	initial, err := store.ResolveCodexAuxiliaryAccountBinding(ctx, key, []int64{11, 22}, 22)
	require.NoError(t, err)
	require.Equal(t, service.CodexAuxiliaryAccountBinding{AccountID: 22}, initial)
	require.Zero(t, mr.TTL(codexAuxiliaryAccountBindingPrefix+key))

	// An independent gateway instance and changed inference affinity cannot move
	// History/Notes to another still-eligible account, even after long inactivity.
	mr.FastForward(365 * 24 * time.Hour)
	other := NewGatewayCache(rdb).(service.CodexAuxiliaryAccountBindingStore)
	kept, err := other.ResolveCodexAuxiliaryAccountBinding(ctx, key, []int64{11, 22}, 11)
	require.NoError(t, err)
	require.Equal(t, service.CodexAuxiliaryAccountBinding{AccountID: 22, Reused: true, HadBinding: true}, kept)
	require.Zero(t, mr.TTL(codexAuxiliaryAccountBindingPrefix+key))

	replaced, err := store.ResolveCodexAuxiliaryAccountBinding(ctx, key, []int64{11, 33}, 33)
	require.NoError(t, err)
	require.Equal(t, service.CodexAuxiliaryAccountBinding{AccountID: 33, HadBinding: true}, replaced)
	kept, err = store.ResolveCodexAuxiliaryAccountBinding(ctx, key, []int64{11, 22, 33}, 22)
	require.NoError(t, err)
	require.Equal(t, service.CodexAuxiliaryAccountBinding{AccountID: 33, Reused: true, HadBinding: true}, kept)

	isolated, err := store.ResolveCodexAuxiliaryAccountBinding(ctx, strings.Repeat("b", 64), []int64{11, 33}, 99)
	require.NoError(t, err)
	require.Equal(t, service.CodexAuxiliaryAccountBinding{AccountID: 11}, isolated)
	stored, err := mr.Get(codexAuxiliaryAccountBindingPrefix + key)
	require.NoError(t, err)
	require.Equal(t, "33", stored)
}

func TestCodexAuxiliaryAccountBindingRedisConcurrentSingleWinner(t *testing.T) {
	for _, replacing := range []bool{false, true} {
		t.Run(map[bool]string{false: "initial", true: "replacement"}[replacing], func(t *testing.T) {
			mr, rdb, store := newCodexAuxiliaryAccountBindingTest(t)
			other := NewGatewayCache(rdb).(service.CodexAuxiliaryAccountBindingStore)
			key := strings.Repeat("c", 64)
			if replacing {
				require.NoError(t, mr.Set(codexAuxiliaryAccountBindingPrefix+key, "99"))
			}
			type outcome struct {
				binding service.CodexAuxiliaryAccountBinding
				err     error
			}
			const count = 32
			start := make(chan struct{})
			results := make(chan outcome, count)
			for i := 0; i < count; i++ {
				go func(index int) {
					<-start
					instance := []service.CodexAuxiliaryAccountBindingStore{store, other}[index%2]
					binding, err := instance.ResolveCodexAuxiliaryAccountBinding(context.Background(), key, []int64{11, 22}, []int64{11, 22}[index%2])
					results <- outcome{binding, err}
				}(i)
			}
			close(start)
			var winner int64
			created := 0
			for i := 0; i < count; i++ {
				result := <-results
				require.NoError(t, result.err)
				if winner == 0 {
					winner = result.binding.AccountID
				}
				require.Equal(t, winner, result.binding.AccountID)
				require.Equal(t, replacing || result.binding.Reused, result.binding.HadBinding)
				if !result.binding.Reused {
					created++
				}
			}
			require.Equal(t, 1, created)
			stored, err := mr.Get(codexAuxiliaryAccountBindingPrefix + key)
			require.NoError(t, err)
			require.Equal(t, strconv.FormatInt(winner, 10), stored)
		})
	}
}

func TestCodexAuxiliaryAccountBindingRedisPreservesFullInt64(t *testing.T) {
	_, _, store := newCodexAuxiliaryAccountBindingTest(t)
	key := strings.Repeat("d", 64)
	initial, err := store.ResolveCodexAuxiliaryAccountBinding(context.Background(), key, []int64{math.MaxInt64 - 1, math.MaxInt64}, math.MaxInt64)
	require.NoError(t, err)
	require.Equal(t, int64(math.MaxInt64), initial.AccountID)
	kept, err := store.ResolveCodexAuxiliaryAccountBinding(context.Background(), key, []int64{math.MaxInt64 - 1, math.MaxInt64}, math.MaxInt64-1)
	require.NoError(t, err)
	require.Equal(t, service.CodexAuxiliaryAccountBinding{AccountID: math.MaxInt64, Reused: true, HadBinding: true}, kept)

	replaced, err := store.ResolveCodexAuxiliaryAccountBinding(context.Background(), key, []int64{math.MaxInt64 - 1}, 0)
	require.NoError(t, err)
	require.Equal(t, service.CodexAuxiliaryAccountBinding{AccountID: math.MaxInt64 - 1, HadBinding: true}, replaced)
}

func TestCodexAuxiliaryAccountBindingRedisRejectsCorruptionWithoutMutation(t *testing.T) {
	for _, invalid := range []string{"", "0", "-1", "01", "+11", "1.0", "11\n", " 11", "invalid", "9223372036854775808", "99999999999999999999"} {
		t.Run(strconv.Quote(invalid), func(t *testing.T) {
			mr, _, store := newCodexAuxiliaryAccountBindingTest(t)
			key := strings.Repeat("e", 64)
			require.NoError(t, mr.Set(codexAuxiliaryAccountBindingPrefix+key, invalid))
			_, err := store.ResolveCodexAuxiliaryAccountBinding(context.Background(), key, []int64{11, 22}, 22)
			require.ErrorIs(t, err, service.ErrCodexAuxiliaryAccountBindingStoredInvalid)
			stored, err := mr.Get(codexAuxiliaryAccountBindingPrefix + key)
			require.NoError(t, err)
			require.Equal(t, invalid, stored)
		})
	}
	t.Run("wrong Redis type", func(t *testing.T) {
		mr, rdb, store := newCodexAuxiliaryAccountBindingTest(t)
		key := strings.Repeat("e", 64)
		err := rdb.RPush(context.Background(), codexAuxiliaryAccountBindingPrefix+key, "11").Err()
		require.NoError(t, err)
		_, err = store.ResolveCodexAuxiliaryAccountBinding(context.Background(), key, []int64{11, 22}, 22)
		require.ErrorIs(t, err, service.ErrCodexAuxiliaryAccountBindingStoredInvalid)
		stored, err := mr.List(codexAuxiliaryAccountBindingPrefix + key)
		require.NoError(t, err)
		require.Equal(t, []string{"11"}, stored)
	})
}

func TestCodexAuxiliaryAccountBindingRedisErrorsKeepExistingBinding(t *testing.T) {
	mr, rdb, store := newCodexAuxiliaryAccountBindingTest(t)
	key := strings.Repeat("f", 64)
	require.NoError(t, mr.Set(codexAuxiliaryAccountBindingPrefix+key, "11"))
	for _, candidates := range [][]int64{nil, {0, 22}, {-1, 22}} {
		_, err := store.ResolveCodexAuxiliaryAccountBinding(context.Background(), key, candidates, 22)
		require.ErrorIs(t, err, service.ErrCodexAuxiliaryAccountBindingInvalid)
	}
	_, err := store.ResolveCodexAuxiliaryAccountBinding(context.Background(), strings.Repeat("G", 64), []int64{22}, 22)
	require.ErrorIs(t, err, service.ErrCodexAuxiliaryAccountBindingInvalid)
	_, err = store.ResolveCodexAuxiliaryAccountBinding(context.Background(), key, []int64{22}, -1)
	require.ErrorIs(t, err, service.ErrCodexAuxiliaryAccountBindingInvalid)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = store.ResolveCodexAuxiliaryAccountBinding(ctx, key, []int64{22}, 22)
	require.ErrorIs(t, err, context.Canceled)
	require.NoError(t, rdb.Close())
	_, err = store.ResolveCodexAuxiliaryAccountBinding(context.Background(), key, []int64{22}, 22)
	require.ErrorIs(t, err, redis.ErrClosed)
	stored, err := mr.Get(codexAuxiliaryAccountBindingPrefix + key)
	require.NoError(t, err)
	require.Equal(t, "11", stored)

	_, err = (*gatewayCache)(nil).ResolveCodexAuxiliaryAccountBinding(context.Background(), key, []int64{22}, 22)
	require.ErrorIs(t, err, service.ErrCodexAuxiliaryAccountBindingStoreUnavailable)
}
