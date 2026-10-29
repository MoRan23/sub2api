package service

import (
	"context"
	"io"
	"maps"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestOpenAIDaybreakAPIKeyCatalogUsesRawProgramsAndSingleflight(t *testing.T) {
	var calls atomic.Int32
	upstream := &codexModelsHTTPUpstreamStub{do: func(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
		calls.Add(1)
		require.Equal(t, "/v1/models", req.URL.Path)
		require.Equal(t, "Bearer sk-upstream", req.Header.Get("Authorization"))
		return &http.Response{StatusCode: 200, Header: http.Header{"Etag": {`"catalog-one"`}}, Body: io.NopCloser(strings.NewReader(`{"data":[{"id":"gpt-6-sol","available_access_programs":{"cyber":["daybreak_blue"]}},{"id":"gpt-6-astra","available_access_programs":{"cyber":["daybreak_blue"]}},{"id":"gpt-5.6-cyber","available_access_programs":{"cyber":["daybreak_red"]}},{"id":"gpt-5.5"},{"id":"unknown-model","available_access_programs":{"cyber":["daybreak_red"]}}]}`))}, nil
	}}
	s := newCodexModelsAPIKeyTestService(upstream)
	account := newCodexModelsAPIKeyTestAccount("https://daybreak.invalid/v1")
	account.Credentials["model_mapping"] = map[string]any{"gpt-daybreak-blue-latest": "gpt-6-sol"}
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			caps, err := s.GetOpenAIDaybreakCapabilities(context.Background(), account)
			require.NoError(t, err)
			require.True(t, caps.BlueAvailable)
			require.True(t, caps.RedAvailable)
			require.Len(t, caps.Models, 3, "local aliases and static descriptors are not live grants")
			require.NotNil(t, caps.CheckedAt)
			require.Equal(t, account.ID, caps.CredentialOwnerID)
			require.Empty(t, caps.AuthorizationGeneration)
			require.Equal(t, OpenAIDaybreakAPIKeyCredentialIdentity(account), caps.CredentialIdentity)
		}()
	}
	wg.Wait()
	require.EqualValues(t, 1, calls.Load())
}

func TestOpenAIDaybreakAPIKeyDoesNotTrustCompletedOrCachedDisplayMetadata(t *testing.T) {
	s := newCodexModelsAPIKeyTestService(&codexModelsHTTPUpstreamStub{do: func(*http.Request, string, int64, int) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"data":[{"id":"gpt-6-sol"}]}`))}, nil
	}})
	account := newCodexModelsAPIKeyTestAccount("https://daybreak.invalid")
	caps, err := s.GetOpenAIDaybreakCapabilities(context.Background(), account)
	require.NoError(t, err)
	require.False(t, caps.BlueAvailable)
	s.openAIModelsCache.mu.Lock()
	for key, entry := range s.openAIModelsCache.entries {
		entry.manifest.Body = []byte(`{"models":[{"slug":"gpt-6-sol","available_access_programs":{"cyber":["daybreak_blue"]}}]}`)
		s.openAIModelsCache.entries[key] = entry
	}
	s.openAIModelsCache.mu.Unlock()
	s.codexModelCapabilities.mu.Lock()
	s.codexModelCapabilities.entries = nil
	s.codexModelCapabilities.mu.Unlock()
	caps, err = s.GetOpenAIDaybreakCapabilities(context.Background(), account)
	require.NoError(t, err)
	require.False(t, caps.BlueAvailable, "the raw upstream catalog is authoritative even if display metadata contains a grant")
}

func TestOpenAIDaybreakAPIKeyCredentialNamespaceIsolation(t *testing.T) {
	account := newCodexModelsAPIKeyTestAccount("https://daybreak.invalid")
	base := openAIDaybreakCapabilitiesNamespace(account)
	s := &OpenAIGatewayService{}
	s.observeOpenAIDaybreakAPIKeyCatalog(account, []byte(`{"data":[{"id":"gpt-6-sol","available_access_programs":{"cyber":["daybreak_blue"]}}]}`), time.Now())
	require.True(t, s.openAICodexModelCapabilities(base, "gpt-6-sol").DaybreakBlue)
	for name, mutate := range map[string]func(*Account){
		"key":    func(a *Account) { a.Credentials["api_key"] = "replacement-key" },
		"url":    func(a *Account) { a.Credentials["base_url"] = "https://other.invalid" },
		"owner":  func(a *Account) { a.ID++ },
		"parent": func(a *Account) { parent := int64(100); a.ParentAccountID = &parent },
		"headers": func(a *Account) {
			a.Credentials["header_override_enabled"] = true
			a.Credentials["header_overrides"] = map[string]any{"openai-project": "different-project"}
		},
		"engine": func(a *Account) { a.Extra[OpenAIAPIKeyModeExtraKey] = "codex_engine" },
	} {
		t.Run(name, func(t *testing.T) {
			changed := *account
			changed.Credentials = maps.Clone(account.Credentials)
			changed.Extra = make(map[string]any)
			mutate(&changed)
			namespace := openAIDaybreakCapabilitiesNamespace(&changed)
			require.NotEqual(t, base, namespace)
			require.False(t, s.openAICodexModelCapabilities(namespace, "gpt-6-sol").Known)
		})
	}
	account.Extra = map[string]any{OpenAIAPIKeyModeExtraKey: "generic", OpenAIDaybreakBlueEnabledKey: true}
	require.Equal(t, base, openAIDaybreakCapabilitiesNamespace(account), "explicit generic mode and preference edits preserve identity")
}

func TestOpenAIDaybreakAPIKey304RevocationAndRestore(t *testing.T) {
	var phase atomic.Int32
	s := newCodexModelsAPIKeyTestService(&codexModelsHTTPUpstreamStub{do: func(*http.Request, string, int64, int) (*http.Response, error) {
		if phase.Load() == 1 {
			return &http.Response{StatusCode: http.StatusNotModified, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(""))}, nil
		}
		body := `{"data":[{"id":"gpt-6-sol","available_access_programs":{"cyber":["daybreak_blue"]}}]}`
		if phase.Load() == 2 {
			body = `{"data":[]}`
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	}})
	account := newCodexModelsAPIKeyTestAccount("https://daybreak.invalid")
	request := openAIModelsRequest{url: "https://daybreak.invalid/v1/models", headers: make(http.Header), credentialAccount: account, useAPIKeyUpstream: true}
	manifest, err := s.fetchCodexModelsManifestUpstream(context.Background(), request, "")
	require.NoError(t, err)
	namespace := openAIDaybreakCapabilitiesNamespace(account)
	require.False(t, s.codexModelCapabilities.get(namespace, "gpt-6-sol", time.Now().Add(codexModelCapabilityCacheTTL)).Known)
	phase.Store(1)
	response, err := s.fetchCodexModelsManifestUpstream(context.Background(), request, `"one"`)
	require.NoError(t, err)
	require.True(t, response.NotModified)
	require.True(t, s.openAICodexModelCapabilities(namespace, "gpt-6-sol").DaybreakBlue)
	phase.Store(2)
	revoked, err := s.fetchCodexModelsManifestUpstream(context.Background(), request, "")
	require.NoError(t, err)
	require.True(t, revoked.observedAt.After(response.observedAt))
	require.True(t, response.observedAt.After(manifest.observedAt))
	require.False(t, s.openAICodexModelCapabilities(namespace, "gpt-6-sol").Known)
	s.observeOpenAIDaybreakAPIKeyCatalog(account, manifest.upstreamSourceBody, manifest.observedAt)
	require.False(t, s.openAICodexModelCapabilities(namespace, "gpt-6-sol").Known, "cached older catalog cannot restore withdrawn capability")
}

func TestOpenAIDaybreakCatalogEqualClockTicksCannotResurrectRevokedCapability(t *testing.T) {
	s := &OpenAIGatewayService{}
	account := newCodexModelsAPIKeyTestAccount("https://daybreak.invalid")
	tick := time.Now()
	first := s.openAIModelsCache.nextObservationTime(tick)
	grant := []byte(`{"data":[{"id":"gpt-6-sol","available_access_programs":{"cyber":["daybreak_blue"]}}]}`)
	s.observeOpenAIDaybreakAPIKeyCatalog(account, grant, first)
	revoked := s.openAIModelsCache.nextObservationTime(tick)
	require.True(t, revoked.After(first), "responses completed in the same clock tick must have distinct ordering")
	s.observeOpenAIDaybreakAPIKeyCatalog(account, []byte(`{"data":[]}`), revoked)
	s.observeOpenAIDaybreakAPIKeyCatalog(account, grant, first)
	require.False(t, s.openAICodexModelCapabilities(openAIDaybreakCapabilitiesNamespace(account), "gpt-6-sol").Known)
	require.True(t, s.openAIModelsCache.nextObservationTime(tick.Add(-time.Second)).After(revoked), "wall-clock regression cannot reorder catalog evidence")
}

func TestOpenAIDaybreakAPIKeyCredentialChangeFetchesNewEvidence(t *testing.T) {
	var calls atomic.Int32
	s := newCodexModelsAPIKeyTestService(&codexModelsHTTPUpstreamStub{do: func(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
		calls.Add(1)
		body := `{"data":[{"id":"gpt-6-sol","available_access_programs":{"cyber":["daybreak_blue"]}}]}`
		if req.Header.Get("Authorization") != "Bearer sk-upstream" {
			body = `{"data":[{"id":"gpt-6-sol"}]}`
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	}})
	account := newCodexModelsAPIKeyTestAccount("https://daybreak.invalid")
	first, err := s.GetOpenAIDaybreakCapabilities(context.Background(), account)
	require.NoError(t, err)
	require.True(t, first.BlueAvailable)
	account.Credentials = maps.Clone(account.Credentials)
	account.Credentials["api_key"] = "new-key"
	second, err := s.GetOpenAIDaybreakCapabilities(context.Background(), account)
	require.NoError(t, err)
	require.False(t, second.BlueAvailable)
	require.NotEqual(t, first.CredentialIdentity, second.CredentialIdentity)
	require.EqualValues(t, 2, calls.Load())
}

func TestDaybreakAdminAPIKeySettingsAndEvidence(t *testing.T) {
	account := newCodexModelsAPIKeyTestAccount("https://daybreak.invalid")
	account.Extra = map[string]any{}
	calls := 0
	s := &adminServiceImpl{daybreakCapabilities: daybreakAdminReader(func(_ context.Context, a *Account) (*OpenAIDaybreakCapabilities, error) {
		calls++
		return &OpenAIDaybreakCapabilities{BlueAvailable: true, RedAvailable: true, CredentialOwnerID: a.ID, CredentialIdentity: OpenAIDaybreakAPIKeyCredentialIdentity(a)}, nil
	})}
	ctx, patch, err := s.prepareDaybreakUpdate(context.Background(), account, map[string]any{OpenAIDaybreakBlueEnabledKey: true, OpenAIDaybreakRedEnabledKey: true})
	require.NoError(t, err)
	evidence, ok := AccountDaybreakEvidenceFromContext(ctx, account.ID)
	require.True(t, ok)
	require.NotEmpty(t, evidence.CredentialIdentity)
	require.Empty(t, evidence.AuthorizationGeneration)
	require.Equal(t, 1, calls)
	account.Extra = patch
	require.Error(t, validateNewAccountDaybreak(account))
	_, _, err = s.prepareDaybreakUpdate(context.Background(), account, map[string]any{OpenAIAPIKeyModeExtraKey: "codex_engine", OpenAIDaybreakBlueEnabledKey: true})
	require.Error(t, err)
	require.Equal(t, 1, calls, "Engine rejects the preference before any discovery")
	current := *account
	target := *account
	target.Extra = map[string]any{}
	require.NoError(t, PreserveAccountConfiguration(&current, &target, AccountConfigurationIntent{}))
	require.Equal(t, true, target.Extra[OpenAIDaybreakBlueEnabledKey])
	target.Extra[OpenAIAPIKeyModeExtraKey] = "codex_engine"
	require.NoError(t, PreserveAccountConfiguration(&current, &target, AccountConfigurationIntent{}))
	require.NotContains(t, target.Extra, OpenAIDaybreakBlueEnabledKey)
}
