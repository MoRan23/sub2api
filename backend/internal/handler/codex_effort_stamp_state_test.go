package handler

import (
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestStampOpenAIRequestedReasoningEffortKeepsResolvedUnknown(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
	c.Request = c.Request.WithContext(service.WithRequestedReasoningEffort(c.Request.Context(), "high"))
	resolved := &service.OpenAIForwardResult{RequestedReasoningEffortResolved: true}
	stampOpenAIRequestedReasoningEffort(resolved, c)
	require.Nil(t, resolved.RequestedReasoningEffort)
	legacy := &service.OpenAIForwardResult{}
	stampOpenAIRequestedReasoningEffort(legacy, c)
	require.Equal(t, "high", *legacy.RequestedReasoningEffort)
}
