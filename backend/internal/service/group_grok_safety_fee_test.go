package service

import (
	"context"
	"encoding/json"
	"math"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestResolveGroupGrokSafetyFee(t *testing.T) {
	yes, no := true, false
	fee, replacement, negative, zero := 0.12345678, 0.987654321, -1.0, 0.0
	enabled, value, err := ResolveGroupGrokSafetyFee(PlatformGrok, false, nil, nil)
	require.NoError(t, err)
	require.False(t, enabled)
	require.Nil(t, value)
	enabled, value, err = ResolveGroupGrokSafetyFee(PlatformGrok, true, &fee, &GroupGrokSafetyFeeUpdate{})
	require.NoError(t, err)
	require.True(t, enabled)
	require.Equal(t, fee, *value)
	require.NotSame(t, &fee, value)
	enabled, value, err = ResolveGroupGrokSafetyFee(PlatformGrok, true, &fee, &GroupGrokSafetyFeeUpdate{Fee: &replacement})
	require.NoError(t, err)
	require.True(t, enabled)
	require.Equal(t, 0.98765432, *value)
	enabled, value, err = ResolveGroupGrokSafetyFee(PlatformGrok, true, &fee, &GroupGrokSafetyFeeUpdate{Fee: &negative})
	require.NoError(t, err)
	require.True(t, enabled)
	require.Nil(t, value)
	_, value, err = ResolveGroupGrokSafetyFee(PlatformGrok, true, &fee, &GroupGrokSafetyFeeUpdate{Fee: &zero})
	require.NoError(t, err)
	require.Equal(t, 0.0, *value)
	for _, platform := range []string{PlatformOpenAI, PlatformComposite, PlatformAnthropic} {
		_, _, err = ResolveGroupGrokSafetyFee(platform, false, nil, &GroupGrokSafetyFeeUpdate{Enabled: &yes})
		require.ErrorContains(t, err, "only be enabled for Grok")
		enabled, value, err = ResolveGroupGrokSafetyFee(platform, true, &fee, &GroupGrokSafetyFeeUpdate{Enabled: &no})
		require.NoError(t, err)
		require.False(t, enabled)
		require.Nil(t, value)
	}
	for _, invalid := range []float64{math.NaN(), math.Inf(1), math.Inf(-1), 1e12} {
		_, _, err := ResolveGroupGrokSafetyFee(PlatformGrok, false, &invalid, nil)
		require.ErrorContains(t, err, "finite amount")
	}
}

func TestAPIKeyAuthSnapshotGrokSafetyFeeRoundtripAndBillingOwnership(t *testing.T) {
	fee := 0.12345678
	groupID := int64(50)
	key := &APIKey{ID: 82, UserID: 40, GroupID: &groupID, Key: "sk-grok-safety-cache", Status: StatusActive,
		User:  &User{ID: 40, Status: StatusActive},
		Group: &Group{ID: groupID, Name: "grok", Platform: PlatformGrok, Status: StatusActive, Hydrated: true, GrokSafetyFeeEnabled: true, GrokSafetyFee: &fee},
	}
	svc := &APIKeyService{}
	payload, err := json.Marshal(&APIKeyAuthCacheEntry{Snapshot: svc.snapshotFromAPIKey(context.Background(), key)})
	require.NoError(t, err)
	var cached APIKeyAuthCacheEntry
	require.NoError(t, json.Unmarshal(payload, &cached))
	prepareAPIKeyAuthBillingSnapshot(cached.Snapshot)
	got, used, err := svc.applyAuthCacheEntry(key.Key, &cached)
	require.NoError(t, err)
	require.True(t, used)
	require.True(t, got.Group.GrokSafetyFeeEnabled)
	require.Equal(t, fee, *got.Group.GrokSafetyFee)
	frozen := frozenUsageBillingGroup(got.Group)
	require.NotNil(t, frozen)
	require.NotSame(t, got.Group.GrokSafetyFee, frozen.GrokSafetyFee)
	*got.Group.GrokSafetyFee = 1
	require.Equal(t, fee, *frozen.GrokSafetyFee)
	_, used, err = svc.applyAuthCacheEntry(key.Key, &APIKeyAuthCacheEntry{Snapshot: &APIKeyAuthSnapshot{Version: 29}})
	require.NoError(t, err)
	require.False(t, used)
}
