package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAPIKeyAuthSnapshotV24LocalFieldsAndModelAllowlistRoundtrip(t *testing.T) {
	for _, limit := range []*float64{nil, new(float64), new(125.5)} {
		apiKey := profitAuthTestAPIKey()
		apiKey.User.Balance = 1.25
		apiKey.User.GiftBalance = 3.75
		apiKey.Group.SubscriptionType = SubscriptionTypeTotalQuota
		apiKey.Group.TotalLimitUSD = limit
		apiKey.Group.ModelAllowlist = GroupModelAllowlist{Enabled: true, Models: []string{"gpt-6-astra"}}
		apiKey.Group.ForceOpenAIFast = true
		apiKey.Group.FreeOpenAIFast = true
		apiKey.Group.MaxReasoningEffortOverLimit = ReasoningEffortOverLimitDeny
		svc := &APIKeyService{}
		snapshot := svc.snapshotFromAPIKey(context.Background(), apiKey)
		require.Equal(t, 24, snapshot.Version)

		payload, err := json.Marshal(&APIKeyAuthCacheEntry{Snapshot: snapshot})
		require.NoError(t, err)
		var entry APIKeyAuthCacheEntry
		require.NoError(t, json.Unmarshal(payload, &entry))
		restored, used, err := svc.applyAuthCacheEntry("local-fields-roundtrip", &entry)
		require.NoError(t, err)
		require.True(t, used)
		require.Equal(t, 1.25, restored.User.Balance)
		require.Equal(t, 3.75, restored.User.GiftBalance)
		require.Equal(t, limit, restored.Group.TotalLimitUSD)
		require.Equal(t, SubscriptionTypeTotalQuota, restored.Group.SubscriptionType)
		require.Equal(t, apiKey.Group.ModelAllowlist, restored.Group.ModelAllowlist)
		require.True(t, restored.Group.ForceOpenAIFast)
		require.True(t, restored.Group.FreeOpenAIFast)
		require.Equal(t, ReasoningEffortOverLimitDeny, restored.Group.MaxReasoningEffortOverLimit)
	}
}

func TestAPIKeyAuthSnapshotRejectsV23BeforeModelAllowlist(t *testing.T) {
	svc := &APIKeyService{}
	snapshot := svc.snapshotFromAPIKey(context.Background(), profitAuthTestAPIKey())
	snapshot.Version = 23
	restored, used, err := svc.applyAuthCacheEntry("old-fields", &APIKeyAuthCacheEntry{Snapshot: snapshot})
	require.NoError(t, err)
	require.False(t, used)
	require.Nil(t, restored)
}
