package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestEnsureOpenAIOAuthIdentityCaptureFallback(t *testing.T) {
	gin.SetMode(gin.TestMode)

	t.Run("prompt cache key wins over gateway session hash", func(t *testing.T) {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
		body := []byte(`{"model":"gpt-5.4","messages":[]}`)
		service.SetOpenAIOAuthIdentityCapture(c, service.CaptureOpenAIOAuthIdentity(c, body, ""))

		ensureOpenAIOAuthIdentityCaptureFallback(c, "prompt-key", "gateway-hash")

		capture, ok := service.OpenAIOAuthIdentityCaptureFromContext(c)
		require.True(t, ok)
		require.Equal(t, "prompt-key", capture.Logical.SessionKey)
		require.Equal(t, "prompt-key", capture.Logical.ThreadKey)
	})

	t.Run("explicit original identity is not overwritten", func(t *testing.T) {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
		body := []byte(`{"model":"gpt-5.4","client_metadata":{"session_id":"explicit-session","thread_id":"explicit-thread"}}`)
		service.SetOpenAIOAuthIdentityCapture(c, service.CaptureOpenAIOAuthIdentity(c, body, ""))

		ensureOpenAIOAuthIdentityCaptureFallback(c, "prompt-key", "gateway-hash")

		capture, ok := service.OpenAIOAuthIdentityCaptureFromContext(c)
		require.True(t, ok)
		require.Equal(t, "explicit-session", capture.Logical.SessionKey)
		require.Equal(t, "explicit-thread", capture.Logical.ThreadKey)
	})

	t.Run("fallback does not parse invalid metadata twice", func(t *testing.T) {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
		body := []byte(`{"model":"gpt-5.4","x-codex-turn-metadata":"opaque"}`)
		service.SetOpenAIOAuthIdentityCapture(c, service.CaptureOpenAIOAuthIdentity(c, body, ""))

		before, ok := service.OpenAIOAuthIdentityCaptureFromContext(c)
		require.True(t, ok)
		require.Equal(t, 1, before.InvalidMetadataCount)

		ensureOpenAIOAuthIdentityCaptureFallback(c, "gateway-hash")

		after, ok := service.OpenAIOAuthIdentityCaptureFromContext(c)
		require.True(t, ok)
		require.Equal(t, before.InvalidMetadataCount, after.InvalidMetadataCount)
		require.Equal(t, "gateway-hash", after.Logical.SessionKey)
	})
}

func TestCompatHandlerPreCaptureForcesTurnBeforeFallback(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"model":"gpt-5.6-sol","client_metadata":{"x-codex-turn-metadata":"{\"request_kind\":\"memory\",\"custom\":\"keep\"}"}}`)

	for _, endpoint := range []string{"/v1/chat/completions", "/v1/messages"} {
		t.Run(endpoint, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, endpoint, nil)
			service.SetOpenAIOAuthIdentityCapture(c, service.CaptureOpenAIOAuthIdentityForCompatTurn(c, body, ""))

			before, ok := service.OpenAIOAuthIdentityCaptureFromContext(c)
			require.True(t, ok)
			require.Equal(t, service.CodexWireRequestTurn, before.WireProfile.RequestKind)
			typedTurnID, valid := service.ResolveCodexTurnID(before.RequestTurn.ID, service.CodexWireRequestTurn)
			require.True(t, valid)
			require.Equal(t, service.CodexTurnIDUserUUIDv7, typedTurnID.Kind)

			ensureOpenAIOAuthIdentityCaptureFallback(c, "derived-prompt-seed")
			after, ok := service.OpenAIOAuthIdentityCaptureFromContext(c)
			require.True(t, ok)
			require.Equal(t, "derived-prompt-seed", after.Logical.SessionKey)
			require.Equal(t, before.RequestTurn, after.RequestTurn)
			require.Equal(t, service.CodexWireRequestTurn, after.WireProfile.RequestKind)

			plan, err := service.FinalizeOpenAICodexWirePlan(service.OpenAIOAuthIdentityPlan{
				Capture:               after,
				RequestTurn:           after.RequestTurn,
				WireProfile:           after.WireProfile,
				TurnIdentityRequested: true,
				ProjectionMode:        service.OpenAIOAuthIdentityProjectionRegular,
				InstallationPolicy:    service.OpenAIOAuthInstallationPreserve,
			}, string(service.CodexWireRequestTurn), service.CodexModelCapabilities{})
			require.NoError(t, err)
			out, err := service.ApplyOpenAIOAuthIdentityPlan(http.Header{}, body, plan)
			require.NoError(t, err)
			nested := gjson.GetBytes(out, "client_metadata.x-codex-turn-metadata").String()
			require.Equal(t, "turn", gjson.Get(nested, "request_kind").String())
			require.Equal(t, after.RequestTurn.ID, gjson.Get(nested, "turn_id").String())
		})
	}
}
