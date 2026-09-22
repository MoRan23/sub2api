package service

import "github.com/gin-gonic/gin"

const openAIOutboundRouteKey = "openai_outbound_route"
const openAIRequestTimezoneTargetsKey = "openai_request_timezone_targets"

type frozenOpenAIOutboundRoute struct {
	accountID int64
	route     OpenAIEgressRoute
}

// FreezeOpenAIOutboundRoute copies transport scalars. A WS connection keeps this
// route across turns; a new physical account attempt explicitly clears it.
func FreezeOpenAIOutboundRoute(c *gin.Context, account *Account) OpenAIEgressRoute {
	if c != nil && account != nil {
		if raw, ok := c.Get(openAIOutboundRouteKey); ok {
			if frozen, valid := raw.(frozenOpenAIOutboundRoute); valid && frozen.accountID == account.ID {
				return frozen.route
			}
		}
	}
	route := OpenAIEgressRoute{}
	if selected := codexHTTPRouteSelectionFromContext(c, account); selected != nil && openAIHTTPBundleRouteEnabled(c) {
		route = selected.route
	} else if account != nil && account.ProxyID != nil && account.Proxy != nil {
		route.ProxyID, route.ProxyURL = *account.ProxyID, account.Proxy.URL()
	}
	if c != nil && account != nil {
		c.Set(openAIOutboundRouteKey, frozenOpenAIOutboundRoute{account.ID, route})
	}
	return route
}

func ClearOpenAIOutboundRoute(c *gin.Context) {
	if c != nil {
		c.Set(openAIOutboundRouteKey, nil)
	}
}

func OpenAIOutboundRouteForAccount(c *gin.Context, account *Account) OpenAIEgressRoute {
	return FreezeOpenAIOutboundRoute(c, account)
}

func ResetOpenAIRequestTimezoneTargets(c *gin.Context) {
	if c != nil {
		c.Set(openAIRequestTimezoneTargetsKey, map[string]OpenAIEgressLocationSnapshot{})
	}
}

func (s *OpenAIGatewayService) resolveOpenAIRequestTimezoneTarget(c *gin.Context, account *Account) (RequestLocationObservation, *OpenAIEgressLocationSnapshot) {
	if account == nil || !account.IsOpenAIOAuth() {
		return openAIRequestSearchLocation(), nil
	}
	route := OpenAIOutboundRouteForAccount(c, account)
	key := DefaultOpenAIEgressLocationSnapshot(route).RouteKey
	var targets map[string]OpenAIEgressLocationSnapshot
	if c != nil {
		raw, _ := c.Get(openAIRequestTimezoneTargetsKey)
		targets, _ = raw.(map[string]OpenAIEgressLocationSnapshot)
		if snapshot, ok := targets[key]; ok {
			return egressLocationTimezoneTarget(snapshot), &snapshot
		}
	}
	var resolver *OpenAIEgressLocationService
	if s != nil {
		resolver = s.egressLocationService
	}
	snapshot := resolver.Resolve(route)
	if c != nil {
		if targets == nil {
			targets = make(map[string]OpenAIEgressLocationSnapshot)
		}
		targets[key] = snapshot
		c.Set(openAIRequestTimezoneTargetsKey, targets)
	}
	return egressLocationTimezoneTarget(snapshot), &snapshot
}

func egressLocationTimezoneTarget(snapshot OpenAIEgressLocationSnapshot) RequestLocationObservation {
	return RequestLocationObservation{Type: "approximate", Country: snapshot.CountryCode, Region: snapshot.Region, City: snapshot.City, Timezone: snapshot.Timezone}
}
