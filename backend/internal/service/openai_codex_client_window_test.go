package service

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func newOpenAICodexClientWindowLocalTest(t *testing.T) (*openAICodexWindowLocalStore, string, OpenAICodexClientWindowTransition) {
	t.Helper()
	store := newOpenAICodexWindowLocalStore(32)
	key := strings.Repeat("a", 64)
	initial, err := store.ResolveOpenAICodexWindow(context.Background(), key, OpenAICodexWindowSnapshot{ThreadID: testOpenAICodexWindowThread, ContextWindowID: testOpenAICodexContextWindowInitial}, time.Hour)
	require.NoError(t, err)
	return store, key, OpenAICodexClientWindowTransition{Expected: initial, Client: OpenAICodexClientWindowIdentity{FirstToken: strings.Repeat("1", 64), CurrentToken: strings.Repeat("1", 64)}, ProposedContextWindowID: initial.ContextWindowID, RolloverDigest: strings.Repeat("b", 64)}
}

func nextOpenAICodexClientWindowTest(transition OpenAICodexClientWindowTransition) OpenAICodexClientWindowTransition {
	transition.Client.Number++
	transition.Client.PreviousToken = transition.Client.CurrentToken
	transition.Client.CurrentToken = strings.Repeat("2", 64)
	transition.ProposedContextWindowID = testOpenAICodexContextWindowNext
	transition.RolloverDigest = strings.Repeat("c", 64)
	return transition
}

func TestOpenAICodexClientWindowLocalBindRolloverAndRetry(t *testing.T) {
	store, key, initial := newOpenAICodexClientWindowLocalTest(t)
	bound, err := store.ResolveOpenAICodexClientWindow(context.Background(), key, initial, time.Hour)
	require.NoError(t, err)
	require.Equal(t, OpenAICodexClientWindowBound, bound.Status)
	require.Equal(t, initial.Expected, bound.Snapshot)
	next := nextOpenAICodexClientWindowTest(initial)
	advanced, err := store.ResolveOpenAICodexClientWindow(context.Background(), key, next, time.Hour)
	require.NoError(t, err)
	require.Equal(t, OpenAICodexClientWindowAdvanced, advanced.Status)
	require.Equal(t, uint64(1), advanced.Snapshot.Number)
	require.Equal(t, testOpenAICodexContextWindowNext, advanced.Snapshot.ContextWindowID)
	require.Equal(t, initial.Expected.ContextWindowID, advanced.Snapshot.FirstContextWindowID)
	require.Equal(t, initial.Expected.ContextWindowID, advanced.Snapshot.PreviousContextWindowID)
	require.Equal(t, next.RolloverDigest, advanced.Snapshot.LastCompactDigest)
	next.ProposedContextWindowID = testOpenAICodexContextWindowLater
	retry, err := store.ResolveOpenAICodexClientWindow(context.Background(), key, next, time.Hour)
	require.NoError(t, err)
	require.Equal(t, OpenAICodexClientWindowUnchanged, retry.Status)
	require.Equal(t, advanced.Snapshot, retry.Snapshot)
	_, err = store.ResolveOpenAICodexClientWindow(context.Background(), key, initial, time.Hour)
	require.ErrorIs(t, err, ErrOpenAICodexClientWindowStale)
}

func TestOpenAICodexClientWindowFirstBindingMayResumeAtHigherClientNumber(t *testing.T) {
	store, key, initial := newOpenAICodexClientWindowLocalTest(t)
	initial.Client.Number = 8
	initial.Client.CurrentToken = strings.Repeat("2", 64)
	initial.Client.PreviousToken = strings.Repeat("3", 64)
	bound, err := store.ResolveOpenAICodexClientWindow(context.Background(), key, initial, time.Hour)
	require.NoError(t, err)
	require.Zero(t, bound.Snapshot.Number)
	next := nextOpenAICodexClientWindowTest(initial)
	next.Client.CurrentToken = strings.Repeat("4", 64)
	advanced, err := store.ResolveOpenAICodexClientWindow(context.Background(), key, next, time.Hour)
	require.NoError(t, err)
	require.Equal(t, uint64(1), advanced.Snapshot.Number)
}

func TestOpenAICodexClientWindowRejectsBranchAndExpectedMismatch(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*OpenAICodexClientWindowTransition)
	}{
		{"skip", func(s *OpenAICodexClientWindowTransition) { s.Client.Number = 2 }},
		{"first changed", func(s *OpenAICodexClientWindowTransition) { s.Client.FirstToken = strings.Repeat("3", 64) }},
		{"wrong previous", func(s *OpenAICodexClientWindowTransition) { s.Client.PreviousToken = strings.Repeat("3", 64) }},
		{"wrong expected uuid", func(s *OpenAICodexClientWindowTransition) {
			s.Expected.ContextWindowID = testOpenAICodexContextWindowLater
			s.Expected.FirstContextWindowID = testOpenAICodexContextWindowLater
		}},
		{"wrong expected number", func(s *OpenAICodexClientWindowTransition) {
			s.Expected.Number = 7
			s.Expected.LastCompactDigest = strings.Repeat("e", 64)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, key, initial := newOpenAICodexClientWindowLocalTest(t)
			_, err := store.ResolveOpenAICodexClientWindow(context.Background(), key, initial, time.Hour)
			require.NoError(t, err)
			next := nextOpenAICodexClientWindowTest(initial)
			test.mutate(&next)
			before := *store.entries[key].clientWindow
			_, err = store.ResolveOpenAICodexClientWindow(context.Background(), key, next, 2*time.Hour)
			require.ErrorIs(t, err, ErrOpenAICodexClientWindowStale)
			require.Equal(t, initial.Expected, store.entries[key].snapshot)
			require.Equal(t, before, *store.entries[key].clientWindow)
		})
	}
}

func TestOpenAICodexClientWindowCompactInterleaving(t *testing.T) {
	for _, compactFirst := range []bool{true, false} {
		t.Run(map[bool]string{true: "compact first", false: "rollover first"}[compactFirst], func(t *testing.T) {
			store, key, initial := newOpenAICodexClientWindowLocalTest(t)
			_, err := store.ResolveOpenAICodexClientWindow(context.Background(), key, initial, time.Hour)
			require.NoError(t, err)
			next := nextOpenAICodexClientWindowTest(initial)
			if compactFirst {
				compact, err := store.CommitOpenAICodexWindow(context.Background(), key, initial.Expected, strings.Repeat("e", 64), testOpenAICodexContextWindowLater, time.Hour)
				require.NoError(t, err)
				retry, err := store.ResolveOpenAICodexClientWindow(context.Background(), key, initial, time.Hour)
				require.NoError(t, err)
				require.Equal(t, initial.Expected, retry.Snapshot)
				bound, err := store.ResolveOpenAICodexClientWindow(context.Background(), key, next, time.Hour)
				require.NoError(t, err)
				require.Equal(t, OpenAICodexClientWindowBound, bound.Status)
				require.Equal(t, compact.Snapshot, bound.Snapshot)
			} else {
				advanced, err := store.ResolveOpenAICodexClientWindow(context.Background(), key, next, time.Hour)
				require.NoError(t, err)
				compact, err := store.CommitOpenAICodexWindow(context.Background(), key, initial.Expected, strings.Repeat("e", 64), testOpenAICodexContextWindowLater, time.Hour)
				require.NoError(t, err)
				require.Equal(t, OpenAICodexWindowCommitStale, compact.Status)
				require.Equal(t, advanced.Snapshot, compact.Snapshot)
			}
			require.Equal(t, uint64(1), store.entries[key].snapshot.Number)
		})
	}
}

func TestOpenAICodexClientWindowLocalConcurrentWinner(t *testing.T) {
	store, key, initial := newOpenAICodexClientWindowLocalTest(t)
	_, err := store.ResolveOpenAICodexClientWindow(context.Background(), key, initial, time.Hour)
	require.NoError(t, err)
	const workers = 24
	results := make(chan OpenAICodexClientWindowResult, workers)
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := store.ResolveOpenAICodexClientWindow(context.Background(), key, nextOpenAICodexClientWindowTest(initial), time.Hour)
			results <- result
			errs <- err
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	advances := 0
	for result := range results {
		if result.Status == OpenAICodexClientWindowAdvanced {
			advances++
		}
		require.Equal(t, testOpenAICodexContextWindowNext, result.Snapshot.ContextWindowID)
	}
	require.Equal(t, 1, advances)
}

type unavailableOpenAICodexClientWindowStore struct{ OpenAICodexWindowStore }

func (s unavailableOpenAICodexClientWindowStore) ResolveOpenAICodexClientWindow(context.Context, string, OpenAICodexClientWindowTransition, time.Duration) (OpenAICodexClientWindowResult, error) {
	return OpenAICodexClientWindowResult{}, ErrOpenAICodexWindowStoreUnavailable
}

func TestOpenAICodexClientWindowRuntimeFailsClosed(t *testing.T) {
	for _, withInterface := range []bool{true, false} {
		store, key, initial := newOpenAICodexClientWindowLocalTest(t)
		var primary OpenAICodexWindowStore = &toggleOpenAICodexWindowStore{store: newOpenAICodexWindowLocalStore(32)}
		if withInterface {
			primary = unavailableOpenAICodexClientWindowStore{primary}
		}
		runtime := newOpenAICodexWindowRuntimeStoreWithLocal(primary, store)
		_, err := runtime.ResolveOpenAICodexClientWindow(context.Background(), key, initial, time.Hour)
		require.ErrorIs(t, err, ErrOpenAICodexWindowStoreUnavailable)
		require.Nil(t, store.entries[key].clientWindow)
		require.Equal(t, initial.Expected, store.entries[key].snapshot)
	}
}

func TestOpenAICodexClientWindowGatewayHelperInitialEmptyPrevious(t *testing.T) {
	svc := &OpenAIGatewayService{cfg: &config.Config{JWT: config.JWTConfig{Secret: "client-window-helper-secret"}}}
	threadID, err := newOpenAICodexContextWindowID()
	require.NoError(t, err)
	key, err := OpenAICodexWindowMappingKey(svc.cfg.JWT.Secret, "account:client-window-helper", 993213, threadID)
	require.NoError(t, err)
	initial, err := svc.ResolveOpenAICodexWindowSnapshot(context.Background(), key, threadID, testOpenAICodexContextWindowInitial)
	require.NoError(t, err)
	signal := OpenAICodexClientWindowSignal{Valid: true, First: "01989f44-7c00-7000-8000-000000000901", Current: "01989f44-7c00-7000-8000-000000000901"}
	bound, err := svc.ResolveOpenAICodexClientWindowSnapshot(context.Background(), key, initial, signal, initial.ContextWindowID)
	require.NoError(t, err)
	require.Equal(t, OpenAICodexClientWindowBound, bound.Status)
	require.Equal(t, initial, bound.Snapshot)
	signal.Number = 1
	signal.Previous = signal.Current
	signal.Current = "01989f44-7c00-7000-8000-000000000902"
	advanced, err := svc.ResolveOpenAICodexClientWindowSnapshot(context.Background(), key, initial, signal, testOpenAICodexContextWindowNext)
	require.NoError(t, err)
	require.Equal(t, OpenAICodexClientWindowAdvanced, advanced.Status)
	retry, err := svc.ResolveOpenAICodexClientWindowSnapshot(context.Background(), key, advanced.Snapshot, signal, testOpenAICodexContextWindowLater)
	require.NoError(t, err)
	require.Equal(t, advanced.Snapshot, retry.Snapshot)
	processOpenAICodexWindowLocalStore.mu.Lock()
	binding := *processOpenAICodexWindowLocalStore.entries[key].clientWindow
	processOpenAICodexWindowLocalStore.mu.Unlock()
	require.Len(t, binding.Client.CurrentToken, 64)
	require.NotContains(t, binding.Client.CurrentToken, signal.Current)
	require.NotEqual(t, signal.Current, advanced.Snapshot.ContextWindowID)
}

type malformedOpenAICodexClientWindowStore struct {
	OpenAICodexWindowStore
	result OpenAICodexClientWindowResult
}

func (s malformedOpenAICodexClientWindowStore) ResolveOpenAICodexClientWindow(context.Context, string, OpenAICodexClientWindowTransition, time.Duration) (OpenAICodexClientWindowResult, error) {
	return s.result, nil
}

func TestOpenAICodexClientWindowRuntimeRejectsMalformedPrimaryResult(t *testing.T) {
	for _, status := range []OpenAICodexClientWindowStatus{OpenAICodexClientWindowBound, OpenAICodexClientWindowUnchanged, OpenAICodexClientWindowAdvanced} {
		t.Run(string(status), func(t *testing.T) {
			store, key, initial := newOpenAICodexClientWindowLocalTest(t)
			foreign := initial.Expected
			foreign.Number = 9
			foreign.ContextWindowID = testOpenAICodexContextWindowLater
			foreign.LastCompactDigest = strings.Repeat("f", 64)
			primary := malformedOpenAICodexClientWindowStore{OpenAICodexWindowStore: newOpenAICodexWindowLocalStore(32), result: OpenAICodexClientWindowResult{Snapshot: foreign, Status: status}}
			runtime := newOpenAICodexWindowRuntimeStoreWithLocal(primary, store)
			_, err := runtime.ResolveOpenAICodexClientWindow(context.Background(), key, nextOpenAICodexClientWindowTest(initial), time.Hour)
			require.ErrorIs(t, err, ErrOpenAICodexWindowStoredInvalid)
			require.Equal(t, initial.Expected, store.entries[key].snapshot)
		})
	}
	store, key, initial := newOpenAICodexClientWindowLocalTest(t)
	next := nextOpenAICodexClientWindowTest(initial)
	validNext := initial.Expected
	validNext.Number++
	validNext.PreviousContextWindowID = initial.Expected.ContextWindowID
	validNext.ContextWindowID = next.ProposedContextWindowID
	validNext.LastCompactDigest = next.RolloverDigest
	for _, mutate := range []func(*OpenAICodexWindowSnapshot){
		func(s *OpenAICodexWindowSnapshot) { s.ContextWindowID = testOpenAICodexContextWindowLater },
		func(s *OpenAICodexWindowSnapshot) { s.LastCompactDigest = strings.Repeat("d", 64) },
		func(s *OpenAICodexWindowSnapshot) { s.FirstContextWindowID = testOpenAICodexContextWindowLater },
		func(s *OpenAICodexWindowSnapshot) { s.PreviousContextWindowID = testOpenAICodexContextWindowLater },
	} {
		invalid := validNext
		mutate(&invalid)
		primary := malformedOpenAICodexClientWindowStore{OpenAICodexWindowStore: newOpenAICodexWindowLocalStore(32), result: OpenAICodexClientWindowResult{Snapshot: invalid, Status: OpenAICodexClientWindowAdvanced}}
		runtime := newOpenAICodexWindowRuntimeStoreWithLocal(primary, store)
		_, err := runtime.ResolveOpenAICodexClientWindow(context.Background(), key, next, time.Hour)
		require.ErrorIs(t, err, ErrOpenAICodexWindowStoredInvalid)
	}
}
