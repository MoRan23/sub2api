package service

import (
	"net/http"
	"sync"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openaicookies"
)

type openAIPluginRoundTripFunc func(*http.Request) (*http.Response, error)

func (f openAIPluginRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

// The manager applies cookie handling only after selecting a live plugin. An
// unhandled dispatch must not claim a physical send or open a cookie attempt.
func roundTripOpenAIPluginWithCookieBundle(manager *PluginManager, request *http.Request, proxyURL string, account *Account) (*http.Response, bool, error) {
	return manager.RoundTripOpenAIOAuth(request.Context(), request, proxyURL, account)
}

func (s *OpenAIGatewayService) SetPluginManager(manager *PluginManager) {
	s.pluginManager = manager
}

// doOpenAIUpstream 只在 OpenAI OAuth 能力绑定已启用时把真实请求交给插件。
// 插件返回标准 http.Response，响应解析、错误映射、SSE 和计费仍由现有核心链处理。
func (s *OpenAIGatewayService) doOpenAIUpstream(request *http.Request, proxyURL string, account *Account) (response *http.Response, err error) {
	defer func() { observeCodexTurnStateHTTPResponse(request, response, err) }()
	if rejected, _ := request.Context().Value(codexHTTPBundleRejectedKey{}).(bool); rejected {
		return nil, openaicookies.ErrBundleSendRejected
	}
	if account != nil && account.Platform == PlatformOpenAI {
		request = ApplyOpenAIRequestPolicy(request, s.settingService)
	}
	recordOpenAIGuardianSourceHTTPRequest(request, account)
	var telemetryAttempt *CodexTelemetryAttempt
	var telemetryStart sync.Once
	request = request.WithContext(openaicookies.WithSendObserver(request.Context(), func(outbound *http.Request) {
		telemetryStart.Do(func() { telemetryAttempt = s.beginCodexTelemetryHTTPRequest(outbound, proxyURL, account) })
	}))
	defer func() {
		if telemetryAttempt != nil {
			observeCodexTelemetryHTTPResponse(telemetryAttempt, response, err)
		}
	}()
	request = withOpenAINativeHTTPRequestScope(request, account, s.accountRepo, "gateway")
	if s.pluginManager != nil {
		var handled bool
		response, handled, err = roundTripOpenAIPluginWithCookieBundle(s.pluginManager, request, proxyURL, account)
		if handled {
			return response, err
		}
	}
	return s.httpUpstream.Do(request, proxyURL, account.ID, account.Concurrency)
}

// doCodexAuxiliaryUpstream keeps History/Notes on the configured OAuth plugin
// route while excluding these requests from the account's transport concurrency.
func (s *OpenAIGatewayService) doCodexAuxiliaryUpstream(request *http.Request, proxyURL string, account *Account) (*http.Response, error) {
	request = ApplyOpenAIRequestPolicy(request, s.settingService)
	request = request.WithContext(WithHTTPUpstreamProfile(request.Context(), HTTPUpstreamProfileCodexAuxiliary))
	if s.pluginManager != nil {
		auxiliaryAccount := *account
		auxiliaryAccount.Concurrency = 0
		response, handled, err := s.pluginManager.RoundTripOpenAIOAuth(request.Context(), request, proxyURL, &auxiliaryAccount)
		if handled {
			return response, err
		}
	}
	request = withOpenAINativeHTTPRequestScope(request, account, s.accountRepo, "codex-auxiliary")
	return s.httpUpstream.Do(request, proxyURL, account.ID, 0)
}

// doOpenAIAccountTestUpstream 让 OpenAI OAuth 账号测试与真实转发使用同一插件路径。
// API Key 和未命中插件的账号保持各自原有的 HTTPUpstream 行为。
func (s *AccountTestService) doOpenAIAccountTestUpstream(
	request *http.Request,
	proxyURL string,
	account *Account,
	useTLSFallback bool,
) (*http.Response, error) {
	if account != nil && account.Platform == PlatformOpenAI {
		request = ApplyOpenAIRequestPolicy(request, s.settingService)
	}
	if s.pluginManager != nil {
		response, handled, err := s.pluginManager.RoundTripOpenAIOAuth(request.Context(), request, proxyURL, account)
		if handled {
			return response, err
		}
	}
	request = withOpenAINativeHTTPRequestScope(request, account, s.accountRepo, "account-test")
	if useTLSFallback {
		return s.httpUpstream.DoWithTLS(
			request,
			proxyURL,
			account.ID,
			account.Concurrency,
			s.tlsFPProfileService.ResolveTLSProfile(account),
		)
	}
	return s.httpUpstream.Do(request, proxyURL, account.ID, account.Concurrency)
}
