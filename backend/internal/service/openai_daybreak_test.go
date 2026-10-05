package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestOpenAIDaybreakTierAndExplicitFieldPolicy(t *testing.T) {
	for _, tc := range []struct {
		name, model, programs, extraBody, expected string
		blue, red                                  bool
	}{
		{name: "off", model: "gpt-6-sol", programs: `["daybreak_blue"]`},
		{name: "blue mainline", model: "gpt-6-sol", programs: `["standard","daybreak_blue"]`, blue: true, expected: "daybreak_blue"},
		{name: "astra blue alone insufficient", model: "gpt-6-astra", programs: `["daybreak_blue"]`, blue: true},
		{name: "astra red tier blue wire", model: "gpt-6-astra", programs: `["daybreak_blue"]`, blue: true, red: true, expected: "daybreak_blue"},
		{name: "61 red tier blue wire", model: "gpt-6.1-sol", programs: `["daybreak_blue"]`, blue: true, red: true, expected: "daybreak_blue"},
		{name: "cyber red", model: "gpt-5.6-cyber", programs: `["daybreak_red"]`, blue: true, red: true, expected: "daybreak_red"},
		{name: "red alone inactive", model: "gpt-5.6-cyber", programs: `["daybreak_red"]`, red: true},
		{name: "blue alias", model: "gpt-daybreak-blue-latest", programs: `["daybreak_blue"]`, blue: true, expected: "daybreak_blue"},
		{name: "red alias", model: "gpt-daybreak-red-latest", programs: `["daybreak_red"]`, blue: true, red: true, expected: "daybreak_red"},
		{name: "wrong program", model: "gpt-6-sol", programs: `["daybreak_red"]`, blue: true, red: true},
		{name: "standard only", model: "gpt-6-sol", programs: `["standard"]`, blue: true},
		{name: "unknown suffix not inferred", model: "gpt-6-sol-unknown", programs: `["daybreak_blue"]`, blue: true, red: true},
		{name: "client standard", model: "gpt-6-sol", programs: `["daybreak_blue"]`, blue: true, extraBody: `,"access_programs":{"cyber":"standard"}`},
		{name: "client red preserved", model: "gpt-6-sol", programs: `["daybreak_blue"]`, blue: true, extraBody: `,"access_programs":{"cyber":"daybreak_red"}`},
		{name: "client null preserved", model: "gpt-6-sol", programs: `["daybreak_blue"]`, blue: true, extraBody: `,"access_programs":{"cyber":null}`},
		{name: "null object preserved", model: "gpt-6-sol", programs: `["daybreak_blue"]`, blue: true, extraBody: `,"access_programs":null`},
		{name: "invalid object preserved", model: "gpt-6-sol", programs: `["daybreak_blue"]`, blue: true, extraBody: `,"access_programs":[]`},
		{name: "invalid value preserved", model: "gpt-6-sol", programs: `["daybreak_blue"]`, blue: true, extraBody: `,"access_programs":{"cyber":false}`},
		{name: "empty object filled", model: "gpt-6-sol", programs: `["daybreak_blue"]`, blue: true, extraBody: `,"access_programs":{"other":{"id":9007199254740993}}`, expected: "daybreak_blue"},
		{name: "prewarm untouched", model: "gpt-6-sol", programs: `["daybreak_blue"]`, blue: true, extraBody: `,"type":"response.create","generate":false`},
		{name: "control frame untouched", model: "gpt-6-sol", programs: `["daybreak_blue"]`, blue: true, extraBody: `,"type":"response.cancel"`},
		{name: "ws infer", model: "gpt-6-sol", programs: `["daybreak_blue"]`, blue: true, extraBody: `,"type":"response.create"`, expected: "daybreak_blue"},
		{name: "remote compact trigger", model: "gpt-6-sol", programs: `["daybreak_blue"]`, blue: true, extraBody: `,"input":[{"type":"compaction_trigger"}]`, expected: "daybreak_blue"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &OpenAIGatewayService{}
			account := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Extra: map[string]any{OpenAIDaybreakBlueEnabledKey: tc.blue, OpenAIDaybreakRedEnabledKey: tc.red}}
			manifest := fmt.Sprintf(`{"models":[{"slug":%q,"available_access_programs":{"cyber":%s}}]}`, tc.model, tc.programs)
			s.codexModelCapabilities.observeManifest(openAICodexModelCapabilitiesNamespace(account), []byte(manifest), time.Now())
			body := []byte(fmt.Sprintf(`{ "model":%q,"opaque":9007199254740993%s}`, tc.model, tc.extraBody))
			original := string(body)
			got := s.applyOpenAIDaybreak(context.Background(), account, body)
			require.Equal(t, original, string(body), "must not mutate the retry source")
			if tc.expected == "" {
				require.Equal(t, original, string(got))
			} else {
				require.Equal(t, tc.expected, gjson.GetBytes(got, "access_programs.cyber").String())
				require.Equal(t, "9007199254740993", gjson.GetBytes(got, "opaque").Raw)
				if tc.name == "empty object filled" {
					require.Equal(t, "9007199254740993", gjson.GetBytes(got, "access_programs.other.id").Raw)
				}
			}
		})
	}
}

func TestOpenAIDaybreakMissingAndMalformedPrograms(t *testing.T) {
	for _, raw := range []string{"", `null`, `{}`, `{"cyber":null}`, `{"cyber":"daybreak_blue"}`, `{"cyber":["daybreak_blue",42]}`} {
		known, blue, red := parseOpenAIDaybreakAccessPrograms(json.RawMessage(raw))
		require.False(t, known, raw)
		require.False(t, blue, raw)
		require.False(t, red, raw)
	}
}

func TestOpenAIDaybreakAccountIsolation(t *testing.T) {
	body := []byte(`{"model":"gpt-6-sol"}`)
	for _, account := range []*Account{
		{Platform: PlatformOpenAI, Type: AccountTypeAPIKey},
		{Platform: PlatformOpenAI, Type: AccountTypeSetupToken},
		{Platform: PlatformAnthropic, Type: AccountTypeOAuth},
		{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"auth_mode": OpenAIAuthModePersonalAccessToken}},
		{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"auth_mode": OpenAIAuthModeAgentIdentity}},
	} {
		account.Extra = map[string]any{OpenAIDaybreakBlueEnabledKey: true, OpenAIDaybreakRedEnabledKey: true}
		require.False(t, IsOpenAIDaybreakAccount(account))
		require.Equal(t, body, (&OpenAIGatewayService{}).applyOpenAIDaybreak(context.Background(), account, body))
	}
}

func TestOpenAIDaybreakCapabilitiesRevocationAndExpiry(t *testing.T) {
	now := time.Now()
	cache := &codexModelCapabilityCache{}
	ns := "account:1/os/windows/authorization/first"
	manifest := []byte(`{"models":[{"slug":"gpt-6-sol","available_access_programs":{"cyber":["daybreak_blue"]}},{"slug":"gpt-5.6-cyber","available_access_programs":{"cyber":["daybreak_red"]}}]}`)
	cache.observeManifest(ns, manifest, now)
	require.True(t, cache.get(ns, "gpt-6-sol", now).DaybreakBlue)
	require.False(t, cache.get("account:1/os/linux/authorization/first", "gpt-6-sol", now).Known)
	require.False(t, cache.get("account:1/os/windows/authorization/second", "gpt-6-sol", now).Known)
	require.False(t, cache.get(ns, "gpt-6-sol", now.Add(codexModelCapabilityCacheTTL)).Known)
	cache.refreshNamespace(ns, now.Add(codexModelCapabilityCacheTTL))
	require.True(t, cache.get(ns, "gpt-6-sol", now.Add(codexModelCapabilityCacheTTL)).DaybreakBlue)
	revokedAt := now.Add(codexModelCapabilityCacheTTL + time.Second)
	cache.observeManifest(ns, []byte(`{"models":[{"slug":"gpt-6-sol","available_access_programs":{"cyber":["standard"]}}]}`), revokedAt)
	require.False(t, cache.get(ns, "gpt-6-sol", revokedAt).DaybreakBlue)
	require.False(t, cache.get(ns, "gpt-5.6-cyber", revokedAt).Known, "removed models lose their grant immediately")
	cache.observeManifest(ns, []byte(`{"models":[]}`), revokedAt)
	require.False(t, cache.get(ns, "gpt-6-sol", revokedAt).Known)
	cache.observeManifest(ns, manifest, revokedAt.Add(-time.Second))
	require.False(t, cache.get(ns, "gpt-6-sol", revokedAt).Known, "an older cached manifest cannot undo a complete revocation")
}

func TestOpenAIDaybreakCatalogUsesRealProgramsAndSingleflight(t *testing.T) {
	_, calls := newCodexModelsOAuthCacheServer(t, `{"models":[{"slug":"gpt-6-sol","available_access_programs":{"cyber":["daybreak_blue"]}},{"slug":"gpt-6-astra","available_access_programs":{"cyber":["daybreak_blue"]}},{"slug":"gpt-5.6-cyber","available_access_programs":{"cyber":["daybreak_red"]}},{"slug":"gpt-5.5"},{"slug":"unknown-model","available_access_programs":{"cyber":["daybreak_red"]}}]}`)
	s := &OpenAIGatewayService{}
	account := newCodexModelsTestAccount()
	registerAuxiliaryOSFixture(t, s, account)
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			caps, err := s.GetOpenAIDaybreakCapabilities(context.Background(), account)
			require.NoError(t, err)
			require.True(t, caps.BlueAvailable)
			require.True(t, caps.RedAvailable)
			require.Len(t, caps.Models, 3)
			require.NotNil(t, caps.CheckedAt)
			require.Equal(t, account.ID, caps.CredentialOwnerID)
			require.Equal(t, "fixture-authorization", caps.AuthorizationGeneration)
		}()
	}
	wg.Wait()
	require.EqualValues(t, 1, calls.Load())
}

func TestOpenAIDaybreakCatalogFailureDoesNotChangeHealthOrBody(t *testing.T) {
	for _, status := range []int{401, 403, 429, 502} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
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
			account.Extra = map[string]any{OpenAIDaybreakBlueEnabledKey: true}
			registerAuxiliaryOSFixture(t, s, account)
			body := []byte(`{"model":"gpt-6-sol","input":"test"}`)
			require.Equal(t, body, s.applyOpenAIDaybreak(context.Background(), account, body))
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

func TestOpenAIDaybreakRuntimeLazilyLoadsAndReusesCatalog(t *testing.T) {
	_, calls := newCodexModelsOAuthCacheServer(t, `{"models":[{"slug":"gpt-6-sol","available_access_programs":{"cyber":["daybreak_blue"]}}]}`)
	s := &OpenAIGatewayService{}
	account := newCodexModelsTestAccount()
	account.Extra = map[string]any{OpenAIDaybreakBlueEnabledKey: true}
	account = scopedAuxiliaryOSFixture(t, s, account)
	body := []byte(`{"model":"gpt-6-sol"}`)
	for range 3 {
		got := s.applyOpenAIDaybreak(context.Background(), account, body)
		require.Equal(t, "daybreak_blue", gjson.GetBytes(got, "access_programs.cyber").String())
	}
	require.EqualValues(t, 1, calls.Load())
	account.Extra[OpenAIDaybreakBlueEnabledKey] = false
	require.Equal(t, body, s.applyOpenAIDaybreak(context.Background(), account, body))
	require.EqualValues(t, 1, calls.Load())
}

func TestOpenAIDaybreakRestoresEvictedCapabilitiesWithoutExtendingObservation(t *testing.T) {
	_, calls := newCodexModelsOAuthCacheServer(t, `{"models":[{"slug":"gpt-6-sol","available_access_programs":{"cyber":["daybreak_blue"]}}]}`)
	s := &OpenAIGatewayService{}
	account := newCodexModelsTestAccount()
	account.Extra = map[string]any{OpenAIDaybreakBlueEnabledKey: true}
	account = scopedAuxiliaryOSFixture(t, s, account)
	first, err := s.GetOpenAIDaybreakCapabilities(context.Background(), account)
	require.NoError(t, err)
	require.True(t, first.BlueAvailable)
	s.codexModelCapabilities.mu.Lock()
	s.codexModelCapabilities.entries = nil // Simulate independent capability eviction.
	s.codexModelCapabilities.mu.Unlock()
	body := []byte(`{"model":"gpt-6-sol"}`)
	got := s.applyOpenAIDaybreak(context.Background(), account, body)
	require.Equal(t, "daybreak_blue", gjson.GetBytes(got, "access_programs.cyber").String())
	second, err := s.GetOpenAIDaybreakCapabilities(context.Background(), account)
	require.NoError(t, err)
	require.True(t, second.BlueAvailable)
	require.Equal(t, first.CheckedAt, second.CheckedAt)
	require.EqualValues(t, 1, calls.Load(), "a fresh cached catalog is enough to restore capabilities")
	ns := openAICodexModelCapabilitiesNamespace(account)
	require.False(t, s.codexModelCapabilities.get(ns, "gpt-6-sol", first.CheckedAt.Add(codexModelCapabilityCacheTTL)).Known)
}

func TestOpenAIDaybreakExpiredTokenDoesNotChangeHealth(t *testing.T) {
	_, calls := newCodexModelsOAuthCacheServer(t, `{"models":[]}`)
	repo := &codexModelsAccountStateRepo{}
	s := newCodexModels401TestService(repo)
	account := newCodexModelsTestAccount()
	account.Credentials["expires_at"] = time.Now().Add(-time.Hour).Format(time.RFC3339)
	delete(account.Credentials, "refresh_token")
	registerAuxiliaryOSFixture(t, s, account)
	s.openAITokenProvider = NewOpenAITokenProvider(s.accountRepo, nil, nil)
	_, err := s.GetOpenAIDaybreakCapabilities(context.Background(), account)
	require.Error(t, err)
	require.Zero(t, calls.Load())
	require.Zero(t, repo.setErrorCalls)
	require.Zero(t, repo.setTempUnschedCalls)
	slots, ok := s.accountRepo.(*auxiliaryOSLegacyTestRepository)
	require.True(t, ok)
	require.Empty(t, slots.errors)
	require.Empty(t, slots.cooldowns)
}

func TestOpenAIDaybreakShadowUsesOwnSwitchAndOwnerCapabilities(t *testing.T) {
	_, calls := newCodexModelsOAuthCacheServer(t, `{"models":[{"slug":"gpt-6-sol","available_access_programs":{"cyber":["daybreak_blue"]}}]}`)
	s := &OpenAIGatewayService{}
	owner := newCodexModelsTestAccount()
	owner.Extra = map[string]any{OpenAIDaybreakBlueEnabledKey: false}
	registerAuxiliaryOSFixture(t, s, owner)
	shadow := &Account{ID: owner.ID + 10, Platform: PlatformOpenAI, Type: AccountTypeOAuth, ParentAccountID: &owner.ID,
		Extra: map[string]any{OpenAIDaybreakBlueEnabledKey: true}}
	repo, ok := s.accountRepo.(*auxiliaryOSLegacyTestRepository)
	require.True(t, ok)
	repo.accounts[shadow.ID] = shadow
	shadow = scopedAuxiliaryOSFixture(t, s, shadow)
	body := []byte(`{"model":"gpt-6-sol"}`)
	got := s.applyOpenAIDaybreak(context.Background(), shadow, body)
	require.Equal(t, "daybreak_blue", gjson.GetBytes(got, "access_programs.cyber").String())
	require.EqualValues(t, 1, calls.Load())
	require.Equal(t, body, s.applyOpenAIDaybreak(context.Background(), owner, body), "owner must not inherit shadow preference")
	shadow.Extra[OpenAIDaybreakBlueEnabledKey] = false
	owner.Extra[OpenAIDaybreakBlueEnabledKey] = true
	require.Equal(t, body, s.applyOpenAIDaybreak(context.Background(), shadow, body), "shadow must not inherit owner preference")
	require.EqualValues(t, 1, calls.Load())
}
