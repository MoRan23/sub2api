package service

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/httpsendobserver"
	"github.com/stretchr/testify/require"
)

type codexTelemetryBoundaryUpstream struct {
	*httpUpstreamRecorder
	beforeSend func(*http.Request)
}

func (u *codexTelemetryBoundaryUpstream) Do(request *http.Request, proxy string, accountID int64, concurrency int) (*http.Response, error) {
	return httpsendobserver.Wrap(openAIPluginRoundTripFunc(func(outbound *http.Request) (*http.Response, error) {
		if u.beforeSend != nil {
			u.beforeSend(outbound)
		}
		return u.httpUpstreamRecorder.Do(outbound, proxy, accountID, concurrency)
	})).RoundTrip(request)
}

func TestCodexTelemetryHTTPStartsAtPhysicalSendWithoutCookieFeature(t *testing.T) {
	telemetry, sent := telemetryCaptureService(t)
	owner := osIdentityTestAccount(t, 741)
	repo := newAuthorizedOpenAIOAuthTestRepo(owner)
	account, err := ResolveOpenAIOAuthCredentialAccount(context.Background(), repo, owner, OpenAIOSWindows)
	require.NoError(t, err)
	upstream := &codexTelemetryBoundaryUpstream{httpUpstreamRecorder: &httpUpstreamRecorder{resp: openAICompatSSECompletedResponse("resp_actual_boundary", "gpt-5.4")}}
	gateway := &OpenAIGatewayService{codexTelemetry: telemetry, accountRepo: repo, httpUpstream: upstream, pluginManager: &PluginManager{}}
	request, err := http.NewRequestWithContext(context.Background(), http.MethodPost, chatgptCodexURL, strings.NewReader(`{"model":"gpt-5.4","input":"private input","client_metadata":{"turn_id":"018f5c3c-6e3a-7abf-8def-1234567890af"}}`))
	require.NoError(t, err)
	request.Header.Set("User-Agent", owner.OpenAIOAuthOSProfiles.Profiles[OpenAIOSWindows].UserAgent)
	request.Header.Set("Authorization", "Bearer fixture-token")
	request.Header.Set("Chatgpt-Account-Id", "fixture-account")
	request.Header.Set("Session_id", "018f5c3c-6e3a-7abf-8def-1234567890ae")
	request.Header.Set("Thread_id", "018f5c3c-6e3a-7abf-8def-1234567890ae")
	request = markCodexTelemetryHTTPRequest(request, context.Background())
	upstream.beforeSend = func(outbound *http.Request) {
		telemetry.mu.Lock()
		attempts := telemetry.counters.Attempts
		telemetry.mu.Unlock()
		require.EqualValues(t, 1, attempts)
		require.Empty(t, outbound.Header.Get("Cookie"))
	}
	response, err := gateway.doOpenAIUpstream(request, "", account)
	require.NoError(t, err)
	require.NotNil(t, response)
	require.NoError(t, response.Body.Close())
	telemetryWaitDrained(t, telemetry)
	telemetry.mu.Lock()
	attempts := telemetry.counters.Attempts
	telemetry.mu.Unlock()
	require.EqualValues(t, 1, attempts, "unhandled plugin fallback cannot count a second send")
	calls := sent()
	require.NotEmpty(t, calls, "telemetry observations: %+v", telemetry.Observations(CodexTelemetryObservationQuery{}))
	for _, call := range calls {
		require.Empty(t, call.input.ProxyURL)
	}
}
