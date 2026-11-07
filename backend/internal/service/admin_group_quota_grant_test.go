//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAdminGroupTotalQuotaModeCreation(t *testing.T) {
	for _, permanent := range []bool{false, true} {
		repo := &groupRepoStubForAdmin{createID: 1}
		svc := &adminServiceImpl{groupRepo: repo}
		group, err := svc.CreateGroup(context.Background(), &CreateGroupInput{
			Name: "quota", Platform: PlatformOpenAI, RateMultiplier: 1,
			SubscriptionType: SubscriptionTypeTotalQuota, TotalQuotaNoExpiry: permanent,
			TotalLimitUSD: new(99.0), // Deprecated clients must not establish a grant amount.
		})
		require.NoError(t, err)
		require.Equal(t, permanent, group.TotalQuotaNoExpiry)
		require.Nil(t, group.TotalLimitUSD)
	}
	for _, kind := range []string{SubscriptionTypeStandard, SubscriptionTypeSubscription} {
		repo := &groupRepoStubForAdmin{}
		svc := &adminServiceImpl{groupRepo: repo}
		_, err := svc.CreateGroup(context.Background(), &CreateGroupInput{
			Name: "invalid", Platform: PlatformOpenAI, RateMultiplier: 1,
			SubscriptionType: kind, TotalQuotaNoExpiry: true,
		})
		require.ErrorContains(t, err, "permanent quota")
		require.Nil(t, repo.created)
	}
}

func TestAdminGroupTotalQuotaModeImmutable(t *testing.T) {
	for _, permanent := range []bool{false, true} {
		repo := &groupRepoStubForAdmin{getByID: &Group{ID: 1, Platform: PlatformOpenAI,
			SubscriptionType: SubscriptionTypeTotalQuota, TotalQuotaNoExpiry: permanent}}
		svc := &adminServiceImpl{groupRepo: repo}
		_, err := svc.UpdateGroup(context.Background(), 1, &UpdateGroupInput{TotalQuotaNoExpiry: new(!permanent)})
		require.ErrorContains(t, err, "only be selected when creating")
		require.Nil(t, repo.updated)
	}
	for _, kind := range []string{SubscriptionTypeStandard, SubscriptionTypeSubscription} {
		repo := &groupRepoStubForAdmin{getByID: &Group{ID: 1, Platform: PlatformOpenAI,
			SubscriptionType: SubscriptionTypeTotalQuota, TotalQuotaNoExpiry: true}}
		svc := &adminServiceImpl{groupRepo: repo}
		_, err := svc.UpdateGroup(context.Background(), 1, &UpdateGroupInput{SubscriptionType: kind})
		require.ErrorContains(t, err, "cannot change billing type")
		require.Nil(t, repo.updated)
	}
}

func TestAdminGroupTotalQuotaUnchangedModeAllowsNormalEdits(t *testing.T) {
	repo := &groupRepoStubForAdmin{getByID: &Group{ID: 1, Name: "old", Platform: PlatformOpenAI,
		SubscriptionType: SubscriptionTypeTotalQuota, TotalQuotaNoExpiry: true, TotalLimitUSD: new(12.0)}}
	svc := &adminServiceImpl{groupRepo: repo}
	group, err := svc.UpdateGroup(context.Background(), 1, &UpdateGroupInput{
		Name: "renamed", TotalQuotaNoExpiry: new(true), TotalLimitUSD: new(88.0),
	})
	require.NoError(t, err)
	require.Equal(t, "renamed", group.Name)
	require.True(t, group.TotalQuotaNoExpiry)
	require.Equal(t, 12.0, *group.TotalLimitUSD)
}

func TestDefaultSubscriptionGroupsRejectTotalQuotaGrants(t *testing.T) {
	repo := &groupRepoStubForAdmin{getByIDByID: map[int64]*Group{
		1: {ID: 1, SubscriptionType: SubscriptionTypeSubscription},
		2: {ID: 2, SubscriptionType: SubscriptionTypeTotalQuota},
	}}
	svc := &SettingService{defaultSubGroupReader: repo}
	require.NoError(t, svc.validateDefaultSubscriptionGroups(context.Background(), []DefaultSubscriptionSetting{{GroupID: 1, ValidityDays: 30}}))
	require.ErrorIs(t, svc.validateDefaultSubscriptionGroups(context.Background(), []DefaultSubscriptionSetting{{GroupID: 2, ValidityDays: 30}}), ErrDefaultSubGroupInvalid)
}
