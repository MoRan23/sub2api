package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCodexModelCapabilityCacheObservesManifestByCredentialOwner(t *testing.T) {
	now := time.Date(2026, 8, 19, 0, 0, 0, 0, time.UTC)
	cache := &codexModelCapabilityCache{}
	cache.observeManifest("account:7", []byte(`{"models":[{"slug":"gpt-5.6-sol","use_responses_lite":true,"node_repl_auto_review_required":true,"node_repl_disabled":false},{"slug":"gpt-5.6-terra"}]}`), now)

	require.Equal(t, CodexModelCapabilities{
		UseResponsesLite:           true,
		NodeREPLAutoReviewRequired: true,
		NodeREPLDisabled:           false,
		Known:                      true,
	}, cache.get("account:7", "GPT-5.6-SOL", now.Add(time.Second)))
	require.Equal(t, CodexModelCapabilities{Known: true}, cache.get("account:7", "gpt-5.6-terra", now.Add(time.Second)))
	require.False(t, cache.get("account:8", "gpt-5.6-sol", now.Add(time.Second)).Known)
	require.False(t, cache.get("account:7", "gpt-5.6-sol", now.Add(codexModelCapabilityCacheTTL)).Known)
}

func TestCodexModelCapabilityCacheIgnoresMalformedManifest(t *testing.T) {
	cache := &codexModelCapabilityCache{}
	cache.observeManifest("account:7", []byte(`{"models":`), time.Now())
	require.False(t, cache.get("account:7", "gpt-5.6-sol", time.Now()).Known)
}

func TestCodexModelCapabilityCacheRefreshesKnownValuesAfter304(t *testing.T) {
	now := time.Date(2026, 8, 19, 0, 0, 0, 0, time.UTC)
	cache := &codexModelCapabilityCache{}
	cache.observeManifest("account:7", []byte(`{"models":[{"slug":"gpt-5.6-sol","use_responses_lite":true}]}`), now)

	expiredAt := now.Add(codexModelCapabilityCacheTTL)
	require.False(t, cache.get("account:7", "gpt-5.6-sol", expiredAt).Known)
	cache.refreshNamespace("account:7", expiredAt)

	got := cache.get("account:7", "gpt-5.6-sol", expiredAt.Add(time.Second))
	require.True(t, got.Known)
	require.True(t, got.UseResponsesLite)
	require.False(t, cache.get("account:8", "gpt-5.6-sol", expiredAt.Add(time.Second)).Known)
}

func TestEffectiveCodexModelCapabilitiesUsesExplicitLiteOnlyWhenUnknown(t *testing.T) {
	require.True(t, effectiveCodexModelCapabilities(CodexModelCapabilities{}, true).UseResponsesLite)
	require.False(t, effectiveCodexModelCapabilities(CodexModelCapabilities{Known: true}, true).UseResponsesLite)
	require.True(t, effectiveCodexModelCapabilities(CodexModelCapabilities{Known: true, UseResponsesLite: true}, false).UseResponsesLite)
}
