package repository

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	_ "github.com/Wei-Shaw/sub2api/ent/runtime"
	"github.com/Wei-Shaw/sub2api/ent/usersubscription"
	"github.com/stretchr/testify/require"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
)

func TestUserSubscriptionGetByIDForUpdateLocksRow(t *testing.T) {
	var queries []string
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherFunc(func(_, actual string) error {
		queries = append(queries, actual)
		return nil
	})))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	driver := entsql.OpenDB(dialect.Postgres, db)
	client := dbent.NewClient(dbent.Driver(driver))
	t.Cleanup(func() { _ = client.Close() })
	repo := NewUserSubscriptionRepository(client)
	now := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)

	mock.ExpectQuery("locked subscription").WillReturnRows(
		sqlmock.NewRows(usersubscription.Columns).AddRow(
			int64(7), now, now, nil, int64(11), int64(13), now, now.AddDate(0, 0, 30), "active",
			nil, nil, nil, 0.0, 0.0, 0.0, nil, now, "renewal",
		),
	)
	mock.ExpectQuery("subscription group").WillReturnRows(
		sqlmock.NewRows([]string{"id", "subscription_type"}).AddRow(int64(13), "total_quota"),
	)

	sub, err := repo.GetByIDForUpdate(context.Background(), 7)
	require.NoError(t, err)
	require.Equal(t, int64(7), sub.ID)
	require.NoError(t, mock.ExpectationsWereMet())
	require.Len(t, queries, 2)
	require.Contains(t, strings.ToUpper(normalizeSQLWhitespace(queries[0])), "FOR UPDATE")
	require.NotNil(t, sub.Group)
	require.True(t, sub.Group.IsTotalQuotaSubscriptionType())
}
