//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCandidateViewSchedulingSortUsesSidecarAndPreservesSharedAccounts(t *testing.T) {
	older, newer := time.Unix(100, 0), time.Unix(200, 0)
	a := &Account{ID: 1, Priority: 1, LastUsedAt: &older}
	b := &Account{ID: 2, Priority: 1, LastUsedAt: &newer}
	candidates := CandidateSet{NewCandidateView(a, &newer), NewCandidateView(b, &older)}
	sortCandidateViewsByPriorityAndLastUsed(candidates, false)
	require.Equal(t, int64(2), candidates[0].ID)
	require.Same(t, b, candidates[0].Ref())
	require.Same(t, &older, a.LastUsedAt)
	require.Same(t, &newer, b.LastUsedAt)
}

type candidateSelectionCache struct {
	*snapshotHydrationCache
	candidates CandidateSet
}

func (c *candidateSelectionCache) GetCandidateSnapshot(context.Context, SchedulerBucket) (CandidateSet, bool, error) {
	return append(CandidateSet(nil), c.candidates...), true, nil
}

func (c *candidateSelectionCache) GetSnapshot(context.Context, SchedulerBucket) ([]*Account, bool, error) {
	panic("production selection must use candidate views")
}

func TestCandidateViewGeminiEntrypointsUseSidecarAndHydrate(t *testing.T) {
	older, newer := time.Unix(100, 0), time.Unix(200, 0)
	a := &Account{ID: 1, Platform: PlatformGemini, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, LastUsedAt: &older, Credentials: map[string]any{"api_key": "snapshot-1"}}
	b := &Account{ID: 2, Platform: PlatformGemini, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, LastUsedAt: &newer, Credentials: map[string]any{"api_key": "snapshot-2"}}
	hydrated := &Account{ID: 2, Platform: PlatformGemini, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "live-selected"}}
	cache := &candidateSelectionCache{
		snapshotHydrationCache: &snapshotHydrationCache{accounts: map[int64]*Account{2: hydrated}},
		candidates:             CandidateSet{NewCandidateView(a, &newer), NewCandidateView(b, &older)},
	}
	svc := &GeminiMessagesCompatService{schedulerSnapshot: NewSchedulerSnapshotService(cache, nil, nil, nil, nil)}
	selected, err := svc.SelectAccountForModel(context.Background(), nil, "", "gemini-2.5-flash")
	require.NoError(t, err)
	require.Same(t, hydrated, selected)
	selected, err = svc.SelectAccountForModelsEndpoints(context.Background(), nil)
	require.NoError(t, err)
	require.Same(t, hydrated, selected)
	require.Same(t, &older, a.LastUsedAt)
	require.Same(t, &newer, b.LastUsedAt)
}

func TestCandidateViewGeminiPreservesOAuthAndModelsRank(t *testing.T) {
	api := &Account{ID: 1, Platform: PlatformGemini, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Credentials: map[string]any{"api_key": "key"}}
	oauth := &Account{ID: 2, Platform: PlatformGemini, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true}
	views := CandidateSet{NewCandidateView(api, nil), NewCandidateView(oauth, nil)}
	svc := &GeminiMessagesCompatService{}
	require.Same(t, oauth, svc.selectBestGeminiCandidate(context.Background(), views, "", nil, PlatformGemini, false))
	require.Same(t, api, selectBestGeminiModelsCandidate(views))
}

func TestCandidateViewOpenAILegacyUsesFreshHydrationTimestamp(t *testing.T) {
	older, newer := time.Unix(100, 0), time.Unix(200, 0)
	for _, firstFresh := range []*time.Time{&newer, nil} {
		name := "fresh timestamp reverses snapshot order"
		wantID := int64(2)
		if firstFresh == nil {
			name, wantID = "fresh never-used outranks prior timestamp", 1
		}
		t.Run(name, func(t *testing.T) {
			a := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, LastUsedAt: &older}
			b := &Account{ID: 2, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, LastUsedAt: &newer}
			freshA, freshB := *a, *b
			freshA.LastUsedAt, freshB.LastUsedAt = firstFresh, &older
			cache := &snapshotHydrationCache{accounts: map[int64]*Account{1: &freshA, 2: &freshB}}
			svc := &OpenAIGatewayService{schedulerSnapshot: NewSchedulerSnapshotService(cache, nil, nil, nil, nil)}
			selected, _, _ := svc.selectBestCandidate(context.Background(), nil, PlatformOpenAI, CandidateSet{NewCandidateView(a, &older), NewCandidateView(b, &newer)}, "", nil, false, "", false)
			require.NotNil(t, selected)
			require.Equal(t, wantID, selected.ID)
			require.Same(t, cache.accounts[wantID], selected)
			require.Same(t, &older, a.LastUsedAt)
			require.Same(t, &newer, b.LastUsedAt)
		})
	}
}

func TestCandidateViewSchedulingLoadLRUHonorsNilSidecar(t *testing.T) {
	used := time.Unix(100, 0)
	a := &Account{ID: 1, Priority: 1, LastUsedAt: &used}
	b := &Account{ID: 2, Priority: 1}
	loads := []accountWithLoad{
		newCandidateWithLoad(CandidateView{Account: a}, &AccountLoadInfo{}),
		newCandidateWithLoad(NewCandidateView(b, &used), &AccountLoadInfo{}),
	}
	selected := selectByLRU(loads, false)
	require.NotNil(t, selected)
	require.Equal(t, int64(1), selected.account.ID)
	require.Same(t, &used, a.LastUsedAt)
	require.Nil(t, b.LastUsedAt)
}

func TestCandidateViewSchedulingPreservesOAuthTiePreference(t *testing.T) {
	candidates := CandidateSet{
		NewCandidateView(&Account{ID: 1, Priority: 1, Type: AccountTypeAPIKey}, nil),
		NewCandidateView(&Account{ID: 2, Priority: 1, Type: AccountTypeOAuth}, nil),
		NewCandidateView(&Account{ID: 3, Priority: 2, Type: AccountTypeOAuth}, nil),
	}
	sortCandidateViewsByPriorityAndLastUsed(candidates, true)
	require.Equal(t, []int64{2, 1, 3}, []int64{candidates[0].ID, candidates[1].ID, candidates[2].ID})
	sortCandidateViewsForFallback(candidates, true, "random")
	require.Equal(t, 1, candidates[0].Priority)
	require.Equal(t, 1, candidates[1].Priority)
	require.Equal(t, int64(3), candidates[2].ID)
}

func TestCandidateViewSchedulingSubscriptionPartitionKeepsEnginePreferred(t *testing.T) {
	engine := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"base_url": "https://engine.example"}}
	engine.Extra = map[string]any{OpenAIAPIKeyModeExtraKey: "codex_engine"}
	regular := &Account{ID: 2, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	views := CandidateSet{NewCandidateView(regular, nil), NewCandidateView(engine, nil)}
	preferred, fallback := partitionOpenAIChatGPTSubscriptionCandidates(views)
	require.Len(t, preferred, 1)
	require.Same(t, engine, preferred[0].Ref())
	require.Len(t, fallback, 1)
	require.Same(t, regular, fallback[0].Ref())
}
