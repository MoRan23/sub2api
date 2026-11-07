package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAPIKeyAuthSnapshotV28LocalFieldsAndModelAllowlistRoundtrip(t *testing.T) {
	for _, limit := range []*float64{nil, new(float64), new(125.5)} {
		apiKey := profitAuthTestAPIKey()
		apiKey.User.Balance = 1.25
		apiKey.User.GiftBalance = 3.75
		apiKey.Group.SubscriptionType = SubscriptionTypeTotalQuota
		apiKey.Group.TotalLimitUSD = limit
		apiKey.Group.ModelAllowlist = GroupModelAllowlist{Enabled: true, Models: []string{"gpt-6-astra"}}
		apiKey.Group.ForceOpenAIFast = true
		apiKey.Group.FreeOpenAIFast = true
		apiKey.Group.OpenAIDaybreakBlueEnabled = true
		apiKey.Group.OpenAIDaybreakRedEnabled = true
		apiKey.Group.ImageCountBilling = true
		apiKey.Group.RequirePrivacySet = true
		apiKey.Group.MaxReasoningEffortOverLimit = ReasoningEffortOverLimitDeny
		svc := &APIKeyService{}
		snapshot := svc.snapshotFromAPIKey(context.Background(), apiKey)
		require.Equal(t, 28, snapshot.Version)
		// The merged schema scopes a cached override to its original group.
		snapshot.User.UserGroupRPMOverride = new(17)
		snapshot.User.UserGroupRPMOverrideGroupID = apiKey.Group.ID

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
		require.True(t, restored.Group.OpenAIDaybreakBlueEnabled)
		require.True(t, restored.Group.OpenAIDaybreakRedEnabled)
		require.True(t, restored.Group.ImageCountBilling)
		require.True(t, restored.Group.RequirePrivacySet)
		require.Equal(t, new(17), restored.User.UserGroupRPMOverride)
		require.Equal(t, apiKey.Group.ID, restored.User.UserGroupRPMOverrideGroupID)
		require.Equal(t, ReasoningEffortOverLimitDeny, restored.Group.MaxReasoningEffortOverLimit)
	}
}

func TestAPIKeyAuthSnapshotRejectsPreV28MergeSchemas(t *testing.T) {
	for _, version := range []int{26, 27} {
		svc := &APIKeyService{}
		snapshot := svc.snapshotFromAPIKey(context.Background(), profitAuthTestAPIKey())
		snapshot.Version = version
		restored, used, err := svc.applyAuthCacheEntry("pre-v28-merge", &APIKeyAuthCacheEntry{Snapshot: snapshot})
		require.NoError(t, err)
		require.False(t, used)
		require.Nil(t, restored)
	}
}

func TestAPIKeyAuthSnapshotRejectsV25BranchSpecificSchemas(t *testing.T) {
	svc := &APIKeyService{}
	snapshot := svc.snapshotFromAPIKey(context.Background(), profitAuthTestAPIKey())
	snapshot.Version = 25
	restored, used, err := svc.applyAuthCacheEntry("pre-merge-fields", &APIKeyAuthCacheEntry{Snapshot: snapshot})
	require.NoError(t, err)
	require.False(t, used)
	require.Nil(t, restored)
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
