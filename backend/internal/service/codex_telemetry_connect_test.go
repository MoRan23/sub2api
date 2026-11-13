package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// A deterministic service: production workers are deliberately not started so
// tests can inspect the bounded handoff and advance the durable worker separately.
func telemetryConnectTestService(t *testing.T) *CodexTelemetryService {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return &CodexTelemetryService{
		configured: true, simulationEnabled: true, observationEnabled: true,
		epoch: 1, sharedEpoch: 1, epochCtx: ctx, epochCancel: cancel,
		store: NewMemoryCodexTelemetryStore(), metrics: newCodexTelemetryMetricStore(),
		mutations:   make(chan codexTelemetryMutation, codexTelemetryQueueSize),
		runtimeWake: make(chan struct{}, 1),
	}
}

func telemetryConnectTestInput() CodexTelemetryConnectInput {
	return CodexTelemetryConnectInput{
		AccountID: 19, OwnerAccountID: 11, AccountName: "business-account",
		OSFamily: "linux", CredentialOS: "linux", CredentialRevision: 2,
		AuthorizationGeneration: "auth-one", InstallationID: "b2fcb324-1dd8-4a3f-8ae4-21be97c050f8",
		SessionID: "aa914e8a-5dfd-49f6-8e2a-5f9d25de9625", Route: "wss://example.test/responses#proxy=1", ProxyID: 1,
		UserAgent: "codex-tui/0.169.0 (Ubuntu 24.4.0; x86_64) unknown", Originator: "codex-tui", Version: "0.169.0",
		Model: "gpt-6-astra", ProductSKU: "codex", CodexResidency: "us",
	}
}

func telemetryConnectTestFinish(t *testing.T, s *CodexTelemetryService, input CodexTelemetryConnectInput, at time.Time, success bool) codexTelemetryConnectMeasurement {
	t.Helper()
	o := s.beginConnect(input, s.epoch, s.sharedEpoch, at)
	require.NotNil(t, o)
	o.once.Do(func() { o.finish(success, at.Add(25*time.Millisecond)) })
	select {
	case m := <-s.mutations:
		require.NotNil(t, m.connect)
		return *m.connect
	default:
		t.Fatal("connect result not enqueued")
		return codexTelemetryConnectMeasurement{}
	}
}

func TestCodexTelemetryConnectObservedSuccessFailureAndTimeout(t *testing.T) {
	s := telemetryConnectTestService(t)
	input, now := telemetryConnectTestInput(), time.Now().UTC()
	for index, sample := range []struct {
		duration time.Duration
		success  bool
	}{
		{25 * time.Millisecond, true}, {5 * time.Millisecond, false}, {30 * time.Second, false},
	} {
		o := s.beginConnect(input, s.epoch, s.sharedEpoch, now.Add(time.Duration(index)*time.Minute))
		require.NotNil(t, o)
		o.once.Do(func() { o.finish(sample.success, o.started.Add(sample.duration)) })
		o.Finish(!sample.success)
		require.Len(t, s.mutations, 1, "a second callback must not add a physical sample")
		m := <-s.mutations
		require.Equal(t, float64(sample.duration)/float64(time.Millisecond), m.connect.durationMS)
		require.Equal(t, sample.success, m.connect.success)
		s.applyRuntimeMutation(m)
	}
	require.Zero(t, s.mutationReservations)
	require.Empty(t, s.runtimeAttempts)
	require.Zero(t, s.counters.Attempts)
	key := CodexTelemetryPoolKey{input.OwnerAccountID, input.CredentialOS, input.InstallationID}
	_, err := s.store.TransactPool(context.Background(), key, now, func(tx *CodexTelemetryPoolTransaction) error {
		for _, value := range runtimeTestActivityMap(tx) {
			require.Contains(t, []string{"policy", "metrics"}, value.Kind, "Dial cannot create a logical request, turn, or sampling")
		}
		metrics, err := loadRuntimeMetrics(tx)
		require.NoError(t, err)
		batches := metrics.flush(now.Add(5 * time.Minute))
		require.Len(t, batches, 1)
		require.Equal(t, "observed", batches[0].source)
		require.Zero(t, batches[0].turns)
		require.Zero(t, batches[0].attempts)
		require.ElementsMatch(t, []string{"codex.websocket.connect", "codex.websocket.connect.duration_ms"}, batches[0].names)
		var count uint64
		for _, point := range codexMetricsTestMetric(batches[0].body, "codex.websocket.connect").Get("sum.dataPoints").Array() {
			count += point.Get("asInt").Uint()
			require.Equal(t, "codex", codexMetricsTestAttribute(point, "product_sku"))
		}
		require.EqualValues(t, 3, count)
		return nil
	})
	require.NoError(t, err)
}

func TestCodexTelemetryConnectReauthOnlyUsesSuccessfulScopedBaseline(t *testing.T) {
	s, input, now := telemetryConnectTestService(t), telemetryConnectTestInput(), time.Now()
	first := telemetryConnectTestFinish(t, s, input, now, true)
	require.Nil(t, first.reauth, "a resumed process has no previous connection evidence")
	same := telemetryConnectTestFinish(t, s, input, now.Add(time.Second), true)
	require.NotNil(t, same.reauth)
	require.False(t, *same.reauth)
	input.CredentialRevision++
	failed := telemetryConnectTestFinish(t, s, input, now.Add(2*time.Second), false)
	require.True(t, *failed.reauth)
	retry := telemetryConnectTestFinish(t, s, input, now.Add(3*time.Second), true)
	require.True(t, *retry.reauth, "failure must not replace the last successful revision")
	confirmed := telemetryConnectTestFinish(t, s, input, now.Add(4*time.Second), true)
	require.False(t, *confirmed.reauth)
	input.AuthorizationGeneration = "auth-two"
	changed := telemetryConnectTestFinish(t, s, input, now.Add(5*time.Second), true)
	require.True(t, *changed.reauth)
	for _, change := range []func(*CodexTelemetryConnectInput){
		func(i *CodexTelemetryConnectInput) { i.OwnerAccountID++ },
		func(i *CodexTelemetryConnectInput) { i.CredentialOS = "windows" },
		func(i *CodexTelemetryConnectInput) { i.InstallationID = "another-installation" },
		func(i *CodexTelemetryConnectInput) { i.SessionID = "another-session" },
		func(i *CodexTelemetryConnectInput) { i.Route = "another-route" },
		func(i *CodexTelemetryConnectInput) { i.CredentialRevision = 0 },
		func(i *CodexTelemetryConnectInput) { i.SessionID = "" },
	} {
		next := input
		change(&next)
		isolated := telemetryConnectTestFinish(t, s, next, now.Add(6*time.Second), true)
		require.Nil(t, isolated.reauth)
	}
}

func TestCodexTelemetryConnectConcurrentDialsLeaveReauthUnknown(t *testing.T) {
	s, input, now := telemetryConnectTestService(t), telemetryConnectTestInput(), time.Now()
	telemetryConnectTestFinish(t, s, input, now, true)
	first := s.beginConnect(input, s.epoch, s.sharedEpoch, now.Add(time.Second))
	input.CredentialRevision++
	second := s.beginConnect(input, s.epoch, s.sharedEpoch, now.Add(time.Second))
	first.Finish(true)
	second.Finish(true)
	for range 2 {
		require.Nil(t, (<-s.mutations).connect.reauth, "overlapping Dial completion order cannot prove previous authorization")
	}
	recovered := telemetryConnectTestFinish(t, s, input, now.Add(2*time.Second), true)
	require.Nil(t, recovered.reauth)
	stable := telemetryConnectTestFinish(t, s, input, now.Add(3*time.Second), true)
	require.False(t, *stable.reauth)
}

func TestCodexTelemetryConnectOverlappingFailuresPreserveSuccessfulBaseline(t *testing.T) {
	s, input, now := telemetryConnectTestService(t), telemetryConnectTestInput(), time.Now()
	telemetryConnectTestFinish(t, s, input, now, true)
	input.CredentialRevision++
	first := s.beginConnect(input, s.epoch, s.sharedEpoch, now.Add(time.Second))
	second := s.beginConnect(input, s.epoch, s.sharedEpoch, now.Add(time.Second))
	first.Finish(false)
	second.Finish(false)
	for range 2 {
		require.Nil(t, (<-s.mutations).connect.reauth)
	}
	next := telemetryConnectTestFinish(t, s, input, now.Add(2*time.Second), true)
	require.NotNil(t, next.reauth)
	require.True(t, *next.reauth, "failed handshakes must preserve the last successful authorization")
}

func TestCodexTelemetryConnectFactoryFreezesScalarsAndPolicy(t *testing.T) {
	s := telemetryConnectTestService(t)
	input := telemetryConnectTestInput()
	factory := s.PrepareConnect(input)
	require.NotNil(t, factory)
	require.Empty(t, s.connectStates, "preparing a pool lookup or delayed prewarm is not a Dial")
	require.Empty(t, s.mutations)
	input.Model, input.AuthorizationGeneration = "changed", "changed"
	o := factory()
	require.Equal(t, "gpt-6-astra", o.input.Model)
	require.Equal(t, "auth-one", o.input.AuthorizationGeneration)
	s.SetPolicy(true, true, false)
	o.Finish(true)
	require.Empty(t, s.mutations)
	require.Zero(t, s.mutationReservations)
	require.Nil(t, factory(), "a delayed prewarm cannot reuse a retired policy")
	s.SetPolicy(true, false, true)
	require.Nil(t, factory(), "reenabling cannot revive the earlier factory")
	fresh := s.PrepareConnect(telemetryConnectTestInput())
	require.NotNil(t, fresh)
	require.NotNil(t, fresh())
}

func TestCodexTelemetryConnectConflictingIdentityIsUnknown(t *testing.T) {
	input := telemetryConnectTestInput()
	for _, test := range []struct {
		headers  http.Header
		expected string
	}{
		{http.Header{"Session_id": {input.SessionID}}, input.SessionID},
		{http.Header{"Session_id": {input.SessionID}, "Session-Id": {input.SessionID}}, input.SessionID},
		{http.Header{"Session_id": {input.SessionID, "another-value"}}, ""},
		{http.Header{"Session_id": {input.SessionID}, "Session-Id": {input.InstallationID}}, ""},
		{http.Header{"Session_id": {input.SessionID}, "Session-Id": {""}}, ""},
	} {
		require.Equal(t, test.expected, codexTelemetryConnectUUID(test.headers, "session_id", "session-id"))
	}
}

func TestCodexTelemetryConnectHonorsThreePolicySwitches(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		for _, simulation := range []bool{false, true} {
			for _, observation := range []bool{false, true} {
				t.Run(fmt.Sprintf("%t/%t/%t", enabled, simulation, observation), func(t *testing.T) {
					s := telemetryConnectTestService(t)
					s.configured, s.simulationEnabled, s.observationEnabled = enabled, simulation, observation
					require.Equal(t, enabled && observation, s.PrepareConnect(telemetryConnectTestInput()) != nil)
				})
			}
		}
	}
	t.Run("environment", func(t *testing.T) {
		t.Setenv("CODEX_TELEMETRY_ENABLED", "false")
		s := telemetryConnectTestService(t)
		require.Nil(t, s.PrepareConnect(telemetryConnectTestInput()))
	})
	for _, change := range []func(*CodexTelemetryConnectInput){
		func(i *CodexTelemetryConnectInput) { i.AccountID = 0 },
		func(i *CodexTelemetryConnectInput) { i.OwnerAccountID = 0 },
		func(i *CodexTelemetryConnectInput) { i.InstallationID = "" },
		func(i *CodexTelemetryConnectInput) { i.AuthorizationGeneration = "" },
		func(i *CodexTelemetryConnectInput) { i.CredentialOS = "unknown" },
		func(i *CodexTelemetryConnectInput) { i.Model = strings.Repeat("a", 513) },
	} {
		s, input := telemetryConnectTestService(t), telemetryConnectTestInput()
		change(&input)
		require.Nil(t, s.PrepareConnect(input))
	}
}

func TestCodexTelemetryConnectQueueAndTrackerAreBounded(t *testing.T) {
	s, input := telemetryConnectTestService(t), telemetryConnectTestInput()
	for range codexTelemetryQueueSize + 10 {
		s.PrepareConnect(input)().Finish(true)
	}
	require.Len(t, s.mutations, codexTelemetryQueueSize)
	require.Equal(t, codexTelemetryQueueSize, s.mutationReservations)
	for range codexTelemetryQueueSize {
		s.applyRuntimeMutation(<-s.mutations)
	}
	require.Zero(t, s.mutationReservations)
	// Active entries cannot be evicted, and additional routes still collect
	// connect metrics without allocating more reauth tracking state.
	for index := range codexTelemetryMaxStates + 10 {
		input.SessionID = fmt.Sprintf("session-%d", index)
		require.NotNil(t, s.PrepareConnect(input)())
	}
	require.Len(t, s.connectStates, codexTelemetryMaxStates)
}

func TestCodexTelemetryConnectStaleDatabaseEpochAndRollback(t *testing.T) {
	s := telemetryConnectTestService(t)
	s.PrepareConnect(telemetryConnectTestInput())().Finish(true)
	_, err := s.store.SyncPolicy(context.Background(), true, false, true)
	require.NoError(t, err)
	s.applyRuntimeMutation(<-s.mutations)
	require.Zero(t, s.mutationReservations)
	store := s.store.(*MemoryCodexTelemetryStore)
	require.Empty(t, store.activities)
	require.Empty(t, store.batches)
	// Local policy synchronization then permits only fresh observations.
	policy, err := s.store.ReadPolicy(context.Background())
	require.NoError(t, err)
	s.applySharedPolicy(policy)
	s.PrepareConnect(telemetryConnectTestInput())().Finish(true)
	s.applyRuntimeMutation(<-s.mutations)
	require.NotEmpty(t, store.activities)
}

func TestCodexTelemetryConnectFinishConcurrentAndNilSafe(t *testing.T) {
	var absent *CodexTelemetryConnectObserver
	absent.Finish(false)
	s := telemetryConnectTestService(t)
	o := s.PrepareConnect(telemetryConnectTestInput())()
	var wg sync.WaitGroup
	for range 32 {
		wg.Go(func() { o.Finish(true) })
	}
	wg.Wait()
	require.Len(t, s.mutations, 1)
	require.Equal(t, 1, s.mutationReservations)
	for _, state := range s.connectStates {
		require.Zero(t, state.active)
	}
}

func TestCodexTelemetryConnectWireSnapshotDoesNotRetainBusinessObjects(t *testing.T) {
	s := telemetryConnectTestService(t)
	gateway := &OpenAIGatewayService{codexTelemetry: s}
	input := telemetryConnectTestInput()
	proxy := input.ProxyID
	account := &Account{ID: input.AccountID, Name: input.AccountName, Platform: PlatformOpenAI, Type: AccountTypeOAuth,
		ProxyID: &proxy, OpenAIOAuthCredentialOwnerID: input.OwnerAccountID, OpenAIOAuthCredentialOS: input.CredentialOS,
		OpenAIOAuthAuthorizationGeneration: input.AuthorizationGeneration, OpenAIOAuthCredentialRevision: input.CredentialRevision,
		Credentials: map[string]any{"access_token": "secret-do-not-retain"}}
	headers := http.Header{}
	headers.Set("User-Agent", input.UserAgent)
	headers.Set("originator", input.Originator)
	headers.Set(codexInstallationIDKey, input.InstallationID)
	headers.Set("session_id", input.SessionID)
	headers.Set("Authorization", "Bearer do-not-retain")
	headers.Set("X-OpenAI-Product-Sku", "codex")
	ctx, cancel := context.WithCancel(context.Background())
	factory := gateway.prepareCodexTelemetryConnect(ctx, account, headers, "wss://user:secret@example.test/responses?token=do-not-retain", input.Model)
	require.NotNil(t, factory)
	cancel()
	account.Name, account.OpenAIOAuthAuthorizationGeneration, proxy = "changed", "changed", 999
	headers.Set("User-Agent", "changed")
	headers.Set("X-OpenAI-Product-Sku", "changed")
	o := factory()
	require.Equal(t, input.UserAgent, o.input.UserAgent)
	require.Equal(t, "codex", o.input.ProductSKU)
	require.Equal(t, input.AuthorizationGeneration, o.input.AuthorizationGeneration)
	require.Equal(t, "wss://example.test/responses#proxy=1", o.input.Route)
	require.Equal(t, int64(1), o.input.ProxyID)
	body, err := json.Marshal(o.input)
	require.NoError(t, err)
	require.NotContains(t, string(body), "secret")
	require.NotContains(t, string(body), "do-not-retain")
	o.Finish(true)
	require.Len(t, s.mutations, 1, "request cancellation does not lose a physical failure/success measurement")
}

func TestCodexTelemetryConnectUsesFinalPhysicalHeaders(t *testing.T) {
	s, input := telemetryConnectTestService(t), telemetryConnectTestInput()
	headers := http.Header{}
	headers.Set("User-Agent", input.UserAgent)
	headers.Set("originator", "final-originator")
	headers.Set("version", "0.169.1")
	headers.Set(codexInstallationIDKey, input.InstallationID)
	headers.Set("session_id", input.SessionID)
	headers.Set("X-OpenAI-Product-Sku", "final-vendor")
	// Cross-origin redirect stripped residency; it must not be restored from
	// the earlier factory snapshot even though all connection identity agrees.
	o := s.PrepareConnect(input)()
	o.FinishWithHeaders(true, headers)
	m := (<-s.mutations).connect
	require.Equal(t, "", m.profile.input.CodexResidency)
	require.Equal(t, "final-vendor", m.profile.input.ProductSKU)
	require.Equal(t, "final-originator", m.profile.client.originator)
	require.Equal(t, "0.169.1", m.profile.client.version)
	headers.Set("originator", "mutated-after-finish")
	require.Equal(t, "final-originator", m.profile.client.originator)
	o.FinishWithHeaders(false, http.Header{})
	require.Empty(t, s.mutations)
	metrics := newCodexTelemetryMetricStore()
	metrics.recordConnect(*m)
	batches := metrics.flush(m.finished.Add(2 * time.Minute))
	require.Len(t, batches, 1)
	point := codexMetricsTestMetric(batches[0].body, "codex.websocket.connect").Get("sum.dataPoints.0")
	require.Equal(t, "other", codexMetricsTestAttribute(point, "product_sku"), "arbitrary SKU values must not create unbounded metric labels")
}

func TestCodexTelemetryConnectFinalIdentityChangeDoesNotRelabelBaseline(t *testing.T) {
	s, input, now := telemetryConnectTestService(t), telemetryConnectTestInput(), time.Now()
	telemetryConnectTestFinish(t, s, input, now, true)
	input.CredentialRevision++
	headers := http.Header{}
	headers.Set("User-Agent", input.UserAgent)
	headers.Set(codexInstallationIDKey, input.InstallationID)
	headers.Set("session_id", "431335ab-3451-462d-bd21-d4e57733cbb0")
	o := s.PrepareConnect(input)()
	o.FinishWithHeaders(true, headers)
	m := (<-s.mutations).connect
	require.Nil(t, m.reauth)
	require.Equal(t, headers.Get("session_id"), m.profile.input.SessionID)
	next := telemetryConnectTestFinish(t, s, input, now.Add(2*time.Second), true)
	require.NotNil(t, next.reauth)
	require.True(t, *next.reauth, "a physically different session must not update the frozen session's success revision")
	missing := s.PrepareConnect(input)()
	missing.FinishWithHeaders(true, http.Header{})
	require.Empty(t, s.mutations, "missing actual installation identity must not borrow an old telemetry pool")
	for _, state := range s.connectStates {
		require.Zero(t, state.active)
	}
}

func TestCodexTelemetryConnectV3SnapshotAndFrozenSealedPayload(t *testing.T) {
	s, input, now := telemetryConnectTestService(t), telemetryConnectTestInput(), time.Now().UTC()
	m := telemetryConnectTestFinish(t, s, input, now, true)
	metrics := newCodexTelemetryMetricStore()
	metrics.recordConnect(m)
	snapshot, err := marshalCodexTelemetryMetricStore(metrics)
	require.NoError(t, err)
	var version struct {
		Version int `json:"version"`
	}
	require.NoError(t, json.Unmarshal(snapshot, &version))
	require.Equal(t, 3, version.Version)
	restored, err := unmarshalCodexTelemetryMetricStore(snapshot)
	require.NoError(t, err)
	batches := restored.flush(now.Add(2 * time.Minute))
	require.Len(t, batches, 1)
	require.ElementsMatch(t, []string{"codex.websocket.connect", "codex.websocket.connect.duration_ms"}, batches[0].names)
	sealed := append([]byte(nil), batches[0].body...)
	metrics.recordConnect(telemetryConnectTestFinish(t, s, input, now.Add(3*time.Minute), false))
	require.Equal(t, sealed, batches[0].body, "later samples cannot change a sealed payload")
}
