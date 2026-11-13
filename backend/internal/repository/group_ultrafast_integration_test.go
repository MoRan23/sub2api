//go:build integration

package repository

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func (s *GroupRepoSuite) TestUltrafastDefaultsPartialAndStaleUpdate() {
	configured := &service.Group{Name: "ultrafast-stale", Platform: service.PlatformOpenAI, Status: service.StatusActive, RateMultiplier: 1, SubscriptionType: service.SubscriptionTypeStandard}
	s.Require().NoError(s.repo.Create(s.ctx, configured))
	s.False(configured.ForceOpenAIUltrafast)
	s.False(configured.FreeOpenAIUltrafast)
	stale, err := s.repo.GetByIDLite(s.ctx, configured.ID)
	s.Require().NoError(err)
	yes, no := true, false
	configured.OpenAIServiceTierUpdate = &service.GroupOpenAIServiceTierUpdate{ForceUltrafast: &yes, FreeFast: &yes, FreeUltrafast: &yes}
	s.Require().NoError(s.repo.Update(s.ctx, configured))
	stale.Name = "unrelated-update"
	s.Require().NoError(s.repo.Update(s.ctx, stale))
	s.True(stale.ForceOpenAIUltrafast)
	s.True(stale.FreeOpenAIFast)
	s.True(stale.FreeOpenAIUltrafast)
	s.Nil(stale.OpenAIServiceTierUpdate)
	stale.OpenAIServiceTierUpdate = &service.GroupOpenAIServiceTierUpdate{ForceFast: &yes}
	s.Require().Error(s.repo.Update(s.ctx, stale), "locked current Ultrafast preference must prevent an invalid force combination")
	stale.OpenAIServiceTierUpdate = &service.GroupOpenAIServiceTierUpdate{ForceFast: &yes, ForceUltrafast: &no}
	s.Require().NoError(s.repo.Update(s.ctx, stale))
	s.True(stale.ForceOpenAIFast)
	s.False(stale.ForceOpenAIUltrafast)
	s.True(stale.FreeOpenAIUltrafast)
	stale.Platform = service.PlatformAnthropic
	s.Require().NoError(s.repo.Update(s.ctx, stale))
	stored, err := s.repo.GetByIDLite(s.ctx, configured.ID)
	s.Require().NoError(err)
	s.False(stored.ForceOpenAIFast)
	s.False(stored.ForceOpenAIUltrafast)
	s.False(stored.FreeOpenAIFast)
	s.False(stored.FreeOpenAIUltrafast)
}

func (s *APIKeyRepoSuite) TestUltrafastAuthSelectPreservesTrustedGroupPolicy() {
	user := s.mustCreateUser("ultrafast-auth@test.com")
	group, err := s.client.Group.Create().SetName("ultrafast-auth").SetPlatform(service.PlatformComposite).
		SetForceOpenaiUltrafast(true).SetFreeOpenaiFast(true).SetFreeOpenaiUltrafast(true).Save(s.ctx)
	s.Require().NoError(err)
	key := &service.APIKey{UserID: user.ID, GroupID: &group.ID, Key: "sk-ultrafast-auth", Name: "ultrafast", Status: service.StatusActive}
	s.Require().NoError(s.repo.Create(s.ctx, key))
	got, err := s.repo.GetByKeyForAuth(s.ctx, key.Key)
	s.Require().NoError(err)
	s.Require().NotNil(got.Group)
	s.False(got.Group.ForceOpenAIFast)
	s.True(got.Group.ForceOpenAIUltrafast)
	s.True(got.Group.FreeOpenAIFast)
	s.True(got.Group.FreeOpenAIUltrafast)
}

func TestGroupUltrafastConcurrentForceUpdates(t *testing.T) {
	ctx := context.Background()
	repo := newGroupRepositoryWithSQL(integrationEntClient, integrationDB)
	configured := &service.Group{
		Name:     fmt.Sprintf("ultrafast-concurrent-%d", time.Now().UnixNano()),
		Platform: service.PlatformOpenAI, Status: service.StatusActive,
		RateMultiplier: 1, SubscriptionType: service.SubscriptionTypeStandard,
	}
	require.NoError(t, repo.Create(ctx, configured))
	t.Cleanup(func() { _, _ = integrationDB.ExecContext(ctx, "DELETE FROM groups WHERE id=$1", configured.ID) })
	fast, err := repo.GetByIDLite(ctx, configured.ID)
	require.NoError(t, err)
	ultra, err := repo.GetByIDLite(ctx, configured.ID)
	require.NoError(t, err)
	yes := true
	fast.OpenAIServiceTierUpdate = &service.GroupOpenAIServiceTierUpdate{ForceFast: &yes}
	ultra.OpenAIServiceTierUpdate = &service.GroupOpenAIServiceTierUpdate{ForceUltrafast: &yes}
	start := make(chan struct{})
	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i, snapshot := range []*service.Group{fast, ultra} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs[i] = repo.Update(ctx, snapshot)
		}()
	}
	close(start)
	wg.Wait()
	require.NotEqual(t, errs[0] == nil, errs[1] == nil, "only one incompatible partial update may commit")
	stored, err := repo.GetByIDLite(ctx, configured.ID)
	require.NoError(t, err)
	require.NotEqual(t, stored.ForceOpenAIFast, stored.ForceOpenAIUltrafast)
}
