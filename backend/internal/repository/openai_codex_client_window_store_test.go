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
	"github.com/alicebob/miniredis/v2"
	"github.com/stretchr/testify/require"
)

type codexClientWindowRedisFixture struct {
	mr        *miniredis.Miniredis
	main      service.OpenAICodexWindowStore
	client    service.OpenAICodexClientWindowStore
	key       string
	mainKey   string
	clientKey string
	initial   service.OpenAICodexWindowSnapshot
}

func newCodexClientWindowRedisFixture(t *testing.T) codexClientWindowRedisFixture {
	t.Helper()
	mr, _, main := newOpenAICodexWindowRedisTest(t)
	client, ok := main.(service.OpenAICodexClientWindowStore)
	require.True(t, ok)
	key := strings.Repeat("b", 64)
	mainKey, err := OpenAICodexWindowRedisKey(key)
	require.NoError(t, err)
	initial, err := main.ResolveOpenAICodexWindow(context.Background(), key, service.OpenAICodexWindowSnapshot{
		ThreadID: repositoryOpenAICodexWindowThread, ContextWindowID: repositoryOpenAICodexContextWindowInitial,
	}, time.Minute)
	require.NoError(t, err)
	return codexClientWindowRedisFixture{mr: mr, main: main, client: client, key: key, mainKey: mainKey, clientKey: OpenAICodexClientWindowKeyPrefix + key, initial: initial}
}

func codexClientWindowToken(index int) string { return fmt.Sprintf("%064x", index) }

func codexClientWindowInitialIdentity() service.OpenAICodexClientWindowIdentity {
	return service.OpenAICodexClientWindowIdentity{FirstToken: codexClientWindowToken(1), CurrentToken: codexClientWindowToken(1)}
}

func codexClientWindowNextIdentity() service.OpenAICodexClientWindowIdentity {
	return service.OpenAICodexClientWindowIdentity{Number: 1, FirstToken: codexClientWindowToken(1), PreviousToken: codexClientWindowToken(1), CurrentToken: codexClientWindowToken(2)}
}

func codexClientWindowTransition(expected service.OpenAICodexWindowSnapshot, client service.OpenAICodexClientWindowIdentity) service.OpenAICodexClientWindowTransition {
	return service.OpenAICodexClientWindowTransition{Expected: expected, Client: client, ProposedContextWindowID: repositoryOpenAICodexContextWindowNext, RolloverDigest: strings.Repeat("c", 64)}
}

func (f codexClientWindowRedisFixture) bind(t *testing.T) {
	t.Helper()
	result, err := f.client.ResolveOpenAICodexClientWindow(context.Background(), f.key, codexClientWindowTransition(f.initial, codexClientWindowInitialIdentity()), time.Minute)
	require.NoError(t, err)
	require.Equal(t, service.OpenAICodexClientWindowBound, result.Status)
	require.Equal(t, f.initial, result.Snapshot)
}

func (f codexClientWindowRedisFixture) stored(t *testing.T) (service.OpenAICodexWindowSnapshot, service.OpenAICodexClientWindowBinding) {
	t.Helper()
	mainRaw, err := f.mr.Get(f.mainKey)
	require.NoError(t, err)
	main, err := decodeStrictOpenAICodexWindowSnapshot([]byte(mainRaw))
	require.NoError(t, err)
	clientRaw, err := f.mr.Get(f.clientKey)
	require.NoError(t, err)
	binding, err := decodeStrictOpenAICodexClientWindowBinding([]byte(clientRaw))
	require.NoError(t, err)
	return main, binding
}

func TestOpenAICodexClientWindowRedisBindAdvanceAndRepeat(t *testing.T) {
	f := newCodexClientWindowRedisFixture(t)
	f.bind(t)
	next := codexClientWindowTransition(f.initial, codexClientWindowNextIdentity())
	advanced, err := f.client.ResolveOpenAICodexClientWindow(context.Background(), f.key, next, 2*time.Minute)
	require.NoError(t, err)
	require.Equal(t, service.OpenAICodexClientWindowAdvanced, advanced.Status)
	require.Equal(t, uint64(1), advanced.Snapshot.Number)
	require.Equal(t, next.ProposedContextWindowID, advanced.Snapshot.ContextWindowID)
	require.Equal(t, f.initial.ContextWindowID, advanced.Snapshot.FirstContextWindowID)
	require.Equal(t, f.initial.ContextWindowID, advanced.Snapshot.PreviousContextWindowID)
	require.Equal(t, next.RolloverDigest, advanced.Snapshot.LastCompactDigest)
	main, binding := f.stored(t)
	require.Equal(t, advanced.Snapshot, main)
	require.Equal(t, main, binding.Server)
	require.Equal(t, next.Client, binding.Client)
	require.Equal(t, 2*time.Minute, f.mr.TTL(f.mainKey))
	require.Equal(t, 2*time.Minute, f.mr.TTL(f.clientKey))

	next.ProposedContextWindowID = repositoryOpenAICodexContextWindowLater
	next.RolloverDigest = strings.Repeat("d", 64)
	repeated, err := f.client.ResolveOpenAICodexClientWindow(context.Background(), f.key, next, 3*time.Minute)
	require.NoError(t, err)
	require.Equal(t, service.OpenAICodexClientWindowUnchanged, repeated.Status)
	require.Equal(t, advanced.Snapshot, repeated.Snapshot)
	main, binding = f.stored(t)
	require.Equal(t, advanced.Snapshot, main)
	require.Equal(t, main, binding.Server)

	second := service.OpenAICodexClientWindowIdentity{Number: 2, FirstToken: next.Client.FirstToken, PreviousToken: next.Client.CurrentToken, CurrentToken: codexClientWindowToken(3)}
	transition := codexClientWindowTransition(main, second)
	transition.ProposedContextWindowID = repositoryOpenAICodexContextWindowLater
	transition.RolloverDigest = strings.Repeat("e", 64)
	result, err := f.client.ResolveOpenAICodexClientWindow(context.Background(), f.key, transition, time.Minute)
	require.NoError(t, err)
	require.Equal(t, uint64(2), result.Snapshot.Number)
	require.Equal(t, f.initial.ContextWindowID, result.Snapshot.FirstContextWindowID)
	require.Equal(t, main.ContextWindowID, result.Snapshot.PreviousContextWindowID)
}

func TestOpenAICodexClientWindowRedisResumeKeepsClientServerNumberOffset(t *testing.T) {
	f := newCodexClientWindowRedisFixture(t)
	client := service.OpenAICodexClientWindowIdentity{Number: 7, FirstToken: codexClientWindowToken(1), PreviousToken: codexClientWindowToken(7), CurrentToken: codexClientWindowToken(8)}
	bound, err := f.client.ResolveOpenAICodexClientWindow(context.Background(), f.key, codexClientWindowTransition(f.initial, client), time.Minute)
	require.NoError(t, err)
	require.Equal(t, service.OpenAICodexClientWindowBound, bound.Status)
	require.Zero(t, bound.Snapshot.Number, "initial sidecar binding cannot invent seven server transitions")
	client.Number++
	client.PreviousToken = client.CurrentToken
	client.CurrentToken = codexClientWindowToken(9)
	advanced, err := f.client.ResolveOpenAICodexClientWindow(context.Background(), f.key, codexClientWindowTransition(bound.Snapshot, client), time.Minute)
	require.NoError(t, err)
	require.Equal(t, service.OpenAICodexClientWindowAdvanced, advanced.Status)
	require.Equal(t, uint64(1), advanced.Snapshot.Number)
	main, binding := f.stored(t)
	require.Equal(t, uint64(8), binding.Client.Number)
	require.Equal(t, main, binding.Server)

	old := codexClientWindowTransition(f.initial, codexClientWindowInitialIdentity())
	_, err = f.client.ResolveOpenAICodexClientWindow(context.Background(), f.key, old, time.Minute)
	require.ErrorIs(t, err, service.ErrOpenAICodexClientWindowStale)
}

func TestOpenAICodexClientWindowRedisRejectsStaleWithoutMutation(t *testing.T) {
	for name, mutate := range map[string]func(*service.OpenAICodexClientWindowTransition){
		"same_number_other_uuid": func(tr *service.OpenAICodexClientWindowTransition) {
			tr.Client.Number = 0
			tr.Client.FirstToken = tr.Client.CurrentToken
			tr.Client.PreviousToken = ""
		},
		"skipped_number": func(tr *service.OpenAICodexClientWindowTransition) { tr.Client.Number = 2 },
		"changed_first":  func(tr *service.OpenAICodexClientWindowTransition) { tr.Client.FirstToken = codexClientWindowToken(4) },
		"wrong_previous": func(tr *service.OpenAICodexClientWindowTransition) {
			tr.Client.PreviousToken = codexClientWindowToken(4)
		},
		"wrong_expected_uuid": func(tr *service.OpenAICodexClientWindowTransition) {
			tr.Expected.ContextWindowID = repositoryOpenAICodexContextWindowLater
			tr.Expected.FirstContextWindowID = tr.Expected.ContextWindowID
		},
		"wrong_expected_number": func(tr *service.OpenAICodexClientWindowTransition) {
			tr.Expected.Number = 2
			tr.Expected.LastCompactDigest = strings.Repeat("e", 64)
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newCodexClientWindowRedisFixture(t)
			f.bind(t)
			mainBefore, err := f.mr.Get(f.mainKey)
			require.NoError(t, err)
			clientBefore, err := f.mr.Get(f.clientKey)
			require.NoError(t, err)
			tr := codexClientWindowTransition(f.initial, codexClientWindowNextIdentity())
			mutate(&tr)
			result, err := f.client.ResolveOpenAICodexClientWindow(context.Background(), f.key, tr, time.Hour)
			require.ErrorIs(t, err, service.ErrOpenAICodexClientWindowStale)
			require.Equal(t, service.OpenAICodexClientWindowStale, result.Status)
			require.Equal(t, f.initial, result.Snapshot)
			mainAfter, err := f.mr.Get(f.mainKey)
			require.NoError(t, err)
			clientAfter, err := f.mr.Get(f.clientKey)
			require.NoError(t, err)
			require.Equal(t, mainBefore, mainAfter)
			require.Equal(t, clientBefore, clientAfter)
			require.Equal(t, time.Minute, f.mr.TTL(f.mainKey))
			require.Equal(t, time.Minute, f.mr.TTL(f.clientKey))
		})
	}
}

func TestOpenAICodexClientWindowRedisConcurrentBranches(t *testing.T) {
	for _, sameBranch := range []bool{true, false} {
		t.Run(fmt.Sprintf("same_branch_%t", sameBranch), func(t *testing.T) {
			f := newCodexClientWindowRedisFixture(t)
			f.bind(t)
			const workers = 12
			type outcome struct {
				result service.OpenAICodexClientWindowResult
				err    error
			}
			outcomes := make(chan outcome, workers)
			start := make(chan struct{})
			var wg sync.WaitGroup
			for i := 0; i < workers; i++ {
				wg.Add(1)
				go func(index int) {
					defer wg.Done()
					tr := codexClientWindowTransition(f.initial, codexClientWindowNextIdentity())
					tr.ProposedContextWindowID = fmt.Sprintf("01989f44-7c00-7000-8000-%012x", index+0x700)
					if !sameBranch {
						tr.Client.CurrentToken = codexClientWindowToken(index + 0x100)
						tr.RolloverDigest = codexClientWindowToken(index + 0x200)
					}
					<-start
					result, err := f.client.ResolveOpenAICodexClientWindow(context.Background(), f.key, tr, time.Minute)
					outcomes <- outcome{result, err}
				}(i)
			}
			close(start)
			wg.Wait()
			close(outcomes)
			main, binding := f.stored(t)
			require.Equal(t, uint64(1), main.Number)
			require.Equal(t, main, binding.Server)
			advanced, repeated, stale := 0, 0, 0
			for outcome := range outcomes {
				require.Equal(t, main, outcome.result.Snapshot)
				switch outcome.result.Status {
				case service.OpenAICodexClientWindowAdvanced:
					require.NoError(t, outcome.err)
					advanced++
				case service.OpenAICodexClientWindowUnchanged:
					require.NoError(t, outcome.err)
					repeated++
				case service.OpenAICodexClientWindowStale:
					require.ErrorIs(t, outcome.err, service.ErrOpenAICodexClientWindowStale)
					stale++
				default:
					t.Fatalf("unexpected status %q: %v", outcome.result.Status, outcome.err)
				}
			}
			require.Equal(t, 1, advanced)
			if sameBranch {
				require.Equal(t, workers-1, repeated)
				require.Zero(t, stale)
			} else {
				require.Equal(t, workers-1, stale)
				require.Zero(t, repeated)
			}
		})
	}
}

func TestOpenAICodexClientWindowRedisCompactOverlap(t *testing.T) {
	for _, order := range []string{"compact_first", "rollover_first", "concurrent"} {
		t.Run(order, func(t *testing.T) {
			f := newCodexClientWindowRedisFixture(t)
			f.bind(t)
			var compact service.OpenAICodexWindowCommitResult
			var rollover service.OpenAICodexClientWindowResult
			var compactErr, rolloverErr error
			doCompact := func() {
				compact, compactErr = f.main.CommitOpenAICodexWindow(context.Background(), f.key, f.initial, strings.Repeat("f", 64), repositoryOpenAICodexContextWindowLater, time.Minute)
			}
			doRollover := func() {
				rollover, rolloverErr = f.client.ResolveOpenAICodexClientWindow(context.Background(), f.key, codexClientWindowTransition(f.initial, codexClientWindowNextIdentity()), time.Minute)
			}
			switch order {
			case "compact_first":
				doCompact()
				old, err := f.client.ResolveOpenAICodexClientWindow(context.Background(), f.key, codexClientWindowTransition(compact.Snapshot, codexClientWindowInitialIdentity()), time.Minute)
				require.NoError(t, err)
				require.Equal(t, service.OpenAICodexClientWindowUnchanged, old.Status)
				require.Equal(t, f.initial, old.Snapshot, "retry preserves its prior client/server binding")
				doRollover()
			case "rollover_first":
				doRollover()
				doCompact()
			case "concurrent":
				var wg sync.WaitGroup
				start := make(chan struct{})
				wg.Add(2)
				go func() { defer wg.Done(); <-start; doCompact() }()
				go func() { defer wg.Done(); <-start; doRollover() }()
				close(start)
				wg.Wait()
			}
			require.NoError(t, compactErr)
			require.NoError(t, rolloverErr)
			main, binding := f.stored(t)
			require.Equal(t, uint64(1), main.Number)
			require.Equal(t, main, binding.Server)
			require.Equal(t, codexClientWindowNextIdentity(), binding.Client)
			require.Equal(t, main, rollover.Snapshot)
			require.Equal(t, main, compact.Snapshot)
			if compact.Status == service.OpenAICodexWindowCommitAdvanced {
				require.Equal(t, service.OpenAICodexClientWindowBound, rollover.Status)
			} else {
				require.Equal(t, service.OpenAICodexWindowCommitStale, compact.Status)
				require.Equal(t, service.OpenAICodexClientWindowAdvanced, rollover.Status)
			}
		})
	}
}

func TestOpenAICodexClientWindowRedisSidecarTTLTracksMain(t *testing.T) {
	for _, operation := range []string{"resolve", "compact"} {
		t.Run(operation, func(t *testing.T) {
			f := newCodexClientWindowRedisFixture(t)
			f.bind(t)
			f.mr.FastForward(50 * time.Second)
			expectedStatus := service.OpenAICodexClientWindowAdvanced
			if operation == "resolve" {
				_, err := f.main.ResolveOpenAICodexWindow(context.Background(), f.key, f.initial, time.Minute)
				require.NoError(t, err)
			} else {
				_, err := f.main.CommitOpenAICodexWindow(context.Background(), f.key, f.initial, strings.Repeat("f", 64), repositoryOpenAICodexContextWindowLater, time.Minute)
				require.NoError(t, err)
				expectedStatus = service.OpenAICodexClientWindowBound
			}
			require.Equal(t, time.Minute, f.mr.TTL(f.clientKey))
			f.mr.FastForward(20 * time.Second)
			require.True(t, f.mr.Exists(f.clientKey))
			result, err := f.client.ResolveOpenAICodexClientWindow(context.Background(), f.key, codexClientWindowTransition(f.initial, codexClientWindowNextIdentity()), time.Minute)
			require.NoError(t, err)
			require.Equal(t, expectedStatus, result.Status)
			require.Equal(t, uint64(1), result.Snapshot.Number)
		})
	}
}

func TestOpenAICodexClientWindowRedisMalformedSidecarFailsClosed(t *testing.T) {
	for _, target := range []string{"client", "server"} {
		fields := []string{"number", "first_token", "current_token", "previous_token"}
		if target == "server" {
			fields = []string{"thread_id", "window_number", "context_window_id", "first_context_window_id", "previous_context_window_id", "last_compact_digest"}
		}
		for _, field := range fields {
			for _, missing := range []bool{true, false} {
				t.Run(fmt.Sprintf("%s_%s_missing_%t", target, field, missing), func(t *testing.T) {
					f := newCodexClientWindowRedisFixture(t)
					f.bind(t)
					raw, err := f.mr.Get(f.clientKey)
					require.NoError(t, err)
					var document map[string]any
					require.NoError(t, json.Unmarshal([]byte(raw), &document))
					part := document[target].(map[string]any)
					if missing {
						delete(part, field)
					} else {
						part[field] = nil
					}
					corrupt, err := json.Marshal(document)
					require.NoError(t, err)
					f.mr.Set(f.clientKey, string(corrupt))
					f.mr.SetTTL(f.clientKey, 37*time.Second)
					mainBefore, err := f.mr.Get(f.mainKey)
					require.NoError(t, err)
					_, err = f.client.ResolveOpenAICodexClientWindow(context.Background(), f.key, codexClientWindowTransition(f.initial, codexClientWindowNextIdentity()), time.Hour)
					require.ErrorIs(t, err, service.ErrOpenAICodexWindowStoredInvalid)
					require.NotErrorIs(t, err, service.ErrOpenAICodexWindowStoreUnavailable)
					after, err := f.mr.Get(f.clientKey)
					require.NoError(t, err)
					require.Equal(t, string(corrupt), after)
					mainAfter, err := f.mr.Get(f.mainKey)
					require.NoError(t, err)
					require.Equal(t, mainBefore, mainAfter)
					require.Equal(t, 37*time.Second, f.mr.TTL(f.clientKey))
					require.Equal(t, time.Minute, f.mr.TTL(f.mainKey))
				})
			}
		}
	}
}

func TestOpenAICodexClientWindowRedisMissingMainAndRawTokensFailClosed(t *testing.T) {
	t.Run("missing_main", func(t *testing.T) {
		f := newCodexClientWindowRedisFixture(t)
		f.bind(t)
		before, err := f.mr.Get(f.clientKey)
		require.NoError(t, err)
		f.mr.Del(f.mainKey)
		_, err = f.client.ResolveOpenAICodexClientWindow(context.Background(), f.key, codexClientWindowTransition(f.initial, codexClientWindowNextIdentity()), time.Hour)
		require.ErrorIs(t, err, service.ErrOpenAICodexWindowStoredInvalid)
		require.False(t, f.mr.Exists(f.mainKey))
		after, err := f.mr.Get(f.clientKey)
		require.NoError(t, err)
		require.Equal(t, before, after)
		require.Equal(t, time.Minute, f.mr.TTL(f.clientKey))
	})
	t.Run("raw_uuid_tokens", func(t *testing.T) {
		f := newCodexClientWindowRedisFixture(t)
		tr := codexClientWindowTransition(f.initial, codexClientWindowInitialIdentity())
		tr.Client.FirstToken = "01989f44-7c00-7000-8000-000000000991"
		tr.Client.CurrentToken = tr.Client.FirstToken
		_, err := f.client.ResolveOpenAICodexClientWindow(context.Background(), f.key, tr, time.Hour)
		require.Error(t, err)
		require.False(t, f.mr.Exists(f.clientKey))
		raw, err := f.mr.Get(f.mainKey)
		require.NoError(t, err)
		require.NotContains(t, raw, tr.Client.CurrentToken)
		f.bind(t)
		_, binding := f.stored(t)
		require.Len(t, binding.Client.FirstToken, 64)
		require.Len(t, binding.Client.CurrentToken, 64)
	})
}
