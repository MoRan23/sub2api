package service

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestOpenAICodexWindowMappingLocalInheritsClientWindowInPlace(t *testing.T) {
	store, legacyKey, transition := newOpenAICodexClientWindowLocalTest(t)
	ctx := context.Background()
	_, err := store.ResolveOpenAICodexClientWindow(ctx, legacyKey, transition, time.Hour)
	require.NoError(t, err)
	canonicalKey := strings.Repeat("d", 64)
	selected, err := store.ResolveOpenAICodexWindowMapping(ctx, canonicalKey, legacyKey, transition.Expected.ThreadID, time.Hour)
	require.NoError(t, err)
	require.Equal(t, legacyKey, selected)
	require.NotContains(t, store.entries, canonicalKey, "the sidecar's mapping-key-scoped tokens must stay in place")
	require.Equal(t, transition.Client, store.entries[legacyKey].clientWindow.Client)

	next := nextOpenAICodexClientWindowTest(transition)
	advanced, err := store.ResolveOpenAICodexClientWindow(ctx, selected, next, time.Hour)
	require.NoError(t, err)
	require.Equal(t, OpenAICodexClientWindowAdvanced, advanced.Status)
	again, err := store.ResolveOpenAICodexWindowMapping(ctx, canonicalKey, strings.Repeat("e", 64), transition.Expected.ThreadID, time.Hour)
	require.NoError(t, err)
	require.Equal(t, selected, again, "a later account cannot choose a different source")
	current, exists := store.current(again)
	require.True(t, exists)
	require.Equal(t, advanced.Snapshot, current)
}

func TestOpenAICodexWindowMappingLocalFreezesMissingLegacy(t *testing.T) {
	store := newOpenAICodexWindowLocalStore(4)
	canonicalKey, legacyKey := strings.Repeat("d", 64), strings.Repeat("a", 64)
	selected, err := store.ResolveOpenAICodexWindowMapping(context.Background(), canonicalKey, legacyKey, testOpenAICodexWindowThread, time.Hour)
	require.NoError(t, err)
	require.Equal(t, canonicalKey, selected)
	_, err = store.ResolveOpenAICodexWindow(context.Background(), legacyKey, OpenAICodexWindowSnapshot{ThreadID: testOpenAICodexWindowThread, ContextWindowID: testOpenAICodexContextWindowInitial}, time.Hour)
	require.NoError(t, err)
	again, err := store.ResolveOpenAICodexWindowMapping(context.Background(), canonicalKey, legacyKey, testOpenAICodexWindowThread, time.Hour)
	require.NoError(t, err)
	require.Equal(t, canonicalKey, again, "a late old-account write must not change the chosen downstream lineage")
}

func TestOpenAICodexWindowMappingLocalCanonicalWindowWinsWithoutAlias(t *testing.T) {
	store := newOpenAICodexWindowLocalStore(4)
	canonicalKey, legacyKey := strings.Repeat("d", 64), strings.Repeat("a", 64)
	for _, key := range []string{canonicalKey, legacyKey} {
		_, err := store.ResolveOpenAICodexWindow(context.Background(), key, OpenAICodexWindowSnapshot{ThreadID: testOpenAICodexWindowThread, ContextWindowID: testOpenAICodexContextWindowInitial}, time.Hour)
		require.NoError(t, err)
	}
	selected, err := store.ResolveOpenAICodexWindowMapping(context.Background(), canonicalKey, legacyKey, testOpenAICodexWindowThread, time.Hour)
	require.NoError(t, err)
	require.Equal(t, canonicalKey, selected)
}

func TestOpenAICodexWindowMappingLocalConcurrentClaimsConverge(t *testing.T) {
	store := newOpenAICodexWindowLocalStore(8)
	canonicalKey := strings.Repeat("d", 64)
	legacyKeys := []string{strings.Repeat("a", 64), strings.Repeat("b", 64)}
	for _, key := range legacyKeys {
		_, err := store.ResolveOpenAICodexWindow(context.Background(), key, OpenAICodexWindowSnapshot{ThreadID: testOpenAICodexWindowThread, ContextWindowID: testOpenAICodexContextWindowInitial}, time.Hour)
		require.NoError(t, err)
	}
	var wg sync.WaitGroup
	const count = 24
	selected := make([]string, count)
	errs := make([]error, count)
	for i := range count {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			selected[i], errs[i] = store.ResolveOpenAICodexWindowMapping(context.Background(), canonicalKey, legacyKeys[i%2], testOpenAICodexWindowThread, time.Hour)
		}(i)
	}
	wg.Wait()
	for i := range count {
		require.NoError(t, errs[i])
		require.Equal(t, selected[0], selected[i])
	}
}

func TestOpenAICodexWindowMappingLocalRejectsCorruptMigration(t *testing.T) {
	for _, problem := range []string{"thread", "client", "future binding", "mixed context"} {
		t.Run(problem, func(t *testing.T) {
			store, legacyKey, transition := newOpenAICodexClientWindowLocalTest(t)
			_, err := store.ResolveOpenAICodexClientWindow(context.Background(), legacyKey, transition, time.Hour)
			require.NoError(t, err)
			entry := store.entries[legacyKey]
			switch problem {
			case "thread":
				entry.snapshot.ThreadID = testOpenAICodexWindowOtherThread
			case "client":
				entry.clientWindow.Client.FirstToken = "invalid"
			case "future binding":
				entry.clientWindow.Server.Number = 1
				entry.clientWindow.Server.LastCompactDigest = strings.Repeat("e", 64)
			case "mixed context":
				entry.clientWindow.Server.ContextWindowID = testOpenAICodexContextWindowNext
				entry.clientWindow.Server.FirstContextWindowID = testOpenAICodexContextWindowNext
			}
			selected, err := store.ResolveOpenAICodexWindowMapping(context.Background(), strings.Repeat("d", 64), legacyKey, testOpenAICodexWindowThread, time.Hour)
			require.ErrorIs(t, err, ErrOpenAICodexWindowStoredInvalid)
			require.Empty(t, selected)
			require.Empty(t, store.mappingAliases)
		})
	}
}

func TestOpenAICodexWindowMappingLocalRefreshesSourceAndAliasTTL(t *testing.T) {
	store, legacyKey, transition := newOpenAICodexClientWindowLocalTest(t)
	canonicalKey := strings.Repeat("d", 64)
	_, err := store.ResolveOpenAICodexWindowMapping(context.Background(), canonicalKey, legacyKey, transition.Expected.ThreadID, time.Second)
	require.NoError(t, err)
	before := time.Now()
	_, err = store.ResolveOpenAICodexWindowMapping(context.Background(), canonicalKey, legacyKey, transition.Expected.ThreadID, time.Hour)
	require.NoError(t, err)
	require.True(t, store.mappingAliases[canonicalKey].expiresAt.After(before.Add(59*time.Minute)))
	require.True(t, store.entries[legacyKey].expiresAt.After(before.Add(59*time.Minute)))
}

type unavailableOpenAICodexWindowMappingStore struct{ *openAICodexWindowLocalStore }

func (s *unavailableOpenAICodexWindowMappingStore) ResolveOpenAICodexWindowMapping(context.Context, string, string, string, time.Duration) (string, error) {
	return "", ErrOpenAICodexWindowStoreUnavailable
}

func TestOpenAICodexWindowMappingRuntimeDoesNotForkOnPrimaryFailure(t *testing.T) {
	local := newOpenAICodexWindowLocalStore(4)
	primary := &unavailableOpenAICodexWindowMappingStore{newOpenAICodexWindowLocalStore(4)}
	store := newOpenAICodexWindowRuntimeStoreWithLocal(primary, local)
	selected, err := store.ResolveOpenAICodexWindowMapping(context.Background(), strings.Repeat("d", 64), strings.Repeat("a", 64), testOpenAICodexWindowThread, time.Hour)
	require.ErrorIs(t, err, ErrOpenAICodexWindowStoreUnavailable)
	require.Empty(t, selected)
	require.Empty(t, local.mappingAliases)
	require.Empty(t, local.entries)
}

type toggleOpenAICodexWindowMappingStore struct {
	*openAICodexWindowLocalStore
	mappingErr error
	resolveErr error
}

func (s *toggleOpenAICodexWindowMappingStore) ResolveOpenAICodexWindowMapping(ctx context.Context, canonicalKey, legacyKey, threadID string, ttl time.Duration) (string, error) {
	if s.mappingErr != nil {
		return "", s.mappingErr
	}
	return s.openAICodexWindowLocalStore.ResolveOpenAICodexWindowMapping(ctx, canonicalKey, legacyKey, threadID, ttl)
}

func (s *toggleOpenAICodexWindowMappingStore) ResolveOpenAICodexWindow(ctx context.Context, mappingKey string, candidate OpenAICodexWindowSnapshot, ttl time.Duration) (OpenAICodexWindowSnapshot, error) {
	if s.resolveErr != nil {
		return OpenAICodexWindowSnapshot{}, s.resolveErr
	}
	return s.openAICodexWindowLocalStore.ResolveOpenAICodexWindow(ctx, mappingKey, candidate, ttl)
}

func TestOpenAICodexWindowMappingRuntimeWarmOutageKeepsMigratedWindow(t *testing.T) {
	ctx := context.Background()
	primary := &toggleOpenAICodexWindowMappingStore{openAICodexWindowLocalStore: newOpenAICodexWindowLocalStore(4)}
	local := newOpenAICodexWindowLocalStore(4)
	store := newOpenAICodexWindowRuntimeStoreWithLocal(primary, local)
	canonicalKey, legacyKey := strings.Repeat("d", 64), strings.Repeat("a", 64)
	initial := OpenAICodexWindowSnapshot{ThreadID: testOpenAICodexWindowThread, ContextWindowID: testOpenAICodexContextWindowInitial}
	seed, err := primary.ResolveOpenAICodexWindow(ctx, legacyKey, initial, time.Hour)
	require.NoError(t, err)
	advanced, err := primary.CommitOpenAICodexWindow(ctx, legacyKey, seed, strings.Repeat("f", 64), testOpenAICodexContextWindowNext, time.Hour)
	require.NoError(t, err)
	selected, err := store.ResolveOpenAICodexWindowMapping(ctx, canonicalKey, legacyKey, initial.ThreadID, time.Hour)
	require.NoError(t, err)
	require.Equal(t, legacyKey, selected)
	winner, err := store.ResolveOpenAICodexWindow(ctx, selected, initial, time.Hour)
	require.NoError(t, err)
	require.Equal(t, advanced.Snapshot, winner)

	primary.mappingErr = ErrOpenAICodexWindowStoreUnavailable
	primary.resolveErr = ErrOpenAICodexWindowStoreUnavailable
	selected, err = store.ResolveOpenAICodexWindowMapping(ctx, canonicalKey, strings.Repeat("b", 64), initial.ThreadID, time.Hour)
	require.NoError(t, err)
	require.Equal(t, legacyKey, selected, "another account during an outage must inherit the confirmed storage key")
	winner, err = store.ResolveOpenAICodexWindow(ctx, selected, initial, time.Hour)
	require.NoError(t, err)
	require.Equal(t, advanced.Snapshot, winner)
	primary.mappingErr = ErrOpenAICodexWindowStoredInvalid
	_, err = store.ResolveOpenAICodexWindowMapping(ctx, canonicalKey, legacyKey, initial.ThreadID, time.Hour)
	require.ErrorIs(t, err, ErrOpenAICodexWindowStoredInvalid, "corruption must not use the outage fallback")

	primary.mappingErr, primary.resolveErr = nil, nil
	selected, err = store.ResolveOpenAICodexWindowMapping(ctx, canonicalKey, "", initial.ThreadID, time.Hour)
	require.NoError(t, err)
	require.Equal(t, legacyKey, selected)
	winner, err = store.ResolveOpenAICodexWindow(ctx, selected, initial, time.Hour)
	require.NoError(t, err)
	require.Equal(t, advanced.Snapshot, winner)
	require.NotContains(t, local.entries, canonicalKey)

	local.mappingAliases[canonicalKey].expiresAt = time.Now().Add(-time.Second)
	primary.mappingErr = ErrOpenAICodexWindowStoreUnavailable
	_, err = store.ResolveOpenAICodexWindowMapping(ctx, canonicalKey, legacyKey, initial.ThreadID, time.Hour)
	require.ErrorIs(t, err, ErrOpenAICodexWindowStoreUnavailable, "expired confirmation cannot select a new lineage")
}

func TestOpenAICodexWindowMappingRuntimeConfirmedAliasAloneIsNotWarm(t *testing.T) {
	ctx := context.Background()
	primary := &toggleOpenAICodexWindowMappingStore{openAICodexWindowLocalStore: newOpenAICodexWindowLocalStore(4)}
	local := newOpenAICodexWindowLocalStore(4)
	store := newOpenAICodexWindowRuntimeStoreWithLocal(primary, local)
	canonicalKey, legacyKey := strings.Repeat("d", 64), strings.Repeat("a", 64)
	selected, err := store.ResolveOpenAICodexWindowMapping(ctx, canonicalKey, legacyKey, testOpenAICodexWindowThread, time.Hour)
	require.NoError(t, err)
	require.Equal(t, canonicalKey, selected)
	primary.resolveErr = ErrOpenAICodexWindowStoreUnavailable
	initial := OpenAICodexWindowSnapshot{ThreadID: testOpenAICodexWindowThread, ContextWindowID: testOpenAICodexContextWindowInitial}
	for range 2 {
		_, err = store.ResolveOpenAICodexWindow(ctx, selected, initial, time.Hour)
		require.ErrorIs(t, err, ErrOpenAICodexWindowStoreUnavailable)
		require.Empty(t, local.entries, "a failed cold read must not poison the next attempt with a tentative candidate")
	}
	primary.mappingErr = ErrOpenAICodexWindowStoreUnavailable
	_, err = store.ResolveOpenAICodexWindowMapping(ctx, canonicalKey, legacyKey, initial.ThreadID, time.Hour)
	require.ErrorIs(t, err, ErrOpenAICodexWindowStoreUnavailable)
	primary.mappingErr, primary.resolveErr = nil, nil
	winner, err := store.ResolveOpenAICodexWindow(ctx, selected, initial, time.Hour)
	require.NoError(t, err)
	require.Equal(t, initial.ContextWindowID, winner.ContextWindowID)
}

func TestOpenAICodexWindowMappingRuntimeUnconfirmedLocalAliasCannotMaskOutage(t *testing.T) {
	ctx := context.Background()
	local := newOpenAICodexWindowLocalStore(4)
	canonicalKey := strings.Repeat("d", 64)
	initial := OpenAICodexWindowSnapshot{ThreadID: testOpenAICodexWindowThread, ContextWindowID: testOpenAICodexContextWindowInitial}
	_, err := local.ResolveOpenAICodexWindowMapping(ctx, canonicalKey, "", initial.ThreadID, time.Hour)
	require.NoError(t, err)
	_, err = local.ResolveOpenAICodexWindow(ctx, canonicalKey, initial, time.Hour)
	require.NoError(t, err)
	primary := &unavailableOpenAICodexWindowMappingStore{newOpenAICodexWindowLocalStore(4)}
	store := newOpenAICodexWindowRuntimeStoreWithLocal(primary, local)
	_, err = store.ResolveOpenAICodexWindowMapping(ctx, canonicalKey, "", initial.ThreadID, time.Hour)
	require.ErrorIs(t, err, ErrOpenAICodexWindowStoreUnavailable)
}

func TestOpenAICodexWindowMappingRuntimeRejectsChangedAliasAfterPrimaryDataLoss(t *testing.T) {
	ctx := context.Background()
	primary := &toggleOpenAICodexWindowMappingStore{openAICodexWindowLocalStore: newOpenAICodexWindowLocalStore(4)}
	local := newOpenAICodexWindowLocalStore(4)
	store := newOpenAICodexWindowRuntimeStoreWithLocal(primary, local)
	canonicalKey, legacyKey := strings.Repeat("d", 64), strings.Repeat("a", 64)
	initial := OpenAICodexWindowSnapshot{ThreadID: testOpenAICodexWindowThread, ContextWindowID: testOpenAICodexContextWindowInitial}
	_, err := primary.ResolveOpenAICodexWindow(ctx, legacyKey, initial, time.Hour)
	require.NoError(t, err)
	selected, err := store.ResolveOpenAICodexWindowMapping(ctx, canonicalKey, legacyKey, initial.ThreadID, time.Hour)
	require.NoError(t, err)
	winner, err := store.ResolveOpenAICodexWindow(ctx, selected, initial, time.Hour)
	require.NoError(t, err)

	// Data loss differs from an ordinary outage: a newly claimed primary alias
	// cannot silently replace the live, confirmed legacy lineage in this process.
	primary.openAICodexWindowLocalStore = newOpenAICodexWindowLocalStore(4)
	for range 2 {
		selected, err = store.ResolveOpenAICodexWindowMapping(ctx, canonicalKey, legacyKey, initial.ThreadID, time.Hour)
		require.ErrorIs(t, err, ErrOpenAICodexWindowStoredInvalid)
		require.Empty(t, selected)
		require.Equal(t, legacyKey, local.mappingAliases[canonicalKey].mappingKey)
		require.Equal(t, winner, local.entries[legacyKey].snapshot)
		require.NotContains(t, local.entries, canonicalKey)
	}
}

func TestOpenAICodexWindowRuntimeExpiredWarmEntryCannotCreateFallbackCandidate(t *testing.T) {
	ctx := context.Background()
	primary := &toggleOpenAICodexWindowMappingStore{openAICodexWindowLocalStore: newOpenAICodexWindowLocalStore(4)}
	local := newOpenAICodexWindowLocalStore(4)
	store := newOpenAICodexWindowRuntimeStoreWithLocal(primary, local)
	key := strings.Repeat("a", 64)
	initial := OpenAICodexWindowSnapshot{ThreadID: testOpenAICodexWindowThread, ContextWindowID: testOpenAICodexContextWindowInitial}
	_, err := store.ResolveOpenAICodexWindow(ctx, key, initial, time.Hour)
	require.NoError(t, err)
	local.entries[key].expiresAt = time.Now().Add(-time.Second)
	primary.resolveErr = ErrOpenAICodexWindowStoreUnavailable
	initial.ContextWindowID = testOpenAICodexContextWindowNext
	for range 2 {
		_, err = store.ResolveOpenAICodexWindow(ctx, key, initial, time.Hour)
		require.ErrorIs(t, err, ErrOpenAICodexWindowStoreUnavailable)
		require.NotContains(t, local.entries, key)
	}
}
