package service

import "github.com/gin-gonic/gin"

// Use the route selected for this physical request, not the account's default
// proxy. A ticket may select another configured proxy without editing account.
func opsOpenAIOutboundProxyID(c *gin.Context, account *Account) *int64 {
	if c != nil && account != nil {
		if raw, ok := c.Get(openAIOutboundRouteKey); ok {
			if frozen, valid := raw.(frozenOpenAIOutboundRoute); valid && frozen.accountID == account.ID {
				if frozen.route.ProxyID > 0 {
					id := frozen.route.ProxyID
					return &id
				}
				return nil
			}
		}
	}
	return opsUpstreamProxyID(account)
}

func opsOpenAIOutboundProxyName(c *gin.Context, account *Account) string {
	if c != nil && account != nil {
		if raw, ok := c.Get(openAIOutboundRouteKey); ok {
			if frozen, valid := raw.(frozenOpenAIOutboundRoute); valid && frozen.accountID == account.ID {
				if frozen.route.ProxyID <= 0 {
					return opsProxyNameDirect
				}
				if account.Proxy != nil && account.Proxy.ID == frozen.route.ProxyID {
					return opsUpstreamProxyName(account)
				}
				return opsProxyNameUnnamed
			}
		}
	}
	return opsUpstreamProxyName(account)
}
