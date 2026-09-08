package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestOpenAIModelsCachePreservesOAuthCapabilityNamespaces(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("ChatGPT-Account-ID") == "acc-other" {
			_, _ = w.Write([]byte(`{"models":[{"slug":"gpt-5.6-sol","node_repl_disabled":true}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"models":[{"slug":"gpt-5.6-sol","use_responses_lite":true,"node_repl_auto_review_required":true}]}`))
	}))
	defer server.Close()
	original := chatgptCodexModelsURL
	chatgptCodexModelsURL = server.URL
	t.Cleanup(func() { chatgptCodexModelsURL = original })

	s := &OpenAIGatewayService{}
	first := newCodexModelsTestAccount()
	other := newCodexModelsTestAccount()
	other.ID = 2
	other.Credentials["chatgpt_account_id"] = "acc-other"
	firstNamespace := openAIOutboundSessionIdentityNamespace(first)
	otherNamespace := openAIOutboundSessionIdentityNamespace(other)
	require.NotEqual(t, firstNamespace, otherNamespace)

	list, err := s.FetchOpenAIModelsList(context.Background(), first)
	require.NoError(t, err)
	require.Contains(t, string(list.Body), `"data"`)
	manifest, err := s.FetchCodexModelsManifest(context.Background(), first, CodexCanonicalClientVersion(), "")
	require.NoError(t, err)
	require.Contains(t, string(manifest.Body), `"models"`)
	require.EqualValues(t, 1, calls.Load())
	require.Equal(t, CodexModelCapabilities{Known: true, UseResponsesLite: true, NodeREPLAutoReviewRequired: true}, s.openAICodexModelCapabilities(firstNamespace, "gpt-5.6-sol"))
	require.False(t, s.openAICodexModelCapabilities(otherNamespace, "gpt-5.6-sol").Known)

	_, err = s.FetchCodexModelsManifest(context.Background(), other, CodexCanonicalClientVersion(), "")
	require.NoError(t, err)
	require.EqualValues(t, 2, calls.Load())
	require.Equal(t, CodexModelCapabilities{Known: true, NodeREPLDisabled: true}, s.openAICodexModelCapabilities(otherNamespace, "gpt-5.6-sol"))
	require.True(t, s.openAICodexModelCapabilities(firstNamespace, "gpt-5.6-sol").UseResponsesLite)
}

func TestOpenAIModelsNotModifiedOnlyRefreshesExistingOAuthManifestCapabilities(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotModified)
	}))
	defer server.Close()
	account := newCodexModelsTestAccount()
	namespace := openAIOutboundSessionIdentityNamespace(account)
	for _, tc := range []struct {
		name     string
		manifest bool
		known    bool
	}{
		{name: "manifest refreshes observed capability", manifest: true, known: true},
		{name: "manifest cannot invent capability", manifest: true},
		{name: "raw model response cannot refresh capability", known: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &OpenAIGatewayService{}
			expiredAt := time.Now().Add(-codexModelCapabilityCacheTTL - time.Second)
			s.codexModelCapabilities.observeManifest("unrelated-account", []byte(`{"models":[{"slug":"gpt-5.6-sol"}]}`), expiredAt)
			if tc.known {
				s.codexModelCapabilities.observeManifest(namespace, []byte(`{"models":[{"slug":"gpt-5.6-sol","use_responses_lite":true}]}`), expiredAt)
			}
			request := openAIModelsRequest{url: server.URL, headers: make(http.Header), credentialAccount: account}
			fetch := s.fetchOpenAIModelsUpstream
			if tc.manifest {
				fetch = s.fetchCodexModelsManifestUpstream
			}
			response, err := fetch(context.Background(), request, `"known-manifest"`)
			require.NoError(t, err)
			require.True(t, response.NotModified)
			require.Equal(t, tc.manifest && tc.known, s.openAICodexModelCapabilities(namespace, "gpt-5.6-sol").Known)
			require.False(t, s.openAICodexModelCapabilities(namespace, "unobserved-model").Known)
			require.False(t, s.openAICodexModelCapabilities("unrelated-account", "gpt-5.6-sol").Known)
		})
	}
}
