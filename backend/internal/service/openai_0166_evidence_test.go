package service

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestOpenAI166HeaderDiagnosticsAreBoundedAndPrivate(t *testing.T) {
	encoded := base64.StdEncoding.EncodeToString([]byte(`{"error":{"code":"identity_expired","message":"private error","access_token":"never-retain"},"extra":"private"}`))
	h := http.Header{"X-Codex-Imagegen-Request-Id": {"req-imagegen"}, "X-Request-Id": {"req-outer"}, "X-Error-Json": {encoded}, "X-Openai-Authorization-Error": {"expired access_token=short Bearer abcdefghijklmnop sk-proj-abcdefghijklmnop"}}
	d := openAIHeaderDiagnosticsFromHeaders(h)
	require.Equal(t, "req-imagegen", d.ImagegenRequestID)
	require.Equal(t, "identity_expired", d.IdentityErrorCode)
	raw, err := json.Marshal(d)
	require.NoError(t, err)
	for _, secret := range []string{encoded, "private", "never-retain", "short", "abcdefghijklmnop", "req-outer"} {
		require.NotContains(t, string(raw), secret)
	}
	for _, value := range []string{"invalid-base64", base64.StdEncoding.EncodeToString([]byte(`{"error":{"code":123}}`)), base64.StdEncoding.EncodeToString([]byte(`{"error":{"code":"sk-abcdefghijklmnop"}}`)), strings.Repeat("a", 17*1024)} {
		require.Nil(t, openAIHeaderDiagnosticsFromHeaders(http.Header{"X-Error-Json": {value}}))
	}
	require.Nil(t, openAIHeaderDiagnosticsFromHeaders(http.Header{"X-Error-Json": {encoded, encoded}}))
	require.Nil(t, openAIHeaderDiagnosticsFromHeaders(http.Header{"X-Codex-Imagegen-Request-Id": {strings.Repeat("a", 257)}}))
	require.Nil(t, openAIHeaderDiagnosticsFromHeaders(http.Header{"X-Codex-Imagegen-Request-Id": {"sk-secret-credential"}}))
	require.Equal(t, "[structured authorization diagnostic]", openAIHeaderDiagnosticsFromHeaders(http.Header{"X-Openai-Authorization-Error": {`{"private_object":"never-store"}`}}).AuthorizationError)
	require.NotContains(t, openAIHeaderDiagnosticsFromHeaders(http.Header{"X-Openai-Authorization-Error": {strings.Repeat("s", 5000)}}).AuthorizationError, strings.Repeat("s", 512))
}

func TestOpenAI166GuardianSummaryIsIdempotentAndDoesNotRetainContent(t *testing.T) {
	var o codexModelEvidenceObserver
	frame := []byte(`{"type":"response.metadata","response_id":"resp-private","metadata":{"guardian":{"item_id":"fc-private","requires_review":false,"tool":"private command"}}}`)
	for range 5 {
		o.observePayload(frame)
	}
	e := o.snapshot("model")
	require.Equal(t, "review_not_required", e.GuardianAssessment.Status)
	require.False(t, e.GuardianAssessment.InvalidPresent)
	require.Equal(t, "not_reported", e.ModelRelation)
	cloned := e.clone()
	cloned.GuardianAssessment.Status = "changed"
	require.Equal(t, "review_not_required", e.GuardianAssessment.Status)
	o.observePayload([]byte(`{"type":"response.metadata","response_id":"resp-private","metadata":{"guardian":{"item_id":"fc2","requires_review":true}}}`))
	o.observePayload([]byte(`{"type":"response.metadata","metadata":{"guardian":{"item_id":"fc2","requires_review":"false"}}}`))
	e = o.snapshot("model")
	require.Equal(t, "mixed", e.GuardianAssessment.Status)
	require.True(t, e.GuardianAssessment.InvalidPresent)
	raw, err := json.Marshal(e)
	require.NoError(t, err)
	for _, forbidden := range []string{"resp-private", "fc-private", "private command", "fc2"} {
		require.NotContains(t, string(raw), forbidden)
	}
	for _, invalid := range []string{`null`, `{}`, `{"item_id":7,"requires_review":false}`, `{"item_id":"fc","requires_review":null}`} {
		var invalidObserver codexModelEvidenceObserver
		invalidObserver.observePayload([]byte(`{"type":"response.metadata","response_id":"r","metadata":{"guardian":` + invalid + `}}`))
		require.Equal(t, "invalid", invalidObserver.snapshot("").GuardianAssessment.Status)
	}
	var unrelated codexModelEvidenceObserver
	unrelated.observePayload([]byte(`{"type":"response.completed","response_id":"r","metadata":{"guardian":{"item_id":"i","requires_review":true}}}`))
	require.Nil(t, unrelated.snapshot("").GuardianAssessment)
}

func TestOpenAI166DiagnosticsAttemptScopeAndOpsFreeze(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	state := beginOpenAIResponseEvidence(c, "model")
	req := httptest.NewRequest(http.MethodPost, "/responses", nil)
	req = markOpenAIResponseEvidenceHTTPRequest(req, c)
	prepareOpenAIResponseEvidenceAttempt(req, &Account{ID: 1})
	resp := &http.Response{Header: http.Header{"X-Request-Id": {"req-first"}, "X-Codex-Imagegen-Request-Id": {"req-nested-first"}}}
	observeOpenAIHTTPResponseEvidence(req, resp)
	observeOpenAIResponseEvidenceEvent(state, []byte(`{"type":"response.metadata","response_id":"r","metadata":{"guardian":{"item_id":"i","requires_review":true}}}`))
	appendOpsUpstreamError(c, OpsUpstreamErrorEvent{AccountID: 1, UpstreamRequestID: "req-first", UpstreamStatusCode: 403})
	require.Nil(t, openAIHeaderDiagnosticsForOps(c, 2, "req-first"))
	require.Nil(t, openAIHeaderDiagnosticsForOps(c, 1, "req-other"))
	prepareOpenAIResponseEvidenceAttempt(req, &Account{ID: 1})
	require.Nil(t, state.snapshot().HeaderDiagnostics, "a network failure cannot retain previous response headers")
	require.Nil(t, state.snapshot().GuardianAssessment, "a retried attempt cannot inherit an earlier Guardian assessment")
	stored, _ := c.Get(OpsUpstreamErrorsKey)
	events, ok := stored.([]*OpsUpstreamErrorEvent)
	require.True(t, ok)
	require.Len(t, events, 1)
	event := events[0]
	require.Equal(t, "req-nested-first", event.HeaderDiagnostics.ImagegenRequestID)
	input := &OpsInsertErrorLogInput{UpstreamErrors: []*OpsUpstreamErrorEvent{event}}
	require.NoError(t, sanitizeOpsUpstreamErrors(input))
	require.NotNil(t, input.UpstreamErrorsJSON)
	require.Contains(t, *input.UpstreamErrorsJSON, "req-nested-first")
	second := beginOpenAIResponseEvidence(c, "other")
	observeOpenAIHTTPResponseEvidence(req, resp)
	require.Nil(t, second.snapshot().HeaderDiagnostics, "late response is bound to its original state")
}

func TestOpenAI166WSHeaderDiagnosticsDoNotLeakAcrossTurns(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	old := beginOpenAIResponseEvidence(c, "old")
	observeOpenAIResponseEvidenceHeaders(old, http.Header{"X-Codex-Imagegen-Request-Id": {"req-old"}}, "connection")
	err := &openAIWSDialError{StatusCode: 401, ResponseHeaders: http.Header{"X-Request-Id": {"req-fail"}, "X-Openai-Authorization-Error": {"identity_expired"}}, Err: errors.New("test")}
	observeOpenAIWSDialDiagnostics(c, &Account{ID: 2, Platform: PlatformOpenAI}, err)
	state := responseEvidenceFromContext(c)
	require.NotSame(t, old, state)
	require.Empty(t, state.snapshot().HeaderDiagnostics.ImagegenRequestID)
	stored, _ := c.Get(OpsUpstreamErrorsKey)
	events, ok := stored.([]*OpsUpstreamErrorEvent)
	require.True(t, ok)
	require.Len(t, events, 1)
	failed := events[0]
	require.Equal(t, "identity_expired", failed.HeaderDiagnostics.AuthorizationError)
	observeOpenAIResponseEvidenceEvent(state, []byte(`{"type":"response.metadata","headers":{"x-codex-imagegen-request-id":"req-response"}}`))
	require.Empty(t, state.snapshot().HeaderDiagnostics.AuthorizationError, "connection diagnostic cannot become response-local evidence")
	require.Equal(t, "identity_expired", failed.HeaderDiagnostics.AuthorizationError, "already recorded failure is immutable")
	observeOpenAIResponseEvidenceEvent(state, []byte(`{"type":"response.metadata","headers":{"x-openai-authorization-error":"expired"}}`))
	require.Equal(t, "req-response", state.snapshot().HeaderDiagnostics.ImagegenRequestID, "partial headers within the same response retain independent evidence")
	newTurn := beginOpenAIResponseEvidence(c, "new")
	require.Nil(t, newTurn.snapshot().HeaderDiagnostics)
}
