package service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	gocache "github.com/patrickmn/go-cache"
	"github.com/stretchr/testify/require"
)

func emptySyncedEngineCatalogAccount() Account {
	a := groupCatalogAccount(17, map[string]any{})
	a.Extra = map[string]any{OpenAIAPIKeyModeExtraKey: "codex_engine", CodexEngineModelsSyncExtraKey: map[string]any{"known": true, "model_count": 0, "identity": "source", "observation_id": "observed"}}
	return a
}

func TestEngineSyncedEmptyCatalogProjection(t *testing.T) {
	account := emptySyncedEngineCatalogAccount()
	group := &Group{ID: 1, Platform: PlatformOpenAI}
	official := catalogFixture(`{"models":[{"slug":"gpt-6-astra"}]}`)
	for _, codex := range []bool{false, true} {
		response, _, err := buildOpenAIGroupModelsCatalog(group, []Account{account}, []Account{account}, nil, official, official, "", codex, nil)
		require.NoError(t, err)
		field := "data"
		if codex {
			field = "models"
		}
		_, entries, err := modelCatalogEntries(response.Body, field)
		require.NoError(t, err)
		require.Empty(t, entries)
	}
	for _, fixture := range []struct {
		body  string
		codex bool
		field string
	}{
		{`{"data":[{"id":"gpt-6-astra"}],"extension":true}`, false, "data"},
		{`{"models":[{"slug":"gpt-6-astra"}],"extension":true}`, true, "models"},
	} {
		projected, err := projectAccountModelsBody([]byte(fixture.body), &account, group, fixture.codex)
		require.NoError(t, err)
		envelope, entries, err := modelCatalogEntries(projected, fixture.field)
		require.NoError(t, err)
		require.Empty(t, entries)
		require.Equal(t, json.RawMessage(`true`), envelope["extension"])
	}
	require.Equal(t, []string{"only-this"}, supplementUnmappedOpenAIModels([]Account{account}, []string{"only-this"}))
	require.False(t, explicitModelMappingClaims(account, "gpt-6-astra"))
}

func TestEngineCatalogInvalidationRejectsLateFill(t *testing.T) {
	gateway := &GatewayService{modelsListCache: gocache.New(time.Minute, time.Minute), modelsListCacheTTL: time.Minute}
	id := int64(3)
	key := modelsListCacheKey(&id, PlatformOpenAI)
	compositeKey := compositeModelOwnershipCacheKey(id, "old-model")
	rev := gateway.modelCatalogRevision()
	gateway.storeModelCatalog(key, []string{"old-model"}, rev)
	gateway.storeModelCatalog(compositeKey, CompositeModelOwnership{Matched: true}, rev)
	snapshot := &SchedulerSnapshotService{}
	snapshot.SetModelCatalogInvalidator(gateway.InvalidateAvailableModelsCache)
	snapshot.invalidateModelCatalogGroups([]int64{id}, PlatformOpenAI)
	gateway.storeModelCatalog(key, []string{"late-old-model"}, rev)
	_, exists := gateway.modelsListCache.Get(key)
	require.False(t, exists)
	_, exists = gateway.modelsListCache.Get(compositeKey)
	require.False(t, exists)
	gateway.storeModelCatalog(key, []string{"new-model"}, gateway.modelCatalogRevision())
	got, _ := gateway.modelsListCache.Get(key)
	require.Equal(t, []string{"new-model"}, got)
}

func TestEngineSyncedEmptyAvailableModelsDoesNotUseDefaults(t *testing.T) {
	id := int64(3)
	repo := &modelsListAccountRepoStub{byGroup: map[int64][]Account{id: {emptySyncedEngineCatalogAccount()}}}
	gateway := &GatewayService{accountRepo: repo, modelsListCache: gocache.New(time.Minute, time.Minute), modelsListCacheTTL: time.Minute}
	for range 2 {
		models := gateway.GetAvailableModels(context.Background(), &id, PlatformOpenAI)
		require.NotNil(t, models)
		require.Empty(t, models)
	}
	require.EqualValues(t, 1, repo.listByGroupCalls.Load())
	repo.byGroup[id] = []Account{groupCatalogAccount(17, map[string]any{"6 Cyber": "6 Cyber"})}
	gateway.InvalidateAvailableModelsCache(&id, PlatformOpenAI)
	require.Equal(t, []string{"6 Cyber"}, gateway.GetAvailableModels(context.Background(), &id, PlatformOpenAI))
}

type engineCatalogRevisionCacheStub struct {
	SchedulerCache
	revision string
	err      error
}

func (c *engineCatalogRevisionCacheStub) OpenAIModelCatalogRevision(context.Context) (string, error) {
	return c.revision, c.err
}

func TestEngineCatalogCrossInstanceRevisionInvalidation(t *testing.T) {
	ctx := context.Background()
	cache := &engineCatalogRevisionCacheStub{revision: "global:instance-a"}
	gateway := &GatewayService{modelsListCache: gocache.New(time.Minute, time.Minute), modelsListCacheTTL: time.Minute}
	snapshot := &SchedulerSnapshotService{cache: cache}
	snapshot.SetModelCatalogInvalidator(gateway.InvalidateAvailableModelsCache)
	id := int64(3)
	key := modelsListCacheKey(&id, PlatformOpenAI)
	compositeKey := compositeModelOwnershipCacheKey(id, "old-model")
	snapshot.refreshModelCatalogRevision(ctx)
	initial := gateway.modelCatalogRevision()
	gateway.storeModelCatalog(key, []string{"old-model"}, initial)
	gateway.storeModelCatalog(compositeKey, CompositeModelOwnership{Matched: true}, initial)
	snapshot.refreshModelCatalogRevision(ctx)
	require.Equal(t, initial, gateway.modelCatalogRevision(), "unchanged metadata must not churn caches")
	_, exists := gateway.modelsListCache.Get(key)
	require.True(t, exists)

	cache.revision = "global:instance-b"
	cache.err = errors.New("Redis unavailable")
	snapshot.refreshModelCatalogRevision(ctx)
	require.Equal(t, initial, gateway.modelCatalogRevision(), "failed reads retain the last successful revision")
	_, exists = gateway.modelsListCache.Get(key)
	require.True(t, exists)
	require.Equal(t, "global:instance-a", *snapshot.modelCatalogRevision.Load())

	cache.err = nil
	snapshot.refreshModelCatalogRevision(ctx)
	require.Greater(t, gateway.modelCatalogRevision(), initial)
	_, exists = gateway.modelsListCache.Get(key)
	require.False(t, exists, "a revision published by another instance invalidates local listings")
	_, exists = gateway.modelsListCache.Get(compositeKey)
	require.False(t, exists, "composite ownership follows the same membership change")
	next := gateway.modelCatalogRevision()
	gateway.storeModelCatalog(key, []string{"new-model"}, next)
	snapshot.refreshModelCatalogRevision(ctx)
	require.Equal(t, next, gateway.modelCatalogRevision())
	got, exists := gateway.modelsListCache.Get(key)
	require.True(t, exists)
	require.Equal(t, []string{"new-model"}, got)
}
