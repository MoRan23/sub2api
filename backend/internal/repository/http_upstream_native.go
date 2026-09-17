package repository

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/codexnative"
	"github.com/Wei-Shaw/sub2api/internal/pkg/servertiming"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

const upstreamProtocolModeNativeHTTP = "codex_native_h1"

// The outer http.Client owns redirects. Each physical RoundTrip selects the
// final UA again, so redirected requests cannot inherit an incompatible pool.
func (s *httpUpstreamService) doNativeHTTP(req *http.Request, proxyURL string, accountID int64, concurrency int) (*http.Response, error) {
	client := &http.Client{Transport: &nativeUpstreamRoundTripper{s: s, proxyURL: proxyURL, accountID: accountID, concurrency: concurrency}}
	if s.shouldValidateResolvedIP() {
		client.CheckRedirect = s.redirectChecker
	}
	client = s.httpClientForUpstreamRequest(client, req)
	response, err := servertiming.Do(client, req)
	if err != nil {
		return nil, err
	}
	decompressResponseBody(response)
	return response, nil
}

type nativeUpstreamRoundTripper struct {
	s           *httpUpstreamService
	proxyURL    string
	accountID   int64
	concurrency int
}

func (t *nativeUpstreamRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := t.s.validateRequestHost(req); err != nil {
		if req.Body != nil {
			_ = req.Body.Close()
		}
		return nil, err
	}
	scope, _ := codexnative.ScopeFromContext(req.Context())
	selection := codexnative.Resolve(codexnative.UserAgent(req.Header), scope)
	profile := service.HTTPUpstreamProfileFromContext(req.Context())
	entry, err := t.s.acquireNativeClient(t.proxyURL, t.accountID, t.concurrency, profile, scope, selection)
	if err != nil {
		if req.Body != nil {
			_ = req.Body.Close()
		}
		return nil, err
	}
	release := func() {
		atomic.AddInt64(&entry.inFlight, -1)
		atomic.StoreInt64(&entry.lastUsed, time.Now().UnixNano())
	}
	response, err := entry.client.Transport.RoundTrip(req)
	if err != nil || response == nil {
		release()
		if err == nil {
			err = fmt.Errorf("native HTTP transport returned no response")
		}
		return response, err
	}
	if response.Body == nil {
		response.Body = http.NoBody
	}
	response.Body = wrapTrackedBody(response.Body, release)
	return response, nil
}

func (s *httpUpstreamService) acquireNativeClient(proxyURL string, accountID int64, concurrency int, profile service.HTTPUpstreamProfile, scope codexnative.Scope, selection codexnative.Selection) (*upstreamClientEntry, error) {
	proxyKey, parsed, err := normalizeProxyURL(proxyURL)
	if err != nil {
		return nil, err
	}
	settings := s.applyProfilePoolSettings(s.resolvePoolSettings(s.getIsolationMode(), concurrency), profile)
	poolKey := buildPoolKey(settings, upstreamProtocolModeNativeHTTP)
	// Always include the account and proxy, even where ordinary HTTP is configured
	// to share a proxy pool. No session/thread/turn identifiers enter this key.
	cacheKey := fmt.Sprintf("native:%d:%d:%x:%s:%s:%s", accountID, scope.AccountID, sha256.Sum256([]byte(proxyKey)), scope.Purpose, selection.Digest, profile)
	now := time.Now()
	s.mu.RLock()
	entry := s.clients[cacheKey]
	if entry != nil && entry.poolKey == poolKey {
		atomic.AddInt64(&entry.inFlight, 1)
		atomic.StoreInt64(&entry.lastUsed, now.UnixNano())
		s.mu.RUnlock()
		return entry, nil
	}
	s.mu.RUnlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if entry = s.clients[cacheKey]; entry != nil {
		if entry.poolKey == poolKey {
			atomic.AddInt64(&entry.inFlight, 1)
			atomic.StoreInt64(&entry.lastUsed, now.UnixNano())
			return entry, nil
		}
		s.removeClientLocked(cacheKey, entry)
	}
	s.evictIdleLocked(now)
	if limit := s.maxUpstreamClients(); limit > 0 && len(s.clients) >= limit && !s.evictOldestIdleLocked() {
		return nil, errUpstreamClientLimitReached
	}
	base, err := buildUpstreamTransport(settings, nil, upstreamProtocolModeOpenAIH1)
	if err != nil {
		return nil, err
	}
	// req implements all proxy protocols itself. In particular HTTPS proxy TLS
	// is separate from the native TLS handshake inside its CONNECT tunnel.
	if parsed != nil {
		base.Proxy = http.ProxyURL(parsed)
	}
	transport, err := codexnative.NewTransport(selection.Platform, base)
	if err != nil {
		return nil, err
	}
	entry = &upstreamClientEntry{client: &http.Client{Transport: transport}, proxyKey: proxyKey, poolKey: poolKey, protocolMode: upstreamProtocolModeNativeHTTP}
	atomic.StoreInt64(&entry.inFlight, 1)
	atomic.StoreInt64(&entry.lastUsed, now.UnixNano())
	s.clients[cacheKey] = entry
	return entry, nil
}
