package service

import (
	"context"
	"time"
)

func (s *stubGatewayCache) ResolveOpenAICodexWindowMapping(ctx context.Context, canonicalKey, legacyKey, threadID string, ttl time.Duration) (string, error) {
	return processOpenAICodexWindowLocalStore.ResolveOpenAICodexWindowMapping(ctx, canonicalKey, legacyKey, threadID, ttl)
}

func (s *outboundIdentityGatewayCacheStub) ResolveOpenAICodexWindowMapping(ctx context.Context, canonicalKey, legacyKey, threadID string, ttl time.Duration) (string, error) {
	return processOpenAICodexWindowLocalStore.ResolveOpenAICodexWindowMapping(ctx, canonicalKey, legacyKey, threadID, ttl)
}

func (s *passthroughCompactWindowCache) ResolveOpenAICodexWindowMapping(ctx context.Context, canonicalKey, legacyKey, threadID string, ttl time.Duration) (string, error) {
	s.mu.Lock()
	store := s.compactWindowStore()
	s.mu.Unlock()
	return store.ResolveOpenAICodexWindowMapping(ctx, canonicalKey, legacyKey, threadID, ttl)
}
