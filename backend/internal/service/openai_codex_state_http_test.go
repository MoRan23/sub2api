package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openaicookies"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func codexStateHTTPRequest(t *testing.T, body string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, "https://chatgpt.com/backend-api/codex/responses", strings.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer test-token")
	return req
}

func TestCodexStateHTTPFrozenCarriersAndFinalModel(t *testing.T) {
	state, repo, account := newCodexStateTestService(t)
	service := &OpenAIGatewayService{codexTurnStateService: state}
	seed, err := state.Prepare(context.Background(), account, "final-model")
	require.NoError(t, err)
	token := codexStateTestToken(10, state.now())
	state.Observe(seed, token)
	require.NoError(t, state.Finish(context.Background(), seed, true))
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	before := `{"model":"final-model","input":"sensitive user prompt","client_metadata":{"x-codex-turn-state":"client-value","other":"kept"}}`
	req := codexStateHTTPRequest(t, before)
	req.Header["x-codex-turn-state"] = []string{"untrusted-duplicate"}
	req.Header["content-length"] = []string{"1"}
	req = service.prepareOpenAICodexStateHTTPRequest(c, account, req)
	require.Equal(t, token, req.Header.Get(openAICodexTurnStateHeader))
	require.NotContains(t, req.Header, "x-codex-turn-state")
	body, err := io.ReadAll(req.Body)
	require.NoError(t, err)
	require.Equal(t, token, gjson.GetBytes(body, "client_metadata.x-codex-turn-state").String())
	require.Equal(t, "kept", gjson.GetBytes(body, "client_metadata.other").String())
	require.Equal(t, int64(len(body)), req.ContentLength)
	require.NotContains(t, req.Header, "content-length")
	retry, _ := req.GetBody()
	defer retry.Close()
	retryBody, _ := io.ReadAll(retry)
	require.Equal(t, body, retryBody)
	entry := FingerprintObservationEntry{}
	populateCodexTurnStateObservation(c, &entry, req.Header, body, false)
	require.Equal(t, 292, entry.CodexTurnState.OutboundLength)
	require.Equal(t, "final-model", entry.CodexTurnState.Model)
	serialized, _ := json.Marshal(entry.CodexTurnState)
	require.NotContains(t, string(serialized), token)
	require.NotContains(t, string(serialized), "sensitive user prompt")
	observeCodexTurnStateHTTPResponse(req, nil, errors.New("connection failure"))
	active, _ := repo.HasBusiness(context.Background(), seed.key, state.now())
	require.False(t, active)
	other := service.prepareOpenAICodexStateHTTPRequest(c, account, codexStateHTTPRequest(t, `{"model":"other-model","input":"hello"}`))
	require.Empty(t, other.Header.Get(openAICodexTurnStateHeader))
	observeCodexTurnStateHTTPResponse(other, nil, errors.New("connection failure"))
}

func TestCodexStateHTTPNaturalResponsePublicationBoundary(t *testing.T) {
	for _, tc := range []struct {
		name                        string
		parse, delivered, completed bool
		status                      int
		event                       bool
	}{
		{"headers_delivered", true, true, true, 200, false}, {"metadata_delivered", true, true, true, 200, true},
		{"headers_abandoned", false, false, false, 200, false}, {"parsed_but_undelivered", true, false, true, 200, false},
		{"delivered_without_terminal", true, true, false, 200, false}, {"failed_status", false, false, false, 429, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state, repo, account := newCodexStateTestService(t)
			now := time.Now().UTC().Truncate(time.Second)
			state.now = func() time.Time { return now }
			service := &OpenAIGatewayService{codexTurnStateService: state}
			token := codexStateTestToken(10, state.now())
			req := service.prepareOpenAICodexStateHTTPRequest(nil, account, codexStateHTTPRequest(t, `{"model":"gpt-5","input":"hello"}`))
			req = withOpenAINativeHTTPRequestScope(req, account, nil, "business")
			scope, ok := openaicookies.ScopeFromContext(req.Context())
			require.True(t, ok && scope.Persistent())
			collector := req.Context().Value(codexTurnStateHTTPRequestKey{}).(*codexTurnStateHTTPCollector)
			transport := openaicookies.NewManager().Wrap(openAIPluginRoundTripFunc(func(outbound *http.Request) (*http.Response, error) {
				require.Empty(t, outbound.Header.Get("Cookie"))
				response := &http.Response{StatusCode: tc.status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(""))}
				response.Header.Add("Set-Cookie", "__oailb=synthetic-route; Path=/; Secure; Max-Age=60")
				if !tc.event {
					response.Header.Set(openAICodexTurnStateHeader, token)
				}
				return response, nil
			}))
			resp, err := transport.RoundTrip(req)
			require.NoError(t, err)
			observeCodexTurnStateHTTPResponse(req, resp, nil)
			if tc.parse {
				beginCodexTelemetryHTTPParsing(resp)
			}
			if tc.event {
				payload, _ := json.Marshal(map[string]any{"type": "response.metadata", "headers": map[string]string{openAICodexTurnStateHeader: token}})
				observeCodexTelemetryHTTPPayload(resp, payload, "response.metadata")
			}
			if tc.completed {
				observeCodexTelemetryHTTPPayload(resp, []byte(`{"type":"response.completed","response":{"model":"gpt-5","status":"completed"}}`), "response.completed")
			}
			if tc.delivered {
				markCodexTurnStateHTTPDelivered(resp)
			}
			_ = resp.Body.Close()
			if tc.parse {
				completeCodexTelemetryHTTPResponse(resp, nil)
			}
			key := collector.attempt.key
			record, err := repo.Get(context.Background(), key)
			require.NoError(t, err)
			if tc.delivered && tc.completed {
				require.NotEmpty(t, record.EncryptedToken)
				require.NotEmpty(t, record.EncryptedCookieBundle)
				require.Equal(t, account.OpenAIOAuthAuthorizationGeneration, record.AuthorizationGeneration)
				require.NotNil(t, record.CookieBundleExpiresAt)
				require.False(t, record.CookieBundleExpiresAt.After(record.ExpiresAt))
				plain, decryptErr := state.encryptor.Decrypt(record.EncryptedCookieBundle)
				require.NoError(t, decryptErr)
				var envelope codexTurnStateCookieEnvelope
				require.NoError(t, json.Unmarshal([]byte(plain), &envelope))
				require.Equal(t, key.OwnerAccountID, envelope.OwnerAccountID)
				require.Equal(t, key.Model, envelope.Model)
				require.Equal(t, record.AuthorizationGeneration, envelope.AuthorizationGeneration)
				require.True(t, envelope.Bundle.ValidAt(state.now()))
				require.Len(t, envelope.Bundle.Entries, 1)
				require.Equal(t, "__oailb", envelope.Bundle.Entries[0].Name)
				require.Equal(t, "synthetic-route", envelope.Bundle.Entries[0].Value)
			} else {
				require.Empty(t, record.EncryptedToken)
				require.Empty(t, record.EncryptedCookieBundle)
			}
			active, _ := repo.HasBusiness(context.Background(), key, state.now())
			require.False(t, active)
		})
	}
}

func TestCodexStateHTTPFailsOpenAndIgnoresNonResponses(t *testing.T) {
	for _, kind := range []string{"disabled", "store_down", "stale_credentials", "auxiliary", "prewarm"} {
		t.Run(kind, func(t *testing.T) {
			isolateCodexTurnStateSummaryStore(t)
			state, repo, account := newCodexStateTestService(t)
			service := &OpenAIGatewayService{codexTurnStateService: state}
			// An available cached token makes passthrough assertions meaningful:
			// the final rejection must discard an already prepared server snapshot.
			seed, err := state.Prepare(context.Background(), account, "gpt-5")
			require.NoError(t, err)
			cachedToken := codexStateTestToken(10, state.now())
			state.Observe(seed, cachedToken)
			require.NoError(t, state.Finish(context.Background(), seed, true))
			before := repo.records[seed.key]
			require.NotEmpty(t, before.EncryptedToken)
			body := `{"model":"gpt-5","input":"hello","client_metadata":{"x-codex-turn-state":"guarded-client"}}`
			req := codexStateHTTPRequest(t, body)
			req.Header.Set(openAICodexTurnStateHeader, "guarded-client")
			switch kind {
			case "disabled":
				account.Extra["codex_turn_state"].(map[string]any)["enabled"] = false
			case "store_down":
				repo.getErr = errors.New("unavailable")
			case "stale_credentials":
				req.Header.Set("Authorization", "Bearer stale")
			case "auxiliary":
				req.URL.Path += "/compact"
			case "prewarm":
				body = `{"model":"gpt-5","generate":false}`
				req = codexStateHTTPRequest(t, body)
				req.Header.Set(openAICodexTurnStateHeader, "guarded-client")
			}
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			out := service.prepareOpenAICodexStateHTTPRequest(c, account, req)
			require.Equal(t, "guarded-client", out.Header.Get(openAICodexTurnStateHeader))
			require.Equal(t, req.Header.Get("Authorization"), out.Header.Get("Authorization"))
			got, err := io.ReadAll(out.Body)
			require.NoError(t, err)
			require.Equal(t, body, string(got))
			collector, _ := out.Context().Value(codexTurnStateHTTPRequestKey{}).(*codexTurnStateHTTPCollector)
			if kind == "auxiliary" || kind == "prewarm" {
				require.Nil(t, collector)
				_, summaries := globalCodexTurnStateSummaryStore.snapshot([]int64{account.ID})
				require.Empty(t, summaries)
			} else {
				require.NotNil(t, collector)
				require.False(t, collector.attempt.Enabled)
				require.Equal(t, account.ID, collector.attempt.OwnerAccountID)
				require.Equal(t, "gpt-5", collector.attempt.Model)
				require.Empty(t, collector.attempt.Generation)
				require.Empty(t, collector.attempt.id)
				require.Empty(t, collector.attempt.Snapshot.Token)
				service.recordFingerprintObservationWithBody(c, account, installationIDResolution{}, out.Header, got)
				responseToken := codexStateTestToken(10, state.now().Add(-time.Minute))
				response := &http.Response{StatusCode: http.StatusOK, Header: http.Header{"X-Codex-Turn-State": {responseToken}}, Body: io.NopCloser(strings.NewReader(""))}
				response, err = openaicookies.NewManager().Wrap(openAIPluginRoundTripFunc(func(*http.Request) (*http.Response, error) {
					return response, nil
				})).RoundTrip(out)
				require.NoError(t, err)
				observeCodexTurnStateHTTPResponse(out, response, nil)
				require.Empty(t, collector.attempt.candidates, "passive diagnostics must not retain response tokens")
				markCodexTurnStateHTTPDelivered(response)
				require.NoError(t, response.Body.Close())
				_, summaries := globalCodexTurnStateSummaryStore.snapshot([]int64{account.ID})
				require.Len(t, summaries[account.ID], 1)
				require.Equal(t, "gpt-5", summaries[account.ID][0].Model)
				require.Equal(t, 292, summaries[account.ID][0].ResponseLength)
				require.Equal(t, "header", summaries[account.ID][0].ResponseSource)
				require.Equal(t, len("guarded-client"), summaries[account.ID][0].OutboundLength)
				encoded, err := json.Marshal(summaries)
				require.NoError(t, err)
				for _, secret := range []string{responseToken, cachedToken, "guarded-client", "Bearer stale", "test-token", before.EncryptedToken} {
					require.NotContains(t, string(encoded), secret)
				}
			}
			require.Equal(t, before, repo.records[seed.key], "passive completion must not publish or mutate the cached state")
			for _, leases := range repo.leases {
				require.Empty(t, leases)
			}
			require.Empty(t, state.business)
			require.Empty(t, state.queue)
			require.Empty(t, SnapshotFingerprintObservations(0))
		})
	}
}

func TestCodexStateIntegrityTrustsOnlyExactPatch(t *testing.T) {
	for _, tc := range []struct{ name, input, mutationPath, changedField string }{
		{"scalar", `"keep"`, "input", "input"},
		{"message_array", `[{"type":"message","role":"user","content":[{"type":"input_text","text":"keep"}]}]`, "input.0.content.0.text", "input[0].content[0].text"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := []byte(`{"model":"gpt-5","input":` + tc.input + `,"client_metadata":{"x-codex-turn-state":"old"}}`)
			after, err := sjson.SetBytes(before, "client_metadata.x-codex-turn-state", "server-snapshot")
			require.NoError(t, err)
			patch := newCodexStateBodyPatch(before, after)
			require.NotNil(t, patch)
			state := NewOpenAIRequestIntegrityState(true, "responses", before)
			opts := RequestIntegrityCheckOptions{Transport: "http", ExpectedModel: "gpt-5", CodexStatePatch: patch}
			good := state.Check(integrityTestAccount(), after, opts)
			require.Equal(t, "expected_transform", good.Status)
			tampered, err := sjson.SetBytes(after, "client_metadata.x-codex-turn-state", "injected-after-freeze")
			require.NoError(t, err)
			bad := state.Check(integrityTestAccount(), tampered, opts)
			require.Equal(t, "difference", bad.Status)
			require.Contains(t, bad.ChangedFields, "client_metadata.x-codex-turn-state")
			changed, err := sjson.SetBytes(after, tc.mutationPath, "corrupted")
			require.NoError(t, err)
			require.Nil(t, newCodexStateBodyPatch(before, changed))
			bad = state.Check(integrityTestAccount(), changed, opts)
			require.Equal(t, "difference", bad.Status)
			require.Contains(t, bad.ChangedFields, tc.changedField)
		})
	}
}

func TestCodexStateHTTPObservationResponseBeforeRowBinding(t *testing.T) {
	SetFingerprintObservationEnabled(true)
	defer SetFingerprintObservationEnabled(false)
	state, _, account := newCodexStateTestService(t)
	attempt, err := state.Prepare(context.Background(), account, "gpt-5")
	require.NoError(t, err)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	noteOpenAICodexStatePatch(c, attempt, nil, nil)
	state.Observe(attempt, codexStateTestToken(10, state.now()))
	finishOpenAICodexStateObservation(attempt)
	seq := globalFingerprintObserver.record(FingerprintObservationEntry{AccountID: account.ID})
	bindCodexTurnStateObservationSequence(codexStateWireObservation(c), seq)
	globalFingerprintObserver.mu.Lock()
	defer globalFingerprintObserver.mu.Unlock()
	for _, row := range globalFingerprintObserver.ring {
		if row.SequenceID == seq {
			require.NotNil(t, row.CodexTurnState)
			require.Equal(t, "target", row.CodexTurnState.ResponseShape)
			require.Equal(t, 292, row.CodexTurnState.ResponseLength)
			return
		}
	}
	t.Fatal("outbound observation row missing")
}
