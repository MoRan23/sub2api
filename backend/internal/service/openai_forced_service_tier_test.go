package service

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestForcedServiceTierNativeResponsesWire(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, supported := range []bool{false, true} {
		t.Run(fmt.Sprint(supported), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				body := `{"models":[{"slug":"gpt-6-astra","service_tiers":[]}]}`
				if supported {
					body = `{"models":[{"slug":"gpt-6-astra","service_tiers":[{"id":"ultrafast"}]}]}`
				}
				_, _ = w.Write([]byte(body))
			}))
			t.Cleanup(server.Close)
			original := chatgptCodexModelsURL
			chatgptCodexModelsURL = server.URL
			t.Cleanup(func() { chatgptCodexModelsURL = original })
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(nil))
			c.Request.Header.Set("User-Agent", "codex_cli_rs/0.1.0")
			upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader("data: [DONE]\n\n"))}}
			svc := newOpenAIGatewayServiceWithSettings(t, DefaultOpenAIFastPolicySettings())
			svc.cfg = &config.Config{Gateway: config.GatewayConfig{ForceCodexCLI: false}}
			svc.httpUpstream = upstream
			account := &Account{ID: 123, Name: "acc", Platform: PlatformOpenAI, Type: AccountTypeOAuth, Concurrency: 1,
				Credentials: map[string]any{"access_token": "oauth-token", "chatgpt_account_id": "chatgpt-acc"},
				Extra:       map[string]any{"openai_passthrough": false, "openai_oauth_responses_websockets_v2_mode": OpenAIWSIngressModeOff, ForceOpenAIUltrafastExtraKey: true},
				Status:      StatusActive, Schedulable: true, RateMultiplier: f64p(1)}
			authorizeOpenAIForwardFixture(svc, account)
			result, err := svc.Forward(forceTierGroupContext(true, false), c, account, []byte(`{"model":"gpt-6-astra","service_tier":"flex","stream":false,"input":[{"type":"text","text":"hi"}]}`))
			require.NoError(t, err)
			require.NotNil(t, result)
			expected := "flex"
			if supported {
				expected = "ultrafast"
			}
			require.Equal(t, expected, gjson.GetBytes(upstream.lastBody, "service_tier").String())
			require.NotNil(t, result.ServiceTier)
			require.Equal(t, expected, *result.ServiceTier)
		})
	}
}

func tierCatalogService(body string, headers http.Header) (*OpenAIGatewayService, *atomic.Int32) {
	calls := &atomic.Int32{}
	upstream := &codexModelsHTTPUpstreamStub{do: func(_ *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
		calls.Add(1)
		return &http.Response{StatusCode: 200, Header: headers.Clone(), Body: io.NopCloser(strings.NewReader(body))}, nil
	}}
	return newCodexModelsAPIKeyTestService(upstream), calls
}

func forceTierGroupContext(fast, ultra bool) context.Context {
	return context.WithValue(context.Background(), ctxkey.Group, &Group{ID: 7, Platform: PlatformOpenAI, Status: StatusActive, Hydrated: true, ForceOpenAIFast: fast, ForceOpenAIUltrafast: ultra})
}

func TestForcedServiceTierCatalogRequiresActualDeclaration(t *testing.T) {
	for _, tc := range []struct{ name, body, expected string }{
		{"manifest", `{"models":[{"slug":"gpt-6-astra","service_tiers":[{"id":"ultrafast"}]}]}`, "supported"},
		{"model list", `{"object":"list","data":[{"id":"gpt-6-astra","service_tiers":[{"id":"ultrafast"}]}]}`, "supported"},
		{"local defaults ignored", `{"object":"list","data":[{"id":"gpt-6-astra"}]}`, "unsupported"},
		{"speed labels ignored", `{"models":[{"slug":"gpt-6-astra","additional_speed_tiers":["ultrafast"]}]}`, "unsupported"},
		{"exact capability key", `{"models":[{"slug":"gpt-6-astra","SERVICE_TIERS":[{"id":"ultrafast"}]}]}`, "unsupported"},
		{"exact tier ID key", `{"models":[{"slug":"gpt-6-astra","service_tiers":[{"ID":"ultrafast"}]}]}`, "unsupported"},
		{"different model", `{"models":[{"slug":"gpt-6","service_tiers":[{"id":"ultrafast"}]}]}`, "unsupported"},
		{"empty tiers", `{"models":[{"slug":"gpt-6-astra","service_tiers":[]}]}`, "unsupported"},
		{"malformed tiers", `{"models":[{"slug":"gpt-6-astra","service_tiers":"ultrafast"}]}`, "unavailable"},
		{"duplicate conflict", `{"models":[{"slug":"gpt-6-astra","service_tiers":[{"id":"ultrafast"}]},{"slug":"gpt-6-astra"}]}`, "unsupported"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, calls := tierCatalogService(tc.body, make(http.Header))
			account := newCodexModelsAPIKeyTestAccount("https://api.openai.com/v1")
			require.Equal(t, tc.expected, svc.openAIUltrafastCapability(context.Background(), account, "gpt-6-astra"))
			require.EqualValues(t, 1, calls.Load())
		})
	}
}

func TestForcedServiceTierPrecedenceAcrossHTTPAndWS(t *testing.T) {
	for _, tc := range []struct {
		name                                                        string
		accountFast, accountUltra, groupFast, groupUltra, supported bool
		expected                                                    string
	}{
		{"account fast overrides group ultra", true, false, false, true, true, "priority"},
		{"account ultra overrides group fast", false, true, true, false, true, "ultrafast"},
		{"unsupported account ultra preserves client", false, true, true, false, false, "flex"},
		{"group ultra", false, false, false, true, true, "ultrafast"},
		{"unsupported group ultra preserves client", false, false, false, true, false, "flex"},
		{"legacy conflict prefers ultra without fast fallback", true, true, true, false, false, "flex"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := `{"models":[{"slug":"mapped-model","service_tiers":[]}]}`
			if tc.supported {
				body = `{"models":[{"slug":"mapped-model","service_tiers":[{"id":"ultrafast"}]}]}`
			}
			svc, calls := tierCatalogService(body, make(http.Header))
			account := newCodexModelsAPIKeyTestAccount("https://upstream.example/v1")
			account.Extra = map[string]any{ForceOpenAIFastExtraKey: tc.accountFast, ForceOpenAIUltrafastExtraKey: tc.accountUltra}
			ctx := forceTierGroupContext(tc.groupFast, tc.groupUltra)
			for _, ws := range []bool{false, true} {
				input := []byte(`{"model":"business-alias","service_tier":"flex","type":"response.create"}`)
				var out []byte
				if ws {
					var blocked *OpenAIFastBlockedError
					var err error
					out, blocked, err = svc.applyOpenAIFastPolicyToWSResponseCreate(ctx, account, "mapped-model", input)
					require.NoError(t, err)
					require.Nil(t, blocked)
				} else {
					var err error
					out, err = svc.applyOpenAIFastPolicyToBody(ctx, account, "mapped-model", input)
					require.NoError(t, err)
				}
				require.Equal(t, tc.expected, gjson.GetBytes(out, "service_tier").String())
			}
			if tc.accountFast && !tc.accountUltra {
				require.Zero(t, calls.Load())
			} else {
				require.EqualValues(t, 1, calls.Load())
			}
		})
	}
}

func TestForcedServiceTierGlobalPolicyStillAuthoritative(t *testing.T) {
	for _, action := range []string{BetaPolicyActionFilter, BetaPolicyActionBlock, OpenAIFastPolicyActionForcePriority} {
		t.Run(action, func(t *testing.T) {
			svc, _ := tierCatalogService(`{"models":[{"slug":"upstream-model","service_tiers":[{"id":"ultrafast"}]}]}`, make(http.Header))
			svc.settingService = newOpenAIGatewayServiceWithSettings(t, &OpenAIFastPolicySettings{Rules: []OpenAIFastPolicyRule{{ServiceTier: OpenAIFastTierUltrafast, Action: action, Scope: BetaPolicyScopeAll}}}).settingService
			account := newCodexModelsAPIKeyTestAccount("https://upstream.example/v1")
			account.Extra = map[string]any{ForceOpenAIUltrafastExtraKey: true}
			out, err := svc.applyOpenAIFastPolicyToBody(context.Background(), account, "upstream-model", []byte(`{"model":"upstream-model"}`))
			if action == BetaPolicyActionBlock {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			if action == BetaPolicyActionFilter {
				require.False(t, gjson.GetBytes(out, "service_tier").Exists())
			} else {
				require.Equal(t, "priority", gjson.GetBytes(out, "service_tier").String())
			}
		})
	}
}

func TestForcedServiceTierGuardianSkipsDiscovery(t *testing.T) {
	svc, calls := tierCatalogService(`{"models":[]}`, make(http.Header))
	account := newCodexModelsTestAccount()
	account.Extra = map[string]any{ForceOpenAIUltrafastExtraKey: true}
	ctx := context.WithValue(forceTierGroupContext(true, false), openAIGuardianPolicyContextKey{}, "reviewer")
	out, err := svc.applyOpenAIFastPolicyToBody(ctx, account, "gpt-6-astra", []byte(`{"service_tier":"priority"}`))
	require.NoError(t, err)
	require.False(t, gjson.GetBytes(out, "service_tier").Exists())
	require.Zero(t, calls.Load())
}

func TestForcedServiceTierEngineDegradedAndFreshCatalog(t *testing.T) {
	for _, degraded := range []bool{false, true} {
		svc, _ := tierCatalogService(`{"object":"list","data":[{"id":"engine-model","service_tiers":[{"id":"ultrafast"}]}]}`, http.Header{"X-Codex-Catalog-Degraded": []string{fmt.Sprint(degraded)}})
		account := newCodexModelsAPIKeyTestAccount("https://engine.example/v1")
		account.Extra = map[string]any{OpenAIAPIKeyModeExtraKey: "codex_engine", ForceOpenAIUltrafastExtraKey: true}
		require.True(t, account.IsCodexEngine())
		expected := "supported"
		if degraded {
			expected = "unavailable"
		}
		require.Equal(t, expected, svc.openAIUltrafastCapability(context.Background(), account, "engine-model"))
	}
}

func TestForcedServiceTierHotCacheSharesIndexWithoutBody(t *testing.T) {
	svc, calls := tierCatalogService(`{"models":[{"slug":"gpt-6-astra","service_tiers":[{"id":"ultrafast"}]}]}`, make(http.Header))
	account := newCodexModelsAPIKeyTestAccount("https://upstream.example/v1")
	full, err := svc.fetchCodexModelsManifest(context.Background(), account, "", "", true)
	require.NoError(t, err)
	index, err := svc.fetchCodexModelsManifestForPurpose(context.Background(), account, "", "", true, true)
	require.NoError(t, err)
	require.Same(t, full.serviceTierCatalog, index.serviceTierCatalog)
	require.Empty(t, index.Body)
	require.Empty(t, index.upstreamSourceBody)
	require.EqualValues(t, 1, calls.Load())
	// Expired capability evidence must not inherit the directory's longer stale window.
	svc.openAIModelsCache.mu.Lock()
	for key, entry := range svc.openAIModelsCache.entries {
		copy := *entry.manifest
		copy.observedAt = time.Now().Add(-openAIModelsCacheTTL - time.Second)
		entry.manifest = &copy
		svc.openAIModelsCache.entries[key] = entry
	}
	svc.openAIModelsCache.mu.Unlock()
	require.Equal(t, "unavailable", svc.openAIUltrafastCapability(context.Background(), account, "gpt-6-astra"))
	require.EqualValues(t, 1, calls.Load())
}

func TestForcedServiceTierCatalogCredentialChangeInvalidates(t *testing.T) {
	var calls atomic.Int32
	svc := newCodexModelsAPIKeyTestService(&codexModelsHTTPUpstreamStub{do: func(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
		calls.Add(1)
		body := `{"models":[{"slug":"gpt-6-astra"}]}`
		if req.Header.Get("Authorization") == "Bearer sk-upstream" {
			body = `{"models":[{"slug":"gpt-6-astra","service_tiers":[{"id":"ultrafast"}]}]}`
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	}})
	account := newCodexModelsAPIKeyTestAccount("https://upstream.example/v1")
	require.Equal(t, "supported", svc.openAIUltrafastCapability(context.Background(), account, "gpt-6-astra"))
	rotated := *account
	rotated.Credentials = map[string]any{"api_key": "replacement", "base_url": "https://upstream.example/v1"}
	require.Equal(t, "unsupported", svc.openAIUltrafastCapability(context.Background(), &rotated, "gpt-6-astra"))
	require.Equal(t, "supported", svc.openAIUltrafastCapability(context.Background(), account, "gpt-6-astra"))
	require.EqualValues(t, 2, calls.Load())
}

func TestForcedServiceTierReusesUnchangedBody(t *testing.T) {
	for _, tc := range []struct {
		name, inputTier string
		ultra           bool
	}{
		{"priority", "priority", false}, {"ultrafast", "ultrafast", true}, {"fast alias", "fast", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, _ := tierCatalogService(`{"models":[{"slug":"model","service_tiers":[{"id":"ultrafast"}]}]}`, make(http.Header))
			account := newCodexModelsAPIKeyTestAccount("https://upstream.example/v1")
			account.Extra = map[string]any{ForceOpenAIFastExtraKey: !tc.ultra, ForceOpenAIUltrafastExtraKey: tc.ultra}
			for _, ws := range []bool{false, true} {
				body := []byte(`{"type":"response.create","model":"model","service_tier":"` + tc.inputTier + `","input":"hello"}`)
				apply := func(input []byte) []byte {
					if !ws {
						out, err := svc.applyOpenAIFastPolicyToBody(context.Background(), account, "model", input)
						require.NoError(t, err)
						return out
					}
					out, blocked, err := svc.applyOpenAIFastPolicyToWSResponseCreate(context.Background(), account, "model", input)
					require.NoError(t, err)
					require.Nil(t, blocked)
					return out
				}
				out := apply(body)
				if tc.inputTier == "fast" {
					require.Equal(t, "priority", gjson.GetBytes(out, "service_tier").String())
					require.True(t, &body[0] != &out[0], "alias normalization needs one patch")
					require.Equal(t, "fast", gjson.GetBytes(body, "service_tier").String(), "original retry body is immutable")
				} else {
					require.True(t, &body[0] == &out[0], "matching forced tier must reuse the body")
				}
				again := apply(out)
				require.True(t, &out[0] == &again[0], "repeated enforcement must reuse the body")
			}
		})
	}
}

func TestForcedServiceTierGlobalOverrideAvoidsIntermediatePatch(t *testing.T) {
	svc, _ := tierCatalogService(`{"models":[{"slug":"model","service_tiers":[{"id":"ultrafast"}]}]}`, make(http.Header))
	svc.settingService = newOpenAIGatewayServiceWithSettings(t, &OpenAIFastPolicySettings{Rules: []OpenAIFastPolicyRule{{ServiceTier: OpenAIFastTierUltrafast, Action: OpenAIFastPolicyActionForcePriority, Scope: BetaPolicyScopeAll}}}).settingService
	account := newCodexModelsAPIKeyTestAccount("https://upstream.example/v1")
	account.Extra = map[string]any{ForceOpenAIUltrafastExtraKey: true}
	body := []byte(`{"model":"model","service_tier":"priority"}`)
	out, err := svc.applyOpenAIFastPolicyToBody(context.Background(), account, "model", body)
	require.NoError(t, err)
	require.True(t, &body[0] == &out[0])
	svc.settingService = newOpenAIGatewayServiceWithSettings(t, openAIFastFilterPriorityPolicy()).settingService
	account.Extra = map[string]any{ForceOpenAIFastExtraKey: true}
	body = []byte(`{"model":"model","input":[]}`)
	out, err = svc.applyOpenAIFastPolicyToBody(context.Background(), account, "model", body)
	require.NoError(t, err)
	require.True(t, &body[0] == &out[0])
}

func TestForcedServiceTierColdQueriesShareRefreshAndBoundWait(t *testing.T) {
	var calls atomic.Int32
	var maxDeadline atomic.Int64
	svc := newCodexModelsAPIKeyTestService(&codexModelsHTTPUpstreamStub{do: func(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
		calls.Add(1)
		deadline, ok := req.Context().Deadline()
		if ok {
			maxDeadline.Store(int64(time.Until(deadline)))
		}
		<-req.Context().Done()
		return nil, req.Context().Err()
	}})
	account := newCodexModelsAPIKeyTestAccount("https://upstream.example/v1")
	ctx := context.Background()
	started := time.Now()
	var wg sync.WaitGroup
	results := make(chan string, 8)
	for range 8 {
		wg.Add(1)
		go func() { defer wg.Done(); results <- svc.openAIUltrafastCapability(ctx, account, "gpt-6-astra") }()
	}
	wg.Wait()
	require.Less(t, time.Since(started), 3*time.Second)
	close(results)
	for result := range results {
		require.Equal(t, "unavailable", result)
	}
	require.EqualValues(t, 1, calls.Load())
	require.Positive(t, maxDeadline.Load())
	require.LessOrEqual(t, time.Duration(maxDeadline.Load()), openAIServiceTierCatalogWait)
}

func TestForcedServiceTierOAuthCatalogFailureDoesNotChangeHealth(t *testing.T) {
	for _, status := range []int{401, 403, 429, 502} {
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
			svc := newCodexModels401TestService(repo)
			account := scopedAuxiliaryOSFixture(t, svc, newCodexModelsTestAccount())
			require.Equal(t, "unavailable", svc.openAIUltrafastCapability(context.Background(), account, "gpt-6-astra"))
			require.Zero(t, repo.setErrorCalls)
			require.Zero(t, repo.setTempUnschedCalls)
			slots := svc.accountRepo.(*auxiliaryOSLegacyTestRepository)
			require.Empty(t, slots.errors)
			require.Empty(t, slots.cooldowns)
		})
	}
}
