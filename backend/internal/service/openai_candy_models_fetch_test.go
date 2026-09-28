package service

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFetchCandyTestModelsAPIKeyUsesLiveUnprojectedCatalog(t *testing.T) {
	var calls atomic.Int32
	type diagnosticContextKey struct{}
	parent := context.WithValue(context.Background(), diagnosticContextKey{}, "kept")
	parent, cancel := context.WithTimeout(parent, 2*time.Second)
	defer cancel()
	s := newCodexModelsAPIKeyTestService(&codexModelsHTTPUpstreamStub{do: func(req *http.Request, proxy string, accountID int64, _ int) (*http.Response, error) {
		call := calls.Add(1)
		assert.EqualValues(t, 2, accountID)
		assert.Equal(t, "", proxy)
		assert.Equal(t, "/v1/models", req.URL.Path)
		if call > 1 {
			assert.True(t, isOpenAICandyTest(req.Context()))
			assert.Equal(t, "kept", req.Context().Value(diagnosticContextKey{}))
			deadline, ok := req.Context().Deadline()
			assert.True(t, ok)
			assert.WithinDuration(t, time.Now(), deadline, 2*time.Second)
			assert.Empty(t, req.Header.Get("If-None-Match"))
		}
		return ordinaryModelsUpstreamResponse(fmt.Sprintf(`{"data":[{"id":"upstream-%d","supported_reasoning_levels":["low","high"]}]}`, call)), nil
	}})
	account := newCodexModelsAPIKeyTestAccount("https://models.example/v1")
	account.Credentials["model_mapping"] = map[string]any{"public-alias": "another-model"}
	cached, err := s.FetchOpenAIModelsList(context.Background(), account)
	require.NoError(t, err)
	for expected := 2; expected <= 3; expected++ {
		response, err := s.FetchCandyTestModels(parent, account)
		require.NoError(t, err)
		require.Contains(t, string(response.Body), fmt.Sprintf(`"id":"upstream-%d"`, expected))
		require.Contains(t, string(response.Body), `"supported_reasoning_levels":["low","high"]`)
		require.NotContains(t, string(response.Body), "public-alias")
	}
	again, err := s.FetchOpenAIModelsList(context.Background(), account)
	require.NoError(t, err)
	require.Equal(t, string(cached.Body), string(again.Body), "diagnostics must not populate the business catalog cache")
	require.EqualValues(t, 3, calls.Load())
}

func TestFetchCandyTestModelsOAuthRetainsCapabilitiesAndDefaultIdentity(t *testing.T) {
	var calls atomic.Int32
	var gotUA atomic.Value
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := calls.Add(1)
		gotUA.Store(r.Header.Get("User-Agent"))
		assert.Equal(t, "Bearer test-access-token", r.Header.Get("Authorization"))
		assert.Empty(t, r.Header.Get("If-None-Match"))
		_, _ = fmt.Fprintf(w, `{"models":[{"slug":"oauth-%d","supported_reasoning_levels":[{"effort":"xhigh"}],"use_responses_lite":true}]}`, call)
	}))
	defer server.Close()
	original := chatgptCodexModelsURL
	chatgptCodexModelsURL = server.URL
	t.Cleanup(func() { chatgptCodexModelsURL = original })
	s := &OpenAIGatewayService{}
	account := newCodexModelsTestAccount()
	account.Credentials["user_agent"] = "codex-tui/0.155.1 (Mac OS 26.6.2; arm64) iTerm.app/3.7.0"
	account.Credentials["model_mapping"] = map[string]any{"public-only": "not-returned"}
	registerAuxiliaryOSFixture(t, s, account)
	for expected := 1; expected <= 2; expected++ {
		response, err := s.FetchCandyTestModels(context.Background(), account)
		require.NoError(t, err)
		require.Contains(t, string(response.Body), fmt.Sprintf(`"slug":"oauth-%d"`, expected))
		require.Contains(t, string(response.Body), `"supported_reasoning_levels":[{"effort":"xhigh"}]`)
		require.NotContains(t, string(response.Body), "public-only")
	}
	require.Contains(t, gotUA.Load(), "(Mac OS 26.6.2; arm64)")
	require.EqualValues(t, 2, calls.Load())
	require.Empty(t, s.openAIModelsCache.entries)
}

func TestFetchCandyTestModelsOAuthErrorsDoNotChangeAccountHealth(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusTooManyRequests} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				w.WriteHeader(status)
				_, _ = w.Write([]byte(`{"error":{"code":"token_revoked"}}`))
			}))
			defer server.Close()
			original := chatgptCodexModelsURL
			chatgptCodexModelsURL = server.URL
			t.Cleanup(func() { chatgptCodexModelsURL = original })
			repo := &codexModelsAccountStateRepo{}
			s := newCodexModels401TestService(repo)
			account := newCodexModelsTestAccount()
			account.Credentials["refresh_token"] = "test-refresh-token"
			registerAuxiliaryOSFixture(t, s, account)
			response, err := s.FetchCandyTestModels(context.Background(), account)
			require.Error(t, err)
			require.Nil(t, response)
			require.EqualValues(t, 1, calls.Load())
			require.Zero(t, repo.setErrorCalls)
			require.Zero(t, repo.setTempUnschedCalls)
			slots, ok := s.accountRepo.(*auxiliaryOSLegacyTestRepository)
			require.True(t, ok)
			require.Empty(t, slots.errors)
			require.Empty(t, slots.cooldowns)
		})
	}
}

func TestFetchCandyTestModelsSetupTokenUsesCodexCatalog(t *testing.T) {
	_, calls := newCodexModelsOAuthCacheServer(t, `{"models":[{"slug":"setup-model"}]}`)
	account := newCodexModelsTestAccount()
	account.Type = AccountTypeSetupToken
	response, err := (&OpenAIGatewayService{}).FetchCandyTestModels(context.Background(), account)
	require.NoError(t, err)
	require.Contains(t, string(response.Body), `"slug":"setup-model"`)
	require.EqualValues(t, 1, calls.Load())
}

func TestFetchCandyTestModelsAgentIdentityDoesNotRecoverFailedTask(t *testing.T) {
	key, privateKey := newTestAgentIdentityKey(t)
	account := &Account{ID: 4, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{
		"auth_mode": OpenAIAuthModeAgentIdentity, "agent_runtime_id": key.runtimeID,
		"agent_private_key": privateKey, "task_id": key.taskID, "chatgpt_account_id": "test-agent",
	}}
	var modelsCalls, registerCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/task/register") {
			registerCalls.Add(1)
			_, _ = w.Write([]byte(`{"task_id":"must-not-register"}`))
			return
		}
		modelsCalls.Add(1)
		assert.True(t, strings.HasPrefix(r.Header.Get("Authorization"), "AgentAssertion "))
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"code":"invalid_task_id"}}`))
	}))
	defer server.Close()
	modelsURL, authURL := chatgptCodexModelsURL, openAIAgentIdentityAuthAPIBaseURL
	chatgptCodexModelsURL, openAIAgentIdentityAuthAPIBaseURL = server.URL, server.URL
	t.Cleanup(func() { chatgptCodexModelsURL, openAIAgentIdentityAuthAPIBaseURL = modelsURL, authURL })
	response, err := (&OpenAIGatewayService{}).FetchCandyTestModels(context.Background(), account)
	require.Error(t, err)
	require.Nil(t, response)
	require.EqualValues(t, 1, modelsCalls.Load())
	require.Zero(t, registerCalls.Load())
	require.Equal(t, key.taskID, account.GetCredential("task_id"))
}

func TestFetchCandyTestModelsPreservesCancellation(t *testing.T) {
	var calls atomic.Int32
	s := newCodexModelsAPIKeyTestService(&codexModelsHTTPUpstreamStub{do: func(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
		calls.Add(1)
		<-req.Context().Done()
		return nil, req.Context().Err()
	}})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	start := time.Now()
	response, err := s.FetchCandyTestModels(ctx, newCodexModelsAPIKeyTestAccount("https://models.example/v1"))
	require.Error(t, err)
	require.Nil(t, response)
	require.Less(t, time.Since(start), time.Second)
	require.EqualValues(t, 1, calls.Load())
	require.ErrorIs(t, ctx.Err(), context.DeadlineExceeded)
}

func TestFetchCandyTestModelsDoesNotFollowRedirects(t *testing.T) {
	var redirected atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redirected" {
			redirected.Add(1)
			_, _ = w.Write([]byte(`{"models":[]}`))
			return
		}
		http.Redirect(w, r, "/redirected", http.StatusFound)
	}))
	defer server.Close()
	original := chatgptCodexModelsURL
	chatgptCodexModelsURL = server.URL
	t.Cleanup(func() { chatgptCodexModelsURL = original })
	s := &OpenAIGatewayService{}
	account := newCodexModelsTestAccount()
	registerAuxiliaryOSFixture(t, s, account)
	response, err := s.FetchCandyTestModels(context.Background(), account)
	require.Error(t, err)
	require.Nil(t, response)
	require.Zero(t, redirected.Load())
}
