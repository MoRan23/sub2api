package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openaicookies"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func prepareCodexHTTPActivityTest(t *testing.T, enabled bool) (*OpenAIGatewayService, *codexStateMemoryRepo, *Account, *http.Request, *CodexTurnStateAttempt, *codexTurnStateWireObservation) {
	t.Helper()
	state, repo, account := newCodexStateTestService(t)
	state.now = time.Now
	account.Extra[CodexTurnStateExtraKey].(map[string]any)["enabled"] = enabled
	gateway := &OpenAIGatewayService{codexTurnStateService: state, accountRepo: state.accounts}
	request := codexStateHTTPRequest(t, `{"model":"gpt-5","input":"private-prompt"}`)
	request.Header.Set(responsesLiteHeader, "true")
	request = request.WithContext(openaicookies.WithScope(request.Context(), openaicookies.Scope{
		OwnerAccountID: account.ID, OSFamily: "windows", AuthorizationGeneration: account.OpenAIOAuthAuthorizationGeneration,
	}))
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = request
	outbound := gateway.prepareOpenAICodexStateHTTPRequest(c, account, request)
	collector, _ := outbound.Context().Value(codexTurnStateHTTPRequestKey{}).(*codexTurnStateHTTPCollector)
	require.NotNil(t, collector)
	require.True(t, collector.attempt.deferHTTPActivity)
	gateway.recordFingerprintObservationWithBody(c, account, installationIDResolution{}, outbound.Header, []byte(`{"model":"gpt-5","input":"private-prompt"}`))
	observation := codexStateWireObservation(c)
	require.NotNil(t, observation)
	require.Zero(t, observation.summarySequence)
	require.True(t, observation.sendStartedAt.IsZero())
	require.Nil(t, observation.value.ActualProxyID)
	require.Nil(t, observation.value.RequestSentAt)
	require.False(t, collector.attempt.historyPhysicalBound)
	require.True(t, collector.attempt.businessSentAt.IsZero())
	return gateway, repo, account, outbound, collector.attempt, observation
}

func TestCodexTurnStateHTTPActivityRequiresTransportBoundary(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		for _, rejected := range []bool{false, true} {
			t.Run("enabled_"+testBoolName(enabled)+"/rejected_"+testBoolName(rejected), func(t *testing.T) {
				isolateCodexTurnStateSummaryStore(t)
				gateway, repo, _, request, attempt, observation := prepareCodexHTTPActivityTest(t, enabled)
				if enabled {
					before, err := repo.Get(context.Background(), attempt.key)
					require.NoError(t, err)
					require.True(t, before.LastEligibleCollectionAt.IsZero())
					require.Equal(t, time.Unix(0, 0), before.LastBusinessAt)
					require.Empty(t, before.DemandReason)
				}
				if rejected {
					ctx := openaicookies.WithSendGuard(request.Context(), func(*http.Request) bool { return false })
					request = request.WithContext(openaicookies.WithRejectedSendError(ctx, openaicookies.ErrBundleSendRejected))
				}
				calls := 0
				_, err := openaicookies.NewManager().Wrap(openAIPluginRoundTripFunc(func(*http.Request) (*http.Response, error) {
					calls++
					require.True(t, attempt.historyPhysicalBound, "the boundary records activity before the real send begins")
					require.False(t, attempt.businessSentAt.IsZero())
					require.NotNil(t, observation.value.RequestSentAt)
					require.NotNil(t, observation.value.ActualProxyID)
					require.Zero(t, *observation.value.ActualProxyID, "the selected direct route is exposed only once sent")
					if enabled {
						row, readErr := repo.Get(context.Background(), attempt.key)
						require.NoError(t, readErr)
						require.Equal(t, attempt.businessSentAt, row.LastBusinessAt)
						require.Equal(t, attempt.businessSentAt, row.LastEligibleCollectionAt)
						require.Equal(t, "business_active", row.DemandReason)
					}
					return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header)}, nil
				})).RoundTrip(request)
				if rejected {
					require.ErrorIs(t, err, openaicookies.ErrBundleSendRejected)
					require.Zero(t, calls)
					require.False(t, attempt.historyPhysicalBound)
					require.True(t, attempt.businessSentAt.IsZero())
					require.Zero(t, observation.summarySequence)
					require.Nil(t, observation.value.RequestSentAt)
					require.Nil(t, observation.value.ActualProxyID)
					require.Equal(t, "not_sent", observation.value.CookieDiagnostic.SendState)
					if enabled {
						row, readErr := repo.Get(context.Background(), attempt.key)
						require.NoError(t, readErr)
						require.Equal(t, time.Unix(0, 0), row.LastBusinessAt)
						require.True(t, row.LastEligibleCollectionAt.IsZero())
						require.Empty(t, row.DemandReason)
					}
				} else {
					require.NoError(t, err)
					require.Equal(t, 1, calls)
					require.Equal(t, "sent", observation.value.CookieDiagnostic.SendState)
					sentAt := attempt.businessSentAt
					observeCodexCookies(attempt, openaicookies.Diagnostic{SendState: "sent", Reason: "cookie_staged"})
					require.Equal(t, sentAt, attempt.businessSentAt, "response staging cannot extend the activity timestamp")
				}
				require.NoError(t, gateway.codexTurnStateService.Finish(context.Background(), attempt, false))
			})
		}
	}
}

func TestCodexTurnStateHTTPPluginPreflightDoesNotClaimActivity(t *testing.T) {
	for _, unavailable := range []bool{false, true} {
		t.Run("unavailable_"+testBoolName(unavailable), func(t *testing.T) {
			isolateCodexTurnStateSummaryStore(t)
			gateway, repo, account, request, attempt, observation := prepareCodexHTTPActivityTest(t, true)
			manager := &PluginManager{}
			if unavailable {
				manager.route.Store(&pluginRoute{pluginID: 1, rolloutPercent: 100, unavailable: "synthetic unavailable"})
			}
			response, handled, err := roundTripOpenAIPluginWithCookieBundle(manager, request, "", account)
			require.Nil(t, response)
			require.Equal(t, unavailable, handled)
			if unavailable {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			require.False(t, attempt.historyPhysicalBound)
			require.True(t, attempt.businessSentAt.IsZero())
			require.Zero(t, observation.summarySequence)
			require.Nil(t, observation.value.ActualProxyID)
			require.Nil(t, observation.value.CookieDiagnostic, "a plugin availability check is not a physical send")
			row, err := repo.Get(context.Background(), attempt.key)
			require.NoError(t, err)
			require.True(t, row.LastEligibleCollectionAt.IsZero())
			require.Empty(t, row.DemandReason)
			_, err = attempt.cookieAttempt.Snapshot(time.Now().Add(time.Minute))
			require.ErrorIs(t, err, openaicookies.ErrNoSnapshot)
			require.NoError(t, gateway.codexTurnStateService.Finish(context.Background(), attempt, false))
		})
	}
}
