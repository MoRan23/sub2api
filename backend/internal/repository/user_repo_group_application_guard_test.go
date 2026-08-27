package repository

import (
	"context"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
)

func newUserGroupApplicationGuardTestRepository(t *testing.T) (*userRepository, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	driver := entsql.OpenDB(dialect.Postgres, db)
	client := dbent.NewClient(dbent.Driver(driver))
	t.Cleanup(func() { _ = client.Close() })
	return newUserRepositoryWithSQL(client, db), mock
}

func expectUserGroupApplicationGuardLock(mock sqlmock.Sqlmock, userID int64) {
	mock.ExpectQuery(`(?s)SELECT .* FROM "users" WHERE .*"id" = \$1.*FOR UPDATE`).
		WithArgs(userID).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(userID))
}

func expectActiveGroupApplicationLookup(mock sqlmock.Sqlmock, userID, groupID int64, active bool) {
	rows := sqlmock.NewRows([]string{"one"})
	if active {
		rows.AddRow(1)
	}
	mock.ExpectQuery(`(?s)SELECT 1.*FROM group_applications.*user_id=\$1 AND group_id=\$2.*pending.*awaiting_reply`).
		WithArgs(userID, groupID).
		WillReturnRows(rows)
}

func TestUserRepositoryAddGroupSerializesAndRejectsActiveApplication(t *testing.T) {
	repo, mock := newUserGroupApplicationGuardTestRepository(t)
	mock.ExpectBegin()
	expectUserGroupApplicationGuardLock(mock, 9)
	expectActiveGroupApplicationLookup(mock, 9, 4, true)
	mock.ExpectRollback()

	err := repo.AddGroupToAllowedGroups(context.Background(), 9, 4)

	require.ErrorIs(t, err, service.ErrGroupApplicationConflict)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUserRepositoryAddGroupCommitsGuardAndInsertTogether(t *testing.T) {
	repo, mock := newUserGroupApplicationGuardTestRepository(t)
	mock.ExpectBegin()
	expectUserGroupApplicationGuardLock(mock, 9)
	expectActiveGroupApplicationLookup(mock, 9, 4, false)
	mock.ExpectExec(`(?s)INSERT INTO "user_allowed_groups".*ON CONFLICT.*DO NOTHING`).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	err := repo.AddGroupToAllowedGroups(context.Background(), 9, 4)

	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUserRepositoryAddGroupReusesExternalEntTransaction(t *testing.T) {
	repo, mock := newUserGroupApplicationGuardTestRepository(t)
	mock.ExpectBegin()
	tx, err := repo.client.Tx(context.Background())
	require.NoError(t, err)
	txCtx := dbent.NewTxContext(context.Background(), tx)
	expectUserGroupApplicationGuardLock(mock, 9)
	expectActiveGroupApplicationLookup(mock, 9, 4, false)
	mock.ExpectExec(`(?s)INSERT INTO "user_allowed_groups".*ON CONFLICT.*DO NOTHING`).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	require.NoError(t, repo.AddGroupToAllowedGroups(txCtx, 9, 4))
	require.NoError(t, tx.Commit())
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUserRepositorySyncAllowedGroupsRejectsNewGroupWithActiveApplication(t *testing.T) {
	repo, mock := newUserGroupApplicationGuardTestRepository(t)
	mock.ExpectBegin()
	tx, err := repo.client.Tx(context.Background())
	require.NoError(t, err)
	txCtx := dbent.NewTxContext(context.Background(), tx)
	expectUserGroupApplicationGuardLock(mock, 9)
	mock.ExpectQuery(`(?s)SELECT .* FROM "user_allowed_groups" WHERE .*"user_id" = \$1`).
		WithArgs(int64(9)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "created_at", "user_id", "group_id"}))
	expectActiveGroupApplicationLookup(mock, 9, 4, true)
	mock.ExpectRollback()

	err = repo.syncUserAllowedGroupsWithClient(txCtx, tx.Client(), 9, []int64{4})

	require.ErrorIs(t, err, service.ErrGroupApplicationConflict)
	require.NoError(t, tx.Rollback())
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUserRepositoryGroupApplicationGuardSupportsSQLite(t *testing.T) {
	repo, _ := newUserEntRepo(t)
	ctx := context.Background()
	user := &service.User{
		Email: "group-application-guard@example.com", PasswordHash: "hash",
		Role: service.RoleUser, Status: service.StatusActive,
	}
	require.NoError(t, repo.Create(ctx, user))
	_, err := repo.sql.ExecContext(ctx, `
		CREATE TABLE group_applications (
			id INTEGER PRIMARY KEY,
			user_id INTEGER NOT NULL,
			group_id INTEGER NOT NULL,
			status TEXT NOT NULL
		)
	`)
	require.NoError(t, err)
	_, err = repo.sql.ExecContext(ctx, `
		INSERT INTO group_applications(id,user_id,group_id,status)
		VALUES(1,$1,$2,'pending')
	`, user.ID, int64(4))
	require.NoError(t, err)

	err = repo.AddGroupToAllowedGroups(ctx, user.ID, 4)

	require.ErrorIs(t, err, service.ErrGroupApplicationConflict)
}

func TestUserRepositoryUpdateAllowedGroupsLeavesExternalTransactionInControl(t *testing.T) {
	repo, client := newUserEntRepo(t)
	ctx := context.Background()
	user := &service.User{
		Email: "update-external-transaction@example.com", PasswordHash: "hash",
		Role: service.RoleUser, Status: service.StatusActive,
	}
	require.NoError(t, repo.Create(ctx, user))
	group, err := client.Group.Create().
		SetName("update-external-transaction-group").
		SetStatus(service.StatusActive).
		Save(ctx)
	require.NoError(t, err)
	_, err = repo.sql.ExecContext(ctx, `
		CREATE TABLE group_applications (
			id INTEGER PRIMARY KEY,
			user_id INTEGER NOT NULL,
			group_id INTEGER NOT NULL,
			status TEXT NOT NULL
		)
	`)
	require.NoError(t, err)

	tx, err := client.Tx(ctx)
	require.NoError(t, err)
	txCtx := dbent.NewTxContext(ctx, tx)
	user.AllowedGroups = []int64{group.ID}
	require.NoError(t, repo.Update(txCtx, user, service.UserUpdateFields{AllowedGroups: true}))
	count, err := tx.Client().UserAllowedGroup.Query().Count(txCtx)
	require.NoError(t, err)
	require.Equal(t, 1, count)
	require.NoError(t, tx.Rollback())

	count, err = client.UserAllowedGroup.Query().Count(ctx)
	require.NoError(t, err)
	require.Zero(t, count)
}
