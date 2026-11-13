//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAdminService_CreateGroupUltrafast(t *testing.T) {
	for _, platform := range []string{PlatformOpenAI, PlatformComposite, PlatformAnthropic} {
		t.Run(platform, func(t *testing.T) {
			repo := &groupRepoStubForAdmin{}
			svc := &adminServiceImpl{groupRepo: repo}
			got, err := svc.CreateGroup(context.Background(), &CreateGroupInput{
				Name: "ultrafast", Platform: platform, RateMultiplier: 1,
				ForceOpenAIUltrafast: true, FreeOpenAIFast: true, FreeOpenAIUltrafast: true,
			})
			require.NoError(t, err)
			want := platform != PlatformAnthropic
			require.Equal(t, want, got.ForceOpenAIUltrafast)
			require.Equal(t, want, got.FreeOpenAIUltrafast)
			require.Equal(t, want, got.FreeOpenAIFast)
		})
	}
	repo := &groupRepoStubForAdmin{}
	svc := &adminServiceImpl{groupRepo: repo}
	_, err := svc.CreateGroup(context.Background(), &CreateGroupInput{
		Name: "invalid", Platform: PlatformOpenAI, RateMultiplier: 1,
		ForceOpenAIFast: true, ForceOpenAIUltrafast: true,
	})
	require.ErrorContains(t, err, "cannot both be enabled")
	require.Nil(t, repo.created)
	got, err := svc.CreateGroup(context.Background(), &CreateGroupInput{Name: "default", Platform: PlatformOpenAI, RateMultiplier: 1})
	require.NoError(t, err)
	require.False(t, got.ForceOpenAIUltrafast)
	require.False(t, got.FreeOpenAIUltrafast)
}

func TestAdminService_UpdateGroupUltrafastPartialAndConflict(t *testing.T) {
	yes, no := true, false
	repo := &groupRepoStubForAdmin{getByID: &Group{ID: 1, Name: "ultrafast", Platform: PlatformOpenAI, Status: StatusActive, ForceOpenAIUltrafast: true, FreeOpenAIUltrafast: true}}
	invalidator := &authCacheInvalidatorStub{}
	svc := &adminServiceImpl{groupRepo: repo, authCacheInvalidator: invalidator}
	got, err := svc.UpdateGroup(context.Background(), 1, &UpdateGroupInput{FreeOpenAIFast: &yes})
	require.NoError(t, err)
	require.True(t, got.ForceOpenAIUltrafast)
	require.True(t, got.FreeOpenAIUltrafast)
	require.True(t, got.FreeOpenAIFast)
	require.Equal(t, []int64{1}, invalidator.groupIDs)
	_, err = svc.UpdateGroup(context.Background(), 1, &UpdateGroupInput{ForceOpenAIFast: &yes})
	require.ErrorContains(t, err, "cannot both be enabled")
	// Recreate the fetched row, as the stub deliberately shares its pointer.
	repo.getByID = &Group{ID: 1, Name: "ultrafast", Platform: PlatformOpenAI, Status: StatusActive, ForceOpenAIUltrafast: true, FreeOpenAIUltrafast: true}
	got, err = svc.UpdateGroup(context.Background(), 1, &UpdateGroupInput{ForceOpenAIFast: &yes, ForceOpenAIUltrafast: &no})
	require.NoError(t, err)
	require.True(t, got.ForceOpenAIFast)
	require.False(t, got.ForceOpenAIUltrafast)
	require.True(t, got.FreeOpenAIUltrafast)
	got, err = svc.UpdateGroup(context.Background(), 1, &UpdateGroupInput{Platform: PlatformAnthropic})
	require.NoError(t, err)
	require.False(t, got.ForceOpenAIFast)
	require.False(t, got.ForceOpenAIUltrafast)
	require.False(t, got.FreeOpenAIUltrafast)
}

func TestCloneGroupForDuplicatePreservesUltrafastPreferences(t *testing.T) {
	source := &Group{ForceOpenAIUltrafast: true, FreeOpenAIFast: true, FreeOpenAIUltrafast: true}
	got := cloneGroupForDuplicate(source, "")
	require.True(t, got.ForceOpenAIUltrafast)
	require.True(t, got.FreeOpenAIFast)
	require.True(t, got.FreeOpenAIUltrafast)
	require.Nil(t, got.OpenAIServiceTierUpdate)
}
