package repository

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestGrokAccessDeniedFallbackPreservesSafetyRejection(t *testing.T) {
	for _, fallback := range []bool{false, true} {
		t.Run(map[bool]string{false: "primary", true: "fallback"}[fallback], func(t *testing.T) {
			calls := 0
			transport := &grokAccessDeniedFallbackTransport{base: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				calls++
				body := `{"error":{"code":"safety_review_rejected","message":"Access denied"},"usage":{"input_tokens":3}}`
				if fallback && calls == 1 {
					body = `{"error":"Access denied"}`
				}
				return &http.Response{StatusCode: 403, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
			})}
			ctx := service.WithGrokSafetyFee(context.Background(), 0.2)
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://cli-chat-proxy.grok.com/v1/responses", strings.NewReader(`{"model":"grok-4"}`))
			require.NoError(t, err)
			req.Header.Set("Authorization", "Bearer test-token")
			req.Header.Set("X-XAI-Token-Auth", "xai-grok-cli")
			req = service.PrepareGrokSafetyRequest(req)
			resp, err := transport.RoundTrip(req)
			require.NoError(t, err)
			data, err := io.ReadAll(resp.Body)
			require.NoError(t, err)
			require.NoError(t, resp.Body.Close())
			require.True(t, service.IsGrokSafetyRejectionJSON(data))
			result, err := service.FinalizeGrokSafetyRejection(ctx, nil, nil)
			require.ErrorIs(t, err, service.ErrGrokSafetyRejected)
			require.Equal(t, 3, result.Usage.InputTokens)
			if fallback {
				require.Equal(t, 2, calls)
			} else {
				require.Equal(t, 1, calls)
			}
		})
	}
}
