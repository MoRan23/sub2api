package repository

import (
	"context"
	"errors"
	"testing"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/DATA-DOG/go-sqlmock"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/stretchr/testify/require"
)

func TestCodexTurnStateConfigurationNotificationWaitsForCommit(t *testing.T) {
	for _, outcome := range []string{"commit", "rollback", "failed_commit"} {
		t.Run(outcome, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
			t.Cleanup(func() { _ = client.Close() })
			mock.ExpectBegin()
			tx, err := client.Tx(context.Background())
			require.NoError(t, err)
			notifications := 0
			afterAccountConfigurationCommit(dbent.NewTxContext(context.Background(), tx), func() { notifications++ })
			require.Zero(t, notifications, "an uncommitted configuration cannot activate historical demand")
			switch outcome {
			case "commit":
				mock.ExpectCommit()
				require.NoError(t, tx.Commit())
				require.Equal(t, 1, notifications)
			case "rollback":
				mock.ExpectRollback()
				require.NoError(t, tx.Rollback())
				require.Zero(t, notifications)
			case "failed_commit":
				mock.ExpectCommit().WillReturnError(errors.New("commit failed"))
				require.Error(t, tx.Commit())
				require.Zero(t, notifications)
			}
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}
