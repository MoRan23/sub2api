package service

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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
