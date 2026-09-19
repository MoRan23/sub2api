package service

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
)

type openAIWSTransportAccountKey struct{}

// withOpenAIWSTransportAccount carries routing ownership without changing the
// dialer interface or adding account identifiers to the upstream handshake.
func withOpenAIWSTransportAccount(ctx context.Context, accountID int64) context.Context {
	return context.WithValue(ctx, openAIWSTransportAccountKey{}, accountID)
}

// Standard TLS is the transport actually used by the WS dialer. An account's
// configured HTTP fingerprint must not be mistaken for an applied WS profile.
type openAIWSTransportScope struct {
	accountID int64
	target    string
	proxy     string
	transport string
}

func newOpenAIWSTransportScope(accountID int64, target, proxy string) openAIWSTransportScope {
	return openAIWSTransportScope{
		accountID: accountID,
		target:    openAIWSTransportDigest(target),
		proxy:     openAIWSTransportDigest(proxy),
		transport: "standard",
	}
}

func openAIWSTransportScopeFromContext(ctx context.Context, target, proxy string) openAIWSTransportScope {
	accountID, _ := ctx.Value(openAIWSTransportAccountKey{}).(int64)
	return newOpenAIWSTransportScope(accountID, target, proxy)
}

func openAIWSTransportDigest(value string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(strings.TrimSpace(value))))
}

func (s openAIWSTransportScope) cacheKey() string {
	return fmt.Sprintf("account:%d|target:%s|proxy:%s|transport:%s", s.accountID, s.target, s.proxy, s.transport)
}

func openAIWSAcquireCompatibility(req openAIWSAcquireRequest) openAIWSHandshakeCompatibilityKey {
	key := normalizeOpenAIWSHandshakeCompatibility(req.Headers, req.IdentityDigest)
	key.codexStateMode = req.CodexStateMode
	accountID := int64(0)
	if req.Account != nil {
		accountID = req.Account.ID
	}
	key.transportScope = newOpenAIWSTransportScope(accountID, req.WSURL, req.ProxyURL)
	return key
}
