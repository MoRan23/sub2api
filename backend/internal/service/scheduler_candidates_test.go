//go:build unit

package service

import (
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/xai"
	"github.com/stretchr/testify/require"
)

func TestCandidateViewAllocatesOnlyForMutatedCandidate(t *testing.T) {
	master := &Account{ID: 1, Name: "master", Credentials: map[string]any{
		"model_mapping":         map[string]any{"model": "target"},
		"compact_model_mapping": map[string]any{"compact": "target"},
	}, Extra: map[string]any{"kept": true}}
	master.PrepareSharedSnapshot()
	usedAt := time.Now()
	first := NewCandidateView(master, &usedAt)
	second := NewCandidateView(master, nil)
	require.Same(t, first.Ref(), second.Ref())
	private := first.MutableAccount()
	require.NotSame(t, master, private)
	require.Same(t, private, first.MutableAccount())
	require.Equal(t, &usedAt, private.LastUsedAt)
	private.TempUnschedulableReason = "request pause"
	setAccountModelRateLimitSnapshot(private, "fable", usedAt.Add(time.Minute), "test", usedAt)
	require.Empty(t, master.TempUnschedulableReason)
	require.Nil(t, master.LastUsedAt)
	require.NotContains(t, master.Extra, modelRateLimitsKey)
	require.Contains(t, private.Extra, modelRateLimitsKey)
	require.Equal(t, map[string]string{"compact": "target"}, second.GetCompactModelMapping())
}

func TestSharedCandidateRuntimeMappingChangeDoesNotWriteMaster(t *testing.T) {
	original := xai.RuntimeModelMappingOptions()
	t.Cleanup(func() { xai.SetRuntimeModelMappingOptions(original) })
	master := &Account{Platform: PlatformGrok}
	master.PrepareSharedSnapshot()
	preparedVersion := master.modelMappingCacheRuntimeVersion
	xai.SetRuntimeModelMappingOptions(original)
	require.NotEqual(t, preparedVersion, xai.RuntimeModelMappingVersion())
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				_ = master.GetModelMapping()
			}
		}()
	}
	wg.Wait()
	require.Equal(t, preparedVersion, master.modelMappingCacheRuntimeVersion)
	require.True(t, master.modelMappingCacheShared)
}
