package service

import (
	"context"
	"maps"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEngineCatalogAlwaysConstrainsPassthroughModels(t *testing.T) {
	for _, filter := range []bool{false, true} {
		account := emptySyncedEngineCatalogAccount()
		account.Extra["openai_passthrough"] = true
		account.Extra["openai_passthrough_model_filter"] = filter
		account.Credentials["model_mapping"] = map[string]any{"allowed": "allowed"}
		gateway := &GatewayService{}
		// An authoritative empty observation wins even if an old map survived.
		require.False(t, account.IsModelSupported("allowed"))
		require.False(t, gateway.isModelSupportedByAccount(&account, "allowed"))
		account.Extra[CodexEngineModelsSyncExtraKey] = map[string]any{"known": true, "model_count": 1, "identity": "source", "observation_id": "observed"}
		require.True(t, account.IsModelSupported("allowed"))
		require.False(t, account.IsModelSupported("other"))
		require.False(t, gateway.isModelSupportedByAccount(&account, "other"))
		for _, codex := range []bool{false, true} {
			body := []byte(`{"data":[{"id":"allowed"},{"id":"other"}]}`)
			want := `{"data":[{"id":"allowed"}]}`
			if codex {
				body = []byte(`{"models":[{"slug":"allowed"},{"slug":"other"}]}`)
				want = `{"models":[{"slug":"allowed"}]}`
			}
			projected, err := projectAccountModelAliases(body, &account, nil, codex)
			require.NoError(t, err)
			require.JSONEq(t, want, string(projected))
		}
		require.Equal(t, []string{"allowed"}, supplementUnmappedOpenAIModels([]Account{account}, []string{"allowed"}))
	}
}

func TestPinnedCatalogCacheTracksPassthroughFilter(t *testing.T) {
	svc := &OpenAIGatewayService{}
	group := &Group{ID: 8, Platform: PlatformOpenAI}
	account := groupCatalogAccount(9, map[string]any{"source": "target"})
	account.Extra = map[string]any{"openai_passthrough": true}
	source := catalogFixture(`{"models":[{"slug":"source","context_window":12345},{"slug":"target","context_window":67890}]}`)
	source.upstreamSourceBody = source.Body
	first, err := svc.cachedPinnedOpenAIGroupSource(context.Background(), group, &account, source)
	require.NoError(t, err)
	account.Extra = maps.Clone(account.Extra)
	account.Extra["openai_passthrough_model_filter"] = true
	filtered, err := svc.cachedPinnedOpenAIGroupSource(context.Background(), group, &account, source)
	require.NoError(t, err)
	require.NotEqual(t, first.ETag, filtered.ETag)
	require.Contains(t, string(filtered.Body), `"source"`)
	require.NotContains(t, string(filtered.Body), `"target"`)
	require.Contains(t, string(source.Body), `"target"`, "the original catalog remains immutable")
}

func TestOpenAIGroupCatalogFilteredPassthroughUsesSourceMetadata(t *testing.T) {
	account := groupCatalogAccount(1, map[string]any{"custom-source": "gpt-6-astra"})
	account.Extra = map[string]any{"openai_passthrough": true, "openai_passthrough_model_filter": true}
	group := &Group{ID: 1, Platform: PlatformOpenAI}
	official := catalogFixture(`{"models":[{"slug":"gpt-6-astra","context_window":999999,"target_only":true}]}`)
	response, _, err := buildOpenAIGroupModelsCatalog(group, []Account{account}, []Account{account}, nil, official, official, "", true, nil)
	require.NoError(t, err)
	models := decodeCodexManifestModels(t, response.Body)
	require.Len(t, models, 1)
	require.Equal(t, "custom-source", models[0]["slug"])
	require.NotContains(t, models[0], "target_only", "a filter's ignored target cannot grant descriptor fields")
	require.Equal(t, "gpt-6-astra", account.GetMappedModel("custom-source"), "projection does not mutate the account")
}

func TestPassthroughFilterPreservesAdminIntentAcrossBackgroundWrites(t *testing.T) {
	current := &Account{ID: 7, Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
		Extra: map[string]any{"openai_passthrough_model_filter": true}}
	stale := &Account{ID: 7, Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
		Extra: map[string]any{"openai_passthrough_model_filter": false}}
	require.NoError(t, PreserveAccountConfiguration(current, stale, AccountConfigurationIntent{}))
	require.Equal(t, true, stale.Extra["openai_passthrough_model_filter"])
	ctx := withAccountConfigurationIntent(context.Background(), []int64{7}, map[string]any{"openai_passthrough_model_filter": false}, nil)
	require.NoError(t, PreserveAccountConfiguration(current, stale, AccountConfigurationIntentFromContext(ctx, 7)))
	require.Equal(t, false, stale.Extra["openai_passthrough_model_filter"])
	require.Empty(t, AccountConfigurationIntentFromContext(ctx, 8).Extra)
}

func TestPinnedEngineCatalogCacheTracksFirstSuccessfulSync(t *testing.T) {
	svc := &OpenAIGatewayService{}
	group := &Group{ID: 8, Platform: PlatformOpenAI}
	account := groupCatalogAccount(9, map[string]any{"source": "source"})
	account.Extra = map[string]any{"openai_passthrough": true, OpenAIAPIKeyModeExtraKey: "codex_engine"}
	source := catalogFixture(`{"models":[{"slug":"source"},{"slug":"other"}]}`)
	source.upstreamSourceBody = source.Body
	first, err := svc.cachedPinnedOpenAIGroupSource(context.Background(), group, &account, source)
	require.NoError(t, err)
	account.Extra[CodexEngineModelsSyncExtraKey] = map[string]any{"known": true, "model_count": 1, "identity": "source", "observation_id": "observed"}
	filtered, err := svc.cachedPinnedOpenAIGroupSource(context.Background(), group, &account, source)
	require.NoError(t, err)
	// Engine already applies its mapping before the first synchronization. The
	// newly authoritative observation needs its own cache key even when the
	// projected bytes, and therefore the public ETag, remain unchanged.
	require.Equal(t, first.ETag, filtered.ETag)
	require.Len(t, svc.catalogStore().entries, 2)
	require.Contains(t, string(filtered.Body), `"source"`)
	require.NotContains(t, string(filtered.Body), `"other"`)
	account.Extra[CodexEngineModelsSyncExtraKey] = map[string]any{"known": true, "model_count": 0, "identity": "source", "observation_id": "empty"}
	empty, err := svc.cachedPinnedOpenAIGroupSource(context.Background(), group, &account, source)
	require.NoError(t, err)
	require.NotEqual(t, filtered.ETag, empty.ETag)
	require.JSONEq(t, `{"object":"list","data":[]}`, string(empty.Body))
}

func TestFilteredPassthroughSynthesisAllowsMissingCredentials(t *testing.T) {
	account := Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
		Extra: map[string]any{"openai_passthrough": true, "openai_passthrough_model_filter": true}}
	projected := openAIGroupCatalogSynthesisAccounts([]Account{account})
	require.Empty(t, projected[0].GetModelMapping())
	require.Nil(t, account.Credentials)
}
