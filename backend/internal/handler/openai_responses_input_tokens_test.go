package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestResponsesInputTokensIsClassifiedAsTokenCountRequest(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses/input_tokens", nil)

	require.True(t, isCountTokensRequest(c))
	require.True(t, isTokenCountRequestPath("/responses/input_tokens"))
	require.False(t, isTokenCountRequestPath("/v1/responses"))
}

func TestResponsesInputTokensRejectsCompactionTriggerWithResponsesPathError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses/input_tokens", strings.NewReader(`{"model":"gpt-5.4","input":[{"type":"compaction_trigger"}]}`))

	rejected := (&OpenAIGatewayHandler{}).rejectResponsesInputTokensCompactionTrigger(
		c,
		[]byte(`{"model":"gpt-5.4","input":[{"type":"compaction_trigger"}]}`),
	)

	require.True(t, rejected)
	require.Equal(t, http.StatusBadRequest, recorder.Code)
	require.JSONEq(t, `{"error":{"type":"invalid_request_error","message":"compaction_trigger is only supported on /responses or /responses/compact"}}`, recorder.Body.String())
}

func TestResponsesInputTokensAllowsOrdinaryInput(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses/input_tokens", nil)

	require.False(t, (&OpenAIGatewayHandler{}).rejectResponsesInputTokensCompactionTrigger(
		c,
		[]byte(`{"model":"gpt-5.4","input":[{"type":"message","role":"user"}]}`),
	))
	require.False(t, c.Writer.Written())
}
