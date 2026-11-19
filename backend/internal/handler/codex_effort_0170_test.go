package handler

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestCodex0170HTTPContinuationPolicyAndRequestedEffort(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
	groupID := int64(4)
	key := &service.APIKey{ID: 3, UserID: 2, GroupID: &groupID, Group: &service.Group{
		Platform: service.PlatformOpenAI, MaxReasoningEffort: "medium",
		ReasoningEffortMappings: []service.ReasoningEffortMapping{{From: "high", To: "low"}, {From: "low", To: "minimal"}},
	}}
	c.Set("api_key", key)
	svc := &service.OpenAIGatewayService{}
	account := &service.Account{ID: 5, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey}
	parent := []byte(`{"model":"gpt-6-cyber","input":[{"type":"configuration_update","reasoning":{"effort":"high"}}]}`)
	require.NoError(t, svc.RecordOpenAIResponseEffort(context.Background(), c, parent, "resp_parent", account))
	body := []byte(`{"model":"gpt-6-cyber","previous_response_id":"resp_parent","reasoning":{"effort":"medium"},"input":[]}`)
	got, changed, err := applyOpenAIReasoningEffortPolicyForRequest(c, key, body, svc)
	require.NoError(t, err)
	require.True(t, changed)
	require.Equal(t, "medium", gjson.GetBytes(got, "reasoning.effort").String())
	require.Equal(t, "low", gjson.GetBytes(got, "input.0.reasoning.effort").String())
	require.Equal(t, "high", *service.RequestedReasoningEffortFromContext(c.Request.Context()))
	ctx, err := svc.PrepareOpenAIReasoningEffortContext(c.Request.Context(), c, got, account)
	require.NoError(t, err)
	require.Equal(t, "low", *service.EffectiveOpenAIReasoningEffortForBody(ctx, got, "gpt-6-cyber"))
}

func TestCodex0170HTTPUnknownContinuationWithCapRejected(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
	key := &service.APIKey{Group: &service.Group{Platform: service.PlatformOpenAI, MaxReasoningEffort: "medium"}}
	_, _, err := applyOpenAIReasoningEffortPolicyForRequest(c, key, []byte(`{"model":"gpt-6-cyber","previous_response_id":"resp_missing","reasoning":{"effort":"low"}}`), &service.OpenAIGatewayService{})
	var continuation *service.ReasoningEffortContinuationError
	require.ErrorAs(t, err, &continuation)
}
