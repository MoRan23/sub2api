//go:build integration

package repository

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/ent/group"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func (s *GroupRepoSuite) TestDaybreakDefaultPersistenceAndStaleUpdate() {
	accountGroup := &service.Group{Name: "daybreak-stale", Platform: service.PlatformOpenAI, Status: service.StatusActive, RateMultiplier: 1, SubscriptionType: service.SubscriptionTypeStandard}
	s.Require().NoError(s.repo.Create(s.ctx, accountGroup))
	s.False(accountGroup.OpenAIDaybreakBlueEnabled)
	s.False(accountGroup.OpenAIDaybreakRedEnabled)
	stale, err := s.repo.GetByIDLite(s.ctx, accountGroup.ID)
	s.Require().NoError(err)
	yes, no := true, false
	accountGroup.OpenAIDaybreakUpdate = &service.GroupDaybreakUpdate{Blue: &yes, Red: &yes}
	s.Require().NoError(s.repo.Update(s.ctx, accountGroup))
	stale.Name = "unrelated-update"
	s.Require().NoError(s.repo.Update(s.ctx, stale))
	s.True(stale.OpenAIDaybreakBlueEnabled)
	s.True(stale.OpenAIDaybreakRedEnabled)
	s.Nil(stale.OpenAIDaybreakUpdate)
	accountGroup.OpenAIDaybreakUpdate = &service.GroupDaybreakUpdate{Blue: &no}
	s.Require().NoError(s.repo.Update(s.ctx, accountGroup))
	stale.OpenAIDaybreakUpdate = &service.GroupDaybreakUpdate{Red: &yes}
	s.Require().Error(s.repo.Update(s.ctx, stale), "stale Blue evidence must not permit Red after another update disabled Blue")
	stored, err := s.repo.GetByIDLite(s.ctx, accountGroup.ID)
	s.Require().NoError(err)
	s.False(stored.OpenAIDaybreakBlueEnabled)
	s.False(stored.OpenAIDaybreakRedEnabled)
}

func (s *GroupRepoSuite) TestDaybreakPlatformChangeAndDatabaseDefault() {
	created, err := s.tx.Client().Group.Create().SetName("daybreak-db-default").SetPlatform(service.PlatformOpenAI).Save(s.ctx)
	s.Require().NoError(err)
	s.False(created.OpenaiDaybreakBlueEnabled)
	s.False(created.OpenaiDaybreakRedEnabled)
	yes := true
	accountGroup, err := s.repo.GetByIDLite(s.ctx, created.ID)
	s.Require().NoError(err)
	accountGroup.OpenAIDaybreakUpdate = &service.GroupDaybreakUpdate{Blue: &yes, Red: &yes}
	s.Require().NoError(s.repo.Update(s.ctx, accountGroup))
	accountGroup.Platform = service.PlatformAnthropic
	s.Require().NoError(s.repo.Update(s.ctx, accountGroup))
	stored, err := s.tx.Client().Group.Query().Where(group.IDEQ(created.ID)).Only(s.ctx)
	s.Require().NoError(err)
	s.False(stored.OpenaiDaybreakBlueEnabled)
	s.False(stored.OpenaiDaybreakRedEnabled)
}

func (s *APIKeyRepoSuite) TestDaybreakAuthSelectPreservesTrustedGroupPolicy() {
	user := s.mustCreateUser("daybreak-auth@test.com")
	group, err := s.client.Group.Create().SetName("daybreak-auth").SetPlatform(service.PlatformComposite).
		SetOpenaiDaybreakBlueEnabled(true).SetOpenaiDaybreakRedEnabled(true).Save(s.ctx)
	s.Require().NoError(err)
	key := &service.APIKey{UserID: user.ID, GroupID: &group.ID, Key: "sk-daybreak-auth", Name: "daybreak", Status: service.StatusActive}
	s.Require().NoError(s.repo.Create(s.ctx, key))
	got, err := s.repo.GetByKeyForAuth(s.ctx, key.Key)
	s.Require().NoError(err)
	s.Require().NotNil(got.Group)
	s.True(got.Group.Hydrated)
	s.True(got.Group.OpenAIDaybreakBlueEnabled)
	s.True(got.Group.OpenAIDaybreakRedEnabled)
}

func TestGroupDaybreakOwnedTransactionRollback(t *testing.T) {
	ctx := context.Background()
	repo := newGroupRepositoryWithSQL(integrationEntClient, integrationDB)
	name := fmt.Sprintf("daybreak-rollback-%d", time.Now().UnixNano())
	first := &service.Group{Name: name, Platform: service.PlatformOpenAI, Status: service.StatusActive, RateMultiplier: 1, SubscriptionType: service.SubscriptionTypeStandard}
	second := &service.Group{Name: name + "-second", Platform: service.PlatformOpenAI, Status: service.StatusActive, RateMultiplier: 1, SubscriptionType: service.SubscriptionTypeStandard}
	require.NoError(t, repo.Create(ctx, first))
	t.Cleanup(func() { _, _ = integrationDB.ExecContext(ctx, "DELETE FROM groups WHERE id=$1", first.ID) })
	require.NoError(t, repo.Create(ctx, second))
	t.Cleanup(func() { _, _ = integrationDB.ExecContext(ctx, "DELETE FROM groups WHERE id=$1", second.ID) })
	yes := true
	second.Name = first.Name
	second.OpenAIDaybreakUpdate = &service.GroupDaybreakUpdate{Blue: &yes, Red: &yes}
	require.Error(t, repo.Update(ctx, second), "duplicate group name must roll back the preference change")
	stored, err := repo.GetByIDLite(ctx, second.ID)
	require.NoError(t, err)
	require.False(t, stored.OpenAIDaybreakBlueEnabled)
	require.False(t, stored.OpenAIDaybreakRedEnabled)
}
