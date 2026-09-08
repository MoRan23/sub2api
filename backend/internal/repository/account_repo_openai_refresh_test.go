package repository

import (
	"context"
	"database/sql/driver"
	"errors"
	"testing"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	sqlmock "github.com/DATA-DOG/go-sqlmock"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func openAIRefreshExpectedAuthForRepoTest() map[string]any {
	return map[string]any{
		"access_token": "old-access", "refresh_token": "old-refresh",
		"id_token": nil, "client_id": nil, "auth_mode": nil,
		"openai_auth_mode": nil, "_token_version": 12,
		"chatgpt_account_id": nil, "chatgpt_user_id": nil, "organization_id": nil,
	}
}

func TestAccountRepository_PatchOpenAIOAuthCredentialsIfUnchanged_AtomicAuthOnlyPatch(t *testing.T) {
	exec := &recordingSQLExecutor{result: rowsAffectedResult(1)}
	repo := newAccountRepositoryWithSQL(nil, exec, nil)
	proxyID := int64(29)
	expected := openAIRefreshExpectedAuthForRepoTest()
	patch := map[string]any{"access_token": "new-access", "_token_version": 13}

	applied, err := repo.PatchOpenAIOAuthCredentialsIfUnchanged(context.Background(), 42, expected, &proxyID, patch, []string{"id_token"})

	require.NoError(t, err)
	require.True(t, applied)
	require.Len(t, exec.execQueries, 1)
	query := normalizeSQLWhitespace(exec.execQueries[0])
	require.Contains(t, query, "WITH updated AS ( UPDATE accounts AS a")
	require.Contains(t, query, "credentials = (COALESCE(a.credentials, '{}'::jsonb) - $1::text[]) || $2::jsonb")
	require.NotContains(t, query, "credentials = $2::jsonb")
	require.Contains(t, query, "a.id = $3 AND a.deleted_at IS NULL AND a.platform = $4 AND a.type = $5 AND a.parent_account_id IS NULL")
	require.Contains(t, query, "a.proxy_id IS NOT DISTINCT FROM $6")
	require.Contains(t, query, "NOT EXISTS ( SELECT 1 FROM jsonb_each($7::jsonb) AS expected(key, value)")
	require.Contains(t, query, "COALESCE(a.credentials -> expected.key, 'null'::jsonb) IS DISTINCT FROM expected.value")
	require.Contains(t, query, "INSERT INTO scheduler_outbox (event_type, account_id, group_id, payload) SELECT $8, updated.id, NULL, NULL FROM updated")
	require.NotContains(t, query, "SET status")
	require.NotContains(t, query, "SET schedulable")
	require.Len(t, exec.execArgs[0], 8)
	args := exec.execArgs[0]
	removed, err := args[0].(driver.Valuer).Value()
	require.NoError(t, err)
	require.Equal(t, `{"id_token"}`, removed)
	require.JSONEq(t, `{"access_token":"new-access","_token_version":13}`, args[1].(string))
	require.Equal(t, int64(42), args[2])
	require.Equal(t, service.PlatformOpenAI, args[3])
	require.Equal(t, service.AccountTypeOAuth, args[4])
	require.Equal(t, &proxyID, args[5])
	require.JSONEq(t, `{"access_token":"old-access","refresh_token":"old-refresh","id_token":null,"client_id":null,"auth_mode":null,"openai_auth_mode":null,"_token_version":12,"chatgpt_account_id":null,"chatgpt_user_id":null,"organization_id":null}`, args[6].(string))
	require.Equal(t, service.SchedulerOutboxEventAccountChanged, args[7])
}

func TestAccountRepository_PatchOpenAIOAuthCredentialsIfUnchanged_NilRemovalsAndCASMiss(t *testing.T) {
	exec := &recordingSQLExecutor{result: rowsAffectedResult(0)}
	repo := newAccountRepositoryWithSQL(nil, exec, nil)
	applied, err := repo.PatchOpenAIOAuthCredentialsIfUnchanged(context.Background(), 42, openAIRefreshExpectedAuthForRepoTest(), nil, nil, nil)
	require.NoError(t, err)
	require.False(t, applied)
	require.Len(t, exec.execQueries, 1)
	removed, err := exec.execArgs[0][0].(driver.Valuer).Value()
	require.NoError(t, err)
	require.Equal(t, "{}", removed)
	require.Equal(t, "{}", exec.execArgs[0][1])
	require.Nil(t, exec.execArgs[0][5])
}

func TestAccountRepository_PatchOpenAIOAuthCredentialsIfUnchanged_ErrorPropagation(t *testing.T) {
	wantErr := errors.New("database failure")
	for _, tt := range []struct {
		name string
		exec *recordingSQLExecutor
	}{
		{"statement", &recordingSQLExecutor{err: wantErr}},
		{"rows affected", &recordingSQLExecutor{result: sqlmock.NewErrorResult(wantErr)}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			repo := newAccountRepositoryWithSQL(nil, tt.exec, nil)
			applied, err := repo.PatchOpenAIOAuthCredentialsIfUnchanged(context.Background(), 42, openAIRefreshExpectedAuthForRepoTest(), nil, nil, nil)
			require.ErrorIs(t, err, wantErr)
			require.False(t, applied)
			require.Len(t, tt.exec.execQueries, 1)
		})
	}
}

func TestAccountRepository_PatchOpenAIOAuthCredentialsIfUnchanged_RejectsInvalidInputs(t *testing.T) {
	for _, tt := range []struct {
		name     string
		expected map[string]any
		patch    map[string]any
	}{
		{"empty expected", nil, nil},
		{"invalid expected JSON", map[string]any{"access_token": make(chan int)}, nil},
		{"invalid patch JSON", openAIRefreshExpectedAuthForRepoTest(), map[string]any{"access_token": make(chan int)}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			exec := &recordingSQLExecutor{result: rowsAffectedResult(1)}
			repo := newAccountRepositoryWithSQL(nil, exec, nil)
			applied, err := repo.PatchOpenAIOAuthCredentialsIfUnchanged(context.Background(), 42, tt.expected, nil, tt.patch, nil)
			require.Error(t, err)
			require.False(t, applied)
			require.Empty(t, exec.execQueries)
		})
	}
	for _, repo := range []*accountRepository{nil, {}} {
		applied, err := repo.PatchOpenAIOAuthCredentialsIfUnchanged(context.Background(), 42, openAIRefreshExpectedAuthForRepoTest(), nil, nil, nil)
		require.Error(t, err)
		require.False(t, applied)
	}
}

func TestAccountRepository_PatchOpenAIOAuthCredentialsIfUnchanged_ReusesCallerTransaction(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	t.Cleanup(func() { _ = client.Close() })
	mock.ExpectBegin()
	tx, err := client.Tx(context.Background())
	require.NoError(t, err)
	t.Cleanup(func() { _ = tx.Rollback() })
	contextTx := dbent.NewTxContext(context.Background(), tx)
	wrongExecutor := &recordingSQLExecutor{err: errors.New("escaped caller transaction")}
	repo := newAccountRepositoryWithSQL(nil, wrongExecutor, nil)
	mock.ExpectExec("WITH updated AS").WillReturnResult(sqlmock.NewResult(0, 1))

	applied, err := repo.PatchOpenAIOAuthCredentialsIfUnchanged(contextTx, 42, openAIRefreshExpectedAuthForRepoTest(), nil, map[string]any{"access_token": "new-access"}, nil)

	require.NoError(t, err)
	require.True(t, applied)
	require.Empty(t, wrongExecutor.execQueries)
	mock.ExpectRollback()
	require.NoError(t, tx.Rollback(), "the repository must not commit the caller transaction")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAccountRepository_PatchOpenAIOAuthCredentialsIfUnchanged_CommitSurvivesCallerCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	exec := &recordingSQLExecutor{result: rowsAffectedResult(1), afterExec: cancel}
	repo := newAccountRepositoryWithSQL(nil, exec, nil)
	applied, err := repo.PatchOpenAIOAuthCredentialsIfUnchanged(ctx, 42, openAIRefreshExpectedAuthForRepoTest(), nil, map[string]any{"access_token": "new-access"}, nil)
	require.NoError(t, err)
	require.True(t, applied)
	require.ErrorIs(t, ctx.Err(), context.Canceled)
	require.Len(t, exec.execQueries, 1)
	require.Contains(t, exec.execQueries[0], "INSERT INTO scheduler_outbox")
}
