//go:build unit

package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestSchedulerMGetPipelineBoundedAndOrdered(t *testing.T) {
	cache, mr, recorder := newSchedulerCacheMemoUnit(t, 128)
	keys := make([]string, 2000)
	for i := range keys {
		keys[i] = fmt.Sprintf("test:%d", i)
		if i%17 != 0 {
			mr.Set(keys[i], fmt.Sprint(i))
		}
	}
	values, err := cache.mgetChunked(context.Background(), keys)
	require.NoError(t, err)
	require.Len(t, values, len(keys))
	for i, v := range values {
		if i%17 == 0 {
			require.Nil(t, v)
		} else {
			require.Equal(t, fmt.Sprint(i), v)
		}
	}
	batches := recorder.pipelineBatches()
	require.Len(t, batches, 2)
	require.Len(t, batches[0], 8)
	require.Len(t, batches[1], 8)
	recorder.reset()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = cache.mgetChunked(ctx, keys)
	require.ErrorIs(t, err, context.Canceled)
	require.Empty(t, recorder.recorded())
}
func TestSchedulerMGetPipelineFailureDoesNotReturnPartialData(t *testing.T) {
	cache, _, recorder := newSchedulerCacheMemoUnit(t, 128)
	recorder.setFailPipelineAt(2)
	keys := make([]string, 2000)
	for i := range keys {
		keys[i] = fmt.Sprint(i)
	}
	values, err := cache.mgetChunked(context.Background(), keys)
	require.Error(t, err)
	require.Nil(t, values)
}
func TestShadowHealthProjectionPreservesAccountJSONFields(t *testing.T) {
	cache, mr, _ := newSchedulerCacheMemoUnit(t, 128)
	now := time.Now().UTC().Truncate(time.Millisecond)
	account := service.Account{ID: 7, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Status: service.StatusActive, ExpiresAt: &now, AutoPauseOnExpired: true, TempUnschedulableUntil: &now, Credentials: map[string]any{"access_token": "must-not-decode"}}
	raw, err := json.Marshal(account)
	require.NoError(t, err)
	mr.Set(schedulerAccountMetaKey("7"), string(raw))
	values, err := cache.GetShadowParentHealthBatch(context.Background(), []int64{7, 8})
	require.NoError(t, err)
	require.Len(t, values, 1)
	projected := values[7].Account()
	require.Equal(t, account.ExpiresAt, projected.ExpiresAt)
	require.True(t, projected.AutoPauseOnExpired)
	require.Equal(t, account.TempUnschedulableUntil, projected.TempUnschedulableUntil)
	require.Nil(t, projected.Credentials)
}

func TestSchedulerLateMissingMetadataKeepsNewEntry(t *testing.T) {
	cache, _, _ := newSchedulerCacheMemoUnit(t, 128)
	old := &schedulerSnapshotEntry{estimatedBytes: 10}
	newer := &schedulerSnapshotEntry{estimatedBytes: 20}
	cache.snapshotEntries["bucket"] = newer
	cache.bucketBytes = 20
	cache.dropSnapshotEntryIfCurrent("bucket", old)
	require.Same(t, newer, cache.snapshotEntries["bucket"])
	require.EqualValues(t, 20, cache.bucketBytes)
	cache.dropSnapshotEntryIfCurrent("bucket", newer)
	require.Empty(t, cache.snapshotEntries)
	require.Zero(t, cache.bucketBytes)
}
