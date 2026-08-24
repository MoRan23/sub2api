package repository

import (
	"context"
	"encoding/json"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestCompareAndUpdateOpenAIAutoResetPreflightUsesEligibilityAndStateCAS(t *testing.T) {
	tests := []struct {
		name          string
		expectedState *service.OpenAIAutoResetCreditState
		affected      int64
		wantUpdated   bool
	}{
		{name: "missing state wins", affected: 1, wantUpdated: true},
		{
			name: "changed state loses",
			expectedState: &service.OpenAIAutoResetCreditState{
				Status:           service.OpenAIAutoResetStatusAvailable,
				AttemptCycleHash: "cycle-a",
			},
			affected: 0,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			t.Cleanup(func() { _ = db.Close() })
			expectedJSON, err := json.Marshal(test.expectedState)
			require.NoError(t, err)
			mock.ExpectExec(
				`(?s)`+regexp.QuoteMeta("UPDATE accounts")+
					`.*`+regexp.QuoteMeta("AND platform = 'openai'")+
					`.*`+regexp.QuoteMeta("AND type = 'oauth'")+
					`.*`+regexp.QuoteMeta("AND parent_account_id IS NULL")+
					`.*`+regexp.QuoteMeta("AND status = 'active'")+
					`.*`+regexp.QuoteMeta("AND schedulable = TRUE")+
					`.*`+regexp.QuoteMeta("auto_reset_credit_enabled")+
					`.*`+regexp.QuoteMeta("COALESCE(extra -> 'codex_auto_reset_credit_state', 'null'::jsonb) = $3::jsonb"),
			).
				WithArgs(sqlmock.AnyArg(), int64(17), string(expectedJSON)).
				WillReturnResult(sqlmock.NewResult(0, test.affected))

			repo := newAccountRepositoryWithSQL(nil, db, nil)
			updated, err := repo.CompareAndUpdateOpenAIAutoResetPreflight(
				context.Background(),
				17,
				test.expectedState,
				map[string]any{
					service.OpenAIAutoResetCreditStateExtraKey: &service.OpenAIAutoResetCreditState{Status: service.OpenAIAutoResetStatusNoCredit},
					"codex_5h_used_percent":                    50.0,
				},
			)

			require.NoError(t, err)
			require.Equal(t, test.wantUpdated, updated)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}
