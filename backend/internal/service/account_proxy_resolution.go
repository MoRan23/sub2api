package service

import (
	"context"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/proxyurl"
)

// ErrAccountProxyUnavailable identifies a local routing failure, not an upstream
// authentication failure. A configured proxy must never become an empty URL.
var ErrAccountProxyUnavailable = infraerrors.ServiceUnavailable("ACCOUNT_PROXY_UNAVAILABLE", "configured account proxy is unavailable")

// ResolveConfiguredProxyURL resolves an explicit proxy binding. Only a missing
// binding means direct access; lookup errors and incomplete records fail closed.
func ResolveConfiguredProxyURL(ctx context.Context, repo ProxyRepository, proxyID *int64) (string, error) {
	if proxyID == nil {
		return "", nil
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if repo == nil || *proxyID <= 0 {
		return "", ErrAccountProxyUnavailable
	}
	proxy, err := repo.GetByID(ctx, *proxyID)
	if err != nil {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		return "", ErrAccountProxyUnavailable
	}
	return validatedAccountProxyURL(proxyID, proxy)
}

// ResolveAccountProxyURL accepts an eager-loaded proxy only when it belongs to
// this binding; callers without the relation resolve it through the repository.
func ResolveAccountProxyURL(ctx context.Context, repo ProxyRepository, account *Account) (string, error) {
	if account == nil {
		return "", ErrAccountProxyUnavailable
	}
	if account.ProxyID != nil && account.Proxy != nil {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		return validatedAccountProxyURL(account.ProxyID, account.Proxy)
	}
	return ResolveConfiguredProxyURL(ctx, repo, account.ProxyID)
}

func validatedAccountProxyURL(proxyID *int64, proxy *Proxy) (string, error) {
	if proxyID == nil || *proxyID <= 0 || proxy == nil || proxy.ID != *proxyID || proxy.Host == "" || proxy.Port < 1 || proxy.Port > 65535 {
		return "", ErrAccountProxyUnavailable
	}
	value, parsed, err := proxyurl.Parse(proxy.URL())
	if err != nil || parsed == nil || value == "" {
		return "", ErrAccountProxyUnavailable
	}
	return value, nil
}
