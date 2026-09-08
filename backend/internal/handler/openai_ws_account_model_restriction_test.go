package handler

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

func TestOpenAIResponsesWebSocket_AccountModelRestrictionChangedAfterConnect(t *testing.T) {
	for _, mode := range []string{service.OpenAIWSIngressModeCtxPool, service.OpenAIWSIngressModePassthrough} {
		t.Run(mode, func(t *testing.T) {
			runOpenAIResponsesWebSocketUsageLogCase(t, openAIResponsesWSUsageLogCase{
				firstPayload:  `{"type":"response.create","model":"gpt-5.6-sol","stream":false}`,
				secondPayload: `{"type":"response.create","model":"gpt-5.6-sol","stream":false}`,
				ingressMode:   mode,
				accountModelMapping: map[string]any{
					"gpt-5.6-sol":   "gpt-5.6-sol",
					"gpt-5.6-terra": "gpt-5.6-terra",
				},
				afterFirstAccountMapping:  map[string]any{"gpt-5.6-terra": "gpt-5.6-terra"},
				accountModelCloseExpected: true,
			})
		})
	}
}
