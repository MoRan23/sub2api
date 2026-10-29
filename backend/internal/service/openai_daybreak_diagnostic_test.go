package service

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func diagnosticDaybreakTestContext(preferences DiagnosticDaybreakPreferences) context.Context {
	ctx := withOpenAICandyTest(context.Background(), &openAICandyTestAttempt{})
	return withDiagnosticDaybreakAttempt(ctx, preferences)
}

func TestDiagnosticDaybreakTierPolicyIgnoresGlobalAndGroup(t *testing.T) {
	for _, tc := range []struct {
		name, model, programs, cyber, reason string
		preferences                          DiagnosticDaybreakPreferences
	}{
		{"blue", "gpt-5.5", `["daybreak_blue"]`, "daybreak_blue", "automatic", DiagnosticDaybreakPreferences{Blue: true}},
		{"red_tier_blue_wire", "gpt-6-astra", `["daybreak_blue"]`, "daybreak_blue", "automatic", DiagnosticDaybreakPreferences{Blue: true, Red: true}},
		{"61_red_tier_blue_wire", "gpt-6.1-sol", `["daybreak_blue"]`, "daybreak_blue", "automatic", DiagnosticDaybreakPreferences{Blue: true, Red: true}},
		{"red_wire", "gpt-5.6-cyber", `["daybreak_red"]`, "daybreak_red", "automatic", DiagnosticDaybreakPreferences{Blue: true, Red: true}},
		{"blue_off", "gpt-5.5", `["daybreak_blue"]`, "", "blue_disabled", DiagnosticDaybreakPreferences{}},
		{"red_off", "gpt-6-astra", `["daybreak_blue"]`, "", "red_disabled", DiagnosticDaybreakPreferences{Blue: true}},
		{"unknown_model", "gpt-5.5-unknown", `["daybreak_blue"]`, "", "unrecognized_model", DiagnosticDaybreakPreferences{Blue: true, Red: true}},
		{"no_capability", "gpt-5.5", `["standard"]`, "", "capability_unavailable", DiagnosticDaybreakPreferences{Blue: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			account := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
			svc := &OpenAIGatewayService{settingService: daybreakHTTPSettings(false)}
			svc.codexModelCapabilities.observeManifest(openAICodexModelCapabilitiesNamespace(account), []byte(fmt.Sprintf(`{"models":[{"slug":%q,"available_access_programs":{"cyber":%s}}]}`, tc.model, tc.programs)), time.Now())
			ctx := diagnosticDaybreakTestContext(tc.preferences)
			body := []byte(fmt.Sprintf(`{"model":%q,"opaque":9007199254740993}`, tc.model))
			original := string(body)
			wire, decision, err := svc.applyOpenAIDaybreakWithDecision(ctx, account, body)
			require.NoError(t, err)
			require.Equal(t, tc.cyber, gjson.GetBytes(wire, "access_programs.cyber").String())
			require.Equal(t, tc.reason, decision)
			require.Equal(t, original, string(body))
			require.Equal(t, "9007199254740993", gjson.GetBytes(wire, "opaque").Raw)
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://synthetic.invalid/v1/responses", bytes.NewReader(wire))
			require.NoError(t, err)
			setOpenAIDaybreakDecision(req, decision)
			require.NoError(t, prepareOpenAIDaybreakHTTPRequest(req, account, svc.settingService))
			require.Equal(t, &DiagnosticDaybreakEvidence{Cyber: tc.cyber, Reason: tc.reason}, diagnosticDaybreakEvidenceFromContext(ctx))
			final, err := io.ReadAll(req.Body)
			require.NoError(t, err)
			require.Equal(t, wire, final)
		})
	}
}

func TestDiagnosticDaybreakPrivateScopeDoesNotExemptOtherRequests(t *testing.T) {
	prefs := DiagnosticDaybreakPreferences{Blue: true, Red: true}
	active := diagnosticDaybreakTestContext(prefs)
	for _, tc := range []struct {
		name string
		ctx  context.Context
		path string
	}{
		{"ordinary", context.Background(), "/v1/responses"},
		{"frozen_only", withDiagnosticDaybreakPreferences(context.Background(), prefs), "/v1/responses"},
		{"old_diagnostic", withOpenAICandyTest(context.Background(), &openAICandyTestAttempt{}), "/v1/responses"},
		{"catalog_scope", withoutDiagnosticDaybreakAttempt(active), "/v1/responses"},
		{"token_scope", openAIModelsTokenContext(active, true), "/v1/responses"},
		{"excluded_endpoint", withOpenAIDaybreakInjectionDisabled(active), "/v1/responses"},
		{"compact", active, "/v1/responses/compact"},
		{"count", active, "/v1/responses/input_tokens"},
		{"auxiliary", active, "/v1/models"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			account := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth}
			req, err := http.NewRequestWithContext(tc.ctx, http.MethodPost, "https://synthetic.invalid"+tc.path, strings.NewReader(`{"model":"gpt-5.5","access_programs":{"cyber":"daybreak_blue"}}`))
			require.NoError(t, err)
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-Diagnostic-Daybreak", "true")
			require.NoError(t, prepareOpenAIDaybreakHTTPRequest(req, account, daybreakHTTPSettings(false)))
			wire, err := io.ReadAll(req.Body)
			require.NoError(t, err)
			require.False(t, gjson.GetBytes(wire, "access_programs.cyber").Exists())
		})
	}
	for _, account := range []*Account{
		{Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Extra: map[string]any{"openai_api_key_mode": "codex_engine"}},
		{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"auth_mode": OpenAIAuthModePersonalAccessToken}},
		{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"auth_mode": OpenAIAuthModeAgentIdentity}},
	} {
		account.Extra = shallowCopyMap(account.Extra)
		if account.Extra == nil {
			account.Extra = make(map[string]any)
		}
		account.Extra[OpenAIDaybreakBlueEnabledKey], account.Extra[OpenAIDaybreakRedEnabledKey] = true, true
		require.Equal(t, DiagnosticDaybreakPreferences{}, ReadDiagnosticDaybreakPreferences(account))
		wire, decision, err := (&OpenAIGatewayService{settingService: daybreakHTTPSettings(false)}).applyOpenAIDaybreakWithDecision(active, account, []byte(`{"model":"gpt-5.5","access_programs":{"cyber":"daybreak_blue"}}`))
		require.NoError(t, err)
		require.Equal(t, "global_disabled_stripped", decision)
		require.False(t, gjson.GetBytes(wire, "access_programs.cyber").Exists())
	}
}

func TestDiagnosticDaybreakProbeAndPelicanObserveFinalWire(t *testing.T) {
	for _, kind := range []string{"pelican", "attribution", "legacy_attribution", "shared"} {
		t.Run(kind, func(t *testing.T) {
			owner := newOpenAIOAuthNamespaceTestAccount()
			account := owner
			if kind == "shared" {
				account = snapshotOAuthRefreshAccount(owner)
				account.ID++
				account.ParentAccountID = &owner.ID
			}
			account.Extra = map[string]any{OpenAIDaybreakBlueEnabledKey: true}
			repo := newAuthorizedOpenAIOAuthTestRepo(owner)
			repo.accounts[account.ID] = account
			upstream := &httpUpstreamRecorder{responses: []*http.Response{diagnosticDaybreakSuccessResponse()}}
			gateway := newOpenAIRejectedFieldTestService(upstream)
			gateway.accountRepo, gateway.settingService = repo, daybreakHTTPSettings(false)
			credential, err := ResolveOpenAIOAuthCredentialAccount(context.Background(), repo, owner, "")
			require.NoError(t, err)
			gateway.codexModelCapabilities.observeManifest(openAICodexModelCapabilitiesNamespace(credential), []byte(`{"models":[{"slug":"gpt-5.5","available_access_programs":{"cyber":["daybreak_blue"]}}]}`), time.Now())
			runner := newCandySyntheticCatalogTransport(repo, gateway)
			ctx := context.Background()
			var result *CandyTestExecution
			if kind == "attribution" || kind == "legacy_attribution" {
				ctx = context.WithValue(ctx, attributionExecutionContextKey{}, true)
				if kind == "attribution" {
					ctx = withDiagnosticDaybreakPreferences(ctx, ReadDiagnosticDaybreakPreferences(account))
					account.Extra[OpenAIDaybreakBlueEnabledKey] = false
				}
				result, err = runner.Probe(ctx, account.ID, "gpt-5.5", "synthetic challenge")
			} else {
				result, err = runner.Execute(ctx, &CandyTestItem{AccountID: account.ID, Model: "gpt-5.5", PromptVersion: CandyTestPromptVersion})
			}
			require.NoError(t, err)
			require.True(t, result.Completed)
			require.Len(t, upstream.bodies, 1)
			if kind == "legacy_attribution" {
				require.Nil(t, result.Daybreak)
				require.False(t, gjson.GetBytes(upstream.bodies[0], "access_programs.cyber").Exists())
			} else {
				require.Equal(t, "daybreak_blue", gjson.GetBytes(upstream.bodies[0], "access_programs.cyber").String())
				require.Equal(t, &DiagnosticDaybreakEvidence{Cyber: "daybreak_blue", Reason: "automatic"}, result.Daybreak)
			}
			if kind == "shared" {
				require.Equal(t, DiagnosticDaybreakPreferences{}, ReadDiagnosticDaybreakPreferences(owner))
			}
		})
	}
}

func TestAPIKeyDiagnosticDaybreakProbeAndPelicanObserveFinalWire(t *testing.T) {
	for _, kind := range []string{"pelican", "attribution", "unavailable"} {
		t.Run(kind, func(t *testing.T) {
			owner := &Account{ID: 8911, Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
				Status: StatusActive, Schedulable: true, Concurrency: 1,
				Credentials: map[string]any{"api_key": "mock-api-key", "base_url": "https://synthetic.invalid"}}
			account := owner
			account.Extra = map[string]any{OpenAIDaybreakBlueEnabledKey: true}
			repo := &installationIdentityRepoStub{accounts: map[int64]*Account{owner.ID: owner, account.ID: account}}
			upstream := &httpUpstreamRecorder{responses: []*http.Response{diagnosticDaybreakSuccessResponse()}}
			gateway := newOpenAIRejectedFieldTestService(upstream)
			gateway.accountRepo, gateway.settingService = repo, daybreakHTTPSettings(false)
			program := "daybreak_blue"
			if kind == "unavailable" {
				program = "standard"
			}
			gateway.observeOpenAIDaybreakAPIKeyCatalog(owner, []byte(fmt.Sprintf(`{"data":[{"id":"gpt-5.5","available_access_programs":{"cyber":[%q]}}]}`, program)), time.Now())
			runner := newCandySyntheticCatalogTransport(repo, gateway)
			ctx := context.Background()
			var result *CandyTestExecution
			var err error
			if kind == "attribution" {
				ctx = context.WithValue(ctx, attributionExecutionContextKey{}, true)
				ctx = withDiagnosticDaybreakPreferences(ctx, ReadDiagnosticDaybreakPreferences(account))
				account.Extra[OpenAIDaybreakBlueEnabledKey] = false
				result, err = runner.Probe(ctx, account.ID, "gpt-5.5", "synthetic challenge")
			} else {
				result, err = runner.Execute(ctx, &CandyTestItem{AccountID: account.ID, Model: "gpt-5.5", PromptVersion: CandyTestPromptVersion})
			}
			require.NoError(t, err)
			require.True(t, result.Completed)
			require.Len(t, upstream.bodies, 1)
			if kind == "unavailable" {
				require.False(t, gjson.GetBytes(upstream.bodies[0], "access_programs.cyber").Exists())
				require.Equal(t, &DiagnosticDaybreakEvidence{Reason: "capability_unavailable"}, result.Daybreak)
			} else {
				require.Equal(t, "daybreak_blue", gjson.GetBytes(upstream.bodies[0], "access_programs.cyber").String())
				require.Equal(t, &DiagnosticDaybreakEvidence{Cyber: "daybreak_blue", Reason: "automatic"}, result.Daybreak)
			}
		})
	}
}

func diagnosticDaybreakSuccessResponse() *http.Response {
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader("data: {\"type\":\"response.completed\",\"response\":{\"model\":\"gpt-5.5\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"synthetic answer\"}]}]}}\n\n"))}
}

func TestDiagnosticDaybreakPreferencesFreezeAndNormalize(t *testing.T) {
	account := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Extra: map[string]any{OpenAIDaybreakBlueEnabledKey: true, OpenAIDaybreakRedEnabledKey: true}}
	ctx := withDiagnosticDaybreakPreferences(context.Background(), ReadDiagnosticDaybreakPreferences(account))
	account.Extra[OpenAIDaybreakBlueEnabledKey] = false
	frozen, ok := diagnosticDaybreakPreferencesFromContext(ctx)
	require.True(t, ok)
	require.Equal(t, DiagnosticDaybreakPreferences{Blue: true, Red: true}, frozen)
	require.Equal(t, DiagnosticDaybreakPreferences{}, ReadDiagnosticDaybreakPreferences(account))
}

func TestDiagnosticDaybreakPelicanRetriesUseFrozenBusinessPreferences(t *testing.T) {
	account := newOpenAIOAuthNamespaceTestAccount()
	account.Extra = map[string]any{OpenAIDaybreakBlueEnabledKey: true}
	repo := newAuthorizedOpenAIOAuthTestRepo(account)
	upstream := &httpUpstreamRecorder{responses: []*http.Response{newOpenAIRejectedFieldTestResponse(500, `{"error":{"message":"synthetic transient"}}`), diagnosticDaybreakSuccessResponse()}}
	gateway := newOpenAIRejectedFieldTestService(upstream)
	gateway.accountRepo, gateway.settingService = repo, daybreakHTTPSettings(false)
	credential, err := ResolveOpenAIOAuthCredentialAccount(context.Background(), repo, account, "")
	require.NoError(t, err)
	gateway.codexModelCapabilities.observeManifest(openAICodexModelCapabilitiesNamespace(credential), []byte(`{"models":[{"slug":"gpt-5.5","available_access_programs":{"cyber":["daybreak_blue"]}}]}`), time.Now())
	runner := newCandySyntheticCatalogTransport(repo, gateway)
	fetch := runner.fetchModels
	runner.fetchModels = func(ctx context.Context, selected *Account) (*OpenAIModelsResponse, error) {
		require.Nil(t, diagnosticDaybreakAttemptFromContext(ctx), "model option queries are outside the inference exception")
		account.Extra[OpenAIDaybreakBlueEnabledKey] = false
		return fetch(ctx, selected)
	}
	result, err := runner.Execute(context.Background(), &CandyTestItem{AccountID: account.ID, Model: "gpt-5.5", PromptVersion: CandyTestPromptVersion})
	require.NoError(t, err)
	require.Equal(t, 1, result.Retries)
	require.Len(t, upstream.bodies, 2)
	for _, body := range upstream.bodies {
		require.Equal(t, "daybreak_blue", gjson.GetBytes(body, "access_programs.cyber").String())
	}
	require.Equal(t, &DiagnosticDaybreakEvidence{Cyber: "daybreak_blue", Reason: "automatic"}, result.Daybreak)
	require.False(t, openAIDaybreakEnabled(account, OpenAIDaybreakBlueEnabledKey))
}

func TestDiagnosticDaybreakDisabledAPIKeyAndEngineHaveExplicitNoProgramEvidence(t *testing.T) {
	for _, mode := range []string{"generic", "codex_engine"} {
		t.Run(mode, func(t *testing.T) {
			account := newOpenAIRejectedFieldTestAccount()
			account.Extra["openai_api_key_mode"] = mode
			account.Extra[OpenAIDaybreakBlueEnabledKey] = mode == "codex_engine"
			repo := &stubOpenAIAccountRepo{accounts: []Account{*account}}
			upstream := &httpUpstreamRecorder{responses: []*http.Response{diagnosticDaybreakSuccessResponse()}}
			gateway := newOpenAIRejectedFieldTestService(upstream)
			gateway.accountRepo, gateway.settingService = repo, daybreakHTTPSettings(false)
			ctx := withDiagnosticDaybreakPreferences(context.WithValue(context.Background(), attributionExecutionContextKey{}, true), ReadDiagnosticDaybreakPreferences(account))
			result, err := newCandySyntheticCatalogTransport(repo, gateway).Probe(ctx, account.ID, "gpt-5.5", "synthetic challenge")
			require.NoError(t, err)
			require.NotNil(t, result.Daybreak)
			require.Empty(t, result.Daybreak.Cyber)
			require.NotEmpty(t, result.Daybreak.Reason)
			require.Len(t, upstream.bodies, 1)
			require.False(t, gjson.GetBytes(upstream.bodies[0], "access_programs.cyber").Exists())
		})
	}
}

func TestDiagnosticDaybreakPrewarmAndControlFramesRemainUnderSystemPolicy(t *testing.T) {
	account := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	svc := &OpenAIGatewayService{settingService: daybreakHTTPSettings(false)}
	for _, suffix := range []string{`,"generate":false`, `,"type":"response.cancel"`} {
		ctx := diagnosticDaybreakTestContext(DiagnosticDaybreakPreferences{Blue: true, Red: true})
		body := []byte(`{"model":"gpt-5.5","access_programs":{"cyber":"daybreak_blue"}` + suffix + `}`)
		wire, reason, err := svc.applyOpenAIDaybreakWithDecision(ctx, account, body)
		require.NoError(t, err)
		require.Equal(t, "global_disabled_stripped", reason)
		require.False(t, gjson.GetBytes(wire, "access_programs.cyber").Exists())
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://synthetic.invalid/v1/responses", bytes.NewReader(body))
		require.NoError(t, err)
		require.NoError(t, prepareOpenAIDaybreakHTTPRequest(req, account, svc.settingService))
		wire, err = io.ReadAll(req.Body)
		require.NoError(t, err)
		require.False(t, gjson.GetBytes(wire, "access_programs.cyber").Exists())
	}
}

func TestDiagnosticDaybreakExpiredCapabilitiesRefreshAndFailureContinue(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusServiceUnavailable} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				w.WriteHeader(status)
				_, _ = io.WriteString(w, `{"models":[{"slug":"gpt-5.5","available_access_programs":{"cyber":["standard"]}}]}`)
			}))
			t.Cleanup(server.Close)
			original := chatgptCodexModelsURL
			chatgptCodexModelsURL = server.URL
			t.Cleanup(func() { chatgptCodexModelsURL = original })
			svc := &OpenAIGatewayService{settingService: daybreakHTTPSettings(false)}
			account := scopedAuxiliaryOSFixture(t, svc, newCodexModelsTestAccount())
			svc.codexModelCapabilities.observeManifest(openAICodexModelCapabilitiesNamespace(account), []byte(`{"models":[{"slug":"gpt-5.5","available_access_programs":{"cyber":["daybreak_blue"]}}]}`), time.Now().Add(-2*codexModelCapabilityCacheTTL))
			ctx := diagnosticDaybreakTestContext(DiagnosticDaybreakPreferences{Blue: true})
			body := []byte(`{"model":"gpt-5.5"}`)
			wire, reason, err := svc.applyOpenAIDaybreakWithDecision(ctx, account, body)
			require.NoError(t, err)
			require.Equal(t, body, wire, "expired capability cannot authorize a diagnostic field")
			if status == http.StatusOK {
				require.Equal(t, "capability_unavailable", reason)
			} else {
				require.Equal(t, "catalog_unavailable", reason)
			}
			require.EqualValues(t, 1, calls.Load())
			repo, ok := svc.accountRepo.(*auxiliaryOSLegacyTestRepository)
			require.True(t, ok)
			require.Empty(t, repo.errors)
			require.Empty(t, repo.cooldowns)
			require.Nil(t, diagnosticDaybreakEvidenceFromContext(ctx), "catalog lookup must not produce inference evidence")
		})
	}
}
