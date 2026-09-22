package service

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/codexnative"
)

// ProvideCodexTurnStateCollectorHTTPDo supplies a separate native HTTP pool and
// explicit proxy route. It never invokes the gateway, daily identity pools,
// telemetry, user billing, proxy fallback, or automatic replay.
func ProvideCodexTurnStateCollectorHTTPDo(accounts AccountRepository, proxies ProxyRepository, upstream HTTPUpstream) CodexTurnStateCollectorHTTPDo {
	return func(ctx context.Context, input CodexTurnStateCollectRequest, request *http.Request) (*http.Response, error) {
		if accounts == nil || proxies == nil || upstream == nil || input.Account == nil || input.ProxyID <= 0 || request == nil || request.URL == nil {
			return nil, ErrCodexTurnStateCollectorProxyUnavailable
		}
		// This adapter only services the constant collector endpoint. It cannot
		// turn an arbitrary request or redirect into a credential-bearing probe.
		if request.Method != http.MethodPost || request.URL.String() != chatgptCodexURL {
			return nil, errors.New("collector_invalid_request")
		}
		owner, err := ReloadOpenAIOAuthCredentialAccount(ctx, accounts, input.Account)
		if err != nil || !codexTurnStateEligible(owner) || owner.IsShadow() || strings.TrimSpace(owner.GetCredential("access_token")) == "" ||
			owner.Status != StatusActive || !owner.Schedulable || (owner.ExpiresAt != nil && !owner.ExpiresAt.After(time.Now())) {
			return nil, errors.New("collector_account_unavailable")
		}
		cfg := CodexTurnStateConfigForAccount(owner)
		if !cfg.Enabled || CodexTurnStateAccountTypeForAccount(owner) == "" || !codexTurnStateProxyAllowed(CodexTurnStateCollectorProxyIDs(cfg), input.ProxyID) ||
			CodexTurnStateGenerationForAccount(owner) != CodexTurnStateGenerationForAccount(input.Account) {
			return nil, errors.New("collector_configuration_changed")
		}
		proxy, err := proxies.GetByID(ctx, input.ProxyID)
		if err != nil || proxy == nil || !proxy.IsActive() || proxy.IsExpired(time.Now()) || strings.TrimSpace(proxy.Host) == "" || proxy.Port <= 0 {
			return nil, ErrCodexTurnStateCollectorProxyUnavailable
		}
		// Freeze the exact route returned by the repository before any later
		// validation or callbacks can observe a proxy edit or another selection.
		proxyURL := proxy.URL()
		binding := CodexTurnStateBundleBinding{WireMode: "lite", EgressKind: "proxy", ProxyID: proxy.ID, ProxyRouteGeneration: proxy.RouteGeneration}
		if proxy.ID != input.ProxyID || !binding.Valid() {
			return nil, ErrCodexTurnStateCollectorProxyUnavailable
		}
		ctx = WithHTTPUpstreamRedirectsDisabled(WithHTTPUpstreamProfile(ctx, HTTPUpstreamProfileCodexAuxiliary))
		ctx = WithOpenAINativeHTTPScope(ctx, owner, "")
		scope, _ := codexnative.ScopeFromContext(ctx)
		scope.Purpose = "turn_state_collector"
		request = request.Clone(codexnative.WithScope(ctx, scope))
		request.Close = true
		if request.Header == nil {
			request.Header = make(http.Header)
		}
		// Use the current owner credentials at the actual send boundary. No
		// business request headers or continuation identifiers enter this adapter.
		request.Header.Set("Authorization", "Bearer "+owner.GetCredential("access_token"))
		request.Header.Del("ChatGPT-Account-Id")
		if accountID := owner.GetCredential("chatgpt_account_id"); accountID != "" {
			request.Header.Set("ChatGPT-Account-Id", accountID)
		}
		for key := range request.Header {
			if strings.EqualFold(key, "x-codex-turn-state") || strings.EqualFold(key, "Cookie") {
				delete(request.Header, key)
			}
		}
		// Demand fixes the credential OS; keep that profile across owner reloads
		// without adopting business installation or continuation identifiers.
		userAgent := owner.GetOpenAIUserAgent()
		_, profilesAvailable := accounts.(OpenAIOAuthOSProfilesEnsurer)
		if IsOpenAIOAuthOSProfileOwner(owner) && (profilesAvailable || OpenAIOAuthOSProfilesComplete(owner.OpenAIOAuthOSProfiles)) {
			profile, err := ResolveOpenAIOAuthOSProfile(ctx, accounts, owner, codexTurnStateOS(owner))
			if err != nil {
				return nil, err
			}
			userAgent = profile.UserAgent
		}
		identity := resolveCodexClientIdentityPlan(CodexClientIdentityNormalize, userAgent)
		ensureCodexIdentityHeadersFromPlan(request.Header, identity)
		request.Header.Set(responsesLiteHeaderKey, "true")
		if input.validateModelPolicy == nil || !input.validateModelPolicy(ctx) {
			return nil, errors.New("collector_model_policy_changed")
		}
		if input.onSend != nil {
			input.onSend(time.Now())
		}
		if input.CaptureBundleBinding != nil {
			input.CaptureBundleBinding(binding)
		}
		return upstream.Do(request, proxyURL, owner.ID, 1)
	}
}

func ProvideCodexTurnStateService(repo CodexTurnStateRepository, accounts AccountRepository, encryptor SecretEncryptor, do CodexTurnStateCollectorHTTPDo, settings *SettingService, proxies ProxyRepository) *CodexTurnStateService {
	svc := NewCodexTurnStateService(repo, accounts, encryptor, NewCodexTurnStateHTTPCollector(do))
	svc.SetProxyRepository(proxies)
	if settings != nil {
		svc.modelPolicy = settings
		settings.AddCodexTurnStateModelsListener(func() {
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				svc.CancelExcludedModels(ctx)
			}()
		})
	}
	svc.Start(context.Background())
	return svc
}
