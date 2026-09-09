package repository

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/DATA-DOG/go-sqlmock"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func newCodexPATSettingsSQLMock(t *testing.T) (*settingRepository, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	t.Cleanup(func() { _ = client.Close() })
	return &settingRepository{client: client}, mock
}

func codexPATSettingsRows(values map[string]string) *sqlmock.Rows {
	rows := sqlmock.NewRows([]string{"id", "key", "value", "updated_at"})
	for i, key := range codexPATSettingsKeys {
		if value, ok := values[key]; ok {
			rows.AddRow(i+1, key, value, time.Now())
		}
	}
	return rows
}

func expectCodexPATSettingsRead(mock sqlmock.Sqlmock, values map[string]string) {
	mock.ExpectBegin()
	mock.ExpectExec(`SELECT pg_advisory_xact_lock\(\$1\)`).
		WithArgs(codexPATSettingsLockID).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT .* FROM "settings" WHERE "settings"\."key" IN`).
		WithArgs(codexPATSettingsKeys[0], codexPATSettingsKeys[1], codexPATSettingsKeys[2]).
		WillReturnRows(codexPATSettingsRows(values))
}

func TestSettingRepositoryCodexPATRejectsConflictingPartialWrites(t *testing.T) {
	for _, dependency := range codexPATSettingsKeys[1:] {
		for _, enablingPAT := range []bool{false, true} {
			t.Run(dependency+"/enable_PAT="+map[bool]string{true: "true", false: "false"}[enablingPAT], func(t *testing.T) {
				repo, mock := newCodexPATSettingsSQLMock(t)
				current := map[string]string{
					service.SettingKeyEnableOpenAICodexPATContextManagement: "true",
					dependency: "true",
				}
				updates := map[string]string{dependency: "false", service.SettingKeySiteName: "must-not-persist"}
				if enablingPAT {
					current[service.SettingKeyEnableOpenAICodexPATContextManagement] = "false"
					current[dependency] = "false"
					delete(updates, dependency)
					updates[service.SettingKeyEnableOpenAICodexPATContextManagement] = "true"
				}
				expectCodexPATSettingsRead(mock, current)
				mock.ExpectRollback()

				err := repo.SetMultiple(context.Background(), updates)

				require.ErrorContains(t, err, "PAT Codex context management requires")
				require.Equal(t, http.StatusBadRequest, infraerrors.Code(err))
				require.Equal(t, "INVALID_CODEX_PAT_CONTEXT_MANAGEMENT", infraerrors.Reason(err))
				require.NoError(t, mock.ExpectationsWereMet(), "validation must precede every write, including unrelated fields")
			})
		}
	}
}

func TestSettingRepositoryCodexPATUsesDefaultsAndAllowsAtomicDisable(t *testing.T) {
	for _, tc := range []struct {
		name    string
		current map[string]string
		updates map[string]string
	}{
		{"missing_default_dependencies", nil, map[string]string{service.SettingKeyEnableOpenAICodexPATContextManagement: "true"}},
		{"malformed_default_dependencies", map[string]string{
			service.SettingKeyEnableOpenAIUUIDv7SessionIdentity:         "invalid",
			service.SettingKeyEnableOpenAICodexFingerprintNormalization: "",
		}, map[string]string{service.SettingKeyEnableOpenAICodexPATContextManagement: "true"}},
		{"disable_adapter_and_dependency", map[string]string{service.SettingKeyEnableOpenAICodexPATContextManagement: "true"}, map[string]string{
			service.SettingKeyEnableOpenAICodexPATContextManagement: "false",
			service.SettingKeyEnableOpenAIUUIDv7SessionIdentity:     "false",
		}},
		{"repair_existing_invalid_configuration", map[string]string{
			service.SettingKeyEnableOpenAICodexPATContextManagement: "true",
			service.SettingKeyEnableOpenAIUUIDv7SessionIdentity:     "false",
		}, map[string]string{service.SettingKeyEnableOpenAIUUIDv7SessionIdentity: "true"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo, mock := newCodexPATSettingsSQLMock(t)
			expectCodexPATSettingsRead(mock, tc.current)
			mock.ExpectQuery(`INSERT INTO "settings"`).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(1))
			mock.ExpectCommit()
			require.NoError(t, repo.SetMultiple(context.Background(), tc.updates))
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestSettingRepositoryCodexPATSingleKeyWriteCannotBypassValidation(t *testing.T) {
	repo, mock := newCodexPATSettingsSQLMock(t)
	expectCodexPATSettingsRead(mock, map[string]string{service.SettingKeyEnableOpenAIUUIDv7SessionIdentity: "false"})
	mock.ExpectRollback()
	require.Error(t, repo.Set(context.Background(), service.SettingKeyEnableOpenAICodexPATContextManagement, "true"))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSettingRepositoryCodexPATWriteErrorsRollback(t *testing.T) {
	for _, stage := range []string{"lock", "read", "write"} {
		t.Run(stage, func(t *testing.T) {
			repo, mock := newCodexPATSettingsSQLMock(t)
			want := errors.New("database failure")
			mock.ExpectBegin()
			lock := mock.ExpectExec(`SELECT pg_advisory_xact_lock\(\$1\)`).WithArgs(codexPATSettingsLockID)
			if stage == "lock" {
				lock.WillReturnError(want)
			} else {
				lock.WillReturnResult(sqlmock.NewResult(0, 1))
				read := mock.ExpectQuery(`SELECT .* FROM "settings"`)
				if stage == "read" {
					read.WillReturnError(want)
				} else {
					read.WillReturnRows(codexPATSettingsRows(nil))
					mock.ExpectQuery(`INSERT INTO "settings"`).WillReturnError(want)
				}
			}
			mock.ExpectRollback()
			require.ErrorIs(t, repo.Set(context.Background(), service.SettingKeyEnableOpenAICodexPATContextManagement, "true"), want)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestSettingRepositoryCodexPATUnrelatedWritesStayIndependent(t *testing.T) {
	repo, mock := newCodexPATSettingsSQLMock(t)
	mock.ExpectQuery(`INSERT INTO "settings"`).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(1))
	require.NoError(t, repo.Set(context.Background(), service.SettingKeySiteName, "name"))
	require.NoError(t, mock.ExpectationsWereMet())
}
