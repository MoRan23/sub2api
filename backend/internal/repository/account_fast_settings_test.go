//go:build unit

package repository

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestAccountFastSettingsBackgroundPatchAndSchedulerProjection(t *testing.T) {
	extra := map[string]any{
		service.ForceOpenAIFastExtraKey: true, service.ForceOpenAIUltrafastExtraKey: false,
		service.FreeOpenAIFastExtraKey: false, service.FreeOpenAIUltrafastExtraKey: true,
	}
	require.Empty(t, accountConfigurationExtraPatch(context.Background(), []int64{1}, extra), "background extra snapshots cannot set admin preferences")
	require.Len(t, extra, 4, "guard must not mutate the caller's snapshot")
	require.Equal(t, extra, filterSchedulerExtra(extra), "explicit false values survive the candidate projection")
	require.True(t, shouldEnqueueSchedulerOutboxForExtraUpdates(extra))
	cache, _, _ := newSchedulerCacheMemoUnit(t, defaultSchedulerSnapshotWriteChunkSize)
	bucket := service.SchedulerBucket{GroupID: 78, Platform: service.PlatformOpenAI, Mode: service.SchedulerModeSingle}
	account := service.Account{ID: 7810, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey,
		Status: service.StatusActive, Schedulable: true, Extra: extra}
	memoPublish(t, cache, bucket, account)
	candidate := memoRead(t, cache, bucket)[0]
	require.Equal(t, extra, candidate.Extra)
	require.True(t, candidate.IsForceOpenAIFastEnabled())
	require.True(t, candidate.IsFreeOpenAIUltrafastEnabled())
	hydrated, err := cache.GetAccount(context.Background(), account.ID)
	require.NoError(t, err)
	require.Equal(t, extra, hydrated.Extra)
}
