package service

import (
	"strings"
)

// Decide the HTTP wire protocol before selecting a ticket, proxy or identity.
// The finalizer reuses this decision instead of stripping a Lite header after
// freezing a Lite bundle and its egress. Native WebSocket does not use this rule.
func effectiveCodexHTTPModelCapabilities(account *Account, finalModel string, observed CodexModelCapabilities, explicitLite bool) CodexModelCapabilities {
	capabilities := effectiveCodexModelCapabilities(observed, explicitLite)
	if codexHTTPModelRequiresNonLite(account, finalModel) {
		capabilities.Known = true
		capabilities.UseResponsesLite = false
	}
	return capabilities
}

func codexHTTPModelRequiresNonLite(account *Account, finalModel string) bool {
	return account != nil && account.IsOpenAIOAuthLike() && strings.TrimSpace(finalModel) == "gpt-5.5"
}
