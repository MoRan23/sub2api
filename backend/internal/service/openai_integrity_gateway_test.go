package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type integrityGatewaySettingsRepo struct {
	*openAIUUIDv7RuntimeRepo
}

func (r *integrityGatewaySettingsRepo) GetValue(_ context.Context, key string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if value, present := r.values[key]; present {
		return value, nil
	}
	return "", ErrSettingNotFound
}

func forwardOpenAIIntegrityTestRequest(t *testing.T, svc *OpenAIGatewayService, c *gin.Context, account *Account, route string, body []byte) *OpenAIForwardResult {
	t.Helper()
	var result *OpenAIForwardResult
	var err error
	switch route {
	case "chat_completions":
		result, err = svc.ForwardAsChatCompletions(context.Background(), c, account, body, "integrity-gateway", "")
	case "messages":
		result, err = svc.ForwardAsAnthropic(context.Background(), c, account, body, "integrity-gateway", "")
	default:
		result, err = svc.Forward(context.Background(), c, account, body)
	}
	require.NoError(t, err)
	require.NotNil(t, result)
	return result
}

func TestOpenAIIntegrityGatewayObservesNativeAndCompatibilityEntrances(t *testing.T) {
	for _, tc := range []struct{ route, path, body, stage string }{
		{"responses", "/v1/responses", `{"model":"gpt-5.4","stream":false,"instructions":"Answer briefly.","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"Hello integrity."}]}]}`, "ingress"},
		{"chat_completions", "/v1/chat/completions", `{"model":"gpt-5.4","stream":false,"messages":[{"role":"system","content":"Answer briefly."},{"role":"user","content":"Hello integrity."}]}`, "responses_adapter_output"},
		{"messages", "/v1/messages", `{"model":"gpt-5.4","stream":false,"max_tokens":64,"system":"Answer briefly.","messages":[{"role":"user","content":"Hello integrity."}]}`, "responses_adapter_output"},
	} {
		t.Run(tc.route, func(t *testing.T) {
			enableOpenAIIdentityPathFingerprintObservation(t)
			upstream := &httpUpstreamRecorder{resp: openAICompatSSECompletedResponse("resp_integrity_gateway", "gpt-5.4")}
			svc, _ := newOpenAIIdentityPathService(t, true, upstream)
			body := []byte(tc.body)
			c, recorder := newOpenAIIdentityPathContext(t, tc.path, body, 9701)
			forwardOpenAIIntegrityTestRequest(t, svc, c, newOpenAIIdentityPathOAuthAccount(9701), tc.route, body)
			require.Equal(t, http.StatusOK, recorder.Code)
			require.Len(t, upstream.bodies, 1)
			require.Contains(t, string(upstream.bodies[0]), "Hello integrity.", "the real forwarding path must preserve user content")
			entries := SnapshotFingerprintObservations(0)
			require.Len(t, entries, 1)
			observed := entries[0].RequestIntegrity
			require.NotNil(t, observed)
			require.Equal(t, tc.route, observed.BaselineProtocol)
			require.Equal(t, tc.stage, observed.BaselineStage)
			require.Equal(t, int64(1), observed.Attempt)
			require.Equal(t, "http", observed.Transport)
			require.Contains(t, []string{"unchanged", "expected_transform"}, observed.Status, "%+v", observed)
			require.Empty(t, observed.ChangedFields)
		})
	}
}

func TestOpenAIIntegrityGatewayPreservesVerbosityForNonNumericModelNames(t *testing.T) {
	for _, model := range []string{"gpt-daybreak-blue-latest", "gpt-reserve"} {
		for _, stream := range []bool{false, true} {
			name := model + "/sync"
			if stream {
				name = model + "/stream"
			}
			t.Run(name, func(t *testing.T) {
				enableOpenAIIdentityPathFingerprintObservation(t)
				upstream := &httpUpstreamRecorder{resp: openAICompatSSECompletedResponse("resp_verbosity", model)}
				svc, _ := newOpenAIIdentityPathService(t, true, upstream)
				body := integrityTestJSON(t, map[string]any{
					"model": model, "stream": stream, "input": "Preserve my output preference.",
					"text": map[string]any{"verbosity": "high", "format": map[string]any{"type": "text"}},
				})
				c, recorder := newOpenAIIdentityPathContext(t, "/responses", body, 9709)
				account := newOpenAIIdentityPathOAuthAccount(9709)
				forwardOpenAIIntegrityTestRequest(t, svc, c, account, "responses", body)
				require.Equal(t, http.StatusOK, recorder.Code)
				require.Len(t, upstream.bodies, 1)
				require.Equal(t, model, gjson.GetBytes(upstream.lastBody, "model").String())
				require.JSONEq(t, gjson.GetBytes(body, "text").Raw, gjson.GetBytes(upstream.lastBody, "text").Raw)
				entries := SnapshotFingerprintObservations(0)
				require.Len(t, entries, 1)
				require.NotNil(t, entries[0].RequestIntegrity)
				require.Contains(t, []string{"unchanged", "expected_transform"}, entries[0].RequestIntegrity.Status)
				require.Empty(t, entries[0].RequestIntegrity.ChangedFields)

				// The fix must preserve the preference on the wire, not hide a real loss
				// by exempting verbosity from the integrity comparison.
				lostPreference := integrityTestJSON(t, map[string]any{
					"model": model, "stream": stream, "input": "Preserve my output preference.",
					"text": map[string]any{"format": map[string]any{"type": "text"}},
				})
				observed := NewOpenAIRequestIntegrityState(true, "responses", body).Check(account, lostPreference, RequestIntegrityCheckOptions{})
				require.Equal(t, "difference", observed.Status)
				require.Contains(t, observed.ChangedFields, "text.verbosity")
			})
		}
	}
}

func TestOpenAIIntegrityGatewayReportsEncryptedContentRecoveryPerPhysicalAttempt(t *testing.T) {
	enableOpenAIIdentityPathFingerprintObservation(t)
	body := []byte(`{"model":"gpt-5.4","stream":false,"instructions":"Answer briefly.","input":[{"type":"reasoning","encrypted_content":"private-encrypted-replay","summary":[{"type":"summary_text","text":"private-summary"}]},{"type":"message","role":"user","content":[{"type":"input_text","text":"Continue please."}]}]}`)
	upstream := &httpUpstreamRecorder{responses: []*http.Response{
		{StatusCode: http.StatusBadRequest, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"error":{"code":"invalid_encrypted_content","type":"invalid_request_error","message":"The encrypted content could not be verified."}}`))},
		openAICompatSSECompletedResponse("resp_integrity_retry", "gpt-5.4"),
	}}
	svc, _ := newOpenAIIdentityPathService(t, true, upstream)
	c, recorder := newOpenAIIdentityPathContext(t, "/v1/responses", body, 9702)
	forwardOpenAIIntegrityTestRequest(t, svc, c, newOpenAIIdentityPathOAuthAccount(9702), "responses", body)
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Len(t, upstream.bodies, 2, "observation must not stop the existing recovery retry")
	require.Equal(t, "private-encrypted-replay", gjson.GetBytes(upstream.bodies[0], "input.0.encrypted_content").String())
	require.NotContains(t, string(upstream.bodies[1]), "private-encrypted-replay")
	entries := SnapshotFingerprintObservations(0)
	require.Len(t, entries, 2)
	byAttempt := make(map[int64]*RequestIntegrityObservation)
	for _, entry := range entries {
		require.NotNil(t, entry.RequestIntegrity)
		byAttempt[entry.RequestIntegrity.Attempt] = entry.RequestIntegrity
	}
	require.Contains(t, []string{"unchanged", "expected_transform"}, byAttempt[1].Status)
	require.Equal(t, "difference", byAttempt[2].Status)
	require.NotEmpty(t, byAttempt[2].ChangedFields)
	require.Contains(t, byAttempt[2].RuleCodes, "encrypted_reasoning_removed")
	raw, err := json.Marshal(entries)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "private-encrypted-replay")
	require.NotContains(t, string(raw), "private-summary")
}

func TestOpenAIIntegrityGatewayStillChecksWithFingerprintCollectionDisabled(t *testing.T) {
	SetFingerprintObservationEnabled(false)
	t.Cleanup(func() { SetFingerprintObservationEnabled(false) })
	body := []byte(`{"model":"gpt-5.4","stream":false,"instructions":"Answer briefly.","input":"Hello integrity."}`)
	upstream := &httpUpstreamRecorder{resp: openAICompatSSECompletedResponse("resp_integrity_no_ring", "gpt-5.4")}
	svc, _ := newOpenAIIdentityPathService(t, true, upstream)
	c, _ := newOpenAIIdentityPathContext(t, "/v1/responses", body, 9703)
	forwardOpenAIIntegrityTestRequest(t, svc, c, newOpenAIIdentityPathOAuthAccount(9703), "responses", body)
	require.Len(t, upstream.requests, 1)
	require.Empty(t, SnapshotFingerprintObservations(0))
	capture := openAIIntegrityCaptureFromContext(c)
	require.NotNil(t, capture)
	require.NotNil(t, capture.state)
	require.Equal(t, int64(1), capture.state.attempt.Load(), "the physical send must run the checker independently of the fingerprint ring")
}

func TestOpenAIIntegrityGatewayExcludesAPIKey(t *testing.T) {
	enableOpenAIIdentityPathFingerprintObservation(t)
	body := []byte(`{"model":"gpt-5.4","stream":false,"input":"Hello integrity."}`)
	upstream := &httpUpstreamRecorder{resp: successfulInstallationTestResponse()}
	svc, _ := newOpenAIIdentityPathService(t, true, upstream)
	c, _ := newOpenAIIdentityPathContext(t, "/v1/responses", body, 9704)
	forwardOpenAIIntegrityTestRequest(t, svc, c, newOpenAIIdentityPathAPIKeyAccount(9704), "responses", body)
	entries := SnapshotFingerprintObservations(0)
	require.Len(t, entries, 1)
	require.Nil(t, entries[0].RequestIntegrity)
	if capture := openAIIntegrityCaptureFromContext(c); capture != nil && capture.state != nil {
		require.Zero(t, capture.state.attempt.Load())
	}
}

func TestOpenAIIntegrityGatewayToggleDoesNotChangeSentBodyOrResponse(t *testing.T) {
	var sent, received [2][]byte
	for index, enabled := range []bool{false, true} {
		enableOpenAIIdentityPathFingerprintObservation(t)
		body := []byte(`{"model":"gpt-5.4","stream":false,"instructions":"Answer briefly.","prompt_cache_key":"stable-integrity-prompt","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"Hello integrity."}]}]}`)
		upstream := &httpUpstreamRecorder{resp: openAICompatSSECompletedResponse("resp_integrity_toggle", "gpt-5.4")}
		svc, _ := newOpenAIIdentityPathService(t, false, upstream)
		settingRepo := &integrityGatewaySettingsRepo{svc.settingService.settingRepo.(*openAIUUIDv7RuntimeRepo)}
		svc.settingService = NewSettingService(settingRepo, nil)
		settingRepo.values[SettingKeyOpenAIRequestIntegrityObserveEnabled] = "false"
		if enabled {
			settingRepo.values[SettingKeyOpenAIRequestIntegrityObserveEnabled] = "true"
		}
		c, recorder := newOpenAIIdentityPathContext(t, "/v1/responses", body, 9705)
		account := newOpenAIIdentityPathOAuthAccount(9705)
		account.Extra = map[string]any{openAIPinnedInstallationIDKey: transportTestPinnedInstallationID}
		forwardOpenAIIntegrityTestRequest(t, svc, c, account, "responses", body)
		sent[index], received[index] = upstream.lastBody, recorder.Body.Bytes()
		entries := SnapshotFingerprintObservations(0)
		require.Len(t, entries, 1)
		if enabled {
			require.NotNil(t, entries[0].RequestIntegrity)
		} else {
			require.Nil(t, entries[0].RequestIntegrity)
		}
	}
	require.Equal(t, string(sent[0]), string(sent[1]), "the observer switch must not affect actual upstream bytes")
	require.Equal(t, string(received[0]), string(received[1]), "the observer switch must not affect the downstream response")
}

func TestOpenAIIntegrityGatewayDailyRootsDoNotChangeContentVerdict(t *testing.T) {
	const streamRoot = "018f5c3c-6e3a-7abf-8def-1234567890ae"
	const syncRoot = "018f5c3c-6e3a-7ac0-8def-1234567890af"
	for _, stream := range []bool{false, true} {
		name, kind, expectedRoot := "sync", "sync", syncRoot
		if stream {
			name, kind, expectedRoot = "stream", "stream", streamRoot
		}
		t.Run(name, func(t *testing.T) {
			enableOpenAIIdentityPathFingerprintObservation(t)
			upstream := &httpUpstreamRecorder{resp: openAICompatSSECompletedResponse("resp_integrity_daily", "gpt-5.4")}
			svc, _ := newOpenAIIdentityPathService(t, true, upstream)
			svc.settingService = NewSettingService(&dailyRotationSettingRepo{values: map[string]string{
				SettingKeyEnableOpenAICodexFingerprintNormalization: "true",
				SettingKeyEnableOpenAIUUIDv7SessionIdentity:         "true",
				SettingKeyEnableOpenAIOAuthDailySessionRotation:     "true",
			}}, nil)
			svc.oauthDailySessionRepo = &fakeOAuthDailyAffinityRepository{
				pool:     OAuthDailySessionPool{AccountID: 9706, BusinessDate: "2026-09-17", Generation: streamRoot, SyncSessionID: syncRoot},
				affinity: OAuthDailySessionAffinity{AccountID: 9706, APIKeyID: 9706, LogicalSessionKey: "integrity-daily", BusinessDate: "2026-09-17", Generation: streamRoot, SlotIndex: 1, StreamSessionID: streamRoot},
			}
			body, err := json.Marshal(map[string]any{"model": "gpt-5.4", "stream": stream, "instructions": "Answer briefly.", "input": "Hello integrity."})
			require.NoError(t, err)
			c, _ := newOpenAIIdentityPathContext(t, "/v1/responses", body, 9706)
			forwardOpenAIIntegrityTestRequest(t, svc, c, newOpenAIIdentityPathOAuthAccount(9706), "responses", body)
			require.Equal(t, expectedRoot, upstream.lastReq.Header.Get("session-id"))
			require.NotEmpty(t, upstream.lastReq.Header.Get("thread-id"))
			entries := SnapshotFingerprintObservations(0)
			require.Len(t, entries, 1)
			require.Equal(t, kind, entries[0].DailyFixedRootKind)
			require.NotNil(t, entries[0].RequestIntegrity)
			require.Contains(t, []string{"unchanged", "expected_transform"}, entries[0].RequestIntegrity.Status)
			require.Empty(t, entries[0].RequestIntegrity.ChangedFields)
		})
	}
}

func TestOpenAIIntegrityGatewayFailoverRetainsBaselineAndFrozenSwitch(t *testing.T) {
	enableOpenAIIdentityPathFingerprintObservation(t)
	body := []byte(`{"model":"gpt-5.4","stream":false,"instructions":"Answer briefly.","input":"Hello integrity."}`)
	upstream := &httpUpstreamRecorder{responses: []*http.Response{
		{StatusCode: http.StatusServiceUnavailable, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"error":{"type":"server_error","message":"upstream unavailable"}}`))},
		openAICompatSSECompletedResponse("resp_integrity_failover", "gpt-5.5"),
		openAICompatSSECompletedResponse("resp_integrity_disabled", "gpt-5.5"),
	}}
	svc, _ := newOpenAIIdentityPathService(t, true, upstream)
	settings := &integrityGatewaySettingsRepo{svc.settingService.settingRepo.(*openAIUUIDv7RuntimeRepo)}
	settings.values[SettingKeyOpenAIRequestIntegrityObserveEnabled] = "true"
	svc.settingService = NewSettingService(settings, nil)
	c, _ := newOpenAIIdentityPathContext(t, "/v1/responses", body, 9707)
	_, err := svc.Forward(context.Background(), c, newOpenAIIdentityPathOAuthAccount(9707), body)
	var failover *UpstreamFailoverError
	require.ErrorAs(t, err, &failover)
	firstCapture := openAIIntegrityCaptureFromContext(c)
	require.NotNil(t, firstCapture)
	require.Equal(t, body, firstCapture.state.baseline)

	// Simulate a committed admin disable while the handler selects another
	// account. Only new accepted requests should see the changed setting.
	svc.settingService.publishOpenAIRequestIntegrityObserveEnabled("false")
	secondAccount := newOpenAIIdentityPathOAuthAccount(9708)
	secondAccount.Credentials["model_mapping"] = map[string]any{"gpt-5.4": "gpt-5.5"}
	forwardOpenAIIntegrityTestRequest(t, svc, c, secondAccount, "responses", body)
	require.Same(t, firstCapture, openAIIntegrityCaptureFromContext(c))
	require.Equal(t, body, firstCapture.state.baseline)
	require.Len(t, upstream.bodies, 2)
	require.Equal(t, "gpt-5.4", gjson.GetBytes(upstream.bodies[0], "model").String())
	require.Equal(t, "gpt-5.5", gjson.GetBytes(upstream.bodies[1], "model").String())
	entries := SnapshotFingerprintObservations(0)
	require.Len(t, entries, 2)
	byAccount := make(map[int64]*RequestIntegrityObservation)
	for _, entry := range entries {
		byAccount[entry.AccountID] = entry.RequestIntegrity
	}
	require.NotNil(t, byAccount[9707])
	require.NotNil(t, byAccount[9708])
	require.Equal(t, int64(1), byAccount[9707].Attempt)
	require.Equal(t, int64(2), byAccount[9708].Attempt)
	require.Equal(t, "expected_transform", byAccount[9708].Status)
	require.Contains(t, byAccount[9708].RuleCodes, "account_model_mapping")

	newContext, _ := newOpenAIIdentityPathContext(t, "/v1/responses", body, 9707)
	forwardOpenAIIntegrityTestRequest(t, svc, newContext, secondAccount, "responses", body)
	latest := SnapshotFingerprintObservations(1)
	require.Len(t, latest, 1)
	require.Nil(t, latest[0].RequestIntegrity)
}
