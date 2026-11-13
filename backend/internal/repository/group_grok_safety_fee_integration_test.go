//go:build integration

package repository

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	dbmigrations "github.com/Wei-Shaw/sub2api/migrations"
	"github.com/stretchr/testify/require"
)

func (s *GroupRepoSuite) TestGrokSafetyFeeDefaultsPartialAndStaleUpdate() {
	configured := &service.Group{Name: "grok-safety-fee-stale", Platform: service.PlatformGrok, Status: service.StatusActive, RateMultiplier: 1, SubscriptionType: service.SubscriptionTypeStandard}
	s.Require().NoError(s.repo.Create(s.ctx, configured))
	s.False(configured.GrokSafetyFeeEnabled)
	s.Nil(configured.GrokSafetyFee)
	stale, err := s.repo.GetByIDLite(s.ctx, configured.ID)
	s.Require().NoError(err)
	yes, no, fee := true, false, 0.12345678
	configured.GrokSafetyFeeUpdate = &service.GroupGrokSafetyFeeUpdate{Enabled: &yes, Fee: &fee}
	s.Require().NoError(s.repo.Update(s.ctx, configured))
	stale.Name = "unrelated-grok-update"
	s.Require().NoError(s.repo.Update(s.ctx, stale))
	s.True(stale.GrokSafetyFeeEnabled)
	s.Equal(fee, *stale.GrokSafetyFee)
	s.Nil(stale.GrokSafetyFeeUpdate)
	stale.Platform = service.PlatformComposite
	s.Require().Error(s.repo.Update(s.ctx, stale))
	stale.GrokSafetyFeeUpdate = &service.GroupGrokSafetyFeeUpdate{Enabled: &no}
	s.Require().NoError(s.repo.Update(s.ctx, stale))
	stored, err := s.repo.GetByIDLite(s.ctx, configured.ID)
	s.Require().NoError(err)
	s.False(stored.GrokSafetyFeeEnabled)
	s.Nil(stored.GrokSafetyFee)
}

func (s *APIKeyRepoSuite) TestGrokSafetyFeeAuthSelect() {
	user := s.mustCreateUser("grok-safety-auth@test.com")
	fee := 0.12345678
	group, err := s.client.Group.Create().SetName("grok-safety-auth").SetPlatform(service.PlatformGrok).
		SetGrokSafetyFeeEnabled(true).SetGrokSafetyFee(fee).Save(s.ctx)
	s.Require().NoError(err)
	key := &service.APIKey{UserID: user.ID, GroupID: &group.ID, Key: "sk-grok-safety-auth", Name: "grok-safety", Status: service.StatusActive}
	s.Require().NoError(s.repo.Create(s.ctx, key))
	got, err := s.repo.GetByKeyForAuth(s.ctx, key.Key)
	s.Require().NoError(err)
	s.Require().NotNil(got.Group)
	s.True(got.Group.GrokSafetyFeeEnabled)
	s.Require().NotNil(got.Group.GrokSafetyFee)
	s.Equal(fee, *got.Group.GrokSafetyFee)
}

func TestGroupGrokSafetyFeeConcurrentPartialUpdates(t *testing.T) {
	ctx := context.Background()
	repo := newGroupRepositoryWithSQL(integrationEntClient, integrationDB)
	configured := &service.Group{Name: fmt.Sprintf("grok-safety-concurrent-%d", time.Now().UnixNano()), Platform: service.PlatformGrok, Status: service.StatusActive, RateMultiplier: 1, SubscriptionType: service.SubscriptionTypeStandard}
	require.NoError(t, repo.Create(ctx, configured))
	t.Cleanup(func() { _, _ = integrationDB.ExecContext(ctx, "DELETE FROM groups WHERE id=$1", configured.ID) })
	enableSnapshot, err := repo.GetByIDLite(ctx, configured.ID)
	require.NoError(t, err)
	priceSnapshot, err := repo.GetByIDLite(ctx, configured.ID)
	require.NoError(t, err)
	yes, fee := true, 0.12345678
	enableSnapshot.GrokSafetyFeeUpdate = &service.GroupGrokSafetyFeeUpdate{Enabled: &yes}
	priceSnapshot.GrokSafetyFeeUpdate = &service.GroupGrokSafetyFeeUpdate{Fee: &fee}
	start := make(chan struct{})
	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i, snapshot := range []*service.Group{enableSnapshot, priceSnapshot} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs[i] = repo.Update(ctx, snapshot)
		}()
	}
	close(start)
	wg.Wait()
	require.NoError(t, errs[0])
	require.NoError(t, errs[1])
	stored, err := repo.GetByIDLite(ctx, configured.ID)
	require.NoError(t, err)
	require.True(t, stored.GrokSafetyFeeEnabled)
	require.NotNil(t, stored.GrokSafetyFee)
	require.Equal(t, fee, *stored.GrokSafetyFee)
}

func TestMigration283GrokSafetyFeeDefaultsAndReapply(t *testing.T) {
	ctx := context.Background()
	tx := testTx(t)
	// A temporary pre-migration table verifies an existing database upgrade
	// without changing the shared integration schema.
	_, err := tx.ExecContext(ctx, `CREATE TEMP TABLE groups (id bigint PRIMARY KEY, name text NOT NULL);
		INSERT INTO groups (id, name) VALUES (1, 'existing-group')`)
	require.NoError(t, err)
	body, err := dbmigrations.FS.ReadFile("283_group_grok_safety_fee.sql")
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, string(body))
	require.NoError(t, err)
	var enabled bool
	var amount *float64
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT grok_safety_fee_enabled, grok_safety_fee FROM groups WHERE id=1`).Scan(&enabled, &amount))
	require.False(t, enabled)
	require.Nil(t, amount)
	_, err = tx.ExecContext(ctx, `UPDATE groups SET grok_safety_fee_enabled=true, grok_safety_fee=0.12345678 WHERE id=1`)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, string(body))
	require.NoError(t, err)
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT grok_safety_fee_enabled, grok_safety_fee FROM groups WHERE id=1`).Scan(&enabled, &amount))
	require.True(t, enabled)
	require.NotNil(t, amount)
	require.Equal(t, 0.12345678, *amount)
}
