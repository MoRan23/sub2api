package repository

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

func TestSettingRepositorySetMultipleLocksKeysInStableOrder(t *testing.T) {
	repo, mock := newCodexPATSettingsSQLMock(t)
	for range 12 {
		mock.ExpectQuery(`INSERT INTO "settings"`).
			WithArgs("a", sqlmock.AnyArg(), "first",
				"b_models", sqlmock.AnyArg(), `["model"]`,
				"c_revision", sqlmock.AnyArg(), "revision",
				"z", sqlmock.AnyArg(), "last").
			WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(1).AddRow(2).AddRow(3).AddRow(4))
		require.NoError(t, repo.SetMultiple(context.Background(), map[string]string{
			"z": "last", "c_revision": "revision",
			"a": "first", "b_models": `["model"]`,
		}))
	}
	require.NoError(t, mock.ExpectationsWereMet())
}
