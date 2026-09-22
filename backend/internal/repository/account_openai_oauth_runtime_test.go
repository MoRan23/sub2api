package repository

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/DATA-DOG/go-sqlmock"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func newOpenAIOAuthRuntimeMock(t *testing.T) (*accountRepository, sqlmock.Sqlmock, *[]string) {
	t.Helper()
	queries := []string{}
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherFunc(func(expected, actual string) error {
		queries = append(queries, actual)
		return sqlmock.QueryMatcherRegexp.Match(expected, actual)
	})))
	require.NoError(t, err)
	client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	t.Cleanup(func() { _ = client.Close() })
	return newAccountRepositoryWithSQL(client, db, nil), mock, &queries
}

func expectOpenAIOAuthRuntimeGrant(mock sqlmock.Sqlmock, generation string, revision int64) {
	mock.ExpectQuery(`SELECT platform, type, parent_account_id, credentials, extra .* FOR NO KEY UPDATE`).
		WithArgs(int64(42)).WillReturnRows(sqlmock.NewRows([]string{"platform", "type", "parent_account_id", "credentials", "extra"}).
		AddRow("openai", "oauth", nil, `{"access_token":"current"}`, `{}`))
	mock.ExpectQuery(`SELECT authorization_generation::text,revision .* FOR UPDATE`).
		WithArgs(int64(42)).WillReturnRows(sqlmock.NewRows([]string{"authorization_generation", "revision"}).AddRow(generation, revision))
}

func expectOpenAIOAuthRuntimeState(mock sqlmock.Sqlmock, status string, temporary any, extra string) {
	mock.ExpectQuery(`SELECT status,rate_limited_at,rate_limit_reset_at,overload_until,temp_unschedulable_until,extra .* FOR NO KEY UPDATE`).
		WithArgs(int64(42), int64(42)).WillReturnRows(sqlmock.NewRows([]string{"status", "rate_limited_at", "rate_limit_reset_at", "overload_until", "temp_unschedulable_until", "extra"}).
		AddRow(status, nil, nil, nil, temporary, extra))
}

func TestOpenAIOAuthRuntimeMutation_StaleGrantDoesNotWrite(t *testing.T) {
	for _, test := range []struct {
		name, generation string
		revision         int64
	}{
		{"reauthorized", "new-generation", 7},
		{"refreshed", "attempted-generation", 8},
	} {
		t.Run(test.name, func(t *testing.T) {
			repo, mock, _ := newOpenAIOAuthRuntimeMock(t)
			mock.ExpectBegin()
			expectOpenAIOAuthRuntimeGrant(mock, test.generation, test.revision)
			mock.ExpectRollback()
			result, err := repo.MutateOpenAIOAuthAccountStateIfUnchanged(context.Background(), 42,
				service.OpenAIOAuthAccountStateSnapshot{OwnerAccountID: 42, AuthorizationGeneration: "attempted-generation", CredentialRevision: 7},
				service.OpenAIOAuthAccountStateChange{Kind: service.OpenAIOAuthAccountStateError, ErrorMessage: "late 401", AuthFailure: true})
			require.NoError(t, err)
			require.False(t, result.Applied)
			require.False(t, result.ClearedError)
			require.False(t, result.ClearedRateLimit)
			require.NoError(t, mock.ExpectationsWereMet(), "a stale grant cannot reach an account update or scheduler outbox write")
		})
	}
}

func TestOpenAIOAuthRuntimeMutation_ErrorAndOutboxCommitAtomically(t *testing.T) {
	for _, outboxFails := range []bool{false, true} {
		name := "commit"
		if outboxFails {
			name = "rollback_on_outbox_failure"
		}
		t.Run(name, func(t *testing.T) {
			repo, mock, queries := newOpenAIOAuthRuntimeMock(t)
			mock.ExpectBegin()
			expectOpenAIOAuthRuntimeGrant(mock, "attempted-generation", 7)
			expectOpenAIOAuthRuntimeState(mock, service.StatusActive, nil, `{}`)
			mock.ExpectExec(`UPDATE accounts SET status=\$2,error_message=\$3,schedulable=false,updated_at=NOW\(\) WHERE id=\$1`).
				WithArgs(int64(42), service.StatusError, "revoked token").WillReturnResult(sqlmock.NewResult(0, 1))
			outbox := mock.ExpectExec(`INSERT INTO scheduler_outbox`)
			outboxErr := errors.New("outbox unavailable")
			if outboxFails {
				outbox.WillReturnError(outboxErr)
				mock.ExpectRollback()
			} else {
				outbox.WillReturnResult(sqlmock.NewResult(1, 1))
				mock.ExpectCommit()
			}
			result, err := repo.MutateOpenAIOAuthAccountStateIfUnchanged(context.Background(), 42,
				service.OpenAIOAuthAccountStateSnapshot{OwnerAccountID: 42, AuthorizationGeneration: "attempted-generation", CredentialRevision: 7},
				service.OpenAIOAuthAccountStateChange{Kind: service.OpenAIOAuthAccountStateError, ErrorMessage: "revoked token"})
			if outboxFails {
				require.ErrorIs(t, err, outboxErr)
				require.Nil(t, result)
			} else {
				require.NoError(t, err)
				require.True(t, result.Applied)
			}
			for _, query := range *queries {
				if strings.Contains(query, "FROM account_openai_oauth_credentials") {
					require.NotContains(t, query, "status", "private authorization status is not a runtime mutation gate")
				}
			}
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestOpenAIOAuthRuntimeMutation_RecoveryPreservesSchedulingAndCredentials(t *testing.T) {
	for _, kind := range []service.OpenAIOAuthAccountStateChangeKind{
		service.OpenAIOAuthAccountStateRecover, service.OpenAIOAuthAccountStateClearError,
	} {
		t.Run(string(kind), func(t *testing.T) {
			repo, mock, queries := newOpenAIOAuthRuntimeMock(t)
			mock.ExpectBegin()
			expectOpenAIOAuthRuntimeGrant(mock, "attempted-generation", 7)
			expectOpenAIOAuthRuntimeState(mock, service.StatusError, time.Now().Add(time.Hour), `{"model_rate_limits":{"gpt-5":{"reason":"limit"}},"keep":true}`)
			update := mock.ExpectExec(`UPDATE accounts SET`)
			if kind == service.OpenAIOAuthAccountStateRecover {
				update.WithArgs(int64(42), true, true, service.StatusActive)
			} else {
				update.WithArgs(int64(42), service.StatusActive)
			}
			update.WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectExec(`INSERT INTO scheduler_outbox`).WillReturnResult(sqlmock.NewResult(1, 1))
			mock.ExpectCommit()
			result, err := repo.MutateOpenAIOAuthAccountStateIfUnchanged(context.Background(), 42,
				service.OpenAIOAuthAccountStateSnapshot{OwnerAccountID: 42, AuthorizationGeneration: "attempted-generation", CredentialRevision: 7},
				service.OpenAIOAuthAccountStateChange{Kind: kind})
			require.NoError(t, err)
			require.True(t, result.Applied)
			require.True(t, result.ClearedError)
			require.Equal(t, kind == service.OpenAIOAuthAccountStateRecover, result.ClearedRateLimit)
			for _, query := range *queries {
				if !strings.Contains(query, "UPDATE accounts SET") {
					continue
				}
				require.NotRegexp(t, `\bschedulable\s*=`, query)
				require.NotRegexp(t, `\bcredentials\s*=`, query)
				if kind == service.OpenAIOAuthAccountStateRecover {
					require.Contains(t, query, "rate_limited_at=CASE WHEN $3 THEN NULL")
					require.Contains(t, query, "temp_unschedulable_reason=CASE WHEN $3 THEN NULL")
					require.Contains(t, query, "-'model_rate_limits'-'antigravity_quota_scopes'")
				} else {
					require.NotContains(t, query, "rate_limit")
					require.NotContains(t, query, "temp_unschedulable")
				}
			}
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestOpenAIOAuthRuntimeMutation_AuthFailureOwnsOriginalPauseAfterAccountUpdate(t *testing.T) {
	for _, previous := range []struct {
		name, status, message string
		schedulable           bool
	}{
		{"active", service.StatusActive, "", true},
		{"manual_pause", service.StatusActive, "manual hold", false},
		{"existing_error", service.StatusError, "earlier account error", false},
	} {
		t.Run(previous.name, func(t *testing.T) {
			repo, mock, queries := newOpenAIOAuthRuntimeMock(t)
			mock.ExpectBegin()
			expectOpenAIOAuthRuntimeGrant(mock, "attempted-generation", 7)
			expectOpenAIOAuthRuntimeState(mock, service.StatusError, nil, `{}`)
			mock.ExpectQuery(`SELECT CASE WHEN c.auth_pause_owned THEN c.auth_pause_previous_status ELSE a.status END,`).
				WithArgs(int64(42)).WillReturnRows(sqlmock.NewRows([]string{"previous_status", "previous_schedulable", "previous_error_message"}).
				AddRow(previous.status, previous.schedulable, previous.message))
			mock.ExpectExec(`UPDATE accounts SET status=\$2,error_message=\$3,schedulable=false,updated_at=NOW\(\) WHERE id=\$1`).
				WithArgs(int64(42), service.StatusError, "401 Unauthorized: invalidated token").WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectExec(`UPDATE account_openai_oauth_credentials SET auth_pause_owned=true,`).
				WithArgs(int64(42), previous.status, previous.schedulable, previous.message).WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectExec(`INSERT INTO scheduler_outbox`).WillReturnResult(sqlmock.NewResult(1, 1))
			mock.ExpectCommit()
			result, err := repo.MutateOpenAIOAuthAccountStateIfUnchanged(context.Background(), 42,
				service.OpenAIOAuthAccountStateSnapshot{OwnerAccountID: 42, AuthorizationGeneration: "attempted-generation", CredentialRevision: 7},
				service.OpenAIOAuthAccountStateChange{Kind: service.OpenAIOAuthAccountStateError, ErrorMessage: "401 Unauthorized: invalidated token", AuthFailure: true})
			require.NoError(t, err)
			require.True(t, result.Applied)
			for _, query := range *queries {
				if strings.Contains(query, "SELECT CASE WHEN c.auth_pause_owned") {
					require.Contains(t, query, "CASE WHEN c.auth_pause_owned THEN c.auth_pause_previous_schedulable ELSE a.schedulable END")
					require.Contains(t, query, "CASE WHEN c.auth_pause_owned THEN c.auth_pause_previous_error_message ELSE COALESCE(a.error_message,'') END")
				}
				if strings.Contains(query, "UPDATE account_openai_oauth_credentials SET") {
					require.NotRegexp(t, `\bstatus\s*=`, query)
					require.NotRegexp(t, `\brevision\s*=`, query)
					require.NotContains(t, query, "authorization_generation")
				}
			}
			require.NoError(t, mock.ExpectationsWereMet(), "pause ownership must be written after the account-state trigger")
		})
	}
}

func TestOpenAIOAuthRuntimeMutation_ShadowAuthFailureCannotOwnParentPause(t *testing.T) {
	repo, mock, _ := newOpenAIOAuthRuntimeMock(t)
	mock.ExpectBegin()
	expectOpenAIOAuthRuntimeGrant(mock, "attempted-generation", 7)
	mock.ExpectQuery(`SELECT status,rate_limited_at,rate_limit_reset_at,overload_until,temp_unschedulable_until,extra .* FOR NO KEY UPDATE`).
		WithArgs(int64(88), int64(42)).WillReturnRows(sqlmock.NewRows([]string{"status", "rate_limited_at", "rate_limit_reset_at", "overload_until", "temp_unschedulable_until", "extra"}).
		AddRow(service.StatusActive, nil, nil, nil, nil, `{}`))
	mock.ExpectExec(`UPDATE accounts SET status=\$2,error_message=\$3,schedulable=false,updated_at=NOW\(\) WHERE id=\$1`).
		WithArgs(int64(88), service.StatusError, "shadow auth failure").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`INSERT INTO scheduler_outbox`).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	result, err := repo.MutateOpenAIOAuthAccountStateIfUnchanged(context.Background(), 88,
		service.OpenAIOAuthAccountStateSnapshot{OwnerAccountID: 42, AuthorizationGeneration: "attempted-generation", CredentialRevision: 7},
		service.OpenAIOAuthAccountStateChange{Kind: service.OpenAIOAuthAccountStateError, ErrorMessage: "shadow auth failure", AuthFailure: true})
	require.NoError(t, err)
	require.True(t, result.Applied)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestOpenAIOAuthOwnedPauseRestorationUsesOwnershipInsteadOfMessage(t *testing.T) {
	repo, mock, queries := newOpenAIOAuthRuntimeMock(t)
	mock.ExpectExec(`UPDATE accounts a SET status=c.auth_pause_previous_status,`).
		WithArgs(int64(42)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE account_openai_oauth_credentials SET auth_pause_owned=false`).
		WithArgs(int64(42)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE accounts a SET temp_unschedulable_until=c.auth_retry_previous_until,`).
		WithArgs(int64(42), openAIOAuthRetryMessage).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`UPDATE account_openai_oauth_credentials SET auth_retry_owned=false,auth_retry_until=NULL`).
		WithArgs(int64(42)).WillReturnResult(sqlmock.NewResult(0, 1))
	require.NoError(t, restoreOpenAIOAuthOwnedPauseLocked(context.Background(), repo.client, 42, true))
	require.NotEmpty(t, *queries)
	query := (*queries)[0]
	require.Contains(t, query, "c.auth_pause_owned AND a.status='error' AND NOT a.schedulable")
	require.NotContains(t, query, "a.error_message=")
	require.NoError(t, mock.ExpectationsWereMet())
}
