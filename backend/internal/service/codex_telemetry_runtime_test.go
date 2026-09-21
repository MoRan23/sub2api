package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func runtimeTestAttempt(at time.Time, os, turn, sample string) *CodexTelemetryAttempt {
	i := CodexTelemetryInput{AccountID: 19, OwnerAccountID: 19, AccountName: "business-account", OSFamily: os,
		InstallationID: "b2fcb324-1dd8-4a3f-8ae4-21be97c050f8", UserAgent: "codex_cli_rs/0.155.1 (Windows 10.0.26200; x86_64) WindowsTerminal",
		AccessToken: "secret-access-token", ChatGPTAccountID: "chatgpt-account", ProxyURL: "http://user:proxy-secret@127.0.0.1:7897",
		SessionID: "session-one", ThreadID: "thread-one", TurnID: turn, SamplingID: sample,
		Originator: "codex_cli_rs", Version: "0.155.1", Model: "gpt-6-astra", StartedAt: at}
	p := codexTelemetryProfile{input: i, client: codexTelemetryClient{localID: i.AccountID, name: i.AccountName, accountID: i.ChatGPTAccountID, accessToken: i.AccessToken, proxyURL: i.ProxyURL,
		userAgent: i.UserAgent, originator: i.Originator, version: i.Version}, sessionID: i.SessionID, threadID: i.ThreadID, turnID: i.TurnID,
		model: i.Model, started: at, simulationEnabled: true, observationEnabled: true, source: "mixed"}
	return &CodexTelemetryAttempt{attemptID: uuid.NewString(), policyEpoch: 1, profile: p,
		poolKey: CodexTelemetryPoolKey{OwnerAccountID: i.OwnerAccountID, OSFamily: os, InstallationID: i.InstallationID}}
}

func runtimeTestMutation(t *testing.T, store CodexTelemetryStore, a *CodexTelemetryAttempt, result *CodexTelemetryResult, retry bool) {
	t.Helper()
	_, err := store.TransactPool(context.Background(), a.poolKey, a.profile.started, func(tx *CodexTelemetryPoolTransaction) error {
		if err := syncCodexTelemetryRuntimeEpoch(tx, a.profile.started); err != nil {
			return err
		}
		if result == nil {
			return beginCodexTelemetryRuntime(tx, a)
		}
		return finishCodexTelemetryRuntime(tx, a, *result, retry)
	})
	require.NoError(t, err)
}

func runtimeTestReadTurn(t *testing.T, store CodexTelemetryStore, a *CodexTelemetryAttempt) codexTelemetryRuntimeTurn {
	t.Helper()
	var turn codexTelemetryRuntimeTurn
	_, err := store.TransactPool(context.Background(), a.poolKey, a.profile.started, func(tx *CodexTelemetryPoolTransaction) error {
		value, exists, err := runtimeActivity[codexTelemetryRuntimeTurn](tx, "turn", codexRuntimeTurnKey(a.profile, a.attemptID))
		require.True(t, exists)
		turn = value
		return err
	})
	require.NoError(t, err)
	return turn
}

func TestCodexTelemetryRuntimeSamplingAndExplicitTurnBoundary(t *testing.T) {
	store := NewMemoryCodexTelemetryStore()
	now := time.Now().UTC()
	first := runtimeTestAttempt(now, "windows", "turn-one", "sample-one")
	runtimeTestMutation(t, store, first, nil, false)
	result := CodexTelemetryResult{Status: "completed", HTTPStatus: 200, ResponseID: "response-one", InputTokens: 10, OutputTokens: 4, FinishedAt: now.Add(time.Second)}
	runtimeTestMutation(t, store, first, &result, false)
	second := runtimeTestAttempt(now.Add(2*time.Second), "windows", "turn-one", "sample-two")
	runtimeTestMutation(t, store, second, nil, false)
	result.ResponseID, result.InputTokens, result.OutputTokens, result.FinishedAt = "response-two", 20, 7, now.Add(3*time.Second)
	runtimeTestMutation(t, store, second, &result, false)
	turn := runtimeTestReadTurn(t, store, first)
	require.False(t, turn.Sealed, "Responses completion must not finish a client turn")
	require.Equal(t, 2, turn.Samples)
	require.EqualValues(t, 30, turn.Result.InputTokens)
	require.EqualValues(t, 11, turn.Result.OutputTokens)
	third := runtimeTestAttempt(now.Add(4*time.Second), "windows", "turn-two", "sample-three")
	runtimeTestMutation(t, store, third, nil, false)
	turn = runtimeTestReadTurn(t, store, first)
	require.True(t, turn.Sealed)
	p, err := unmarshalCodexTelemetryProfile(turn.Profile)
	require.NoError(t, err)
	require.Equal(t, 2, p.samplingCount)
	require.Zero(t, p.clientRetryCount)
	batches, err := store.ClaimBatches(context.Background(), now.Add(5*time.Second), 20)
	require.NoError(t, err)
	require.NotEmpty(t, batches)
	for _, b := range batches {
		require.NotContains(t, string(b.Metadata), "secret-access-token")
		require.NotContains(t, string(b.Metadata), "proxy-secret")
	}
}

func TestCodexTelemetryRuntimeRetryAndDuplicateResultAreNotNewSamples(t *testing.T) {
	store := NewMemoryCodexTelemetryStore()
	now := time.Now().UTC()
	a := runtimeTestAttempt(now, "windows", "turn", "sampling")
	runtimeTestMutation(t, store, a, nil, false)
	failure := CodexTelemetryResult{Status: "failed", HTTPStatus: 500, FinishedAt: now.Add(time.Second)}
	runtimeTestMutation(t, store, a, &failure, true)
	b := runtimeTestAttempt(now.Add(2*time.Second), "windows", "turn", "sampling")
	runtimeTestMutation(t, store, b, nil, false)
	result := CodexTelemetryResult{Status: "completed", HTTPStatus: 200, InputTokens: 12, OutputTokens: 5, FinishedAt: now.Add(3 * time.Second)}
	runtimeTestMutation(t, store, b, &result, false)
	runtimeTestMutation(t, store, b, &result, false)
	turn := runtimeTestReadTurn(t, store, a)
	require.Equal(t, 2, turn.Attempts)
	require.Equal(t, 1, turn.Samples)
	require.EqualValues(t, 12, turn.Result.InputTokens)
	require.Empty(t, turn.Inflight)
}

func TestCodexTelemetryRuntimePendingToolAndContinuePreventSealing(t *testing.T) {
	store := NewMemoryCodexTelemetryStore()
	now := time.Now().UTC()
	a := runtimeTestAttempt(now, "windows", "turn-one", "sample-one")
	runtimeTestMutation(t, store, a, nil, false)
	end := false
	result := CodexTelemetryResult{Status: "completed", EndTurn: &end, PendingToolCallIDs: []string{"call-one"}, FinishedAt: now.Add(time.Second)}
	runtimeTestMutation(t, store, a, &result, false)
	b := runtimeTestAttempt(now.Add(2*time.Second), "windows", "turn-two", "sample-two")
	runtimeTestMutation(t, store, b, nil, false)
	require.False(t, runtimeTestReadTurn(t, store, a).Sealed)
	returned := runtimeTestAttempt(now.Add(3*time.Second), "windows", "turn-one", "sample-three")
	returned.profile.input.ReturnedToolCallIDs = []string{"call-one"}
	runtimeTestMutation(t, store, returned, nil, false)
	end = true
	result.PendingToolCallIDs, result.FinishedAt = nil, now.Add(4*time.Second)
	runtimeTestMutation(t, store, returned, &result, false)
	turn := runtimeTestReadTurn(t, store, a)
	require.Empty(t, turn.PendingTools)
	require.False(t, turn.Continue)
	c := runtimeTestAttempt(now.Add(5*time.Second), "windows", "turn-three", "sample-four")
	runtimeTestMutation(t, store, c, nil, false)
	require.True(t, runtimeTestReadTurn(t, store, a).Sealed)
}

func TestCodexTelemetryRuntimeIdleIsIncompleteAndNeverExtendsBusinessActivity(t *testing.T) {
	store := NewMemoryCodexTelemetryStore()
	now := time.Now().UTC()
	a := runtimeTestAttempt(now, "windows", "turn-one", "sample-one")
	runtimeTestMutation(t, store, a, nil, false)
	result := CodexTelemetryResult{Status: "completed", HTTPStatus: 200, FinishedAt: now.Add(time.Second)}
	runtimeTestMutation(t, store, a, &result, false)
	_, err := store.TransactPool(context.Background(), a.poolKey, now.Add(31*time.Minute), func(tx *CodexTelemetryPoolTransaction) error {
		require.Equal(t, now, tx.Pool.LastBusinessAt)
		return tickCodexTelemetryRuntime(tx, now.Add(31*time.Minute))
	})
	require.NoError(t, err)
	turn := runtimeTestReadTurn(t, store, a)
	require.True(t, turn.Incomplete)
	require.False(t, turn.Sealed)
}

func TestCodexTelemetryRuntimePoolSurvivesRoutingAndIdentityPartitions(t *testing.T) {
	store := NewMemoryCodexTelemetryStore().(*MemoryCodexTelemetryStore)
	now := time.Now().UTC()
	a := runtimeTestAttempt(now, "windows", "turn-one", "sample-one")
	runtimeTestMutation(t, store, a, nil, false)
	before := store.pools[a.poolKey]
	b := runtimeTestAttempt(now.Add(time.Second), "windows", "turn-two", "sample-two")
	proxy := int64(900)
	b.profile.input.ProxyID, b.profile.input.AccountName = &proxy, "renamed-account"
	b.profile.input.AccessToken = "refreshed-access-token"
	b.profile.client.version, b.profile.input.Version = "0.156.0", "0.156.0"
	runtimeTestMutation(t, store, b, nil, false)
	require.Equal(t, before.ID, store.pools[a.poolKey].ID)
	require.Equal(t, before.Seed, store.pools[a.poolKey].Seed)
	for _, os := range []string{"macos", "linux"} {
		other := runtimeTestAttempt(now.Add(time.Second), os, "turn-one", "sample-one")
		runtimeTestMutation(t, store, other, nil, false)
		require.NotEqual(t, before.ID, store.pools[other.poolKey].ID)
	}
	renewed := runtimeTestAttempt(now.Add(2*time.Second), "windows", "turn-one", "sample-one")
	renewed.poolKey.InstallationID, renewed.profile.input.InstallationID = uuid.NewString(), uuid.NewString()
	runtimeTestMutation(t, store, renewed, nil, false)
	require.NotEqual(t, before.ID, store.pools[renewed.poolKey].ID)
	require.Len(t, store.pools, 4)
}

func TestCodexTelemetryRuntimePolicyFenceDiscardsPendingButKeepsMarkers(t *testing.T) {
	store := NewMemoryCodexTelemetryStore()
	now := time.Now().UTC()
	a := runtimeTestAttempt(now, "windows", "turn-one", "sample-one")
	runtimeTestMutation(t, store, a, nil, false)
	result := CodexTelemetryResult{Status: "completed", HTTPStatus: 200, FinishedAt: now.Add(time.Second)}
	runtimeTestMutation(t, store, a, &result, false)
	_, err := store.SyncPolicy(context.Background(), false, true, true)
	require.NoError(t, err)
	_, err = store.SyncPolicy(context.Background(), true, true, true)
	require.NoError(t, err)
	_, err = store.TransactPool(context.Background(), a.poolKey, now.Add(2*time.Minute), func(tx *CodexTelemetryPoolTransaction) error {
		require.NoError(t, tickCodexTelemetryRuntime(tx, now.Add(2*time.Minute)))
		require.Empty(t, tx.Batches, "disabled-generation pending metrics must not be replayed")
		metrics, err := loadRuntimeMetrics(tx)
		require.NoError(t, err)
		require.NotEmpty(t, metrics.clients, "startup marker must survive disabling")
		thread, exists, err := runtimeActivity[codexTelemetryRuntimeThread](tx, "thread", "thread-one")
		require.NoError(t, err)
		require.True(t, exists)
		require.True(t, thread.Initialized)
		return nil
	})
	require.NoError(t, err)
	require.True(t, runtimeTestReadTurn(t, store, a).Incomplete)
}

func TestCodexTelemetryRuntimeProfileAllowlist(t *testing.T) {
	a := runtimeTestAttempt(time.Now().UTC(), "windows", "turn", "sample")
	data, err := marshalCodexTelemetryProfile(a.profile)
	require.NoError(t, err)
	require.NotContains(t, string(data), "secret-access-token")
	require.NotContains(t, string(data), "proxy-secret")
	require.NotContains(t, strings.ToLower(string(data)), "proxy_url")
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(data, &decoded))
	require.NotContains(t, decoded, "input")
	p, err := unmarshalCodexTelemetryProfile(data)
	require.NoError(t, err)
	require.Equal(t, "chatgpt-account", p.input.ChatGPTAccountID)
	require.Equal(t, "business-account", p.client.name)
	require.Empty(t, p.client.accessToken)
	require.Empty(t, p.input.AccessToken)
}

type telemetryBlockingStore struct {
	CodexTelemetryStore
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (s *telemetryBlockingStore) TransactPool(ctx context.Context, key CodexTelemetryPoolKey, at time.Time, callback func(*CodexTelemetryPoolTransaction) error) (*CodexTelemetryPool, error) {
	s.once.Do(func() { close(s.entered) })
	select {
	case <-s.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return s.CodexTelemetryStore.TransactPool(ctx, key, at, callback)
}

func TestCodexTelemetryRuntimeBusinessNeverWaitsForStorage(t *testing.T) {
	s := NewCodexTelemetryService(nil)
	defer s.Stop()
	blocked := &telemetryBlockingStore{CodexTelemetryStore: NewMemoryCodexTelemetryStore(), entered: make(chan struct{}), release: make(chan struct{})}
	require.NoError(t, s.SetStore(blocked))
	a := runtimeTestAttempt(time.Now().UTC(), "windows", "turn", "sample")
	started := time.Now()
	attempt := s.Begin(context.Background(), a.profile.input)
	require.NotNil(t, attempt)
	require.Less(t, time.Since(started), 100*time.Millisecond)
	select {
	case <-blocked.entered:
	case <-time.After(time.Second):
		t.Fatal("mutation worker did not run")
	}
	started = time.Now()
	attempt.Finish(CodexTelemetryResult{Status: "completed", HTTPStatus: 200, FinishedAt: time.Now()})
	require.Less(t, time.Since(started), 100*time.Millisecond)
	close(blocked.release)
	require.Eventually(t, func() bool { s.mu.Lock(); defer s.mu.Unlock(); return s.mutationReservations == 0 }, time.Second, time.Millisecond)
}

func TestCodexTelemetryRuntimeCapacityPreservesActiveAndStableMarkers(t *testing.T) {
	now := time.Now().UTC()
	tx := &CodexTelemetryPoolTransaction{Activities: map[string]CodexTelemetryActivity{}}
	require.NoError(t, putRuntimeActivity(tx, "thread", "stable", codexTelemetryRuntimeThread{Initialized: true}, now.Add(-30*24*time.Hour), time.Time{}))
	for i := range codexTelemetryMaxStates {
		key := fmt.Sprintf("turn-%05d", i)
		require.NoError(t, putRuntimeActivity(tx, "turn", key, codexTelemetryRuntimeTurn{PendingTools: map[string]bool{"pending": true}}, now, time.Time{}))
	}
	require.ErrorIs(t, pruneCodexTelemetryRuntime(tx, now, 1), errCodexTelemetryRuntimeCapacity)
	require.Len(t, tx.Activities, codexTelemetryMaxStates+1)
	key := CodexTelemetryActivityMapKey("turn", "turn-00000")
	delete(tx.Activities, key)
	require.NoError(t, putRuntimeActivity(tx, "sampling", "completed", map[string]bool{"completed": true}, now.Add(-time.Hour), time.Time{}))
	require.NoError(t, pruneCodexTelemetryRuntime(tx, now, 1))
	require.NotContains(t, tx.Activities, CodexTelemetryActivityMapKey("sampling", "completed"))
	require.Contains(t, tx.Activities, CodexTelemetryActivityMapKey("thread", "stable"))
	require.Contains(t, tx.Activities, CodexTelemetryActivityMapKey("turn", "turn-00001"))
}

func TestCodexTelemetryRuntimeRetentionKeepsInitializationMarkers(t *testing.T) {
	now := time.Now().UTC()
	old := now.Add(-codexTelemetryActivityRetention - time.Minute)
	tx := &CodexTelemetryPoolTransaction{Activities: map[string]CodexTelemetryActivity{}}
	require.NoError(t, putRuntimeActivity(tx, "thread", "stable", codexTelemetryRuntimeThread{Initialized: true}, old, time.Time{}))
	require.NoError(t, putRuntimeActivity(tx, "turn", "closed", codexTelemetryRuntimeTurn{Sealed: true}, old, now.Add(-time.Minute)))
	require.NoError(t, putRuntimeActivity(tx, "turn", "active", codexTelemetryRuntimeTurn{PendingTools: map[string]bool{"pending": true}}, old, time.Time{}))
	require.NoError(t, putRuntimeActivity(tx, "attempt", "finished", codexTelemetryRuntimeAttempt{Finished: true, TurnKey: "closed"}, old, now.Add(-time.Minute)))
	require.NoError(t, putRuntimeActivity(tx, "sampling", "sample", map[string]bool{"completed": true}, old, now.Add(-time.Minute)))
	require.NoError(t, pruneCodexTelemetryRuntime(tx, now, 0))
	require.Len(t, tx.Activities, 2)
	require.Contains(t, tx.Activities, CodexTelemetryActivityMapKey("thread", "stable"))
	require.Contains(t, tx.Activities, CodexTelemetryActivityMapKey("turn", "active"))
}

func TestCodexTelemetryRuntimePreservesIncompleteMeasurementStatus(t *testing.T) {
	terminal := codexTelemetryTerminalFromResult(CodexTelemetryResult{Status: "incomplete", HTTPStatus: 200, FinishedAt: time.Now()})
	require.Equal(t, "incomplete", terminal.result.Status)
	require.Equal(t, 200, terminal.result.HTTPStatus)
}

func TestCodexTelemetryRuntimeLiveBusinessLeasePreventsIdleCompletion(t *testing.T) {
	store := NewMemoryCodexTelemetryStore()
	now := time.Now().UTC()
	a := runtimeTestAttempt(now.Add(-31*time.Minute), "windows", "turn", "sample")
	runtimeTestMutation(t, store, a, nil, false)
	_, err := store.TransactPool(context.Background(), a.poolKey, now, func(tx *CodexTelemetryPoolTransaction) error { return tickCodexTelemetryRuntime(tx, now) })
	require.NoError(t, err)
	turn := runtimeTestReadTurn(t, store, a)
	require.False(t, turn.Incomplete)
	require.False(t, turn.Sealed)
	require.Contains(t, turn.Inflight, a.attemptID)
}

type telemetryMaintenanceStore struct {
	CodexTelemetryStore
	mu          sync.Mutex
	maintenance int
}

func (s *telemetryMaintenanceStore) Maintain(ctx context.Context, now time.Time) error {
	s.mu.Lock()
	s.maintenance++
	s.mu.Unlock()
	return s.CodexTelemetryStore.Maintain(ctx, now)
}

func TestCodexTelemetryRuntimeDisabledMaintenance(t *testing.T) {
	for _, tc := range []struct {
		name                                          string
		enabled, simulation, observation, envDisabled bool
		maintenance                                   int
	}{
		{"global disabled", false, true, true, false, 1},
		{"both modes disabled", true, false, false, false, 1},
		{"node override", true, true, true, true, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.envDisabled {
				t.Setenv("CODEX_TELEMETRY_ENABLED", "false")
			} else {
				t.Setenv("CODEX_TELEMETRY_ENABLED", "true")
			}
			store := &telemetryMaintenanceStore{CodexTelemetryStore: NewMemoryCodexTelemetryStore()}
			_, err := store.SyncPolicy(context.Background(), tc.enabled, tc.simulation, tc.observation)
			require.NoError(t, err)
			s := NewCodexTelemetryService(nil)
			defer s.Stop()
			require.NoError(t, s.SetStore(store))
			s.pollPersistentRuntime(time.Now())
			store.mu.Lock()
			count := store.maintenance
			store.mu.Unlock()
			require.Equal(t, tc.maintenance, count)
			policy, err := store.ReadPolicy(context.Background())
			require.NoError(t, err)
			require.Equal(t, tc.enabled, policy.Enabled, "local environment must not disable another node's shared policy")
		})
	}
}
