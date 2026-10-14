package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type telemetryPollStore struct {
	CodexTelemetryStore
	dueScans, claims, transactions int
	keys                           []CodexTelemetryPoolKey
	listErr                        error
	transaction                    func(context.Context, CodexTelemetryPoolKey) error
}

func (s *telemetryPollStore) ListDuePools(context.Context, time.Time, int) ([]CodexTelemetryPoolKey, error) {
	s.dueScans++
	return s.keys, s.listErr
}

func (s *telemetryPollStore) ClaimBatches(ctx context.Context, now time.Time, limit int) ([]CodexTelemetryBatch, error) {
	s.claims++
	return s.CodexTelemetryStore.ClaimBatches(ctx, now, limit)
}

func (s *telemetryPollStore) TransactPool(ctx context.Context, key CodexTelemetryPoolKey, now time.Time, fn func(*CodexTelemetryPoolTransaction) error) (*CodexTelemetryPool, error) {
	s.transactions++
	if s.transaction != nil {
		if err := s.transaction(ctx, key); err != nil {
			return nil, err
		}
	}
	return s.CodexTelemetryStore.TransactPool(ctx, key, now, fn)
}

func TestCodexTelemetryRuntimeWakeBurstsThrottleDueScanButDispatchNewBatches(t *testing.T) {
	t.Setenv("CODEX_TELEMETRY_ENABLED", "true")
	now := time.Now()
	store := &telemetryPollStore{CodexTelemetryStore: NewMemoryCodexTelemetryStore()}
	// No background workers or transport: inspect the locally enqueued batch.
	s := &CodexTelemetryService{store: store, configured: true, simulationEnabled: true,
		observationEnabled: true, epoch: 1, lastPolicyRefresh: now}
	s.pollPersistentRuntime(now)
	for i := range 100 {
		s.pollPersistentRuntime(now.Add(time.Duration(i+1) * time.Millisecond))
	}
	require.Equal(t, 1, store.dueScans)
	require.Equal(t, 101, store.claims)

	a := runtimeTestAttempt(now.Add(-time.Second), "windows", "turn", "sample")
	runtimeTestMutation(t, store, a, nil, false)
	s.pollPersistentRuntime(now.Add(200 * time.Millisecond))
	require.Equal(t, 1, store.dueScans, "a new batch must not trigger another due scan")
	require.Positive(t, s.queueDepth, "batch dispatch must not wait for the next due scan")

	s.pollPersistentRuntime(now.Add(time.Second))
	require.Equal(t, 2, store.dueScans)
}

func TestCodexTelemetryDueMaintenanceStopsAfterContextCancellation(t *testing.T) {
	now := time.Now()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store := &telemetryPollStore{CodexTelemetryStore: NewMemoryCodexTelemetryStore()}
	for i := range 100 {
		store.keys = append(store.keys, CodexTelemetryPoolKey{OwnerAccountID: int64(i + 1)})
	}
	store.transaction = func(context.Context, CodexTelemetryPoolKey) error {
		cancel()
		return context.Canceled
	}
	s := &CodexTelemetryService{}
	s.maintainDueRuntimePools(ctx, store, now)
	require.Equal(t, 1, store.transactions, "do not attempt the remaining 99 transactions with an expired context")
	require.Equal(t, 1, s.runtimeFailures.pools)
}

func TestCodexTelemetryDueMaintenanceContinuesAfterDeletedOwner(t *testing.T) {
	now := time.Now()
	store := &telemetryPollStore{CodexTelemetryStore: NewMemoryCodexTelemetryStore(), keys: []CodexTelemetryPoolKey{
		{OwnerAccountID: 1, OSFamily: "windows"}, {OwnerAccountID: 2, OSFamily: "windows"},
	}}
	store.transaction = func(_ context.Context, key CodexTelemetryPoolKey) error {
		if key.OwnerAccountID == 1 {
			return fmt.Errorf("account lifecycle: %w", ErrCodexTelemetryOwnerUnavailable)
		}
		return nil
	}
	s := &CodexTelemetryService{}
	s.maintainDueRuntimePools(context.Background(), store, now)
	require.Equal(t, 2, store.transactions)
	require.Zero(t, s.runtimeFailures.pools, "concurrent owner deletion is an expected lifecycle transition")
}

func TestCodexTelemetryRuntimeHeartbeatStopsAfterContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store := &telemetryPollStore{transaction: func(context.Context, CodexTelemetryPoolKey) error {
		cancel()
		return context.Canceled
	}}
	s := &CodexTelemetryService{runtimeAttempts: map[uint64]*CodexTelemetryAttempt{}}
	for i := range 100 {
		s.runtimeAttempts[uint64(i)] = &CodexTelemetryAttempt{poolKey: CodexTelemetryPoolKey{OwnerAccountID: int64(i + 1)}}
	}
	require.Equal(t, 1, s.heartbeatRuntimeAttempts(ctx, store, time.Now()))
	require.Equal(t, 1, store.transactions)
}

func TestCodexTelemetryRuntimeFailureLogsAreSanitizedAndThrottled(t *testing.T) {
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	now := time.Now()
	s := &CodexTelemetryService{}
	store := &telemetryPollStore{listErr: errors.New("database payload contains secret-access-token")}
	s.maintainDueRuntimePools(context.Background(), store, now)
	s.reportRuntimeFailures(now)
	require.Equal(t, 1, strings.Count(output.String(), "\n"))
	require.NotContains(t, output.String(), "secret-access-token")
	for range 20 {
		s.maintainDueRuntimePools(context.Background(), store, now.Add(time.Second))
		s.reportRuntimeFailures(now.Add(time.Second))
	}
	require.Equal(t, 1, strings.Count(output.String(), "\n"))
	s.reportRuntimeFailures(now.Add(codexTelemetryRuntimeLogInterval))
	require.Equal(t, 2, strings.Count(output.String(), "\n"))
	require.Contains(t, output.String(), `"due_scan_failures":20`)
}

func TestCodexTelemetryRuntimePruneKeepsFreshRowsBelowCapacity(t *testing.T) {
	now := time.Now()
	tx := &CodexTelemetryPoolTransaction{Activities: map[string]CodexTelemetryActivity{}}
	require.NoError(t, putRuntimeActivity(tx, "sampling", "old", map[string]bool{"completed": true}, now.Add(-codexTelemetryActivityRetention), now))
	require.NoError(t, putRuntimeActivity(tx, "sampling", "fresh", map[string]bool{"completed": true}, now.Add(-time.Minute), now))
	require.NoError(t, putRuntimeActivity(tx, "turn", "inflight", codexTelemetryRuntimeTurn{Inflight: map[string]time.Time{"active": now.Add(time.Minute)}}, now, now.Add(time.Minute)))
	require.NoError(t, pruneCodexTelemetryRuntime(tx, now, 2))
	require.NotContains(t, tx.Activities, CodexTelemetryActivityMapKey("sampling", "old"))
	require.Contains(t, tx.Activities, CodexTelemetryActivityMapKey("sampling", "fresh"))
	require.Contains(t, tx.Activities, CodexTelemetryActivityMapKey("turn", "inflight"))
}

func TestCodexTelemetryRuntimeExpiredLeaseWaitsForIdleDeadline(t *testing.T) {
	now := time.Now().UTC()
	idleAt := now.Add(10 * time.Minute)
	tx := &CodexTelemetryPoolTransaction{Policy: CodexTelemetryPolicy{Epoch: 1, Enabled: true, Observation: true}, Activities: map[string]CodexTelemetryActivity{}}
	require.NoError(t, syncCodexTelemetryRuntimeEpoch(tx, now))
	require.NoError(t, putRuntimeActivity(tx, "turn", "active", codexTelemetryRuntimeTurn{
		LastSeen: idleAt.Add(-codexTelemetryStateTTL), Inflight: map[string]time.Time{"expired": now.Add(-time.Second)},
	}, now.Add(-time.Minute), now.Add(-time.Second)))
	require.NoError(t, tickCodexTelemetryRuntime(tx, now))
	turn, exists, err := runtimeActivity[codexTelemetryRuntimeTurn](tx, "turn", "active")
	require.NoError(t, err)
	require.True(t, exists)
	require.Empty(t, turn.Inflight)
	require.False(t, turn.Incomplete)
	require.False(t, turn.Sealed)
	require.Equal(t, idleAt, tx.Activities[CodexTelemetryActivityMapKey("turn", "active")].DueAt)
	require.Empty(t, tx.Batches, "expired leases do not synthesize completion telemetry")

	require.NoError(t, tickCodexTelemetryRuntime(tx, idleAt))
	turn, _, err = runtimeActivity[codexTelemetryRuntimeTurn](tx, "turn", "active")
	require.NoError(t, err)
	require.True(t, turn.Incomplete)
	require.False(t, turn.Sealed)
	require.Empty(t, tx.Batches)
	require.Equal(t, idleAt.Add(codexTelemetryActivityRetention), tx.Activities[CodexTelemetryActivityMapKey("turn", "active")].DueAt)
}

func TestCodexTelemetryRuntimePruneStillRejectsInvalidStateBelowCapacity(t *testing.T) {
	for _, kind := range []string{"attempt", "turn"} {
		t.Run(kind, func(t *testing.T) {
			tx := &CodexTelemetryPoolTransaction{Activities: map[string]CodexTelemetryActivity{
				CodexTelemetryActivityMapKey(kind, "invalid"): {Kind: kind, Key: "invalid", Data: []byte(`{"unfinished":`), UpdatedAt: time.Now()},
			}}
			require.ErrorIs(t, pruneCodexTelemetryRuntime(tx, time.Now(), 0), ErrCodexTelemetryInvalidState)
		})
	}
}

func BenchmarkCodexTelemetryRuntimePruneBelowCapacity(b *testing.B) {
	now := time.Now()
	tx := &CodexTelemetryPoolTransaction{Activities: map[string]CodexTelemetryActivity{}}
	for i := range 3000 {
		key := fmt.Sprintf("sample-%04d", i)
		tx.Activities[key] = CodexTelemetryActivity{Kind: "sampling", Key: key, UpdatedAt: now.Add(-time.Duration(i) * time.Second)}
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if err := pruneCodexTelemetryRuntime(tx, now, 2); err != nil {
			b.Fatal(err)
		}
	}
}
