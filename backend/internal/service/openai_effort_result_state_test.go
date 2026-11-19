package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOpenAIForwardResultFreezesUnknownAfterCredentialChange(t *testing.T) {
	priorAccount := &Account{ID: 3101, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "first"}}
	priorState := &openAIEffortRequestState{
		previousID: "resp_prior", found: true,
		inherited: OpenAIResponseEffortState{AccountID: priorAccount.ID, Identity: openAIEffortIdentity(priorAccount), Override: true, Effort: OpenAIEffortValue{Type: "string", Value: "high"}},
	}
	ingressCtx := WithRequestedReasoningEffort(context.WithValue(context.Background(), openAIEffortContextKey{}, priorState), "high")
	replacement := *priorAccount
	replacement.Credentials = map[string]any{"api_key": "replacement"}
	svc := &OpenAIGatewayService{}
	attemptCtx, err := svc.PrepareOpenAIReasoningEffortContext(ingressCtx, nil, []byte(`{"previous_response_id":"resp_prior","model":"gpt-6-cyber","input":[]}`), &replacement)
	require.NoError(t, err)
	result := &OpenAIForwardResult{}
	freezeOpenAIForwardRequestedReasoningEffort(result, attemptCtx)
	require.True(t, result.RequestedReasoningEffortResolved)
	require.Nil(t, result.RequestedReasoningEffort)
	require.Equal(t, "high", *RequestedReasoningEffortFromContext(ingressCtx), "another attempt must keep its immutable ingress context")
	require.True(t, priorState.found)
}

func TestOpenAIForwardResultFreezesResolvedRequestedValue(t *testing.T) {
	ctx := WithRequestedReasoningEffort(context.WithValue(context.Background(), openAIEffortContextKey{}, &openAIEffortRequestState{}), "CustomEffort")
	result := &OpenAIForwardResult{}
	freezeOpenAIForwardRequestedReasoningEffort(result, ctx)
	require.True(t, result.RequestedReasoningEffortResolved)
	require.Equal(t, "CustomEffort", *result.RequestedReasoningEffort)
	legacy := &OpenAIForwardResult{}
	freezeOpenAIForwardRequestedReasoningEffort(legacy, context.Background())
	require.False(t, legacy.RequestedReasoningEffortResolved)
}
