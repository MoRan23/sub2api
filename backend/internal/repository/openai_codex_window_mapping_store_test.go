package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestOpenAICodexWindowMappingRedisInheritsLegacySidecarAndRollover(t *testing.T) {
	f := newCodexClientWindowRedisFixture(t)
	f.bind(t)
	advanced, err := f.client.ResolveOpenAICodexClientWindow(context.Background(), f.key, codexClientWindowTransition(f.initial, codexClientWindowNextIdentity()), time.Minute)
	require.NoError(t, err)
	mainRaw, err := f.mr.Get(f.mainKey)
	require.NoError(t, err)
	sidecarRaw, err := f.mr.Get(f.clientKey)
	require.NoError(t, err)
	canonical := strings.Repeat("a", 64)
	mapping := f.main.(service.OpenAICodexWindowMappingStore)
	selected, err := mapping.ResolveOpenAICodexWindowMapping(context.Background(), canonical, f.key, f.initial.ThreadID, 3*time.Minute)
	require.NoError(t, err)
	require.Equal(t, f.key, selected)
	require.False(t, f.mr.Exists(OpenAICodexWindowKeyPrefix+canonical))
	storedMain, err := f.mr.Get(f.mainKey)
	require.NoError(t, err)
	storedSidecar, err := f.mr.Get(f.clientKey)
	require.NoError(t, err)
	require.Equal(t, mainRaw, storedMain)
	require.Equal(t, sidecarRaw, storedSidecar)
	for _, key := range []string{OpenAICodexWindowMappingKeyPrefix + canonical, f.mainKey, f.clientKey} {
		require.Equal(t, 3*time.Minute, f.mr.TTL(key))
	}

	// A different process/account may no longer supply the original source key.
	// The fixed alias still resolves it, including its original client tokens.
	selected, err = mapping.ResolveOpenAICodexWindowMapping(context.Background(), canonical, "", f.initial.ThreadID, 4*time.Minute)
	require.NoError(t, err)
	require.Equal(t, f.key, selected)
	next := codexClientWindowTransition(advanced.Snapshot, service.OpenAICodexClientWindowIdentity{
		Number: 2, FirstToken: codexClientWindowToken(1), PreviousToken: codexClientWindowToken(2), CurrentToken: codexClientWindowToken(3),
	})
	next.ProposedContextWindowID = repositoryOpenAICodexContextWindowLater
	next.RolloverDigest = strings.Repeat("d", 64)
	result, err := f.client.ResolveOpenAICodexClientWindow(context.Background(), selected, next, time.Minute)
	require.NoError(t, err)
	require.Equal(t, service.OpenAICodexClientWindowAdvanced, result.Status)
	require.Equal(t, uint64(2), result.Snapshot.Number)
	require.Equal(t, advanced.Snapshot.ContextWindowID, result.Snapshot.PreviousContextWindowID)
	require.Equal(t, f.initial.ContextWindowID, result.Snapshot.FirstContextWindowID)
}

func TestOpenAICodexWindowMappingRedisPrefersExistingCanonical(t *testing.T) {
	f := newCodexClientWindowRedisFixture(t)
	f.bind(t)
	canonical := strings.Repeat("a", 64)
	candidate := f.initial
	candidate.ContextWindowID = repositoryOpenAICodexContextWindowLater
	candidate.FirstContextWindowID = candidate.ContextWindowID
	_, err := f.main.ResolveOpenAICodexWindow(context.Background(), canonical, candidate, time.Minute)
	require.NoError(t, err)
	mapping := f.main.(service.OpenAICodexWindowMappingStore)
	selected, err := mapping.ResolveOpenAICodexWindowMapping(context.Background(), canonical, f.key, f.initial.ThreadID, 2*time.Minute)
	require.NoError(t, err)
	require.Equal(t, canonical, selected)
	require.Equal(t, time.Minute, f.mr.TTL(f.mainKey))

	// An unused old source cannot invalidate the already selected lineage.
	require.NoError(t, f.mr.Set(f.mainKey, "malformed"))
	selected, err = mapping.ResolveOpenAICodexWindowMapping(context.Background(), canonical, f.key, f.initial.ThreadID, 3*time.Minute)
	require.NoError(t, err)
	require.Equal(t, canonical, selected)
}

func TestOpenAICodexWindowMappingRedisAcceptsHistoricalWindowFormats(t *testing.T) {
	for _, fieldCount := range []int{3, 4} {
		for _, number := range []uint64{0, 2} {
			t.Run(fmt.Sprintf("%d fields/window %d", fieldCount, number), func(t *testing.T) {
				mr, _, main := newOpenAICodexWindowRedisTest(t)
				canonical, legacy := strings.Repeat("a", 64), strings.Repeat("b", 64)
				stored := map[string]any{"thread_id": repositoryOpenAICodexWindowThread, "window_number": number, "last_compact_digest": ""}
				if number > 0 {
					stored["last_compact_digest"] = strings.Repeat("c", 64)
				}
				if fieldCount == 4 {
					stored["context_window_id"] = repositoryOpenAICodexContextWindowInitial
				}
				raw, err := json.Marshal(stored)
				require.NoError(t, err)
				legacyRedisKey := OpenAICodexWindowKeyPrefix + legacy
				require.NoError(t, mr.Set(legacyRedisKey, string(raw)))
				selected, err := main.(service.OpenAICodexWindowMappingStore).ResolveOpenAICodexWindowMapping(context.Background(), canonical, legacy, repositoryOpenAICodexWindowThread, time.Minute)
				require.NoError(t, err)
				require.Equal(t, legacy, selected)
				after, err := mr.Get(legacyRedisKey)
				require.NoError(t, err)
				require.Equal(t, string(raw), after)
				resolved, err := main.ResolveOpenAICodexWindow(context.Background(), selected, service.OpenAICodexWindowSnapshot{
					ThreadID: repositoryOpenAICodexWindowThread, ContextWindowID: repositoryOpenAICodexContextWindowNext,
				}, time.Minute)
				require.NoError(t, err)
				require.Equal(t, number, resolved.Number)
				if fieldCount == 4 {
					require.Equal(t, repositoryOpenAICodexContextWindowInitial, resolved.ContextWindowID)
				} else {
					require.Equal(t, repositoryOpenAICodexContextWindowNext, resolved.ContextWindowID)
				}
			})
		}
	}
}

func TestOpenAICodexWindowMappingRedisRejectsHistoricalMainWithSidecarWithoutContext(t *testing.T) {
	f := newCodexClientWindowRedisFixture(t)
	f.bind(t)
	raw, err := json.Marshal(map[string]any{"thread_id": f.initial.ThreadID, "window_number": 0, "last_compact_digest": ""})
	require.NoError(t, err)
	require.NoError(t, f.mr.Set(f.mainKey, string(raw)))
	_, err = f.main.(service.OpenAICodexWindowMappingStore).ResolveOpenAICodexWindowMapping(context.Background(), strings.Repeat("a", 64), f.key, f.initial.ThreadID, time.Minute)
	require.ErrorIs(t, err, service.ErrOpenAICodexWindowStoredInvalid)
}

func TestOpenAICodexWindowMappingRedisKeepsFirstClaimBeforeWindowExists(t *testing.T) {
	mr, rdb, main := newOpenAICodexWindowRedisTest(t)
	canonical, legacy := strings.Repeat("a", 64), strings.Repeat("b", 64)
	mapping := NewOpenAICodexWindowStore(rdb).(service.OpenAICodexWindowMappingStore)
	selected, err := mapping.ResolveOpenAICodexWindowMapping(context.Background(), canonical, legacy, repositoryOpenAICodexWindowThread, 0)
	require.NoError(t, err)
	require.Equal(t, canonical, selected)
	require.Equal(t, service.OpenAICodexWindowTTL, mr.TTL(OpenAICodexWindowMappingKeyPrefix+canonical))
	_, err = main.ResolveOpenAICodexWindow(context.Background(), legacy, service.OpenAICodexWindowSnapshot{
		ThreadID: repositoryOpenAICodexWindowThread, ContextWindowID: repositoryOpenAICodexContextWindowInitial,
	}, time.Minute)
	require.NoError(t, err)
	selected, err = mapping.ResolveOpenAICodexWindowMapping(context.Background(), canonical, legacy, repositoryOpenAICodexWindowThread, time.Minute)
	require.NoError(t, err)
	require.Equal(t, canonical, selected)
	// Even if a legacy alias's records expire, it must not switch storage keys.
	require.NoError(t, mr.Set(OpenAICodexWindowMappingKeyPrefix+canonical, legacy))
	mr.Del(OpenAICodexWindowKeyPrefix + legacy)
	selected, err = mapping.ResolveOpenAICodexWindowMapping(context.Background(), canonical, "", repositoryOpenAICodexWindowThread, time.Minute)
	require.NoError(t, err)
	require.Equal(t, legacy, selected)
}

func TestOpenAICodexWindowMappingRedisRejectsCorruption(t *testing.T) {
	for _, name := range []string{"main JSON", "main thread", "sidecar JSON", "sidecar thread", "sidecar context", "sidecar ahead", "sidecar lineage", "orphan sidecar", "wrong type main", "wrong type sidecar", "invalid alias", "padded alias", "wrong type alias", "canonical corruption"} {
		t.Run(name, func(t *testing.T) {
			f := newCodexClientWindowRedisFixture(t)
			f.bind(t)
			canonical := strings.Repeat("a", 64)
			aliasKey := OpenAICodexWindowMappingKeyPrefix + canonical
			switch name {
			case "main JSON":
				require.NoError(t, f.mr.Set(f.mainKey, "{}"))
			case "main thread":
				snapshot := f.initial
				snapshot.ThreadID = repositoryOpenAICodexContextWindowLater
				raw, err := json.Marshal(snapshot)
				require.NoError(t, err)
				require.NoError(t, f.mr.Set(f.mainKey, string(raw)))
			case "sidecar JSON":
				require.NoError(t, f.mr.Set(f.clientKey, "{}"))
			case "sidecar thread":
				_, binding := f.stored(t)
				binding.Server.ThreadID = repositoryOpenAICodexContextWindowLater
				raw, err := json.Marshal(binding)
				require.NoError(t, err)
				require.NoError(t, f.mr.Set(f.clientKey, string(raw)))
			case "sidecar context", "sidecar ahead", "sidecar lineage":
				_, binding := f.stored(t)
				binding.Server.ContextWindowID = repositoryOpenAICodexContextWindowNext
				binding.Server.FirstContextWindowID = binding.Server.ContextWindowID
				if name == "sidecar ahead" {
					binding.Server.Number = 1
					binding.Server.FirstContextWindowID = f.initial.ContextWindowID
					binding.Server.PreviousContextWindowID = f.initial.ContextWindowID
					binding.Server.LastCompactDigest = strings.Repeat("c", 64)
				}
				if name == "sidecar lineage" {
					_, err := f.main.CommitOpenAICodexWindow(context.Background(), f.key, f.initial, strings.Repeat("c", 64), repositoryOpenAICodexContextWindowLater, time.Minute)
					require.NoError(t, err)
				}
				raw, err := json.Marshal(binding)
				require.NoError(t, err)
				require.NoError(t, f.mr.Set(f.clientKey, string(raw)))
			case "orphan sidecar":
				f.mr.Del(f.mainKey)
			case "wrong type main":
				f.mr.Del(f.mainKey)
				_, err := f.main.(*gatewayCache).rdb.LPush(context.Background(), f.mainKey, "invalid").Result()
				require.NoError(t, err)
			case "wrong type sidecar":
				f.mr.Del(f.clientKey)
				_, err := f.main.(*gatewayCache).rdb.LPush(context.Background(), f.clientKey, "invalid").Result()
				require.NoError(t, err)
			case "invalid alias":
				require.NoError(t, f.mr.Set(aliasKey, "bad"))
			case "padded alias":
				require.NoError(t, f.mr.Set(aliasKey, " "+f.key+" "))
			case "wrong type alias":
				_, err := f.main.(*gatewayCache).rdb.LPush(context.Background(), aliasKey, "invalid").Result()
				require.NoError(t, err)
			case "canonical corruption":
				require.NoError(t, f.mr.Set(OpenAICodexWindowKeyPrefix+canonical, "{}"))
			}
			selected, err := f.main.(service.OpenAICodexWindowMappingStore).ResolveOpenAICodexWindowMapping(context.Background(), canonical, f.key, f.initial.ThreadID, time.Hour)
			require.ErrorIs(t, err, service.ErrOpenAICodexWindowStoredInvalid)
			require.NotErrorIs(t, err, service.ErrOpenAICodexWindowStoreUnavailable)
			require.Empty(t, selected)
			if !strings.Contains(name, "alias") {
				require.False(t, f.mr.Exists(aliasKey))
			}
		})
	}
}

func TestOpenAICodexWindowMappingRedisConcurrentClaimsChooseOneSource(t *testing.T) {
	_, rdb, main := newOpenAICodexWindowRedisTest(t)
	canonical := strings.Repeat("a", 64)
	legacyKeys := []string{strings.Repeat("b", 64), strings.Repeat("c", 64)}
	for i, key := range legacyKeys {
		_, err := main.ResolveOpenAICodexWindow(context.Background(), key, service.OpenAICodexWindowSnapshot{
			ThreadID: repositoryOpenAICodexWindowThread, ContextWindowID: []string{repositoryOpenAICodexContextWindowInitial, repositoryOpenAICodexContextWindowNext}[i],
		}, time.Minute)
		require.NoError(t, err)
	}
	stores := []service.OpenAICodexWindowMappingStore{main.(service.OpenAICodexWindowMappingStore), NewOpenAICodexWindowStore(rdb).(service.OpenAICodexWindowMappingStore)}
	const count = 24
	results := make([]string, count)
	errors := make([]error, count)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range results {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			<-start
			results[index], errors[index] = stores[index%2].ResolveOpenAICodexWindowMapping(context.Background(), canonical, legacyKeys[index%2], repositoryOpenAICodexWindowThread, time.Minute)
		}(i)
	}
	close(start)
	wg.Wait()
	for i := range results {
		require.NoError(t, errors[i])
		require.Contains(t, legacyKeys, results[i])
		require.Equal(t, results[0], results[i])
	}
}

func TestOpenAICodexWindowMappingRedisValidatesInputsAndAvailability(t *testing.T) {
	_, _, main := newOpenAICodexWindowRedisTest(t)
	mapping := main.(service.OpenAICodexWindowMappingStore)
	canonical := strings.Repeat("a", 64)
	for _, args := range [][3]string{{"bad", "", repositoryOpenAICodexWindowThread}, {canonical, "bad", repositoryOpenAICodexWindowThread}, {canonical, "", "not-a-thread"}} {
		_, err := mapping.ResolveOpenAICodexWindowMapping(context.Background(), args[0], args[1], args[2], time.Minute)
		require.Error(t, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := mapping.ResolveOpenAICodexWindowMapping(ctx, canonical, "", repositoryOpenAICodexWindowThread, time.Minute)
	require.ErrorIs(t, err, context.Canceled)
	var nilStore *openAICodexWindowRedisStore
	_, err = nilStore.ResolveOpenAICodexWindowMapping(context.Background(), canonical, "", repositoryOpenAICodexWindowThread, time.Minute)
	require.ErrorIs(t, err, service.ErrOpenAICodexWindowStoreUnavailable)
	var nilCache *gatewayCache
	_, err = nilCache.ResolveOpenAICodexWindowMapping(context.Background(), canonical, "", repositoryOpenAICodexWindowThread, time.Minute)
	require.ErrorIs(t, err, service.ErrOpenAICodexWindowStoreUnavailable)
}
