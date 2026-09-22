package repository

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

func TestCodexStateListByAccountsUsesOneQueryWithLiveGenerationFilter(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	repo := &openAICodexStateRepository{db: db}
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	rows := sqlmock.NewRows([]string{
		"owner_account_id", "os_family", "model", "generation", "version", "encrypted_token",
		"issued_at", "expires_at", "token_length", "cipher_blocks", "source", "shape",
		"refresh_reason", "last_business_at", "last_collected_at", "next_collect_at", "collector_paused", "last_error",
		"demand_reason", "demand_at", "history_proof_observed_at", "collection_status", "collection_reason",
		"collector_proxy_id", "collector_extended_count", "last_collector_proxy_id", "collector_attempt_id",
		"business_in_flight",
	}).AddRow(3, "windows", "gpt-5.3", "generation-3", 2, "encrypted-one", now, now.Add(time.Hour), 292, 10, "business", "target", "", now, nil, nil, false, "", "", nil, nil, "", "", nil, 0, nil, nil, true).
		AddRow(3, "linux", "gpt-5.4", "generation-3", 4, "encrypted-two", now, now.Add(time.Hour), 332, 12, "collector", "target", "", now, now, now.Add(time.Minute), false, "", "", nil, nil, "", "", 202, 2, 101, "06aee3d4-720c-4e11-aeb4-0f0be2dcc027", false).
		AddRow(9, "macos", "gpt-5.4", "generation-9", 1, "", nil, nil, 0, 0, "", "", "missing", now, nil, nil, true, "authorization_failed", "extended_shape", now, now, "paused", "authorization_failed", 101, 1, 101, nil, true)
	// Match the security predicates explicitly so an accidentally broader batch
	// query cannot expose a disabled/deleted account or an obsolete generation.
	mock.ExpectQuery(`(?s)SELECT .* FROM openai_codex_state s\s+JOIN accounts a ON a.id=s.owner_account_id WHERE s.owner_account_id = ANY\(\$1\) AND a.deleted_at IS NULL AND a.platform = 'openai' AND a.type = 'oauth'\s+AND a.extra->'codex_turn_state'->>'enabled' = 'true'\s+AND EXISTS \(SELECT 1 FROM account_openai_oauth_os_credentials credential\s+WHERE credential.account_id = a.id AND credential.os_family = s.os_family\s+AND credential.status = 'authorized' AND credential.state_generation::text = s.generation\)\s+ORDER BY s.owner_account_id, s.model`).
		WithArgs("{9,3,9}").WillReturnRows(rows).RowsWillBeClosed()
	records, err := repo.ListByAccounts(context.Background(), []int64{9, 3, 9})
	require.NoError(t, err)
	require.Len(t, records, 3)
	require.EqualValues(t, 3, records[0].OwnerAccountID)
	require.Equal(t, "windows", records[0].OSFamily)
	require.Equal(t, "linux", records[1].OSFamily)
	require.Equal(t, "gpt-5.3", records[0].Model)
	require.Equal(t, "generation-3", records[0].Generation)
	require.Equal(t, "encrypted-one", records[0].EncryptedToken, "repository keeps ciphertext opaque for the status projection")
	require.Equal(t, now.Add(time.Hour), records[0].ExpiresAt)
	require.True(t, records[0].BusinessInFlight)
	require.Equal(t, "gpt-5.4", records[1].Model)
	require.Equal(t, now, records[1].LastCollectedAt)
	require.Equal(t, now.Add(time.Minute), records[1].NextCollectAt)
	require.False(t, records[1].BusinessInFlight)
	require.EqualValues(t, 202, records[1].CollectorProxyID)
	require.Equal(t, 2, records[1].CollectorExtendedCount)
	require.EqualValues(t, 101, records[1].LastCollectorProxyID)
	require.Equal(t, "06aee3d4-720c-4e11-aeb4-0f0be2dcc027", records[1].CollectorAttemptID)
	require.EqualValues(t, 9, records[2].OwnerAccountID)
	require.True(t, records[2].CollectorPaused)
	require.Equal(t, "authorization_failed", records[2].LastError)
	require.Equal(t, "extended_shape", records[2].DemandReason)
	require.Equal(t, now, records[2].HistoryProofObservedAt)
	require.Equal(t, "paused", records[2].CollectionStatus)
	require.True(t, records[2].BusinessInFlight)
	require.True(t, records[2].ExpiresAt.IsZero())
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestCodexStateListByAccountsEmptyDoesNotQuery(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	for _, repo := range []*openAICodexStateRepository{{db: db}, {}, nil} {
		for _, ids := range [][]int64{nil, {}} {
			records, err := repo.ListByAccounts(context.Background(), ids)
			require.NoError(t, err)
			require.Empty(t, records)
		}
	}
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestCodexStateListByAccountsDatabaseFailureReturnsNoRecords(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	repo := &openAICodexStateRepository{db: db}
	databaseErr := errors.New("status database unavailable")
	mock.ExpectQuery(`SELECT .* FROM openai_codex_state s`).WithArgs("{3,9}").WillReturnError(databaseErr)
	records, err := repo.ListByAccounts(context.Background(), []int64{3, 9})
	require.ErrorIs(t, err, databaseErr)
	require.Nil(t, records)
	require.NoError(t, mock.ExpectationsWereMet())
	records, err = (&openAICodexStateRepository{}).ListByAccounts(context.Background(), []int64{3})
	require.ErrorContains(t, err, "database unavailable")
	require.Nil(t, records)
}
