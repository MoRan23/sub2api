package repository

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestAPIKeyRepository_GetByKeyForAuth_PreservesPermanentQuota(t *testing.T) {
	repo, client := newAPIKeyRepoSQLite(t)
	ctx := context.Background()
	user := mustCreateAPIKeyRepoUser(t, ctx, client, "permanent-quota-auth@test.com")
	group, err := client.Group.Create().
		SetName("permanent-quota-auth").
		SetPlatform(service.PlatformOpenAI).
		SetSubscriptionType(service.SubscriptionTypeTotalQuota).
		SetTotalQuotaNoExpiry(true).
		Save(ctx)
	require.NoError(t, err)
	key := &service.APIKey{
		UserID: user.ID, GroupID: &group.ID, Key: "sk-permanent-quota-auth",
		Name: "Permanent quota", Status: service.StatusActive,
	}
	require.NoError(t, repo.Create(ctx, key))

	got, err := repo.GetByKeyForAuth(ctx, key.Key)
	require.NoError(t, err)
	require.NotNil(t, got.Group)
	require.Equal(t, service.SubscriptionTypeTotalQuota, got.Group.SubscriptionType)
	require.True(t, got.Group.TotalQuotaNoExpiry, "authentication must preserve the permanent quota mode")
}
