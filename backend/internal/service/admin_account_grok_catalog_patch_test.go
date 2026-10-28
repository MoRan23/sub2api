package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAdminAccountExtraPatchesKeepGrokCatalogManaged(t *testing.T) {
	freshModels := map[string]any{"models": []string{"fresh-model"}, "fetched_at": "2026-10-25T12:00:00Z"}
	freshMetadata := map[string]any{"models": map[string]any{"fresh-model": map[string]any{"id": "fresh-model"}}}
	staleModels := map[string]any{"models": []string{"stale-model"}, "fetched_at": "2026-10-24T12:00:00Z"}
	staleMetadata := map[string]any{"models": map[string]any{"stale-model": map[string]any{"id": "stale-model"}}}

	cases := []struct {
		name          string
		storedModels  any
		storedMeta    any
		incomingModel any
		incomingMeta  any
	}{
		{"stale snapshot", freshModels, freshMetadata, staleModels, staleMetadata},
		{"explicit null", freshModels, freshMetadata, nil, nil},
		{"injected catalog", nil, nil, staleModels, staleMetadata},
	}
	for _, mode := range []string{"extra", "bulk"} {
		t.Run(mode, func(t *testing.T) {
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					const accountID = int64(25)
					stored := map[string]any{"note": "keep"}
					if tc.storedModels != nil {
						stored[grokObservedModelsExtraKey] = tc.storedModels
					}
					if tc.storedMeta != nil {
						stored[UpstreamModelMetadataExtraKey] = tc.storedMeta
					}
					repo := &upstreamBillingProbeAccountRepo{accounts: map[int64]*Account{
						accountID: {ID: accountID, Name: "before", Platform: PlatformGrok, Type: AccountTypeOAuth, Extra: stored},
					}}
					input := map[string]any{
						grokObservedModelsExtraKey:       tc.incomingModel,
						UpstreamModelMetadataExtraKey:    tc.incomingMeta,
						"grok_client_tool_cache_enabled": true,
					}
					svc := &adminServiceImpl{accountRepo: repo}
					var persistedPatch map[string]any
					if mode == "extra" {
						require.NoError(t, svc.UpdateAccountExtra(context.Background(), accountID, input))
						require.Len(t, repo.updates[accountID], 1)
						persistedPatch = repo.updates[accountID][0]
						require.Equal(t, "keep", repo.accounts[accountID].Extra["note"])
						if tc.storedModels == nil {
							require.NotContains(t, repo.accounts[accountID].Extra, grokObservedModelsExtraKey)
							require.NotContains(t, repo.accounts[accountID].Extra, UpstreamModelMetadataExtraKey)
						} else {
							require.Equal(t, tc.storedModels, repo.accounts[accountID].Extra[grokObservedModelsExtraKey])
							require.Equal(t, tc.storedMeta, repo.accounts[accountID].Extra[UpstreamModelMetadataExtraKey])
						}
					} else {
						result, err := svc.BulkUpdateAccounts(context.Background(), &BulkUpdateAccountsInput{
							AccountIDs: []int64{accountID}, Name: "after", Extra: input,
						})
						require.NoError(t, err)
						require.Equal(t, []int64{accountID}, result.SuccessIDs)
						require.Len(t, repo.bulkUpdates, 1)
						persistedPatch = repo.bulkUpdates[0].Extra
						require.NotNil(t, repo.bulkUpdates[0].Name)
						require.Equal(t, "after", *repo.bulkUpdates[0].Name)
					}
					// Both repository methods merge only the supplied keys, so managed
					// snapshots must never reach persistence through either admin path.
					require.NotContains(t, persistedPatch, grokObservedModelsExtraKey)
					require.NotContains(t, persistedPatch, UpstreamModelMetadataExtraKey)
					require.Equal(t, true, persistedPatch["grok_client_tool_cache_enabled"])
				})
			}
		})
	}
}

func TestUpdateAccountExtraSkipsManagedOnlyGrokCatalogPatch(t *testing.T) {
	repo := &upstreamBillingProbeAccountRepo{}
	err := (&adminServiceImpl{accountRepo: repo}).UpdateAccountExtra(context.Background(), 25, map[string]any{
		grokObservedModelsExtraKey:    map[string]any{"models": []string{"injected-model"}},
		UpstreamModelMetadataExtraKey: nil,
	})
	require.NoError(t, err)
	require.Empty(t, repo.updates)
}
