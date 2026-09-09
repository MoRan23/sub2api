package service

import (
	"context"
	"fmt"
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
	require.ErrorIs(t, err, ErrOpenAICodexClientWindowStale, "a rewind must capture the current server CAS")
}

func TestOpenAICodexClientWindowJumpAndRollbackUseFreshServerGenerations(t *testing.T) {
	store, key, initial := newOpenAICodexClientWindowLocalTest(t)
	_, err := store.ResolveOpenAICodexClientWindow(context.Background(), key, initial, time.Hour)
	require.NoError(t, err)
	jump := initial
	jump.Client.Number = 2
	jump.Client.PreviousToken = strings.Repeat("2", 64)
	jump.Client.CurrentToken = strings.Repeat("3", 64)
	jump.ProposedContextWindowID = testOpenAICodexContextWindowNext
	jump.RolloverDigest = strings.Repeat("c", 64)
	jumped, err := store.ResolveOpenAICodexClientWindow(context.Background(), key, jump, time.Hour)
	require.NoError(t, err)
	require.Equal(t, OpenAICodexClientWindowAdvanced, jumped.Status)
	require.Equal(t, uint64(1), jumped.Snapshot.Number, "unobserved client windows create one fresh observed server generation")
	require.Equal(t, jump.ProposedContextWindowID, jumped.Snapshot.ContextWindowID)
	require.Equal(t, initial.Expected.ContextWindowID, jumped.Snapshot.PreviousContextWindowID)
	next := jump
	next.Expected = jumped.Snapshot
	next.Client.Number = 3
	next.Client.PreviousToken = jump.Client.CurrentToken
	next.Client.CurrentToken = strings.Repeat("4", 64)
	next.ProposedContextWindowID = testOpenAICodexContextWindowLater
	next.RolloverDigest = strings.Repeat("d", 64)
	advanced, err := store.ResolveOpenAICodexClientWindow(context.Background(), key, next, time.Hour)
	require.NoError(t, err)
	require.Equal(t, OpenAICodexClientWindowAdvanced, advanced.Status)
	require.Equal(t, uint64(2), advanced.Snapshot.Number)
	require.Equal(t, jumped.Snapshot.ContextWindowID, advanced.Snapshot.PreviousContextWindowID)
	_, err = store.ResolveOpenAICodexClientWindow(context.Background(), key, initial, time.Hour)
	require.ErrorIs(t, err, ErrOpenAICodexClientWindowStale)
	rewind := initial
	rewind.Expected = advanced.Snapshot
	rewind.ProposedContextWindowID = "01989f44-7c00-7000-8000-000000000801"
	rewind.RolloverDigest = strings.Repeat("e", 64)
	rollback, err := store.ResolveOpenAICodexClientWindow(context.Background(), key, rewind, time.Hour)
	require.NoError(t, err)
	require.Equal(t, OpenAICodexClientWindowAdvanced, rollback.Status)
	require.Equal(t, uint64(3), rollback.Snapshot.Number)
	require.Equal(t, rewind.ProposedContextWindowID, rollback.Snapshot.ContextWindowID)
	require.Equal(t, advanced.Snapshot.ContextWindowID, rollback.Snapshot.PreviousContextWindowID)
	resume := nextOpenAICodexClientWindowTest(initial)
	resume.Expected = rollback.Snapshot
	resume.Client.Number = 1
	resume.Client.CurrentToken = strings.Repeat("2", 64)
	resume.ProposedContextWindowID = "01989f44-7c00-7000-8000-000000000802"
	resume.RolloverDigest = strings.Repeat("f", 64)
	resumed, err := store.ResolveOpenAICodexClientWindow(context.Background(), key, resume, time.Hour)
	require.NoError(t, err)
	require.Equal(t, OpenAICodexClientWindowAdvanced, resumed.Status)
	require.Equal(t, uint64(4), resumed.Snapshot.Number)
	require.Equal(t, rollback.Snapshot.ContextWindowID, resumed.Snapshot.PreviousContextWindowID)
	require.Equal(t, initial.Expected.ContextWindowID, resumed.Snapshot.FirstContextWindowID)
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
		{"first changed", func(s *OpenAICodexClientWindowTransition) { s.Client.FirstToken = strings.Repeat("3", 64) }},
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

func TestOpenAICodexClientWindowRejectsReusedUUIDWithChangedMetadata(t *testing.T) {
	store, key, initial := newOpenAICodexClientWindowLocalTest(t)
	_, err := store.ResolveOpenAICodexClientWindow(context.Background(), key, initial, time.Hour)
	require.NoError(t, err)
	next := nextOpenAICodexClientWindowTest(initial)
	advanced, err := store.ResolveOpenAICodexClientWindow(context.Background(), key, next, time.Hour)
	require.NoError(t, err)
	for _, field := range []string{"number", "previous"} {
		t.Run(field, func(t *testing.T) {
			conflict := next
			conflict.Expected = advanced.Snapshot
			conflict.ProposedContextWindowID = testOpenAICodexContextWindowLater
			if field == "number" {
				conflict.Client.Number++
			} else {
				conflict.Client.PreviousToken = strings.Repeat("3", 64)
			}
			_, err := store.ResolveOpenAICodexClientWindow(context.Background(), key, conflict, time.Hour)
			require.ErrorIs(t, err, ErrOpenAICodexClientWindowStale)
			require.Equal(t, advanced.Snapshot, store.entries[key].snapshot)
			require.Equal(t, next.Client, store.entries[key].clientWindow.Client)
		})
	}
}

func TestOpenAICodexClientWindowRecoveryConcurrentSingleWinner(t *testing.T) {
	for _, mode := range []string{"gap", "rollback", "same_ordinal", "adjacent_after_unobserved_rollback"} {
		t.Run(mode, func(t *testing.T) {
			store, key, initial := newOpenAICodexClientWindowLocalTest(t)
			initial.Client.Number = 5
			initial.Client.PreviousToken = strings.Repeat("5", 64)
			initial.Client.CurrentToken = strings.Repeat("6", 64)
			_, err := store.ResolveOpenAICodexClientWindow(context.Background(), key, initial, time.Hour)
			require.NoError(t, err)
			const workers = 12
			type outcome struct {
				result OpenAICodexClientWindowResult
				err    error
			}
			outcomes := make(chan outcome, workers)
			start := make(chan struct{})
			var wg sync.WaitGroup
			for i := range workers {
				wg.Add(1)
				go func(index int) {
					defer wg.Done()
					tr := initial
					tr.Client.PreviousToken = strings.Repeat("4", 64)
					switch mode {
					case "gap":
						tr.Client.Number = 8
					case "rollback":
						tr.Client.Number = 2
					case "adjacent_after_unobserved_rollback":
						tr.Client.Number = 6
					}
					tr.Client.CurrentToken = fmt.Sprintf("%064x", index+0x100)
					tr.ProposedContextWindowID = fmt.Sprintf("01989f44-7c00-7000-8000-%012x", index+0x700)
					tr.RolloverDigest = fmt.Sprintf("%064x", index+0x200)
					<-start
					result, err := store.ResolveOpenAICodexClientWindow(context.Background(), key, tr, time.Hour)
					outcomes <- outcome{result, err}
				}(i)
			}
			close(start)
			wg.Wait()
			close(outcomes)
			winner := store.entries[key].snapshot
			require.Equal(t, uint64(1), winner.Number)
			require.NotEqual(t, initial.Expected.ContextWindowID, winner.ContextWindowID)
			require.Equal(t, initial.Expected.ContextWindowID, winner.PreviousContextWindowID)
			require.Equal(t, winner, store.entries[key].clientWindow.Server)
			advanced, stale := 0, 0
			for outcome := range outcomes {
				require.Equal(t, winner, outcome.result.Snapshot)
				if outcome.result.Status == OpenAICodexClientWindowAdvanced {
					require.NoError(t, outcome.err)
					advanced++
				} else {
					require.Equal(t, OpenAICodexClientWindowStale, outcome.result.Status)
					require.ErrorIs(t, outcome.err, ErrOpenAICodexClientWindowStale)
					stale++
				}
			}
			require.Equal(t, 1, advanced)
			require.Equal(t, workers-1, stale)
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

func TestOpenAICodexClientWindowRestoresBindingAfterMultipleCompacts(t *testing.T) {
	store, key, initial := newOpenAICodexClientWindowLocalTest(t)
	_, err := store.ResolveOpenAICodexClientWindow(context.Background(), key, initial, time.Hour)
	require.NoError(t, err)
	first, err := store.CommitOpenAICodexWindow(context.Background(), key, initial.Expected, strings.Repeat("c", 64), testOpenAICodexContextWindowNext, time.Hour)
	require.NoError(t, err)
	oneAhead := initial
	oneAhead.Expected = first.Snapshot
	unchanged, err := store.ResolveOpenAICodexClientWindow(context.Background(), key, oneAhead, time.Hour)
	require.NoError(t, err)
	require.Equal(t, OpenAICodexClientWindowUnchanged, unchanged.Status)
	require.Equal(t, initial.Expected, unchanged.Snapshot, "one compact still preserves the original retry mapping")
	second, err := store.CommitOpenAICodexWindow(context.Background(), key, first.Snapshot, strings.Repeat("d", 64), testOpenAICodexContextWindowLater, time.Hour)
	require.NoError(t, err)
	require.Equal(t, initial.Expected, store.entries[key].clientWindow.Server)
	otherBranch := second.Snapshot
	otherBranch.ContextWindowID = "01989f44-7c00-7000-8000-000000000850"
	for _, expected := range []OpenAICodexWindowSnapshot{initial.Expected, first.Snapshot, otherBranch} {
		old := initial
		old.Expected = expected
		expiresAt := store.entries[key].expiresAt
		result, err := store.ResolveOpenAICodexClientWindow(context.Background(), key, old, 2*time.Hour)
		require.ErrorIs(t, err, ErrOpenAICodexClientWindowStale)
		require.Equal(t, second.Snapshot, result.Snapshot)
		require.Equal(t, second.Snapshot, store.entries[key].snapshot)
		require.Equal(t, initial.Expected, store.entries[key].clientWindow.Server)
		require.Equal(t, expiresAt, store.entries[key].expiresAt, "stale attempts cannot refresh the binding")
	}
	restore := initial
	restore.Expected = second.Snapshot
	restore.ProposedContextWindowID = "01989f44-7c00-7000-8000-000000000851"
	restore.RolloverDigest = strings.Repeat("e", 64)
	recovered, err := store.ResolveOpenAICodexClientWindow(context.Background(), key, restore, time.Hour)
	require.NoError(t, err)
	require.Equal(t, OpenAICodexClientWindowAdvanced, recovered.Status)
	require.Equal(t, uint64(3), recovered.Snapshot.Number)
	require.Equal(t, restore.ProposedContextWindowID, recovered.Snapshot.ContextWindowID)
	require.Equal(t, second.Snapshot.ContextWindowID, recovered.Snapshot.PreviousContextWindowID)
	require.Equal(t, initial.Expected.ContextWindowID, recovered.Snapshot.FirstContextWindowID)
	require.Equal(t, restore.RolloverDigest, recovered.Snapshot.LastCompactDigest)
	require.Equal(t, recovered.Snapshot, store.entries[key].snapshot)
	require.Equal(t, recovered.Snapshot, store.entries[key].clientWindow.Server)
	require.Equal(t, initial.Client, store.entries[key].clientWindow.Client)
	for _, expected := range []OpenAICodexWindowSnapshot{restore.Expected, recovered.Snapshot} {
		repeated := restore
		repeated.Expected = expected
		repeated.ProposedContextWindowID = "01989f44-7c00-7000-8000-000000000852"
		result, err := store.ResolveOpenAICodexClientWindow(context.Background(), key, repeated, time.Hour)
		require.NoError(t, err)
		require.Equal(t, OpenAICodexClientWindowUnchanged, result.Status)
		require.Equal(t, recovered.Snapshot, result.Snapshot)
	}
	_, err = store.ResolveOpenAICodexClientWindow(context.Background(), key, initial, time.Hour)
	require.ErrorIs(t, err, ErrOpenAICodexClientWindowStale, "a pre-recovery attempt cannot adopt the newly restored mapping")
	next := nextOpenAICodexClientWindowTest(restore)
	next.Expected = recovered.Snapshot
	next.ProposedContextWindowID = "01989f44-7c00-7000-8000-000000000853"
	advanced, err := store.ResolveOpenAICodexClientWindow(context.Background(), key, next, time.Hour)
	require.NoError(t, err)
	require.Equal(t, OpenAICodexClientWindowAdvanced, advanced.Status)
	require.Equal(t, uint64(4), advanced.Snapshot.Number)
}

func TestOpenAICodexClientWindowRestoreAfterCompactsConcurrentWinner(t *testing.T) {
	store, key, initial := newOpenAICodexClientWindowLocalTest(t)
	_, err := store.ResolveOpenAICodexClientWindow(context.Background(), key, initial, time.Hour)
	require.NoError(t, err)
	first, err := store.CommitOpenAICodexWindow(context.Background(), key, initial.Expected, strings.Repeat("c", 64), testOpenAICodexContextWindowNext, time.Hour)
	require.NoError(t, err)
	second, err := store.CommitOpenAICodexWindow(context.Background(), key, first.Snapshot, strings.Repeat("d", 64), testOpenAICodexContextWindowLater, time.Hour)
	require.NoError(t, err)
	const workers = 12
	type outcome struct {
		result OpenAICodexClientWindowResult
		err    error
	}
	outcomes := make(chan outcome, workers)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range workers {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			restore := initial
			restore.Expected = second.Snapshot
			restore.ProposedContextWindowID = fmt.Sprintf("01989f44-7c00-7000-8000-%012x", index+0x860)
			restore.RolloverDigest = strings.Repeat("e", 64)
			<-start
			result, err := store.ResolveOpenAICodexClientWindow(context.Background(), key, restore, time.Hour)
			outcomes <- outcome{result, err}
		}(i)
	}
	close(start)
	wg.Wait()
	close(outcomes)
	winner := store.entries[key].snapshot
	require.Equal(t, uint64(3), winner.Number)
	require.Equal(t, second.Snapshot.ContextWindowID, winner.PreviousContextWindowID)
	require.Equal(t, winner, store.entries[key].clientWindow.Server)
	advanced := 0
	for outcome := range outcomes {
		require.NoError(t, outcome.err)
		require.Equal(t, winner, outcome.result.Snapshot)
		if outcome.result.Status == OpenAICodexClientWindowAdvanced {
			advanced++
		} else {
			require.Equal(t, OpenAICodexClientWindowUnchanged, outcome.result.Status)
		}
	}
	require.Equal(t, 1, advanced)
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
