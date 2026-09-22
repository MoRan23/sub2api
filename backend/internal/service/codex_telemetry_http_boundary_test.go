package service

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openaicookies"
	"github.com/stretchr/testify/require"
)

type codexTelemetryBoundaryUpstream struct {
	*httpUpstreamRecorder
	beforeSend func(*http.Request)
}

func (u *codexTelemetryBoundaryUpstream) Do(request *http.Request, proxy string, accountID int64, concurrency int) (*http.Response, error) {
	return openaicookies.NewManager().Wrap(openAIPluginRoundTripFunc(func(outbound *http.Request) (*http.Response, error) {
		if u.beforeSend != nil {
			u.beforeSend(outbound)
		}
		return u.httpUpstreamRecorder.Do(outbound, proxy, accountID, concurrency)
	})).RoundTrip(request)
}

func TestCodexTelemetryHTTPRejectedBundleStartsOnlyBaselineAttempt(t *testing.T) {
	telemetry, sent := telemetryCaptureService(t)
	owner := osIdentityTestAccount(t, 741)
	repo := newAuthorizedOpenAIOAuthTestRepo(owner)
	account, err := ResolveOpenAIOAuthCredentialAccount(context.Background(), repo, owner, OpenAIOSWindows)
	require.NoError(t, err)
	upstream := &codexTelemetryBoundaryUpstream{httpUpstreamRecorder: &httpUpstreamRecorder{resp: openAICompatSSECompletedResponse("resp_actual_boundary", "gpt-5.4")}}
	gateway := &OpenAIGatewayService{codexTelemetry: telemetry, accountRepo: repo, httpUpstream: upstream, pluginManager: &PluginManager{}}
	newRequest := func(ctx context.Context) *http.Request {
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, chatgptCodexURL, strings.NewReader(`{"model":"gpt-5.4","input":"private input","client_metadata":{"turn_id":"018f5c3c-6e3a-7abf-8def-1234567890af"}}`))
		require.NoError(t, err)
		request.Header.Set("User-Agent", owner.OpenAIOAuthOSProfiles.Profiles[OpenAIOSWindows].UserAgent)
		request.Header.Set("Authorization", "Bearer fixture-token")
		request.Header.Set("Chatgpt-Account-Id", "fixture-account")
		request.Header.Set("Session_id", "018f5c3c-6e3a-7abf-8def-1234567890ae")
		request.Header.Set("Thread_id", "018f5c3c-6e3a-7abf-8def-1234567890ae")
		return markCodexTelemetryHTTPRequest(request, context.Background())
	}
	ctx := openaicookies.WithBundle(context.Background(), pluginCookieBundleFixture(time.Now()))
	ctx = openaicookies.WithSendGuard(ctx, func(*http.Request) bool { return false })
	ctx = openaicookies.WithRejectedSendError(ctx, openaicookies.ErrBundleSendRejected)
	rejected := newRequest(ctx)
	_, err = gateway.doOpenAIUpstream(rejected, "http://old-route.invalid:8080", account)
	require.ErrorIs(t, err, openaicookies.ErrBundleSendRejected)
	require.Nil(t, upstream.lastReq)
	telemetry.mu.Lock()
	attempts, pools, reservations := telemetry.counters.Attempts, len(telemetry.transportInputs), telemetry.mutationReservations
	telemetry.mu.Unlock()
	require.Zero(t, attempts, "a rejected local send cannot begin or skip a telemetry attempt")
	require.Zero(t, pools)
	require.Zero(t, reservations)
	require.Empty(t, sent())
	upstream.beforeSend = func(outbound *http.Request) {
		telemetry.mu.Lock()
		attempts := telemetry.counters.Attempts
		telemetry.mu.Unlock()
		require.EqualValues(t, 1, attempts, "the baseline starts telemetry at its physical send boundary")
		require.Empty(t, outbound.Header.Get("Cookie"))
	}
	baseline := newRequest(openaicookies.Bypass(context.Background()))
	response, err := gateway.doOpenAIUpstream(baseline, "", account)
	require.NoError(t, err)
	require.NotNil(t, response)
	require.NoError(t, response.Body.Close())
	telemetryWaitDrained(t, telemetry)
	telemetry.mu.Lock()
	attempts = telemetry.counters.Attempts
	telemetry.mu.Unlock()
	require.EqualValues(t, 1, attempts)
	calls := sent()
	require.NotEmpty(t, calls, "telemetry observations: %+v", telemetry.Observations(CodexTelemetryObservationQuery{}))
	for _, call := range calls {
		require.Empty(t, call.input.ProxyURL, "the rejected ticket route cannot enter telemetry")
	}
}
