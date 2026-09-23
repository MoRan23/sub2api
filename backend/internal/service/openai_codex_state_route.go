package service

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openaicookies"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

const codexHTTPRouteSelectionKey = "openai_codex_http_route_selection"
const codexHTTPRouteBaselineKey = "openai_codex_http_route_baseline"
const codexHTTPBridgeRouteKey = "openai_codex_http_bridge_route"

// The selected bundle and transport scalars belong to one business attempt.
// Neither a later account reload nor a new cache publication may retarget it.
type codexHTTPRouteSelection struct {
	accountID       int64
	model           string
	capabilities    CodexModelCapabilities
	baseline, route OpenAIEgressRoute
	baselineBinding CodexTurnStateBundleBinding
	attempt         *CodexTurnStateAttempt
	physical        bool
}

type codexHTTPBaselineScope struct {
	accountID    int64
	model        string
	capabilities CodexModelCapabilities
}

func (s *OpenAIGatewayService) SetCodexTurnStateProxyRepository(repo ProxyRepository) {
	if s != nil {
		s.codexTurnStateProxyRepo = repo
	}
}

func codexHTTPRouteSelectionFromContext(c *gin.Context, account *Account) *codexHTTPRouteSelection {
	if c == nil || account == nil {
		return nil
	}
	value, _ := c.Get(codexHTTPRouteSelectionKey)
	selected, _ := value.(*codexHTTPRouteSelection)
	if selected == nil || selected.accountID != account.ID {
		return nil
	}
	return selected
}

func openAIHTTPBundleRouteEnabled(c *gin.Context) bool {
	if GetOpenAIClientTransport(c) != OpenAIClientTransportWS {
		return true
	}
	bridge, _ := c.Get(codexHTTPBridgeRouteKey)
	return bridge == true
}

func (s *OpenAIGatewayService) codexHTTPRouteBinding(ctx context.Context, account *Account, mode string) (OpenAIEgressRoute, CodexTurnStateBundleBinding) {
	route := OpenAIEgressRoute{}
	binding := CodexTurnStateBundleBinding{WireMode: mode, EgressKind: "direct"}
	if account == nil || account.ProxyID == nil || account.Proxy == nil {
		return route, binding
	}
	proxy := account.Proxy
	// Read the managed route once per attempt, including baseline reconstruction.
	// A scheduler account may predate a URL or credential change on the same ID.
	// Never mutate that account or its configured proxy ID.
	if s.codexTurnStateProxyRepo != nil {
		if current, err := s.codexTurnStateProxyRepo.GetByID(ctx, *account.ProxyID); err == nil && current != nil {
			proxy = current
		}
	}
	route.ProxyID, route.ProxyURL = *account.ProxyID, proxy.URL()
	binding.EgressKind, binding.ProxyID, binding.ProxyRouteGeneration = "proxy", route.ProxyID, proxy.RouteGeneration
	return route, binding
}

func (s *OpenAIGatewayService) selectOpenAIHTTPBundleRoute(ctx context.Context, c *gin.Context, account *Account, model string, body []byte) *codexHTTPRouteSelection {
	if s == nil || c == nil || !codexTurnStateEligible(account) || !openAIHTTPBundleRouteEnabled(c) || isOpenAIResponsesCompactPath(c) {
		return nil
	}
	model = strings.TrimSpace(model)
	if model == "" || gjson.GetBytes(body, "generate").Type == gjson.False {
		return nil
	}
	if selected := codexHTTPRouteSelectionFromContext(c, account); selected != nil && selected.model == model {
		return selected
	}
	if old := codexHTTPRouteSelectionFromContext(c, account); old != nil && old.attempt != nil && !old.physical {
		finishCodexTurnStateHTTPAttempt(s.codexTurnStateService, old.attempt, false)
	}
	capabilities := effectiveCodexHTTPModelCapabilities(account, model, s.openAICodexModelCapabilities(openAICodexModelCapabilitiesNamespace(account), model), explicitOpenAIResponsesLiteHTTP(c, nil) || isOpenAIResponsesLiteWebSocketPayload(body))
	baselineValue, _ := c.Get(codexHTTPRouteBaselineKey)
	baseline, baselineSet := baselineValue.(codexHTTPBaselineScope)
	baselineOnly := baselineSet && baseline.accountID == account.ID && baseline.model == model
	if baselineOnly {
		capabilities = baseline.capabilities
	}
	mode := "responses"
	if capabilities.UseResponsesLite {
		mode = "lite"
	}
	route, binding := s.codexHTTPRouteBinding(ctx, account, mode)
	selected := &codexHTTPRouteSelection{accountID: account.ID, model: model, capabilities: capabilities, baseline: route, route: route, baselineBinding: binding}
	c.Set(codexHTTPRouteSelectionKey, selected)
	if s.codexTurnStateService == nil || baselineOnly {
		return selected
	}
	// Compat tools have not been adapted yet. The finalizer validates the final
	// wire payload before sending; an early attempt is released if it rejects.
	attempt, err := s.codexTurnStateService.PrepareForHTTP(ctx, account, model, binding)
	if err != nil || attempt == nil {
		return selected
	}
	selected.attempt = attempt
	if attempt.Snapshot.Token == "" {
		return selected
	}
	if !attempt.keepAccountProxy && mode != "lite" && attempt.Snapshot.BundleBinding != binding {
		attempt.DiscardBundle("bundle_route_unprepared", binding)
		return selected
	}
	if resolved, ok := s.resolveOpenAIHTTPBundleProxy(ctx, account, attempt); ok {
		if !attempt.keepAccountProxy {
			selected.route = resolved
		}
	} else {
		attempt.DiscardBundle("bundle_proxy_unavailable", binding)
	}
	return selected
}

func (s *OpenAIGatewayService) resolveOpenAIHTTPBundleProxy(ctx context.Context, account *Account, attempt *CodexTurnStateAttempt) (OpenAIEgressRoute, bool) {
	binding := attempt.Snapshot.BundleBinding
	if !binding.Valid() || binding.WireMode != attempt.WireMode {
		return OpenAIEgressRoute{}, false
	}
	owner, err := s.codexTurnStateService.currentOwnerForAttempt(ctx, account)
	if err != nil || owner == nil {
		return OpenAIEgressRoute{}, false
	}
	if binding.EgressKind == "direct" {
		return OpenAIEgressRoute{}, attempt.keepAccountProxy || owner.ProxyID == nil
	}
	if s.codexTurnStateProxyRepo == nil {
		return OpenAIEgressRoute{}, false
	}
	allowed := codexTurnStateProxyAllowed(CodexTurnStateCollectorProxyIDs(CodexTurnStateConfigForAccount(owner)), binding.ProxyID)
	// A naturally learned response remains valid on the account's own route.
	if owner.ProxyID != nil && *owner.ProxyID == binding.ProxyID {
		allowed = true
	}
	if !allowed {
		return OpenAIEgressRoute{}, false
	}
	proxy, err := s.codexTurnStateProxyRepo.GetByID(ctx, binding.ProxyID)
	if err != nil || proxy == nil || !proxy.IsActive() || proxy.IsExpired(s.codexTurnStateService.now()) || proxy.RouteGeneration != binding.ProxyRouteGeneration || strings.TrimSpace(proxy.Host) == "" || proxy.Port <= 0 {
		return OpenAIEgressRoute{}, false
	}
	return OpenAIEgressRoute{ProxyID: proxy.ID, ProxyURL: proxy.URL()}, true
}

func (s *OpenAIGatewayService) validateOpenAIHTTPBundleRoute(ctx context.Context, c *gin.Context, account *Account, attempt *CodexTurnStateAttempt) bool {
	selected := codexHTTPRouteSelectionFromContext(c, account)
	if attempt == nil || attempt.Snapshot.Token == "" {
		return true
	}
	if selected == nil && (!attempt.keepAccountProxy || attempt.Snapshot.BundleBinding == attempt.OutboundBinding) {
		return true
	}
	route, ok := s.resolveOpenAIHTTPBundleProxy(ctx, account, attempt)
	if selected == nil {
		return ok
	}
	if attempt.keepAccountProxy {
		return ok && selected.route == selected.baseline && attempt.OutboundBinding == selected.baselineBinding
	}
	return ok && route == selected.route
}

func (a *CodexTurnStateAttempt) bundleMatchesOutbound() bool {
	if a == nil || !a.Snapshot.BundleBinding.Valid() || !a.OutboundBinding.Valid() ||
		a.Snapshot.BundleBinding.WireMode != a.WireMode || a.OutboundBinding.WireMode != a.WireMode {
		return false
	}
	return a.keepAccountProxy || a.Snapshot.BundleBinding == a.OutboundBinding
}

// Rebuild only after a strictly local, pre-send rejection. The original business
// body enters its normal adapters again, including timezone projection; a real
// network failure is never replayed by this wrapper.
func (s *OpenAIGatewayService) withOpenAIHTTPBundleBaseline(c *gin.Context, account *Account, forward func() (*OpenAIForwardResult, error)) (*OpenAIForwardResult, error) {
	result, err := forward()
	if errors.Is(err, openaicookies.ErrBundleSendRejected) && c != nil {
		selected := codexHTTPRouteSelectionFromContext(c, account)
		alreadyValue, _ := c.Get(codexHTTPRouteBaselineKey)
		already, _ := alreadyValue.(codexHTTPBaselineScope)
		if selected != nil && !codexHTTPBundleAttemptWasSent(selected.attempt) && (already.accountID != account.ID || already.model != selected.model) {
			if selected.attempt != nil && !selected.physical {
				finishCodexTurnStateHTTPAttempt(s.codexTurnStateService, selected.attempt, false)
			}
			c.Set(codexHTTPRouteBaselineKey, codexHTTPBaselineScope{account.ID, selected.model, selected.capabilities})
			c.Set(codexHTTPRouteSelectionKey, nil)
			ClearOpenAIOutboundRoute(c)
			result, err = forward()
		}
	}
	if selected := codexHTTPRouteSelectionFromContext(c, account); selected != nil && selected.attempt != nil && !selected.physical {
		finishCodexTurnStateHTTPAttempt(s.codexTurnStateService, selected.attempt, false)
	}
	return result, err
}

func codexHTTPBundleAttemptWasSent(attempt *CodexTurnStateAttempt) bool {
	if attempt == nil {
		return false
	}
	attempt.mu.Lock()
	defer attempt.mu.Unlock()
	// A redirect may reject a later physical request after the original was
	// already sent. Such a rejection cannot authorize a business replay.
	return attempt.historyPhysicalBound
}

func (s *OpenAIGatewayService) prepareOpenAIHTTPBridgeBundleRoute(ctx context.Context, c *gin.Context, account *Account, body []byte, newFrame bool) ([]byte, *RequestTimezoneState) {
	if c == nil {
		return body, nil
	}
	if s == nil || s.codexTurnStateService == nil || !codexTurnStateEligible(account) {
		state, _ := RequestTimezoneStateFromContext(c)
		return body, state
	}
	c.Set(codexHTTPBridgeRouteKey, true)
	if newFrame {
		if previous := codexHTTPRouteSelectionFromContext(c, account); previous != nil && previous.attempt != nil && !previous.physical {
			finishCodexTurnStateHTTPAttempt(s.codexTurnStateService, previous.attempt, false)
		}
		c.Set(codexHTTPRouteSelectionKey, nil)
		c.Set(codexHTTPRouteBaselineKey, nil)
	}
	s.selectOpenAIHTTPBundleRoute(ctx, c, account, gjson.GetBytes(body, "model").String(), body)
	ClearOpenAIOutboundRoute(c)
	FreezeOpenAIOutboundRoute(c, account)
	target, egress := s.resolveOpenAIRequestTimezoneTarget(c, account)
	if state, ok := RequestTimezoneStateFromContext(c); ok {
		if state.Target == target {
			state = CloneRequestTimezoneState(state)
			state.EgressLocation = egress
			SetRequestTimezoneState(c, state)
			return body, state
		}
		projectedBody, projected, applied := state.ProjectToTarget(body, target)
		if applied {
			projected.EgressLocation = egress
			SetRequestTimezoneState(c, projected)
			return projectedBody, projected
		}
		return body, state
	}
	return body, nil
}

func (s *OpenAIGatewayService) prepareOpenAIHTTPBundleModel(ctx context.Context, c *gin.Context, account *Account, body []byte, protocol, defaultModel string) {
	if !codexTurnStateEligible(account) {
		return
	}
	model := strings.TrimSpace(gjson.GetBytes(body, "model").String())
	switch protocol {
	case "messages":
		model = normalizeOpenAIModelForUpstream(account, resolveOpenAIForwardModel(account, NormalizeOpenAICompatRequestedModel(model), defaultModel))
	case "chat":
		model = normalizeOpenAIModelForUpstream(account, resolveOpenAIForwardModel(account, model, defaultModel))
	default:
		model = s.resolveOpenAIResponsesHTTPFinalModel(c, account, model)
	}
	s.selectOpenAIHTTPBundleRoute(ctx, c, account, model, body)
}

// Match the actual forwarding path before any model-sensitive normalization,
// route or identity selection. Passthrough preserves the wire model except for
// compact fallback; account mappings apply to the normal Responses adapter.
func (s *OpenAIGatewayService) resolveOpenAIResponsesHTTPFinalModel(c *gin.Context, account *Account, model string) string {
	if isOpenAIResponsesCompactPath(c) {
		if compactModel := s.resolveOpenAICompactFallbackModel(account, model); compactModel != "" {
			return compactModel
		}
	}
	if account != nil && account.IsOpenAIPassthroughEnabled() {
		return model
	}
	_, model = resolveOpenAIForwardMappedModels(account, model, isOpenAIResponsesCompactPath(c))
	return model
}

func frozenOpenAIHTTPBundleCapabilities(c *gin.Context, account *Account, model string) (CodexModelCapabilities, bool) {
	selected := codexHTTPRouteSelectionFromContext(c, account)
	if selected == nil || selected.model != strings.TrimSpace(model) || !openAIHTTPBundleRouteEnabled(c) {
		return CodexModelCapabilities{}, false
	}
	return selected.capabilities, true
}

func rejectedOpenAIHTTPBundleRequest(request *http.Request) *http.Request {
	return request.WithContext(context.WithValue(request.Context(), codexHTTPBundleRejectedKey{}, true))
}

type codexHTTPBundleRejectedKey struct{}

func codexHTTPBundleWireModeMatches(attempt *CodexTurnStateAttempt, headers http.Header) bool {
	if attempt == nil || attempt.Snapshot.Token == "" {
		return true
	}
	mode := "responses"
	if hasOpenAIResponsesLiteHeader(headers) {
		mode = "lite"
	}
	return mode == attempt.WireMode
}
