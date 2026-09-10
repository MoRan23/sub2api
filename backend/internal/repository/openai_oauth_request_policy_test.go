package repository

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/stretchr/testify/require"
)

func TestOpenAIOAuthRequestPolicyActualRequests(t *testing.T) {
	requests := make(chan http.Header, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"test-token","expires_in":3600}`)
	}))
	defer server.Close()
	svc := &openaiOAuthService{tokenURL: server.URL}
	for _, enabled := range []bool{false, true} {
		policy := openai.DefaultRequestPolicy()
		policy.CodexResidencyUS = enabled
		ctx := openai.WithRequestPolicy(context.Background(), policy)
		_, err := svc.ExchangeCode(ctx, "test-code", "test-verifier", "", "", "")
		require.NoError(t, err)
		_, err = svc.RefreshTokenWithClientID(ctx, "test-refresh", "", "")
		require.NoError(t, err)
		for range 2 {
			values := (<-requests).Values(openai.CodexResidencyHeaderName)
			if enabled {
				require.Equal(t, []string{"us"}, values)
			} else {
				require.Empty(t, values)
			}
		}
	}
}
