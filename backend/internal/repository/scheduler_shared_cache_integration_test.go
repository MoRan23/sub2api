//go:build integration

package repository

import (
	"context"
	"errors"
	"net"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestSchedulerSharedCacheCrossInstanceAuthorityIntegration(t *testing.T) {
	ctx := context.Background()
	rdb := testRedis(t)
	writer := NewSchedulerCache(rdb).(*schedulerCache)
	reader := NewSchedulerCache(rdb).(*schedulerCache)
	a := service.SchedulerBucket{GroupID: 901, Platform: service.PlatformAnthropic, Mode: service.SchedulerModeSingle}
	b := service.SchedulerBucket{GroupID: 902, Platform: service.PlatformAnthropic, Mode: service.SchedulerModeSingle}
	account := service.Account{ID: 9011, Name: "before", Platform: service.PlatformAnthropic, Type: service.AccountTypeOAuth, Status: service.StatusActive, Schedulable: true,
		GroupIDs: []int64{a.GroupID, b.GroupID}, AccountGroups: []service.AccountGroup{{AccountID: 9011, GroupID: a.GroupID}, {AccountID: 9011, GroupID: b.GroupID}},
		Credentials: map[string]any{"access_token": "fixture-private-token", "model_mapping": map[string]any{"model": "target"}},
	}
	other := service.Account{ID: 9012, Platform: service.PlatformAnthropic, Status: service.StatusActive, Schedulable: true, GroupIDs: []int64{a.GroupID}}
	tokenA, err := writer.CaptureBucketWriteToken(ctx, a)
	require.NoError(t, err)
	require.NoError(t, writer.SetSnapshot(ctx, a, tokenA, []service.Account{account, other}))
	tokenB, err := writer.CaptureBucketWriteToken(ctx, b)
	require.NoError(t, err)
	require.NoError(t, writer.SetSnapshotByAccountIDs(ctx, b, tokenB, []int64{account.ID}))
	read := func(bucket service.SchedulerBucket) service.CandidateSet {
		t.Helper()
		views, hit, err := reader.GetCandidateSnapshot(ctx, bucket)
		require.NoError(t, err)
		require.True(t, hit)
		return views
	}
	beforeA, beforeB := read(a), read(b)
	require.Same(t, beforeA[0].Ref(), beforeB[0].Ref())
	require.Empty(t, beforeA[0].GetCredential("access_token"))
	usedAt := time.Now().UTC().Truncate(time.Millisecond)
	require.NoError(t, writer.UpdateLastUsed(ctx, map[int64]time.Time{account.ID: usedAt}))
	hot := read(b)
	require.Same(t, beforeB[0].Ref(), hot[0].Ref())
	require.Nil(t, beforeB[0].LastUsedAt)
	require.Equal(t, usedAt, *hot[0].LastUsedAt)
	require.Nil(t, hot[0].Ref().LastUsedAt)

	account.Name = "after"
	account.GroupIDs = []int64{b.GroupID}
	account.AccountGroups = []service.AccountGroup{{AccountID: account.ID, GroupID: b.GroupID}}
	require.NoError(t, writer.SetAccount(ctx, &account))
	require.NoError(t, writer.SetSnapshotByAccountIDs(ctx, a, tokenA, []int64{other.ID}))
	afterA, afterB := read(a), read(b)
	require.Len(t, afterA, 1)
	require.Equal(t, other.ID, afterA[0].ID, "removed group membership cannot be resurrected from the account pool")
	require.Equal(t, []int64{b.GroupID}, afterB[0].GroupIDs)
	require.Equal(t, "after", afterB[0].Name)
	require.NotSame(t, beforeB[0].Ref(), afterB[0].Ref())
	require.Equal(t, "before", beforeB[0].Name)
	full, err := reader.GetAccount(ctx, account.ID)
	require.NoError(t, err)
	require.Equal(t, "fixture-private-token", full.GetCredential("access_token"))
	full.Credentials["model_mapping"].(map[string]any)["model"] = "request-only"
	require.Equal(t, "target", afterB[0].GetModelMapping()["model"])

	require.NoError(t, writer.RetireBucket(ctx, b))
	_, hit, err := reader.GetCandidateSnapshot(ctx, b)
	require.NoError(t, err)
	require.False(t, hit)
	newToken, err := writer.ReopenBucket(ctx, b)
	require.NoError(t, err)
	require.ErrorIs(t, writer.SetSnapshotByAccountIDs(ctx, b, tokenB, []int64{account.ID}), service.ErrSchedulerBucketWriteFenced)
	require.NoError(t, writer.SetSnapshotByAccountIDs(ctx, b, newToken, []int64{account.ID}))
	require.Equal(t, account.ID, read(b)[0].ID)
	require.NoError(t, writer.DeleteAccount(ctx, account.ID))
	_, hit, err = reader.GetCandidateSnapshot(ctx, b)
	require.NoError(t, err)
	require.False(t, hit)
}

func TestSchedulerSharedCacheRedisReconnectIntegration(t *testing.T) {
	ctx := context.Background()
	writerRedis := testRedis(t)
	// Reuse the test's namespace on a distinct one-connection client. Only that
	// client's socket is killed; the Redis server and other tests stay running.
	const marker = "scheduler-reconnect-namespace"
	command := writerRedis.Set(ctx, marker, "1", time.Minute)
	require.NoError(t, command.Err())
	prefix := strings.TrimSuffix(command.Args()[1].(string), marker)
	opts := *writerRedis.Options()
	opts.PoolSize, opts.MinIdleConns, opts.MaxRetries = 1, 0, -1
	var offline atomic.Bool
	baseDial := opts.Dialer
	opts.Dialer = func(ctx context.Context, network, address string) (net.Conn, error) {
		if offline.Load() {
			return nil, errors.New("fixture Redis link unavailable")
		}
		if baseDial != nil {
			return baseDial(ctx, network, address)
		}
		return (&net.Dialer{Timeout: opts.DialTimeout}).DialContext(ctx, network, address)
	}
	readerRedis := redis.NewClient(&opts)
	readerRedis.AddHook(prefixHook{prefix: prefix})
	t.Cleanup(func() { _ = readerRedis.Close() })
	writer := NewSchedulerCache(writerRedis).(*schedulerCache)
	reader := NewSchedulerCache(readerRedis).(*schedulerCache)
	bucket := service.SchedulerBucket{GroupID: 903, Platform: service.PlatformAnthropic, Mode: service.SchedulerModeSingle}
	account := service.Account{ID: 9031, Name: "before", Platform: service.PlatformAnthropic, Status: service.StatusActive, Schedulable: true}
	token, err := writer.CaptureBucketWriteToken(ctx, bucket)
	require.NoError(t, err)
	require.NoError(t, writer.SetSnapshot(ctx, bucket, token, []service.Account{account}))
	before, hit, err := reader.GetCandidateSnapshot(ctx, bucket)
	require.NoError(t, err)
	require.True(t, hit)
	connectionID, err := readerRedis.ClientID(ctx).Result()
	require.NoError(t, err)
	offline.Store(true)
	killed, err := writerRedis.ClientKillByFilter(ctx, "ID", strconv.FormatInt(connectionID, 10)).Result()
	require.NoError(t, err)
	require.EqualValues(t, 1, killed)
	_, hit, err = reader.GetCandidateSnapshot(ctx, bucket)
	require.Error(t, err)
	require.False(t, hit, "a local snapshot must not bypass the Redis freshness check during an outage")
	account.Name, account.Schedulable = "after reconnect", false
	require.NoError(t, writer.SetAccount(ctx, &account))
	offline.Store(false)
	require.Eventually(t, func() bool {
		views, hit, err := reader.GetCandidateSnapshot(ctx, bucket)
		return err == nil && hit && len(views) == 1 && views[0].Name == account.Name && !views[0].Schedulable
	}, 5*time.Second, 20*time.Millisecond)
	require.Equal(t, "before", before[0].Name)
	require.True(t, before[0].Schedulable)
}
