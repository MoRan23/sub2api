package service

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const codexCollectorCompleteFrame = "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n"

func TestCodexTurnStateCollectorRequiresCompleteSuccessfulStream(t *testing.T) {
	token := codexStateTestToken(10, time.Now().Add(-time.Minute))
	for _, tc := range []struct {
		name, stream string
		want         error
	}{
		{"missing_completion_status", "data: {\"type\":\"response.completed\"}\n\n", errCodexTurnStateCollectorResponseIncomplete},
		{"unterminated_completion", strings.TrimSuffix(codexCollectorCompleteFrame, "\n"), errCodexTurnStateCollectorResponseIncomplete},
		{"unterminated_tail", codexCollectorCompleteFrame + "data: {}", errCodexTurnStateCollectorResponseIncomplete},
		{"failure_after_completion", codexCollectorCompleteFrame + "data: {\"type\":\"error\"}\n\n", errCodexTurnStateCollectorResponseFailed},
		{"incomplete_after_completion", codexCollectorCompleteFrame + "data: {\"response\":{\"status\":\"incomplete\"}}\n\n", errCodexTurnStateCollectorResponseIncomplete},
		{"failed_root_status", "data: {\"type\":\"response.completed\",\"status\":\"failed\",\"response\":{\"status\":\"completed\"}}\n\n", errCodexTurnStateCollectorResponseFailed},
		{"failed_nested_status", "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"failed\"}}\n\n", errCodexTurnStateCollectorResponseFailed},
		{"nested_error", codexCollectorCompleteFrame + "data: {\"response\":{\"error\":{\"code\":\"failed\"}}}\n\n", errCodexTurnStateCollectorResponseFailed},
		{"invalid_tail_json", codexCollectorCompleteFrame + "data: {private\n\n", errCodexTurnStateCollectorResponseIncomplete},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err, body := codexStateCollectSSETest(t, tc.stream, token)
			require.ErrorIs(t, err, tc.want)
			require.False(t, result.completed)
			require.Empty(t, result.Tokens)
			require.True(t, body.closed)
		})
	}
}

func TestCodexTurnStateCollectorTotalResponseBudget(t *testing.T) {
	// Individually small, valid frames ensure this exercises the total budget,
	// rather than the separate per-event or scanner-line limit.
	frame := "data: {\"type\":\"response.output_text.delta\",\"delta\":\"" + strings.Repeat("x", 60<<10) + "\"}\n\n"
	stream := codexCollectorCompleteFrame + strings.Repeat(frame, 18)
	result, err, body := codexStateCollectSSETest(t, stream, codexStateTestToken(10, time.Now()))
	require.ErrorIs(t, err, errCodexTurnStateCollectorEventTooLarge)
	require.LessOrEqual(t, body.read, (1<<20)+1)
	require.False(t, result.completed)
	require.Empty(t, result.Tokens)
}

func TestCodexTurnStateCollectorRequiresHTTP200(t *testing.T) {
	_, _, account := newCodexStateTestService(t)
	collector := NewCodexTurnStateHTTPCollector(func(context.Context, CodexTurnStateCollectRequest, *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusCreated, Header: http.Header{"X-Codex-Turn-State": {codexStateTestToken(10, time.Now())}}, Body: io.NopCloser(strings.NewReader(codexCollectorCompleteFrame))}, nil
	})
	result, err := collector.Collect(context.Background(), CodexTurnStateCollectRequest{Account: account, Model: "gpt-5", ProxyID: 2})
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, result.StatusCode)
	require.False(t, result.completed)
	require.Empty(t, result.Tokens)
}

func TestCodexTurnStateCollectorCapturesFrozenTransportBinding(t *testing.T) {
	account, proxy := codexCollectorTransportFixture()
	upstream := &codexCollectorTransportUpstream{}
	nativeDo := ProvideCodexTurnStateCollectorHTTPDo(codexCollectorTransportAccounts{account: account}, codexCollectorTransportProxies{proxy: proxy}, upstream)
	collector := NewCodexTurnStateHTTPCollector(func(ctx context.Context, input CodexTurnStateCollectRequest, request *http.Request) (*http.Response, error) {
		response, err := nativeDo(ctx, input, request)
		if response != nil {
			_ = response.Body.Close()
			response.Body = io.NopCloser(strings.NewReader(codexCollectorCompleteFrame))
		}
		return response, err
	})
	actualURL := proxy.URL()
	want := CodexTurnStateBundleBinding{WireMode: "lite", EgressKind: "proxy", ProxyID: proxy.ID, ProxyRouteGeneration: proxy.RouteGeneration}
	result, err := collector.Collect(context.Background(), CodexTurnStateCollectRequest{
		Account: account, Model: "gpt-5", ProxyID: proxy.ID,
		validateModelPolicy: func(context.Context) bool {
			// Simulate a concurrently updated repository object after route choice.
			proxy.Host = "next-route.invalid"
			proxy.RouteGeneration++
			return true
		},
	})
	require.NoError(t, err)
	require.True(t, result.completed)
	require.Equal(t, actualURL, upstream.proxyURL)
	require.Equal(t, want, result.BundleBinding)
	require.NotEqual(t, proxy.RouteGeneration, result.BundleBinding.ProxyRouteGeneration)
}
