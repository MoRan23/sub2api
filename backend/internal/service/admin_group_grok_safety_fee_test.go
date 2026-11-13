//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAdminGroupGrokSafetyFeeCreateAndPartialUpdate(t *testing.T) {
	ctx := context.Background()
	fee := 0.12345678
	repo := &groupRepoStubForAdmin{}
	svc := &adminServiceImpl{groupRepo: repo}
	_, err := svc.CreateGroup(ctx, &CreateGroupInput{Name: "invalid", Platform: PlatformComposite, RateMultiplier: 1, GrokSafetyFeeEnabled: true, GrokSafetyFee: &fee})
	require.ErrorContains(t, err, "only be enabled for Grok")
	require.Nil(t, repo.created)
	got, err := svc.CreateGroup(ctx, &CreateGroupInput{Name: "grok", Platform: PlatformGrok, RateMultiplier: 1, GrokSafetyFeeEnabled: true, GrokSafetyFee: &fee})
	require.NoError(t, err)
	require.True(t, got.GrokSafetyFeeEnabled)
	require.Equal(t, fee, *got.GrokSafetyFee)
	got.ID = 1
	repo.getByID = got
	invalidator := &authCacheInvalidatorStub{}
	svc.authCacheInvalidator = invalidator
	got, err = svc.UpdateGroup(ctx, 1, &UpdateGroupInput{Name: "renamed"})
	require.NoError(t, err)
	require.True(t, got.GrokSafetyFeeEnabled)
	require.Equal(t, fee, *got.GrokSafetyFee)
	require.Equal(t, []int64{1}, invalidator.groupIDs)
	negative := -1.0
	got, err = svc.UpdateGroup(ctx, 1, &UpdateGroupInput{GrokSafetyFee: &negative})
	require.NoError(t, err)
	require.True(t, got.GrokSafetyFeeEnabled)
	require.Nil(t, got.GrokSafetyFee)
	no := false
	got, err = svc.UpdateGroup(ctx, 1, &UpdateGroupInput{Platform: PlatformOpenAI, GrokSafetyFeeEnabled: &no})
	require.NoError(t, err)
	require.False(t, got.GrokSafetyFeeEnabled)
}

func TestCloneGroupForDuplicateGrokSafetyFee(t *testing.T) {
	fee := 0.12345678
	source := &Group{Platform: PlatformGrok, GrokSafetyFeeEnabled: true, GrokSafetyFee: &fee, GrokSafetyFeeUpdate: &GroupGrokSafetyFeeUpdate{}}
	got := cloneGroupForDuplicate(source, "")
	require.True(t, got.GrokSafetyFeeEnabled)
	require.Equal(t, fee, *got.GrokSafetyFee)
	require.NotSame(t, source.GrokSafetyFee, got.GrokSafetyFee)
	require.Nil(t, got.GrokSafetyFeeUpdate)
}
