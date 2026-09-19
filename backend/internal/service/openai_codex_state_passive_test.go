package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func enableCodexStatePassiveObservation(t *testing.T) {
	t.Helper()
	SetFingerprintObservationEnabled(false)
	SetFingerprintObservationEnabled(true)
	t.Cleanup(func() { SetFingerprintObservationEnabled(false) })
}

func TestCodexStatePassiveHTTPGatewayAllPaths(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, path := range []string{"responses", "passthrough", "chat", "messages"} {
		for _, stream := range []bool{false, true} {
			for _, carrier := range []string{"header", "metadata"} {
				t.Run(path+"/stream="+strconv.FormatBool(stream)+"/"+carrier, func(t *testing.T) {
					enableCodexStatePassiveObservation(t)
					state, repo, account := newCodexStateTestService(t)
					account.Extra[CodexTurnStateExtraKey].(map[string]any)["enabled"] = false
					account.Concurrency = 1
					if path == "passthrough" {
						account.Extra["openai_passthrough"] = true
					}
					// Passive observation does not require the runtime store/encryptor.
					state.repo, state.encryptor = nil, nil
					token := codexStateTestToken(10, state.now())
					upstream := &httpUpstreamRecorder{resp: codexStateHTTPIntegrationResponse(token, carrier, false)}
					gateway := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream, codexTurnStateService: state}
					result, recorder, err := codexStateHTTPIntegrationForward(t, gateway, account, path, codexStateHTTPIntegrationBody(path, stream))
					require.NoError(t, err, recorder.Body.String())
					require.NotNil(t, result)
					require.Len(t, upstream.requests, 1)
					require.Empty(t, upstream.requests[0].Header.Get(openAICodexTurnStateHeader))
					var summaries []*CodexTurnStateObservation
					for _, row := range SnapshotFingerprintObservations(0) {
						if row.CodexTurnState != nil {
							summaries = append(summaries, row.CodexTurnState)
						}
					}
					require.Len(t, summaries, 1)
					got := summaries[0]
					require.False(t, got.Enabled)
					require.Equal(t, "passthrough", got.Action)
					require.Equal(t, "gpt-5.4", got.Model)
					require.Zero(t, got.OutboundLength)
					require.Empty(t, got.Source)
					require.Equal(t, 292, got.ResponseLength)
					require.Equal(t, "target", got.ResponseShape)
					require.Equal(t, carrier, got.ResponseSource)
					require.Nil(t, got.ExpiresAt)
					require.Empty(t, got.RenewalReason)
					encoded, err := json.Marshal(summaries)
					require.NoError(t, err)
					require.NotContains(t, string(encoded), token)
					require.Empty(t, repo.records)
					require.Empty(t, repo.leases)
					require.Empty(t, state.business)
					require.Empty(t, state.queue)
				})
			}
		}
	}
}

func TestCodexStatePassiveClassificationAndNoRetainedTokens(t *testing.T) {
	for _, tc := range []struct {
		name, accountType, wantShape string
		blocks                       int
		age                          time.Duration
	}{
		{"personal", "personal", "target", 10, 0},
		{"personal_extended", "personal", "suspect", 11, 0},
		{"team", "team_business", "target", 12, 0},
		{"team_extended", "team_business", "suspect", 13, 0},
		{"wrong_shape", "personal", "unknown", 12, 0},
		{"unknown_plan", "auto", "unknown", 10, 0},
		{"expired", "personal", "expired", 10, 2 * time.Hour},
		{"future", "personal", "unknown", 10, -time.Hour},
		{"malformed", "personal", "unknown", 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			enableCodexStatePassiveObservation(t)
			state, repo, account := newCodexStateTestService(t)
			account.Extra[CodexTurnStateExtraKey] = map[string]any{"enabled": false, "account_type": tc.accountType}
			account.Credentials["plan_type"] = "unknown"
			attempt, err := state.Prepare(context.Background(), account, "final-model")
			require.NoError(t, err)
			require.NotNil(t, attempt)
			require.False(t, attempt.Enabled)
			require.Empty(t, attempt.id)
			require.Empty(t, attempt.Generation)
			require.Empty(t, attempt.Snapshot.Token)
			token := codexStateTestToken(tc.blocks, state.now().Add(-tc.age))
			if tc.blocks == 0 {
				token = strings.Repeat("!", 292)
			}
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			noteOpenAICodexStatePatch(c, attempt, nil, nil)
			state.ObserveHeaders(attempt, http.Header{"X-Codex-Turn-State": {token}})
			require.Empty(t, attempt.candidates, "passive observation never retains response token candidates")
			require.NoError(t, state.Finish(context.Background(), attempt, true))
			finishOpenAICodexStateObservation(attempt)
			observation := codexStateWireObservation(c).value
			require.Equal(t, len(token), observation.ResponseLength)
			require.Equal(t, tc.wantShape, observation.ResponseShape)
			require.Equal(t, "header", observation.ResponseSource)
			require.Empty(t, observation.RenewalReason)
			require.Empty(t, repo.records)
			require.Empty(t, repo.leases)
			require.Empty(t, state.queue)
		})
	}
}

func TestCodexStatePassiveHTTPPreservesGuardedCarriersAndRetryIsolation(t *testing.T) {
	enableCodexStatePassiveObservation(t)
	state, repo, account := newCodexStateTestService(t)
	account.Extra[CodexTurnStateExtraKey].(map[string]any)["enabled"] = false
	gateway := &OpenAIGatewayService{codexTurnStateService: state}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	firstBody := `{"model":"model-one","input":"private body","client_metadata":{"x-codex-turn-state":"guarded-body"}}`
	first := codexStateHTTPRequest(t, firstBody)
	first.Header.Set(openAICodexTurnStateHeader, "guarded-header")
	first = gateway.prepareOpenAICodexStateHTTPRequest(c, account, first)
	gotBody, err := io.ReadAll(first.Body)
	require.NoError(t, err)
	require.Equal(t, firstBody, string(gotBody))
	require.Equal(t, "guarded-header", first.Header.Get(openAICodexTurnStateHeader))
	retry, err := first.GetBody()
	require.NoError(t, err)
	defer retry.Close()
	retryBody, err := io.ReadAll(retry)
	require.NoError(t, err)
	require.Equal(t, gotBody, retryBody)
	firstEntry := FingerprintObservationEntry{AccountID: account.ID}
	firstObservation := populateCodexTurnStateObservation(c, &firstEntry, first.Header, gotBody, false)
	bindCodexTurnStateObservationSequence(firstObservation, globalFingerprintObserver.record(firstEntry))
	require.Equal(t, "client", firstEntry.CodexTurnState.Source)
	require.Equal(t, len("guarded-header"), firstEntry.CodexTurnState.OutboundLength)
	require.Equal(t, len("guarded-header"), firstEntry.CodexTurnState.OutboundHeaderLength)
	require.Equal(t, len("guarded-body"), firstEntry.CodexTurnState.OutboundBodyLength)
	require.Equal(t, "header_and_body", firstEntry.CodexTurnState.OutboundCarrier)

	second := gateway.prepareOpenAICodexStateHTTPRequest(c, account, codexStateHTTPRequest(t, `{"model":"model-two","input":"retry"}`))
	secondEntry := FingerprintObservationEntry{AccountID: account.ID}
	secondObservation := populateCodexTurnStateObservation(c, &secondEntry, second.Header, nil, false)
	bindCodexTurnStateObservationSequence(secondObservation, globalFingerprintObserver.record(secondEntry))
	firstToken := codexStateTestToken(11, state.now())
	failed := &http.Response{StatusCode: 429, Header: http.Header{"X-Codex-Turn-State": {firstToken}}, Body: io.NopCloser(strings.NewReader(""))}
	observeCodexTurnStateHTTPResponse(first, failed, nil)
	require.NoError(t, failed.Body.Close())
	secondToken := codexStateTestToken(10, state.now())
	ok := &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(""))}
	observeCodexTurnStateHTTPResponse(second, ok, nil)
	beginCodexTurnStateHTTPParsing(ok)
	observeCodexTurnStateHTTPPayload(ok, []byte(fmt.Sprintf(`{"type":"response.metadata","headers":{"x-codex-turn-state":%q}}`, secondToken)))
	markCodexTurnStateHTTPDelivered(ok)
	completeCodexTurnStateHTTPResponse(ok, nil)
	require.NoError(t, ok.Body.Close())
	rows := SnapshotFingerprintObservations(0)
	require.Len(t, rows, 2)
	require.Equal(t, "model-two", rows[0].CodexTurnState.Model)
	require.Equal(t, "target", rows[0].CodexTurnState.ResponseShape)
	require.Equal(t, "metadata", rows[0].CodexTurnState.ResponseSource)
	require.Equal(t, "model-one", rows[1].CodexTurnState.Model)
	require.Equal(t, "suspect", rows[1].CodexTurnState.ResponseShape)
	require.Equal(t, "header", rows[1].CodexTurnState.ResponseSource)
	require.Empty(t, repo.records)
	require.Empty(t, repo.leases)
	encoded, err := json.Marshal(rows)
	require.NoError(t, err)
	for _, secret := range []string{firstToken, secondToken, "guarded-header", "guarded-body", "private body"} {
		require.NotContains(t, string(encoded), secret)
	}
}

func TestCodexStatePassiveHTTPBodyOnlyCarrier(t *testing.T) {
	enableCodexStatePassiveObservation(t)
	state, _, account := newCodexStateTestService(t)
	account.Extra[CodexTurnStateExtraKey].(map[string]any)["enabled"] = false
	gateway := &OpenAIGatewayService{codexTurnStateService: state}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	body := `{"model":"gpt-5","client_metadata":{"x-codex-turn-state":"guarded-body-only"}}`
	request := gateway.prepareOpenAICodexStateHTTPRequest(c, account, codexStateHTTPRequest(t, body))
	actualBody, err := io.ReadAll(request.Body)
	require.NoError(t, err)
	require.Equal(t, body, string(actualBody))
	require.Empty(t, request.Header.Get(openAICodexTurnStateHeader))
	entry := FingerprintObservationEntry{}
	populateCodexTurnStateObservation(c, &entry, request.Header, actualBody, false)
	require.Equal(t, "client", entry.CodexTurnState.Source)
	require.Equal(t, "body", entry.CodexTurnState.OutboundCarrier)
	require.Equal(t, len("guarded-body-only"), entry.CodexTurnState.OutboundLength)
	require.Zero(t, entry.CodexTurnState.OutboundHeaderLength)
	require.Equal(t, len("guarded-body-only"), entry.CodexTurnState.OutboundBodyLength)
	observeCodexTurnStateHTTPResponse(request, nil, errors.New("send failed"))
}

func TestCodexStatePassiveObservationSkipsWhenObserverDisabled(t *testing.T) {
	SetFingerprintObservationEnabled(false)
	state, repo, account := newCodexStateTestService(t)
	account.Extra[CodexTurnStateExtraKey].(map[string]any)["enabled"] = false
	attempt, err := state.Prepare(context.Background(), account, "gpt-5")
	require.NoError(t, err)
	require.Nil(t, attempt)
	require.Empty(t, repo.records)
}

func TestCodexStatePassiveObservationSendErrorDoesNotInventResponse(t *testing.T) {
	enableCodexStatePassiveObservation(t)
	state, _, account := newCodexStateTestService(t)
	account.Extra[CodexTurnStateExtraKey].(map[string]any)["enabled"] = false
	gateway := &OpenAIGatewayService{codexTurnStateService: state}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	request := gateway.prepareOpenAICodexStateHTTPRequest(c, account, codexStateHTTPRequest(t, `{"model":"gpt-5"}`))
	observeCodexTurnStateHTTPResponse(request, nil, errors.New("send failed"))
	observation := codexStateWireObservation(c).value
	require.Zero(t, observation.ResponseLength)
	require.Empty(t, observation.ResponseShape)
	require.Empty(t, observation.ResponseSource)
}
