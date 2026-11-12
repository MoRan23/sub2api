//go:build unit

package repository

import (
	"context"
	"encoding/json"
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
	require.False(t, got.IsOpenAIAPIKeyAutoAttributionEnabled(), "Engine automatic attribution defaults off")
	a.Extra[service.OpenAIAPIKeyAutoAttributionEnabledExtraKey] = true
	require.NoError(t, cache.SetAccount(context.Background(), a))
	got, err = cache.GetAccount(context.Background(), a.ID)
	require.NoError(t, err)
	require.True(t, got.IsOpenAIAPIKeyAutoAttributionEnabled())
	a.Extra[service.OpenAIAPIKeyAutoAttributionEnabledExtraKey] = false
	require.NoError(t, cache.SetAccount(context.Background(), a))
	got, err = cache.GetAccount(context.Background(), a.ID)
	require.NoError(t, err)
	require.False(t, got.IsOpenAIAPIKeyAutoAttributionEnabled())
	a.Extra[service.OpenAIAPIKeyModeExtraKey] = "generic"
	require.NoError(t, cache.SetAccount(context.Background(), a))
	got, err = cache.GetAccount(context.Background(), a.ID)
	require.NoError(t, err)
	require.False(t, got.IsCodexEngine())
	a.Extra[service.OpenAIAPIKeyAutoAttributionEnabledExtraKey] = true
	require.NoError(t, cache.SetAccount(context.Background(), a))
	got, err = cache.GetAccount(context.Background(), a.ID)
	require.NoError(t, err)
	require.True(t, got.IsOpenAIAPIKeyAutoAttributionEnabled())
	a.Extra[service.OpenAIAPIKeyAutoAttributionEnabledExtraKey] = false
	require.NoError(t, cache.SetAccount(context.Background(), a))
	got, err = cache.GetAccount(context.Background(), a.ID)
	require.NoError(t, err)
	require.False(t, got.IsOpenAIAPIKeyAutoAttributionEnabled())
}

func TestCodexEngineModelsEmptySchedulerProjection(t *testing.T) {
	cache := newSchedulerCacheUnit(t)
	a := &service.Account{ID: 778, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey,
		Credentials: map[string]any{"model_mapping": map[string]any{}},
		Extra:       map[string]any{service.OpenAIAPIKeyModeExtraKey: "codex_engine", service.CodexEngineModelsSyncExtraKey: map[string]any{"known": true, "model_count": 0, "identity": "source", "observation_id": "observed"}}}
	require.Contains(t, filterSchedulerExtra(a.Extra), service.CodexEngineModelsSyncExtraKey)
	require.NoError(t, cache.SetAccount(context.Background(), a))
	got, err := cache.GetAccount(context.Background(), a.ID)
	require.NoError(t, err)
	require.NotNil(t, service.CodexEngineModelsStateFromAccount(got))
	require.False(t, got.IsModelSupported("gpt-6-astra"))
	raw, err := cache.rdb.Get(context.Background(), schedulerAccountMetaPrefix+"778").Bytes()
	require.NoError(t, err)
	var metadata service.Account
	require.NoError(t, json.Unmarshal(raw, &metadata))
	require.False(t, metadata.IsModelSupported("gpt-6-astra"))
	rev, err := cache.OpenAIModelCatalogRevision(context.Background())
	require.NoError(t, err)
	require.NotEmpty(t, rev)

}
