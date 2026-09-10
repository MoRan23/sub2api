package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/imroc/req/v3"
	"github.com/stretchr/testify/require"
)

func TestOpenAIAuxRequestsRespectFrozenResidencyPolicy(t *testing.T) {
	for _, test := range []struct {
		name     string
		enabled  bool
		existing string
		want     string
	}{
		{name: "enabled", enabled: true, want: "us"},
		{name: "enabled overrides client value", enabled: true, existing: "eu", want: "us"},
		{name: "disabled", want: ""},
		{name: "disabled preserves client value", existing: "eu", want: "eu"},
	} {
		t.Run(test.name, func(t *testing.T) {
			type receivedRequest struct {
				path   string
				values []string
			}
			received := make(chan receivedRequest, 8)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				received <- receivedRequest{path: r.URL.Path, values: r.Header.Values(openai.CodexResidencyHeaderName)}
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/backend-api/accounts/check/v4-2023-04-27":
					_, _ = w.Write([]byte(`{"accounts":{"org-test":{"account":{"account_id":"org-test","plan_type":"plus"}}}}`))
				case "/backend-api/subscriptions":
					_, _ = w.Write([]byte(`{"active_until":"2030-01-01T00:00:00Z"}`))
				case "/backend-api/wham/rate-limit-reset-credits":
					_, _ = w.Write([]byte(`{"available_count":0,"credits":[]}`))
				default:
					_, _ = w.Write([]byte(`{}`))
				}
			}))
			defer server.Close()
			sharedClient, err := newQuotaRedirectingFactory(server)("")
			require.NoError(t, err)
			if test.existing != "" {
				sharedClient.SetCommonHeader(openai.CodexResidencyHeaderName, test.existing)
			}
			factory := func(string) (*req.Client, error) { return sharedClient, nil }
			policy := openai.DefaultRequestPolicy()
			policy.CodexResidencyUS = test.enabled
			ctx := openai.WithRequestPolicy(context.Background(), policy)

			require.Equal(t, PrivacyModeTrainingOff, disableOpenAITraining(ctx, factory, "test-token", ""))
			require.NotNil(t, fetchChatGPTAccountInfo(ctx, factory, "test-token", "", "org-test"))
			require.Equal(t, "2030-01-01T00:00:00Z", fetchChatGPTSubscriptionExpiresAt(ctx, factory, "test-token", "", "org-test"))

			account := &Account{
				ID: 100, Platform: PlatformOpenAI, Type: AccountTypeOAuth,
				Credentials: map[string]any{"chatgpt_account_id": "org-test"},
			}
			repo := &stubQuotaAccountRepo{accounts: map[int64]*Account{account.ID: account}}
			tokens := &stubQuotaTokenCache{tokens: map[string]string{OpenAITokenCacheKey(account): "test-token"}}
			svc := NewOpenAIQuotaService(repo, nil, NewOpenAITokenProvider(repo, tokens, nil), factory)
			_, err = svc.QueryUsage(ctx, account.ID)
			require.NoError(t, err)
			_, err = svc.ResetCreditTargeted(ctx, account.ID, "credit-test", "redeem-test")
			require.NoError(t, err)

			paths := make(map[string]bool)
			for range 6 {
				got := <-received
				paths[got.path] = true
				if test.want == "" {
					require.Empty(t, got.values, got.path)
				} else {
					require.Equal(t, []string{test.want}, got.values, got.path)
				}
			}
			require.Len(t, paths, 6, "all account and quota endpoints must reach upstream")

			// The same cached client may also serve unrelated requests. Its default
			// headers and round-trip middleware must not retain the forced value.
			_, err = sharedClient.R().Get(server.URL + "/unrelated")
			require.NoError(t, err)
			got := <-received
			if test.existing == "" {
				require.Empty(t, got.values)
			} else {
				require.Equal(t, []string{test.existing}, got.values)
			}
		})
	}
}
