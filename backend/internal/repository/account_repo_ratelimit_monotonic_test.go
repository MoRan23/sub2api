package repository

import (
	"context"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/DATA-DOG/go-sqlmock"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/stretchr/testify/require"
)

func TestAccountRepositorySetRateLimitedUsesAtomicExtension(t *testing.T) {
	for _, updated := range []int64{0, 1} {
		name := "preserves_existing_generation"
		if updated == 1 {
			name = "extends_and_enqueues_outbox"
		}
		t.Run(name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			t.Cleanup(func() { _ = db.Close() })
			client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
			t.Cleanup(func() { _ = client.Close() })
			outbox := &recordingSQLExecutor{result: rowsAffectedResult(1)}
			repo := newAccountRepositoryWithSQL(client, outbox, nil)

			// The extension predicate must be evaluated by the same UPDATE, not by
			// a preceding read that a concurrent 429 could invalidate.
			mock.ExpectExec(`UPDATE "accounts" SET .* WHERE .*"accounts"\."id" = \$[0-9]+ AND \("accounts"\."rate_limit_reset_at" IS NULL OR "accounts"\."rate_limit_reset_at" < \$[0-9]+\)`).
				WillReturnResult(sqlmock.NewResult(0, updated))
			require.NoError(t, repo.SetRateLimited(context.Background(), 42, time.Now().Add(time.Minute)))
			require.NoError(t, mock.ExpectationsWereMet())
			if updated == 0 {
				require.Empty(t, outbox.execQueries, "a shorter or equal reset must not emit an account-change event")
			} else {
				require.Len(t, outbox.execQueries, 1)
				require.Contains(t, outbox.execQueries[0], "scheduler_outbox")
			}
		})
	}
}
