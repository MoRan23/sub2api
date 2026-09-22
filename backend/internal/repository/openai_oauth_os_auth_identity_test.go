package repository

import (
	"context"
	"net/http"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestOpenAIOAuthTokenExchangeAndRefreshUseBoundOSIdentity(t *testing.T) {
	for _, os := range service.OpenAIOAuthOSFamilies() {
		t.Run(os, func(t *testing.T) {
			account := &service.Account{ID: 7, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth}
			profiles, err := service.BuildOpenAIOAuthOSProfiles(account, nil)
			require.NoError(t, err)
			ua := profiles.Profiles[os].UserAgent
			account.Credentials = map[string]any{"user_agent": ua}
			seen := make(chan string, 2)
			server := newLocalTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				seen <- r.Header.Get("User-Agent")
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"access_token":"test-at","refresh_token":"test-rt","expires_in":3600}`))
			}))
			defer server.Close()
			client := &openaiOAuthService{tokenURL: server.URL}
			ctx := service.WithOpenAINativeHTTPScope(context.Background(), account, "")
			_, err = client.ExchangeCode(ctx, "code", "verifier", "", "", "")
			require.NoError(t, err)
			require.Equal(t, ua, <-seen)
			_, err = client.RefreshTokenWithClientID(ctx, "refresh-token", "", "")
			require.NoError(t, err)
			require.Equal(t, ua, <-seen)
		})
	}
}
