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
	return CodexTelemetryInput{AccountID: 11, AccountName: "example", AccessToken: "private-oauth-token", ChatGPTAccountID: "chatgpt-account",
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
		v := s.Observations(CodexTelemetryObservationQuery{})
		return v.QueueDepth == 0 && v.Counters.Queued == v.Counters.Sent+v.Counters.Failed+v.Counters.Cancelled
	}, 5*time.Second, time.Millisecond)
}

func TestCodexTelemetryAuthenticationAndObservationIsolation(t *testing.T) {
	t.Setenv("CODEX_STATSIG_API_KEY", "metrics-sdk-key")
	s, calls := telemetryCaptureService(t)
	in := telemetryTestInput()
	a := s.Begin(context.Background(), in)
	require.NotNil(t, a)
	a.Finish(CodexTelemetryResult{Status: "completed", ResponseID: "resp-real", InputTokens: 42, OutputTokens: 9})
	s.flushMetrics(time.Now())
	telemetryWaitDrained(t, s)
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
	first := s.Begin(ctx, in)
	first.Retry(CodexTelemetryResult{Status: "failed", HTTPStatus: 503})
	second := s.Begin(ctx, in)
	second.Finish(CodexTelemetryResult{Status: "completed", HTTPStatus: 200, ResponseID: "response-2", InputTokens: 30})
	cancel()
	telemetryWaitDrained(t, s)
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
			}
		}
	}
	require.Equal(t, 1, mainTurns)
	require.EqualValues(t, 2, s.Observations(CodexTelemetryObservationQuery{}).Counters.Attempts)
}

func TestCodexTelemetryRetryPreservesPhysicalMeasurementsAndFinalModel(t *testing.T) {
	s, calls := telemetryCaptureService(t)
	in := telemetryTestInput()
	in.WebSocket = true
	in.Model = "first-model"
	in.StartedAt = time.Now().Add(-time.Second)
	first := s.Begin(context.Background(), in)
	first.Retry(CodexTelemetryResult{Status: "failed", HTTPStatus: 503,
		FirstEventAt: in.StartedAt.Add(10 * time.Millisecond), FinishedAt: in.StartedAt.Add(100 * time.Millisecond)})
	in.Model = "final-model"
	in.StartedAt = in.StartedAt.Add(200 * time.Millisecond)
	second := s.Begin(context.Background(), in)
	second.Finish(CodexTelemetryResult{Status: "completed", ServiceTier: "priority", HTTPStatus: 200,
		FirstEventAt: in.StartedAt.Add(20 * time.Millisecond), FinishedAt: in.StartedAt.Add(300 * time.Millisecond)})
	s.flushMetrics(time.Now())
	telemetryWaitDrained(t, s)
	mainTurns := 0
	physicalModels := map[string]string{}
	for _, call := range calls() {
		if call.metrics {
			for _, point := range codexMetricsTestMetric(call.body, "codex.websocket.request").Get("sum.dataPoints").Array() {
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
				require.EqualValues(t, 2, event.EventParams["sampling_request_count"])
				require.EqualValues(t, 1, event.EventParams["sampling_retry_count"])
			}
		}
	}
	require.Equal(t, 1, mainTurns)
	require.Equal(t, map[string]string{"first-model": "false", "final-model": "true"}, physicalModels)
}

func TestCodexTelemetryRequestEndFinalizesPendingFailure(t *testing.T) {
	s, calls := telemetryCaptureService(t)
	ctx, cancel := context.WithCancel(context.Background())
	a := s.Begin(ctx, telemetryTestInput())
	a.Retry(CodexTelemetryResult{Status: "failed", HTTPStatus: 429})
	cancel()
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
	telemetryWaitDrained(t, s)
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
	require.NotSame(t, a.turn, b.turn)
	a.Finish(CodexTelemetryResult{Status: "completed"})
	b.Finish(CodexTelemetryResult{Status: "completed"})
	s.flushMetrics(time.Now())
	telemetryWaitDrained(t, s)
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
	result := s.Observations(CodexTelemetryObservationQuery{Status: "skipped"})
	require.Equal(t, 2, result.Total)
	for _, item := range result.Items {
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
	require.Eventually(t, func() bool {
		v := s.Observations(CodexTelemetryObservationQuery{})
		return v.QueueDepth == 0 && v.Counters.Cancelled == v.Counters.Queued
	}, 5*time.Second, time.Millisecond)
	before := s.Observations(CodexTelemetryObservationQuery{})
	require.False(t, before.EffectiveEnabled)
	require.Nil(t, s.Begin(context.Background(), in))
	s.SetEnabled(true)
	a.Finish(CodexTelemetryResult{Status: "completed"})
	s.flushMetrics(time.Now())
	after := s.Observations(CodexTelemetryObservationQuery{})
	require.Equal(t, before.Counters.Queued, after.Counters.Queued)
	require.True(t, after.EffectiveEnabled)
}

func TestCodexTelemetryQueueAndHistoryAreBounded(t *testing.T) {
	t.Setenv("CODEX_TELEMETRY_ENABLED", "true")
	s := NewCodexTelemetryService(nil)
	t.Cleanup(s.Stop)
	s.SetSender(func(ctx context.Context, _ *http.Request, _ CodexTelemetryInput, _ bool) (*http.Response, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})
	for i := 0; i < 300; i++ {
		in := telemetryTestInput()
		in.TurnID = "turn-" + strconv.Itoa(i)
		s.Begin(context.Background(), in).Finish(CodexTelemetryResult{Status: "completed"})
	}
	v := s.Observations(CodexTelemetryObservationQuery{PageSize: 1000})
	require.LessOrEqual(t, v.QueueDepth, 256)
	require.Greater(t, v.Counters.Dropped, uint64(0))
	require.Equal(t, 500, v.Total)
	require.Equal(t, 100, v.PageSize)
	require.Len(t, v.Items, 100)
	filtered := s.Observations(CodexTelemetryObservationQuery{Status: "dropped", AccountID: 11, Type: "analytics"})
	require.Greater(t, filtered.Total, 0)
	for _, item := range filtered.Items {
		require.Equal(t, "dropped", item.Status)
		require.Equal(t, "queue_full", item.Error)
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
	v := s.Observations(CodexTelemetryObservationQuery{Status: "failed"})
	require.Greater(t, v.Total, 0)
	for _, item := range v.Items {
		require.Equal(t, "transport_error", item.Error)
	}
}
