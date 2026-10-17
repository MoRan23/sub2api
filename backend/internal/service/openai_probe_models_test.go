package service

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFetchOpenAIProbeModelsSharesRawAPIKeyCache(t *testing.T) {
	var calls atomic.Int32
	s := newCodexModelsAPIKeyTestService(&codexModelsHTTPUpstreamStub{do: func(_ *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
		calls.Add(1)
		return ordinaryModelsUpstreamResponse(`{"data":[{"id":"upstream"}]}`), nil
	}})
	account := newCodexModelsAPIKeyTestAccount("https://models.example/v1")
	account.Credentials["model_mapping"] = map[string]any{"alias": "target"}
	first, err := s.FetchOpenAIModelsList(context.Background(), account)
	require.NoError(t, err)
	second, err := s.FetchOpenAIProbeModels(context.Background(), account)
	require.NoError(t, err)
	require.Equal(t, first.Body, second.Body)
	require.EqualValues(t, 1, calls.Load())
	require.NotContains(t, string(second.Body), "alias")
	require.NotContains(t, string(second.Body), "target")
}

func TestFetchOpenAIProbeModelsOAuthSharesCatalogCache(t *testing.T) {
	_, calls := newCodexModelsOAuthCacheServer(t, `{"models":[{"slug":"real-model"}]}`)
	s := &OpenAIGatewayService{}
	account := newCodexModelsTestAccount()
	registerAuxiliaryOSFixture(t, s, account)
	first, err := s.FetchOpenAIProbeModels(context.Background(), account)
	require.NoError(t, err)
	second, err := s.FetchOpenAIModelsList(context.Background(), account)
	require.NoError(t, err)
	require.Equal(t, first.Body, second.Body)
	require.EqualValues(t, 1, calls.Load())
}

func TestFetchOpenAIProbeModelsOAuthFailureDoesNotChangeHealth(t *testing.T) {
	for _, status := range []int{401, 403, 429, 503} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(status)
				_, _ = w.Write([]byte(`{"error":{"code":"token_revoked"}}`))
			}))
			t.Cleanup(server.Close)
			original := chatgptCodexModelsURL
			chatgptCodexModelsURL = server.URL
			t.Cleanup(func() { chatgptCodexModelsURL = original })
			repo := &codexModelsAccountStateRepo{}
			s := newCodexModels401TestService(repo)
			account := newCodexModelsTestAccount()
			registerAuxiliaryOSFixture(t, s, account)
			_, err := s.FetchOpenAIProbeModels(context.Background(), account)
			require.Error(t, err)
			require.Zero(t, repo.setErrorCalls)
			require.Zero(t, repo.setTempUnschedCalls)
			slots, ok := s.accountRepo.(*auxiliaryOSLegacyTestRepository)
			require.True(t, ok)
			require.Empty(t, slots.errors)
			require.Empty(t, slots.cooldowns)
		})
	}
}
