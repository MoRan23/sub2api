//go:build unit

package service

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type catalogProjectionRepoStub struct {
	AccountRepository
	calls     int
	accounts  []Account
	fullCalls int
}

func (r *catalogProjectionRepoStub) ListCatalogAccountProjections(context.Context, int64) ([]Account, error) {
	r.calls++
	return append([]Account(nil), r.accounts...), nil
}
func (r *catalogProjectionRepoStub) GetByID(context.Context, int64) (*Account, error) {
	r.fullCalls++
	return nil, nil
}

func TestCatalogProjectionReadOnceAndInvalidate(t *testing.T) {
	repo := &catalogProjectionRepoStub{accounts: []Account{{ID: 1, Status: StatusActive, Schedulable: true, Credentials: map[string]any{"model_mapping": map[string]any{"model": "target"}}}}}
	svc := &OpenAIGatewayService{accountRepo: repo}
	first, catalog, err := svc.loadCatalogAccounts(context.Background(), 7)
	require.NoError(t, err)
	require.Len(t, first, 1)
	require.Len(t, catalog, 1)
	second, catalog2, err := svc.loadCatalogAccounts(context.Background(), 7)
	require.NoError(t, err)
	require.Equal(t, StatusActive, second[0].Status)
	require.Empty(t, catalog2[0].Name)
	require.Same(t, &first[0], &second[0])
	require.Same(t, &catalog[0], &catalog2[0])
	require.Equal(t, 1, repo.calls)
	svc.catalogStore().Invalidate()
	_, _, err = svc.loadCatalogAccounts(context.Background(), 7)
	require.NoError(t, err)
	require.Equal(t, 2, repo.calls)
}
func TestGroupCatalogRepresentationReusesJSONAndIsolatesSources(t *testing.T) {
	svc := &OpenAIGatewayService{}
	group := &Group{ID: 7, Platform: PlatformOpenAI}
	accounts := []Account{groupCatalogAccount(1, map[string]any{"gpt-5.6-sol": "gpt-5.6-sol"})}
	official := catalogFixture(`{"models":[{"slug":"gpt-5.6-sol","shell_type":"shell_command","unknown":{"keep":true}}]}`)
	official.sourceIdentity = "os-linux-generation-1"
	first, _, err := svc.cachedOpenAIGroupModelsCatalog(context.Background(), group, accounts, accounts, nil, official, official, "", "client", "projection", true, nil)
	require.NoError(t, err)
	second, _, err := svc.cachedOpenAIGroupModelsCatalog(context.Background(), group, accounts, accounts, nil, official, official, "", "client", "projection", true, nil)
	require.NoError(t, err)
	require.Same(t, &first.Body[0], &second.Body[0])
	require.Equal(t, first.ETag, second.ETag)
	conditional, _, err := svc.cachedOpenAIGroupModelsCatalog(context.Background(), group, accounts, accounts, nil, official, official, first.ETag, "client", "projection", true, nil)
	require.NoError(t, err)
	require.True(t, conditional.NotModified)
	require.Nil(t, conditional.Body)
	require.Len(t, svc.catalogStore().entries, 1)
	official.sourceIdentity = "os-linux-generation-2"
	_, _, err = svc.cachedOpenAIGroupModelsCatalog(context.Background(), group, accounts, accounts, nil, official, official, "", "client", "projection", true, nil)
	require.NoError(t, err)
	require.Len(t, svc.catalogStore().entries, 2)
	official.Body = []byte(`{"models":[{"slug":"gpt-5.6-sol","unknown":{"new":true}}]}`)
	changed, _, err := svc.cachedOpenAIGroupModelsCatalog(context.Background(), group, accounts, accounts, nil, official, official, "", "client", "projection", true, nil)
	require.NoError(t, err)
	require.NotEqual(t, first.ETag, changed.ETag)
}

func TestCatalogSharedProjectionConcurrentReaders(t *testing.T) {
	repo := &catalogProjectionRepoStub{accounts: []Account{groupCatalogAccount(1, map[string]any{"gpt-5.6-sol": "gpt-5.6-sol"})}}
	svc := &OpenAIGatewayService{accountRepo: repo}
	ctx := context.Background()
	visible, accounts, err := svc.loadCatalogAccounts(ctx, 7)
	require.NoError(t, err)
	source := catalogFixture(`{"models":[{"slug":"gpt-5.6-sol","shell_type":"shell_command"}]}`)
	source.ETag = codexModelsManifestBodyETag(source.Body)
	group := &Group{ID: 7, Platform: PlatformOpenAI}
	var wg sync.WaitGroup
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				result, _, err := svc.cachedOpenAIGroupModelsCatalog(ctx, group, visible, accounts, nil, source, source, "", "client", accounts[0].catalogProjectionVersion, true, nil)
				if err != nil || len(result.Body) == 0 {
					t.Errorf("catalog read failed: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()
	require.Len(t, svc.catalogStore().entries, 2)
}

func TestPinnedCatalogSourceCachesProjectionAndTracksMapping(t *testing.T) {
	svc := &OpenAIGatewayService{}
	group := &Group{ID: 8, Platform: PlatformOpenAI}
	account := groupCatalogAccount(9, map[string]any{"public-a": "upstream"})
	source := catalogFixture(`{"models":[{"slug":"upstream","shell_type":"shell_command"}]}`)
	source.upstreamSourceBody = source.Body
	first, err := svc.cachedPinnedOpenAIGroupSource(context.Background(), group, &account, source)
	require.NoError(t, err)
	second, err := svc.cachedPinnedOpenAIGroupSource(context.Background(), group, &account, source)
	require.NoError(t, err)
	require.Same(t, &first.Body[0], &second.Body[0])
	account.Credentials = map[string]any{"model_mapping": map[string]any{"public-b": "upstream"}}
	changed, err := svc.cachedPinnedOpenAIGroupSource(context.Background(), group, &account, source)
	require.NoError(t, err)
	require.NotEqual(t, first.ETag, changed.ETag)
	require.Contains(t, string(changed.Body), "public-b")
	require.NotContains(t, string(changed.Body), "public-a")
}

func TestCatalogSourceViewBorrowsImmutableBytes(t *testing.T) {
	svc := &OpenAIGatewayService{}
	request := openAIModelsRequest{accountID: 1}
	key := buildOpenAIModelsCacheKey(request)
	source := catalogFixture(`{"models":[]}`)
	svc.openAIModelsCache.set(key, source, time.Now())
	cached, _ := svc.openAIModelsCache.get(key, time.Now())
	ctx := context.WithValue(context.Background(), catalogSourceReadOnlyContextKey{}, true)
	view, err := svc.fetchCachedOpenAIModels(ctx, request, nil, "")
	require.NoError(t, err)
	require.Same(t, &cached.Body[0], &view.Body[0])
	ordinary, err := svc.fetchCachedOpenAIModels(context.Background(), request, nil, "")
	require.NoError(t, err)
	require.NotSame(t, &cached.Body[0], &ordinary.Body[0])
}

func TestCatalogSourceHydrationBoundedByDistinctOwner(t *testing.T) {
	for _, shared := range []bool{false, true} {
		repo := &catalogProjectionRepoStub{}
		svc := &OpenAIGatewayService{accountRepo: repo}
		accounts := make([]Account, 100)
		parent := int64(1000)
		for i := range accounts {
			accounts[i] = Account{ID: int64(i + 1), Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true}
			if shared {
				accounts[i].ParentAccountID = &parent
			}
		}
		manifest, _ := svc.fetchGroupOfficialCodexModels(context.Background(), &Group{ID: 7, Platform: PlatformOpenAI}, accounts, "")
		require.Nil(t, manifest)
		if shared {
			require.Equal(t, 1, repo.fullCalls)
		} else {
			require.Equal(t, openAIGroupOfficialCatalogMaxSources, repo.fullCalls)
		}
	}
}
