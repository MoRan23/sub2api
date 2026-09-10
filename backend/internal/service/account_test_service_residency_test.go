//go:build unit

package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/stretchr/testify/require"
)

type accountResidencyHTTPUpstream struct {
	client *http.Client
	target *url.URL
}

func (u *accountResidencyHTTPUpstream) Do(request *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	request = request.Clone(request.Context())
	request.URL.Scheme, request.URL.Host = u.target.Scheme, u.target.Host
	request.Host = ""
	return u.client.Do(request)
}

func (u *accountResidencyHTTPUpstream) DoWithTLS(request *http.Request, proxy string, accountID int64, concurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(request, proxy, accountID, concurrency)
}

func TestAccountTestChatResidencyOnlyAppliesToOpenAI(t *testing.T) {
	for _, test := range []struct {
		name     string
		platform string
		enabled  bool
		want     string
	}{
		{name: "OpenAI enabled", platform: PlatformOpenAI, enabled: true, want: "us"},
		{name: "OpenAI disabled", platform: PlatformOpenAI},
		{name: "CN compatible route", platform: PlatformDeepseek, enabled: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			received := make(chan http.Header, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				received <- r.Header.Clone()
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = w.Write([]byte("data: {\"id\":\"test\",\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":null}]}\n\ndata: [DONE]\n\n"))
			}))
			defer server.Close()
			target, err := url.Parse(server.URL)
			require.NoError(t, err)
			svc := &AccountTestService{httpUpstream: &accountResidencyHTTPUpstream{client: server.Client(), target: target}}
			account := &Account{ID: 10, Platform: test.platform, Type: AccountTypeAPIKey, Concurrency: 1}
			c, _ := newTestContext()
			policy := openai.DefaultRequestPolicy()
			policy.CodexResidencyUS = test.enabled
			c.Request = c.Request.WithContext(openai.WithRequestPolicy(context.Background(), policy))
			require.NoError(t, svc.testOpenAIChatCompletionsConnection(c, account, "test-model", "test", "https://upstream.example", "test-token"))
			headers := <-received
			require.Equal(t, test.want, headers.Get(openai.CodexResidencyHeaderName))
			if test.want != "" {
				require.Equal(t, []string{test.want}, headers.Values(openai.CodexResidencyHeaderName))
			}
		})
	}
}
