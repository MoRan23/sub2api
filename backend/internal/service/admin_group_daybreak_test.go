//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAdminGroupDaybreakCreateAndPartialUpdate(t *testing.T) {
	for _, platform := range []string{PlatformOpenAI, PlatformComposite, PlatformAnthropic} {
		t.Run(platform, func(t *testing.T) {
			repo := &groupRepoStubForAdmin{}
			svc := &adminServiceImpl{groupRepo: repo}
			group, err := svc.CreateGroup(context.Background(), &CreateGroupInput{Name: "daybreak", Platform: platform, RateMultiplier: 1})
			require.NoError(t, err)
			require.False(t, group.OpenAIDaybreakBlueEnabled)
			require.False(t, group.OpenAIDaybreakRedEnabled)
			group.ID = 1
			repo.getByID = group
			yes, no := true, false
			group, err = svc.UpdateGroup(context.Background(), 1, &UpdateGroupInput{OpenAIDaybreakBlueEnabled: &yes, OpenAIDaybreakRedEnabled: &yes})
			require.NoError(t, err)
			want := platform != PlatformAnthropic
			require.Equal(t, want, group.OpenAIDaybreakBlueEnabled)
			require.Equal(t, want, group.OpenAIDaybreakRedEnabled)
			group, err = svc.UpdateGroup(context.Background(), 1, &UpdateGroupInput{Name: "renamed"})
			require.NoError(t, err)
			require.Equal(t, want, group.OpenAIDaybreakRedEnabled)
			require.Nil(t, group.OpenAIDaybreakUpdate.Blue)
			require.Nil(t, group.OpenAIDaybreakUpdate.Red)
			group, err = svc.UpdateGroup(context.Background(), 1, &UpdateGroupInput{OpenAIDaybreakBlueEnabled: &no})
			require.NoError(t, err)
			require.False(t, group.OpenAIDaybreakBlueEnabled)
			require.False(t, group.OpenAIDaybreakRedEnabled)
			if want {
				_, err = svc.UpdateGroup(context.Background(), 1, &UpdateGroupInput{OpenAIDaybreakRedEnabled: &yes})
				require.Error(t, err)
			}
		})
	}
}

func TestAdminGroupDaybreakPlatformChangeClearsPreferences(t *testing.T) {
	repo := &groupRepoStubForAdmin{getByID: &Group{ID: 1, Platform: PlatformOpenAI, OpenAIDaybreakBlueEnabled: true, OpenAIDaybreakRedEnabled: true}}
	svc := &adminServiceImpl{groupRepo: repo}
	got, err := svc.UpdateGroup(context.Background(), 1, &UpdateGroupInput{Platform: PlatformAnthropic})
	require.NoError(t, err)
	require.False(t, got.OpenAIDaybreakBlueEnabled)
	require.False(t, got.OpenAIDaybreakRedEnabled)
}

func TestAdminGroupDaybreakInvalidatesAuthCache(t *testing.T) {
	repo := &groupRepoStubForAdmin{getByID: &Group{ID: 12, Platform: PlatformOpenAI}}
	invalidator := &authCacheInvalidatorStub{}
	svc := &adminServiceImpl{groupRepo: repo, authCacheInvalidator: invalidator}
	yes := true
	_, err := svc.UpdateGroup(context.Background(), 12, &UpdateGroupInput{OpenAIDaybreakBlueEnabled: &yes})
	require.NoError(t, err)
	require.Equal(t, []int64{12}, invalidator.groupIDs)
}
