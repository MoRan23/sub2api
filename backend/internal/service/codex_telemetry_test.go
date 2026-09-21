package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type codexTelemetrySent struct {
	headers http.Header
	body    []byte
	input   CodexTelemetryInput
	metrics bool
}

func telemetryTestInput() CodexTelemetryInput {
	return CodexTelemetryInput{AccountID: 11, OwnerAccountID: 11, OSFamily: "linux", AccountName: "example", AccessToken: "private-oauth-token", ChatGPTAccountID: "chatgpt-account",
		ProxyURL: "http://proxy-user:proxy-secret@localhost:3128", UserAgent: "codex-tui/0.154.0 (Ubuntu 24.04; x86_64)",
		Originator: "codex_cli_rs", Version: "0.154.0", SessionID: "actual-root", ThreadID: "actual-thread", TurnID: "actual-turn",
		ParentThreadID: "actual-parent", ParentTurnID: "actual-parent-turn", RootTurnID: "actual-root-turn", Model: "gpt-6-astra",
		Effort: "medium", ServiceTier: "default", StartedAt: time.Now().Add(-time.Second)}
}

func telemetryCaptureService(t *testing.T) (*CodexTelemetryService, func() []codexTelemetrySent) {
	t.Helper()
	t.Setenv("CODEX_TELEMETRY_ENABLED", "true")
	s := NewCodexTelemetryService(nil)
	var mu sync.Mutex
	var calls []codexTelemetrySent
	s.SetSender(func(ctx context.Context, req *http.Request, input CodexTelemetryInput, metrics bool) (*http.Response, error) {
		body, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		if !HTTPUpstreamRedirectsDisabled(ctx) || HTTPUpstreamProfileFromContext(ctx) != HTTPUpstreamProfileCodexAuxiliary {
			return nil, errors.New("missing isolated transport policy")
		}
		mu.Lock()
		calls = append(calls, codexTelemetrySent{req.Header.Clone(), body, input, metrics})
		mu.Unlock()
		return &http.Response{StatusCode: http.StatusAccepted, Body: io.NopCloser(strings.NewReader("{}"))}, nil
	})
	t.Cleanup(s.Stop)
	return s, func() []codexTelemetrySent {
		mu.Lock()
		defer mu.Unlock()
		return append([]codexTelemetrySent{}, calls...)
	}
}

func telemetryWaitDrained(t *testing.T, s *CodexTelemetryService) {
	t.Helper()
	require.Eventually(t, func() bool {
		s.mu.Lock()
		pending, store := s.mutationReservations, s.store
		s.mu.Unlock()
		if pending != 0 {
			return false
		}
		s.dispatchPersistedBatches(context.Background())
		if memory, ok := store.(*MemoryCodexTelemetryStore); ok {
			memory.mu.Lock()
			pendingBatches := false
			for _, b := range memory.batches {
				if !memoryCodexTelemetryTerminal(b.Status) && !b.NotBefore.After(time.Now()) {
					pendingBatches = true
					break
				}
			}
			memory.mu.Unlock()
			if pendingBatches {
				return false
			}
		}
		v := s.Observations(CodexTelemetryObservationQuery{})
		// Skipped and queue-full observations were never enqueued, so they do
		// not belong on the completed side of the queue accounting identity.
		return v.QueueDepth == 0 && v.Counters.Queued == v.Counters.Sent+v.Counters.Failed+v.Counters.Cancelled+v.Counters.Unknown
	}, 5*time.Second, time.Millisecond)
}

// Advance only the persisted Delta collection window. The scheduler and sender
// keep real wall time, so no test sleeps for a full exporter interval or creates
// future-dated batches that could not yet be dispatched.
func telemetryFlushMetrics(t *testing.T, s *CodexTelemetryService) {
	t.Helper()
	telemetryWaitDrained(t, s)
	s.mu.Lock()
	store := s.store
	s.mu.Unlock()
	memory, ok := store.(*MemoryCodexTelemetryStore)
	require.True(t, ok)
	memory.mu.Lock()
	keys := make([]CodexTelemetryPoolKey, 0, len(memory.pools))
	for key := range memory.pools {
		keys = append(keys, key)
	}
	memory.mu.Unlock()
	now := time.Now().UTC()
	for _, key := range keys {
		_, err := store.TransactPool(context.Background(), key, now, func(tx *CodexTelemetryPoolTransaction) error {
			metrics, err := loadRuntimeMetrics(tx)
			if err != nil {
				return err
			}
			for _, state := range metrics.states {
				state.collectedAt = now.Add(-time.Minute)
			}
			return saveRuntimeMetrics(tx, metrics, now)
		})
		require.NoError(t, err)
	}
	s.flushMetrics(now)
	telemetryWaitDrained(t, s)
}

func telemetryNextTurn(t *testing.T, s *CodexTelemetryService, in CodexTelemetryInput) {
	t.Helper()
	telemetryWaitDrained(t, s)
	in.TurnID, in.SamplingID = in.TurnID+":next", "boundary-sampling"
	in.Model, in.StartedAt = "boundary-model", time.Now()
	a := s.Begin(context.Background(), in)
	require.NotNil(t, a)
	a.Finish(CodexTelemetryResult{Status: "completed", HTTPStatus: 200})
	telemetryWaitDrained(t, s)
}

func TestCodexTelemetryAuthenticationAndObservationIsolation(t *testing.T) {
	t.Setenv("CODEX_STATSIG_API_KEY", "metrics-sdk-key")
	s, calls := telemetryCaptureService(t)
	in := telemetryTestInput()
	a := s.Begin(context.Background(), in)
	require.NotNil(t, a)
	a.Finish(CodexTelemetryResult{Status: "completed", ResponseID: "resp-real", InputTokens: 42, OutputTokens: 9})
	telemetryFlushMetrics(t, s)
	seenAnalytics, seenMetrics := false, false
	for _, sent := range calls() {
		require.Equal(t, in.ProxyURL, sent.input.ProxyURL)
		if sent.metrics {
			seenMetrics = true
			require.Empty(t, sent.headers.Get("Authorization"))
			require.Empty(t, sent.headers.Get("Chatgpt-Account-Id"))
			require.Equal(t, "metrics-sdk-key", sent.headers.Get("statsig-api-key"))
		} else {
			seenAnalytics = true
			require.Equal(t, "Bearer "+in.AccessToken, sent.headers.Get("Authorization"))
			require.Equal(t, in.ChatGPTAccountID, sent.headers.Get("Chatgpt-Account-Id"))
			require.Empty(t, sent.headers.Get("statsig-api-key"))
		}
	}
	require.True(t, seenAnalytics && seenMetrics)
	result := s.Observations(CodexTelemetryObservationQuery{PageSize: 100})
	data, err := json.Marshal(result)
	require.NoError(t, err)
	for _, secret := range []string{in.AccessToken, "proxy-secret", "metrics-sdk-key", "Authorization"} {
		require.NotContains(t, string(data), secret)
	}
	for _, item := range result.Items {
		if item.Type == "metrics" {
			require.Empty(t, item.SessionID)
			require.Empty(t, item.ThreadID)
			require.Empty(t, item.TurnID)
		}
	}
}

func TestCodexTelemetryRetryCommitsOneFinalTurn(t *testing.T) {
	s, calls := telemetryCaptureService(t)
	ctx, cancel := context.WithCancel(context.Background())
	in := telemetryTestInput()
	in.SamplingID = "same-logical-sampling"
	first := s.Begin(ctx, in)
	first.Retry(CodexTelemetryResult{Status: "failed", HTTPStatus: 503})
	second := s.Begin(ctx, in)
	second.Finish(CodexTelemetryResult{Status: "completed", HTTPStatus: 200, ResponseID: "response-2", InputTokens: 30})
	cancel()
	telemetryWaitDrained(t, s)
	require.EqualValues(t, 2, s.Observations(CodexTelemetryObservationQuery{}).Counters.Attempts)
	telemetryNextTurn(t, s, in)
	mainTurns := 0
	for _, call := range calls() {
		if call.metrics {
			continue
		}
		var payload struct {
			Events []codexAnalyticsEvent `json:"events"`
		}
		require.NoError(t, json.Unmarshal(call.body, &payload))
		for _, event := range payload.Events {
			if event.EventType == "codex_turn_event" && event.EventParams["thread_id"] == in.ThreadID {
				mainTurns++
				require.Equal(t, "completed", event.EventParams["status"])
				require.EqualValues(t, 1, event.EventParams["sampling_request_count"])
				require.EqualValues(t, 0, event.EventParams["sampling_retry_count"], "a gateway retry is not an observed client retry")
			}
		}
	}
	require.Equal(t, 1, mainTurns)
}

func TestCodexTelemetryRetryPreservesPhysicalMeasurementsAndFinalModel(t *testing.T) {
	s, calls := telemetryCaptureService(t)
	in := telemetryTestInput()
	in.WebSocket = true
	in.SamplingID = "same-logical-sampling"
	in.Model = "first-model"
	in.StartedAt = time.Now().Add(-time.Second)
	failedSend, successfulSend := false, true
	first := s.Begin(context.Background(), in)
	first.Retry(CodexTelemetryResult{Status: "failed", HTTPStatus: 503,
		SendSucceeded: &failedSend, SendDurationMS: 5,
		FirstEventAt: in.StartedAt.Add(10 * time.Millisecond), FinishedAt: in.StartedAt.Add(100 * time.Millisecond)})
	in.Model = "final-model"
	in.StartedAt = in.StartedAt.Add(200 * time.Millisecond)
	second := s.Begin(context.Background(), in)
	second.Finish(CodexTelemetryResult{Status: "completed", ServiceTier: "priority", HTTPStatus: 200,
		SendSucceeded: &successfulSend, SendDurationMS: 7,
		FirstEventAt: in.StartedAt.Add(20 * time.Millisecond), FinishedAt: in.StartedAt.Add(300 * time.Millisecond)})
	telemetryNextTurn(t, s, in)
	telemetryFlushMetrics(t, s)
	mainTurns := 0
	physicalModels := map[string]string{}
	for _, call := range calls() {
		if call.metrics {
			for _, point := range codexMetricsTestMetric(call.body, "codex.websocket.request").Get("sum.dataPoints").Array() {
				if codexMetricsTestAttribute(point, "model") == "boundary-model" {
					continue
				}
				require.EqualValues(t, 1, point.Get("asInt").Int())
				physicalModels[codexMetricsTestAttribute(point, "model")] = codexMetricsTestAttribute(point, "success")
			}
			continue
		}
		var payload struct {
			Events []codexAnalyticsEvent `json:"events"`
		}
		require.NoError(t, json.Unmarshal(call.body, &payload))
		for _, event := range payload.Events {
			if event.EventType == "codex_turn_event" && event.EventParams["thread_id"] == in.ThreadID {
				mainTurns++
				require.Equal(t, "final-model", event.EventParams["model"])
				require.Equal(t, "priority", event.EventParams["service_tier"])
				require.EqualValues(t, 1, event.EventParams["sampling_request_count"])
				require.EqualValues(t, 0, event.EventParams["sampling_retry_count"])
			}
		}
	}
	require.Equal(t, 1, mainTurns)
	require.Equal(t, map[string]string{"first-model": "false", "final-model": "true"}, physicalModels)
}

func TestCodexTelemetryRequestEndPreservesFailureUntilNextTurn(t *testing.T) {
	s, calls := telemetryCaptureService(t)
	ctx, cancel := context.WithCancel(context.Background())
	a := s.Begin(ctx, telemetryTestInput())
	a.Retry(CodexTelemetryResult{Status: "failed", HTTPStatus: 429})
	cancel()
	telemetryWaitDrained(t, s)
	for _, call := range calls() {
		require.NotContains(t, string(call.body), `"codex_error_http_status_code":429`, "context cancellation cannot invent a client turn boundary")
	}
	telemetryNextTurn(t, s, telemetryTestInput())
	require.Eventually(t, func() bool {
		for _, call := range calls() {
			if strings.Contains(string(call.body), `"status":"failed"`) && strings.Contains(string(call.body), `"codex_error_http_status_code":429`) {
				return true
			}
		}
		return false
	}, 5*time.Second, time.Millisecond)
}

func TestCodexTelemetryContextCancelDoesNotPreemptActiveTerminal(t *testing.T) {
	s, calls := telemetryCaptureService(t)
	ctx, cancel := context.WithCancel(context.Background())
	a := s.Begin(ctx, telemetryTestInput())
	cancel()
	a.finishOnContextEnd() // Force the callback-before-terminal ordering.
	a.Finish(CodexTelemetryResult{Status: "completed", ResponseID: "terminal-after-cancel"})
	telemetryNextTurn(t, s, telemetryTestInput())
	mainTurns := 0
	for _, call := range calls() {
		if call.metrics {
			continue
		}
		var payload struct {
			Events []codexAnalyticsEvent `json:"events"`
		}
		require.NoError(t, json.Unmarshal(call.body, &payload))
		for _, event := range payload.Events {
			if event.EventType == "codex_turn_event" && event.EventParams["thread_id"] == "actual-thread" {
				mainTurns++
				require.Equal(t, "completed", event.EventParams["status"])
				require.Nil(t, event.EventParams["explicit_client_interrupt_requested_at_ms"])
			}
		}
	}
	require.Equal(t, 1, mainTurns)
}

func TestCodexTelemetryRetryAfterContextCancelUsesFailure(t *testing.T) {
	s, calls := telemetryCaptureService(t)
	ctx, cancel := context.WithCancel(context.Background())
	a := s.Begin(ctx, telemetryTestInput())
	cancel()
	a.finishOnContextEnd() // Pending is empty; a later Retry must still finalize.
	a.Retry(CodexTelemetryResult{Status: "failed", HTTPStatus: 502})
	telemetryNextTurn(t, s, telemetryTestInput())
	require.Eventually(t, func() bool {
		for _, call := range calls() {
			if strings.Contains(string(call.body), `"status":"failed"`) && strings.Contains(string(call.body), `"codex_error_http_status_code":502`) {
				return true
			}
		}
		return false
	}, 5*time.Second, time.Millisecond)
}

func TestCodexTelemetryMissingTurnDoesNotInventOrDeduplicateIdentity(t *testing.T) {
	s, calls := telemetryCaptureService(t)
	in := telemetryTestInput()
	in.TurnID, in.RootTurnID = "", ""
	a, b := s.Begin(context.Background(), in), s.Begin(context.Background(), in)
	require.NotEqual(t, a.attemptID, b.attemptID)
	a.Finish(CodexTelemetryResult{Status: "completed"})
	b.Finish(CodexTelemetryResult{Status: "completed"})
	telemetryFlushMetrics(t, s)
	for _, call := range calls() {
		if call.metrics {
			continue
		}
		var payload struct {
			Events []codexAnalyticsEvent `json:"events"`
		}
		require.NoError(t, json.Unmarshal(call.body, &payload))
		for _, event := range payload.Events {
			require.False(t, event.EventType == "codex_turn_event" && event.EventParams["thread_id"] == in.ThreadID)
		}
	}
	_, err := s.store.TransactPool(context.Background(), a.poolKey, time.Now(), func(tx *CodexTelemetryPoolTransaction) error {
		for _, attempt := range []*CodexTelemetryAttempt{a, b} {
			turn, exists, err := runtimeActivity[codexTelemetryRuntimeTurn](tx, "turn", "request:"+attempt.attemptID)
			require.NoError(t, err)
			require.True(t, exists)
			require.False(t, turn.Explicit)
			require.Equal(t, 1, turn.Samples)
		}
		return nil
	})
	require.NoError(t, err)
	skipped := s.Observations(CodexTelemetryObservationQuery{Status: "skipped"})
	require.Equal(t, 2, skipped.Total)
	for _, item := range skipped.Items {
		require.Empty(t, item.TurnID)
		require.Equal(t, "missing_outbound_turn_identity", item.Error)
	}
}

func TestCodexTelemetryDisableCancelsQueuedAndInflightAndDoesNotReplay(t *testing.T) {
	t.Setenv("CODEX_TELEMETRY_ENABLED", "true")
	s := NewCodexTelemetryService(nil)
	t.Cleanup(s.Stop)
	entered := make(chan struct{}, 1)
	s.SetSender(func(ctx context.Context, _ *http.Request, _ CodexTelemetryInput, _ bool) (*http.Response, error) {
		select {
		case entered <- struct{}{}:
		default:
		}
		<-ctx.Done()
		return nil, ctx.Err()
	})
	in := telemetryTestInput()
	a := s.Begin(context.Background(), in)
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not start")
	}
	a.Finish(CodexTelemetryResult{Status: "completed"})
	s.SetEnabled(false)
	telemetryWaitDrained(t, s)
	before := s.Observations(CodexTelemetryObservationQuery{})
	require.Equal(t, before.Counters.Queued, before.Counters.Cancelled+before.Counters.Unknown)
	require.False(t, before.EffectiveEnabled)
	require.Nil(t, s.Begin(context.Background(), in))
	s.SetEnabled(true)
	a.Finish(CodexTelemetryResult{Status: "completed"})
	s.flushMetrics(time.Now())
	telemetryWaitDrained(t, s)
	after := s.Observations(CodexTelemetryObservationQuery{})
	require.Equal(t, before.Counters.Queued, after.Counters.Queued)
	require.True(t, after.EffectiveEnabled)
}

func TestCodexTelemetryQueueAndHistoryAreBounded(t *testing.T) {
	t.Setenv("CODEX_TELEMETRY_ENABLED", "true")
	s := NewCodexTelemetryService(nil)
	t.Cleanup(s.Stop)
	gate := &telemetryBlockedMutationStore{CodexTelemetryStore: NewMemoryCodexTelemetryStore(), release: make(chan struct{})}
	require.NoError(t, s.SetStore(gate))
	t.Cleanup(func() { close(gate.release) })
	s.SetSender(func(ctx context.Context, _ *http.Request, _ CodexTelemetryInput, _ bool) (*http.Response, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})
	for i := 0; i < 700; i++ {
		in := telemetryTestInput()
		in.TurnID = "turn-" + strconv.Itoa(i)
		a := s.Begin(context.Background(), in)
		if a != nil {
			a.Finish(CodexTelemetryResult{Status: "completed"})
		}
	}
	v := s.Observations(CodexTelemetryObservationQuery{PageSize: 1000})
	require.LessOrEqual(t, v.QueueDepth, 256)
	s.mu.Lock()
	reservations := s.mutationReservations
	s.mu.Unlock()
	require.LessOrEqual(t, reservations, 256, "each accepted business attempt reserves its result slot")
	require.Greater(t, v.Counters.Skipped, uint64(0))
	require.Equal(t, 500, v.Total)
	require.Equal(t, 100, v.PageSize)
	require.Len(t, v.Items, 100)
	filtered := s.Observations(CodexTelemetryObservationQuery{Status: "skipped", AccountID: 11, Type: "analytics"})
	require.Greater(t, filtered.Total, 0)
	for _, item := range filtered.Items {
		require.Equal(t, "skipped", item.Status)
		require.Equal(t, "mutation_queue_full", item.Error)
	}
	s.SetEnabled(false)
}

type telemetryBlockedMutationStore struct {
	CodexTelemetryStore
	release chan struct{}
}

func (s *telemetryBlockedMutationStore) TransactPool(ctx context.Context, key CodexTelemetryPoolKey, now time.Time, fn func(*CodexTelemetryPoolTransaction) error) (*CodexTelemetryPool, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-s.release:
		return s.CodexTelemetryStore.TransactPool(ctx, key, now, fn)
	}
}

func TestCodexTelemetryEnvironmentOverride(t *testing.T) {
	t.Setenv("CODEX_TELEMETRY_ENABLED", "false")
	s := NewCodexTelemetryService(nil)
	t.Cleanup(s.Stop)
	require.Nil(t, s.Begin(context.Background(), telemetryTestInput()))
	v := s.Observations(CodexTelemetryObservationQuery{})
	require.True(t, v.ConfiguredEnabled)
	require.False(t, v.EffectiveEnabled)
	require.Equal(t, "CODEX_TELEMETRY_ENABLED", v.ForcedOffReason)
}

func TestCodexTelemetryErrorsNeverExposeTransportCredentials(t *testing.T) {
	t.Setenv("CODEX_TELEMETRY_ENABLED", "true")
	s := NewCodexTelemetryService(nil)
	t.Cleanup(s.Stop)
	s.SetSender(func(context.Context, *http.Request, CodexTelemetryInput, bool) (*http.Response, error) {
		return nil, errors.New("proxy error http://proxy-user:proxy-secret@localhost:3128 and token private-oauth-token")
	})
	s.Begin(context.Background(), telemetryTestInput()).Finish(CodexTelemetryResult{Status: "failed"})
	telemetryWaitDrained(t, s)
	v := s.Observations(CodexTelemetryObservationQuery{Status: "unknown"})
	require.Greater(t, v.Total, 0)
	for _, item := range v.Items {
		require.Equal(t, "transport_error", item.Error)
	}
	raw, err := json.Marshal(v)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "proxy-secret")
	require.NotContains(t, string(raw), "private-oauth-token")
}
