package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/iotest"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openaicookies"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestCodexModelEvidenceDeclarationRelations(t *testing.T) {
	for _, tc := range []struct{ name, sent, frames, model, relation, source string }{
		{"exact", "gpt-6-astra", `{"type":"response.completed","response":{"model":"gpt-6-astra"}}`, "gpt-6-astra", "exact", "response.model"},
		{"missing", "gpt-6-astra", `{"type":"response.completed","response":{}}`, "", "not_reported", ""},
		{"fallback", "gpt-6-astra", `{"type":"response.completed","response":{"model":null},"model":"gpt-5.6-luna"}`, "gpt-5.6-luna", "different", "model"},
		{"nested_priority", "gpt-6-astra", `{"type":"response.completed","response":{"model":"gpt-6-astra"},"model":"gpt-5.6-luna"}`, "gpt-6-astra", "exact", "response.model"},
		{"existing_alias", "gpt-5.4-mini", `{"model":"gpt5.4mini"}`, "gpt5.4mini", "known_alias", "model"},
		{"provider_spelling_alias", "openai/gpt-5.4-mini", `{"model":"GPT_5.4_MINI"}`, "GPT_5.4_MINI", "known_alias", "model"},
		{"no_grok_alias", "grok-4.5", `{"model":"grok-4.5-build"}`, "grok-4.5-build", "different", "model"},
		{"no_latest_guess", "gpt-6-astra", `{"model":"gpt-6-astra-latest"}`, "gpt-6-astra-latest", "different", "model"},
		{"no_date_guess", "gpt-6-astra", `{"model":"gpt-6-astra-2026-09-22"}`, "gpt-6-astra-2026-09-22", "different", "model"},
		{"conflicting", "gpt-6-astra", "{\"type\":\"response.created\",\"model\":\"gpt-5.6-luna\"}\n{\"type\":\"response.completed\",\"response\":{\"model\":\"gpt-6-astra\"}}", "gpt-6-astra", "conflicting", "response.model"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var o codexModelEvidenceObserver
			for _, frame := range strings.Split(tc.frames, "\n") {
				o.observePayload([]byte(frame))
			}
			e := o.snapshot(tc.sent)
			require.Equal(t, tc.model, e.UpstreamResponseModel)
			require.Equal(t, tc.relation, e.ModelRelation)
			require.Equal(t, tc.source, e.ModelEvidenceSource)
			require.Equal(t, tc.relation == "conflicting", e.ModelConflict)
		})
	}
}

func TestCodexCollectorModelEvidenceAndFailurePrecedence(t *testing.T) {
	_, _, account := newCodexStateTestService(t)
	for _, tc := range []struct {
		name, stream      string
		blocks, status    int
		mismatch          bool
		relation, errText string
	}{
		{"har_astra_luna_312", `data: {"type":"response.completed","response":{"model":"gpt-5.6-luna"}}` + "\n\n", 11, 200, true, "different", ""},
		{"target_mismatch", `data: {"type":"response.completed","response":{"model":"gpt-5.6-luna"}}` + "\n\n", 10, 200, true, "different", ""},
		{"not_reported_allowed", `data: {"type":"response.completed"}` + "\n\n", 10, 200, false, "not_reported", ""},
		{"event_fallback", "event: response.completed\ndata: {\"response\":{\n" + "data: \"model\":\"gpt-5.6-luna\"}}\n\n", 10, 200, true, "different", ""},
		{"conflicting", `data: {"type":"response.created","response":{"model":"gpt-5.6-luna"}}` + "\n\n" + `data: {"type":"response.completed","response":{"model":"gpt-6-astra"}}` + "\n\n", 10, 200, true, "conflicting", ""},
		{"rate_limit_wins", `data: {"type":"response.failed","response":{"model":"gpt-5.6-luna","error":{"code":"rate_limit_exceeded"}}}` + "\n\n" + `data: {"type":"response.completed"}` + "\n\n", 10, 200, false, "different", "collector_rate_limited"},
		{"stream_error_wins", `data: {"type":"response.failed","response":{"model":"gpt-5.6-luna"}}` + "\n\n", 10, 200, false, "different", "collector_response_failed"},
		{"http_auth_wins", `data: {"type":"response.completed","response":{"model":"gpt-5.6-luna"}}` + "\n\n", 10, 401, false, "not_reported", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			token := codexStateTestToken(tc.blocks, time.Now())
			collector := NewCodexTurnStateHTTPCollector(func(_ context.Context, input CodexTurnStateCollectRequest, request *http.Request) (*http.Response, error) {
				require.Equal(t, "gpt-6-astra", input.Model)
				return &http.Response{StatusCode: tc.status, Header: http.Header{"X-Codex-Turn-State": {token}, "X-Codex-Safety-Buffering-Enabled": {"true"}, "X-Codex-Safety-Buffering-Faster-Model": {"gpt-5.6-luna"}}, Body: io.NopCloser(iotest.OneByteReader(strings.NewReader(tc.stream)))}, nil
			})
			result, err := collector.Collect(context.Background(), CodexTurnStateCollectRequest{Account: account, ProxyID: 2, Model: "gpt-6-astra"})
			if tc.errText != "" {
				require.EqualError(t, err, tc.errText)
				require.Empty(t, result.Tokens)
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, tc.mismatch, result.ModelMismatch())
			require.Equal(t, tc.relation, result.ModelEvidence.ModelRelation)
			require.NotNil(t, result.ModelEvidence.SafetyBufferingEnabled)
			require.True(t, *result.ModelEvidence.SafetyBufferingEnabled)
			require.Equal(t, "gpt-5.6-luna", result.ModelEvidence.SafetyBufferingFasterModel)
			require.Equal(t, "response", result.ModelEvidence.HeaderEvidenceScope)
			require.Equal(t, len(token), result.Observation.TokenLength)
		})
	}
}

func TestCodexModelEvidenceHeadersDoNotInventResponseModel(t *testing.T) {
	var o codexModelEvidenceObserver
	o.observeHeaders(http.Header{"x-codex-safety-buffering-enabled": {"invalid"}, "x-codex-safety-buffering-faster-model": {"gpt-5.6-luna"}}, "connection")
	e := o.snapshot("gpt-6-astra")
	require.Nil(t, e.SafetyBufferingEnabled)
	require.Equal(t, "not_reported", e.ModelRelation)
	require.Empty(t, e.UpstreamResponseModel)
	require.Equal(t, "connection", e.HeaderEvidenceScope)
	o.observePayload([]byte(`{"type":"response.metadata","headers":{"x-codex-safety-buffering-enabled":"false","x-codex-safety-buffering-faster-model":"gpt-5.6-sol"}}`))
	e = o.snapshot("gpt-6-astra")
	require.Equal(t, "response", e.HeaderEvidenceScope)
	require.False(t, *e.SafetyBufferingEnabled)
	require.Equal(t, "not_reported", e.ModelRelation)
	o.observeHeaders(http.Header{"X-Codex-Safety-Buffering-Enabled": {"true"}}, "connection")
	e = o.snapshot("gpt-6-astra")
	require.Empty(t, e.SafetyBufferingFasterModel, "response hints cannot be relabeled as connection evidence")
}

func TestCodexModelEvidenceCookieDiagnosticIsDetachedAndTicketScoped(t *testing.T) {
	expiry := time.Now().Add(time.Minute)
	diagnostic := openaicookies.Diagnostic{Sent: true, Source: "persistent", Reason: "cookie_sent", Names: []string{"__cf_bm"}, Cookies: []openaicookies.DiagnosticCookie{{Name: "__cf_bm", ExpiresAt: &expiry}}, SentCount: 1}
	a := &CodexTurnStateAttempt{Model: "gpt-6-astra"}
	observeCodexCookies(a, diagnostic)
	first := a.SafeObservation()
	require.Zero(t, first.ObservedAt, "cookie/model observation alone is not a ticket")
	require.True(t, first.CookieDiagnostic.Sent)
	diagnostic.Names[0] = "mutated"
	first.CookieDiagnostic.Names[0] = "mutated snapshot"
	*first.CookieDiagnostic.Cookies[0].ExpiresAt = time.Time{}
	second := a.SafeObservation()
	require.Equal(t, []string{"__cf_bm"}, second.CookieDiagnostic.Names)
	require.Equal(t, expiry, *second.CookieDiagnostic.Cookies[0].ExpiresAt)
	encoded, err := json.Marshal(second.CodexModelEvidence)
	require.NoError(t, err)
	require.Contains(t, string(encoded), `"cookie_diagnostic":{"sent":true`)
	require.Contains(t, string(encoded), `"expires_at"`)
	require.NotContains(t, string(encoded), "Value")
	a.finished = true
	observeCodexCookies(a, openaicookies.Diagnostic{Reason: "late"})
	require.Equal(t, "cookie_sent", a.SafeObservation().CookieDiagnostic.Reason)
}

func TestCodexModelEvidenceStatusLatestIsIndependentOfCache(t *testing.T) {
	status := &CodexTurnStateStatus{Models: []CodexTurnStateModelStatus{{OSFamily: "windows", Model: "gpt-6-astra", Shape: "target", TokenLength: 292, CacheAvailable: true}}}
	evidence := CodexModelEvidence{UpstreamResponseModel: "gpt-5.6-luna", ModelRelation: "different", ModelEvidenceSource: "response.model"}
	attachCodexTurnStateObservations(status, true, []CodexTurnStateModelObservation{{OSFamily: "windows", Model: "gpt-6-astra", CodexModelEvidence: evidence}})
	require.Equal(t, evidence, *status.Models[0].LatestResponseEvidence)
	require.Equal(t, "target", status.Models[0].Shape)
	require.Equal(t, 292, status.Models[0].TokenLength)
	require.True(t, status.Models[0].CacheAvailable)
}

func TestCodexModelEvidenceNoTicketDoesNotRewritePreviousObservation(t *testing.T) {
	isolateCodexHistory(t)
	state, owner := codexStateIdentityFixture(t)
	first := prepareCodexIdentityObservation(t, state, owner, owner.GetCredential("access_token"))
	state.ObserveEvent(first, []byte(`{"type":"response.completed","response":{"model":"gpt-6-astra"}}`))
	finishCodexIdentityObservation(t, state, first, codexStateTestToken(10, time.Now()))
	before, err := state.GetStatus(context.Background(), owner.ID)
	require.NoError(t, err)
	require.Equal(t, "exact", before.Observations[0].ModelRelation)
	second := prepareCodexIdentityObservation(t, state, owner, owner.GetCredential("access_token"))
	state.ObserveEvent(second, []byte(`{"type":"response.completed","response":{"model":"gpt-5.6-luna"}}`))
	require.NoError(t, state.Finish(context.Background(), second, true))
	finishOpenAICodexStateObservation(second)
	after, err := state.GetStatus(context.Background(), owner.ID)
	require.NoError(t, err)
	require.Equal(t, before.Observations, after.Observations)
	encoded, err := json.Marshal(after)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "gpt-5.6-luna")
}

func TestOpenAICompatBufferedModelEvidenceIncludesNonTerminalFrames(t *testing.T) {
	for _, tc := range []struct {
		name, frames, model string
		conflict            bool
	}{
		{"created_only", `data: {"type":"response.created","response":{"model":"gpt-5.6-luna"}}` + "\n\n" + `data: {"type":"response.completed","response":{"status":"completed"}}` + "\n\n", "gpt-5.6-luna", false},
		{"conflict", `data: {"type":"response.created","response":{"model":"gpt-5.6-luna"}}` + "\n\n" + `data: {"type":"response.completed","response":{"status":"completed","model":"gpt-6-astra"}}` + "\n\n", "gpt-6-astra", true},
		{"missing", `data: {"type":"response.completed","response":{"status":"completed"}}` + "\n\n", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			s := &OpenAIGatewayService{}
			response, _, _, err := s.readOpenAICompatBufferedTerminal(&http.Response{Body: io.NopCloser(strings.NewReader(tc.frames))}, c, "test", "test")
			require.NoError(t, err)
			require.NotNil(t, response)
			require.Equal(t, tc.model, observedUpstreamResponseModel(c))
			require.Equal(t, tc.conflict, observedUpstreamResponseModelConflict(c))
		})
	}
}
