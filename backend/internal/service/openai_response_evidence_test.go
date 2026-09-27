package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestOpenAIResponseEvidenceHTTPUsesFinalModelAndPreservesContext(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	state := beginOpenAIResponseEvidence(c, "actual-model")
	const body = `{"model":"actual-model","input":"private input","previous_response_id":"private-response"}`
	request, err := http.NewRequest(http.MethodPost, "https://example.invalid/responses", strings.NewReader(body))
	require.NoError(t, err)
	request = markOpenAIResponseEvidenceHTTPRequest(request, c)
	type preservedKey struct{}
	responseRequest := request.WithContext(context.WithValue(request.Context(), preservedKey{}, "preserved"))
	response := &http.Response{Request: responseRequest, Header: http.Header{
		"X-Codex-Safety-Buffering-Enabled":      {"true"},
		"X-Codex-Safety-Buffering-Faster-Model": {"hint-model"},
		"X-Codex-Turn-State":                    {"opaque-token"},
		"Set-Cookie":                            {"__oailb=private-cookie"},
	}}
	observeOpenAIHTTPResponseEvidence(request, response)
	require.Equal(t, "preserved", response.Request.Context().Value(preservedKey{}))
	observeOpenAIHTTPResponseEvidencePayload(response, []byte(`{"type":"response.completed","response":{"model":"actual-model","output":[{"text":"private output"}]}}`))
	evidence := state.snapshot()
	require.Equal(t, "actual-model", evidence.UpstreamResponseModel)
	require.Equal(t, "exact", evidence.ModelRelation)
	require.Equal(t, "response", evidence.HeaderEvidenceScope)
	require.True(t, *evidence.SafetyBufferingEnabled)
	actualBody, err := io.ReadAll(request.Body)
	require.NoError(t, err)
	require.Equal(t, body, string(actualBody), "evidence extraction cannot consume the send body")
	encoded, err := json.Marshal(evidence)
	require.NoError(t, err)
	for _, forbidden := range []string{"opaque-token", "private-cookie", "private input", "private output", "private-response", "turn_state", "cookie", "encrypted_content"} {
		require.NotContains(t, string(encoded), forbidden)
	}
}

func TestOpenAIResponseEvidenceWSScopeDoesNotMixConnectionAndResponseHints(t *testing.T) {
	state := beginOpenAIResponseEvidence(nil, "gpt-6-astra")
	observeOpenAIResponseEvidenceHeaders(state, http.Header{
		"X-Codex-Safety-Buffering-Enabled":      {"true"},
		"X-Codex-Safety-Buffering-Faster-Model": {"gpt-5.6-luna"},
	}, "connection")
	before := state.snapshot()
	require.Equal(t, "connection", before.HeaderEvidenceScope)
	require.Equal(t, "not_reported", before.ModelRelation)
	observeOpenAIResponseEvidenceEvent(state, []byte(`{"type":"response.metadata","headers":{"x-codex-safety-buffering-enabled":"false","x-codex-turn-state":"opaque-ws"}}`))
	observeOpenAIResponseEvidenceEvent(state, []byte(`{"type":"response.completed","response":{"model":"gpt-6-astra"}}`))
	after := state.snapshot()
	require.Equal(t, "response", after.HeaderEvidenceScope)
	require.False(t, *after.SafetyBufferingEnabled)
	require.Empty(t, after.SafetyBufferingFasterModel, "a connection hint cannot be relabeled as response evidence")
	require.Equal(t, "exact", after.ModelRelation)
	require.True(t, *before.SafetyBufferingEnabled, "previous snapshots remain immutable")
}

func TestOpenAIResponseEvidenceLateResponseCannotUpdateNextRequest(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	first := beginOpenAIResponseEvidence(c, "first")
	request, err := http.NewRequest(http.MethodPost, "https://example.invalid/responses", strings.NewReader(`{"model":"first"}`))
	require.NoError(t, err)
	request = markOpenAIResponseEvidenceHTTPRequest(request, c)
	firstResponse := &http.Response{Header: make(http.Header)}
	observeOpenAIHTTPResponseEvidence(request, firstResponse)
	second := beginOpenAIResponseEvidence(c, "second")
	observeOpenAIHTTPResponseEvidencePayload(firstResponse, []byte(`{"type":"response.completed","response":{"model":"first"}}`))
	require.Same(t, second, responseEvidenceFromContext(c))
	require.Equal(t, "first", first.snapshot().UpstreamResponseModel)
	require.Equal(t, "not_reported", second.snapshot().ModelRelation)
	observeOpenAIResponseEvidenceEvent(second, []byte(`{"model":"second"}`))
	require.Equal(t, "second", second.snapshot().UpstreamResponseModel)
	require.Equal(t, "first", first.snapshot().UpstreamResponseModel)
}

func TestOpenAIResponseEvidenceRetainsOriginalDeclarationConflicts(t *testing.T) {
	state := beginOpenAIResponseEvidence(nil, "gpt-6-astra")
	observeOpenAIResponseEvidenceEvent(state, []byte(`{"type":"response.created","response":{"model":"gpt-5.6-luna"}}`))
	observeOpenAIResponseEvidenceEvent(state, []byte(`{"type":"response.completed","response":{"model":"gpt-6-astra"}}`))
	evidence := state.snapshot()
	require.True(t, evidence.ModelConflict)
	require.Equal(t, "conflicting", evidence.ModelRelation)
	require.Equal(t, "gpt-6-astra", evidence.UpstreamResponseModel)
	require.Equal(t, "response.model", evidence.ModelEvidenceSource)
}

func TestOpenAIResponseEvidenceSnapshotsAreIndependentAndConcurrent(t *testing.T) {
	state := beginOpenAIResponseEvidence(nil, "model")
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 100 {
				observeOpenAIResponseEvidenceHeaders(state, http.Header{"X-Codex-Safety-Buffering-Enabled": {"true"}}, "response")
				observeOpenAIResponseEvidenceEvent(state, []byte(`{"model":"model"}`))
				snapshot := state.snapshot()
				if snapshot.SafetyBufferingEnabled != nil {
					*snapshot.SafetyBufferingEnabled = false
				}
			}
		}()
	}
	wg.Wait()
	require.True(t, *state.snapshot().SafetyBufferingEnabled)
	require.Equal(t, "exact", state.snapshot().ModelRelation)
}

func TestOpenAIResponseEvidenceFingerprintUpdateClonesAndOmitsTicketFields(t *testing.T) {
	observer := &fingerprintObserver{ring: []FingerprintObservationEntry{{SequenceID: 10}, {SequenceID: 11}}}
	observer.enabled.Store(true)
	enabled := true
	evidence := CodexModelEvidence{UpstreamResponseModel: "model", ModelRelation: "exact", SafetyBufferingEnabled: &enabled}
	observer.updateResponseEvidence(10, evidence)
	enabled = false
	require.True(t, *observer.ring[0].ResponseEvidence.SafetyBufferingEnabled)
	require.Nil(t, observer.ring[1].ResponseEvidence)
	encoded, err := json.Marshal(observer.ring[0])
	require.NoError(t, err)
	require.True(t, gjson.GetBytes(encoded, "response_evidence").Exists())
	require.False(t, gjson.GetBytes(encoded, "codex_turn_state").Exists())
	require.False(t, gjson.GetBytes(encoded, "codex_cookies").Exists())
	observer.enabled.Store(false)
	observer.updateResponseEvidence(11, evidence)
	require.Nil(t, observer.ring[1].ResponseEvidence)
}
