package repository

import (
	"context"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestExcelRouteProjectionAndBackgroundPatch(t *testing.T) {
	extra := map[string]any{service.OpenAIExcelUpstreamEnabledExtraKey: true, service.OpenAIUpstreamRouteGenerationExtraKey: "generation", "observation": true}
	projection := filterSchedulerExtra(extra)
	require.Equal(t, true, projection[service.OpenAIExcelUpstreamEnabledExtraKey])
	require.Equal(t, "generation", projection[service.OpenAIUpstreamRouteGenerationExtraKey])
	patch := accountConfigurationExtraPatch(context.Background(), []int64{1}, extra)
	require.NotContains(t, patch, service.OpenAIExcelUpstreamEnabledExtraKey)
	require.NotContains(t, patch, service.OpenAIUpstreamRouteGenerationExtraKey)
	require.Equal(t, true, patch["observation"])
	require.Equal(t, true, extra[service.OpenAIExcelUpstreamEnabledExtraKey], "filter must not mutate source")
}

func TestUpdateCredentialsUsesExcelRouteFenceGuard(t *testing.T) {
	client, mock := newOllamaCloudUsageRepositoryTestClient(t)
	mock.ExpectBegin()
	// UpdateCredentials has its own SQL statement rather than going through
	// UpdateExtra. Keep this assertion so a later refactor cannot bypass the
	// managed Excel route flag/generation guard on credential replacement.
	mock.ExpectQuery(`(?s)WITH previous_profile.*UPDATE accounts.*openai_excel_upstream_enabled.*openai_upstream_route_generation`).
		WithArgs(`{"access_token":"synthetic-pat","auth_mode":"personalAccessToken"}`, int64(17)).
		WillReturnRows(sqlmock.NewRows([]string{"profile_eligibility_changed"}).AddRow(false))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO scheduler_outbox")).
		WithArgs(service.SchedulerOutboxEventAccountChanged, int64(17), nil, nil, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	repo := newAccountRepositoryWithSQL(client, nil, nil)

	ctx := service.WithOpenAIOAuthCredentialModeChangeIntent(context.Background(), 17)
	require.NoError(t, repo.UpdateCredentials(ctx, 17, map[string]any{
		"auth_mode": "personalAccessToken", "access_token": "synthetic-pat",
	}))
	require.NoError(t, mock.ExpectationsWereMet())
}
