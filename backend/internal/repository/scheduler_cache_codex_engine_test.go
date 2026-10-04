//go:build unit

package repository

import (
	"context"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestCodexEngineSchedulerCacheRoundTrip(t *testing.T) {
	cache := newSchedulerCacheUnit(t)
	a := &service.Account{ID: 777, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Extra: map[string]any{service.OpenAIAPIKeyModeExtraKey: "codex_engine"}}
	require.NoError(t, cache.SetAccount(context.Background(), a))
	got, err := cache.GetAccount(context.Background(), a.ID)
	require.NoError(t, err)
	require.True(t, got.IsCodexEngine())
	a.Extra[service.OpenAIAPIKeyModeExtraKey] = "generic"
	require.NoError(t, cache.SetAccount(context.Background(), a))
	got, err = cache.GetAccount(context.Background(), a.ID)
	require.NoError(t, err)
	require.False(t, got.IsCodexEngine())
}
