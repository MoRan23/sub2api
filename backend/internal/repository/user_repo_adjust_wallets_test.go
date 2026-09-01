package repository

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

func TestAdjustWalletsProjectsAndUpdatesTotalRecharged(t *testing.T) {
	repo, mock := newRedeemAdjustmentRepoMock(t)
	mock.ExpectQuery(`(?s)WITH target AS \(\s*SELECT id, balance, gift_balance, total_recharged\s*FROM users.*UPDATE users AS u.*total_recharged = target\.total_recharged \+ \$1`).
		WithArgs(5.0, 4.0, int64(42)).
		WillReturnRows(sqlmock.NewRows([]string{
			"old_ordinary", "new_ordinary", "old_gift", "new_gift",
		}).AddRow(1.0, 6.0, 2.0, 6.0))

	change, err := repo.AdjustWallets(context.Background(), 42, 5, 4)
	require.NoError(t, err)
	require.Equal(t, 1.0, change.OldOrdinary)
	require.Equal(t, 6.0, change.NewOrdinary)
	require.Equal(t, 2.0, change.OldGift)
	require.Equal(t, 6.0, change.NewGift)
	require.NoError(t, mock.ExpectationsWereMet())
}
