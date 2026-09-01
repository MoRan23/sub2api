package repository

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestUserRepositorySortByBalanceUsesCombinedWallets(t *testing.T) {
	repo, _ := newUserEntRepo(t)
	ctx := context.Background()

	ordinaryHigh := &service.User{
		Email: "ordinary-high@example.com", Username: "ordinary-high", PasswordHash: "hash",
		Role: service.RoleUser, Status: service.StatusActive, Balance: 10,
	}
	giftHigh := &service.User{
		Email: "gift-high@example.com", Username: "gift-high", PasswordHash: "hash",
		Role: service.RoleUser, Status: service.StatusActive, Balance: 1, GiftBalance: 20,
	}
	require.NoError(t, repo.Create(ctx, ordinaryHigh))
	require.NoError(t, repo.Create(ctx, giftHigh))

	users, _, err := repo.ListWithFilters(ctx, pagination.PaginationParams{
		Page: 1, PageSize: 10, SortBy: "balance", SortOrder: pagination.SortOrderDesc,
	}, service.UserListFilters{})
	require.NoError(t, err)
	require.Len(t, users, 2)
	require.Equal(t, giftHigh.ID, users[0].ID)
	require.Equal(t, ordinaryHigh.ID, users[1].ID)
}
