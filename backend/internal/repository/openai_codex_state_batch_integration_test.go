//go:build integration

package repository

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

func codexBatchSnapshot(t *testing.T, ctx context.Context, ids []int64) (states, leases string) {
	t.Helper()
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT COALESCE(
		jsonb_agg(to_jsonb(s) ORDER BY s.owner_account_id, s.model), '[]'::jsonb)::text
		FROM openai_codex_state s WHERE s.owner_account_id = ANY($1)`, pq.Array(ids)).Scan(&states))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT COALESCE(
		jsonb_agg(to_jsonb(l) ORDER BY l.owner_account_id, l.model, l.generation, l.attempt_id), '[]'::jsonb)::text
		FROM openai_codex_state_business_leases l WHERE l.owner_account_id = ANY($1)`, pq.Array(ids)).Scan(&leases))
	return states, leases
}

func TestCodexStateBatchPostgresOwnerFilterOrderingAndReadOnly(t *testing.T) {
	ctx := context.Background()
	repo := NewOpenAICodexStateRepository(integrationDB, integrationRedis)
	owners := []service.CodexTurnStateKey{createCodexStateFixture(t), createCodexStateFixture(t), createCodexStateFixture(t)}
	ids := []int64{owners[0].OwnerAccountID, owners[1].OwnerAccountID, owners[2].OwnerAccountID}
	now := time.Now().UTC().Truncate(time.Microsecond)
	for i, owner := range owners {
		// Insert models in reverse order, and keep live business leases. The batch
		// status read must neither register a new attempt nor touch existing ones.
		for _, model := range []string{"z-model", "a-model"} {
			key := owner
			key.Model = model
			record, err := repo.BeginBusiness(ctx, key, fmt.Sprintf("batch-%d-%s", i, model), now, now.Add(time.Minute))
			require.NoError(t, err)
			require.NotNil(t, record)
			require.NoError(t, repo.MarkBusinessSent(ctx, key, now))
		}
	}
	beforeStates, beforeLeases := codexBatchSnapshot(t, ctx, ids)
	const missingOwnerID int64 = 1 << 62
	for range 3 {
		records, err := repo.ListByAccounts(ctx, []int64{ids[2], missingOwnerID, ids[0], ids[2]})
		require.NoError(t, err)
		require.Len(t, records, 4, "duplicate IDs must not duplicate rows or leak the unrequested middle owner")
		for i, want := range []struct {
			owner int64
			model string
		}{{ids[0], "a-model"}, {ids[0], "z-model"}, {ids[2], "a-model"}, {ids[2], "z-model"}} {
			require.Equal(t, want.owner, records[i].OwnerAccountID)
			require.Equal(t, want.model, records[i].Model)
			require.EqualValues(t, 1, records[i].Version)
			require.Equal(t, now, records[i].LastBusinessAt)
		}
	}
	missing, err := repo.ListByAccounts(ctx, []int64{missingOwnerID})
	require.NoError(t, err)
	require.Empty(t, missing)
	afterStates, afterLeases := codexBatchSnapshot(t, ctx, ids)
	require.Equal(t, beforeStates, afterStates, "status listing must not change versions, activity, or collection scheduling")
	require.Equal(t, beforeLeases, afterLeases, "status listing must not create, renew, or remove business leases")
}

func TestCodexStateBatchPostgresLiveAccountAndGenerationFilter(t *testing.T) {
	ctx := context.Background()
	repo := NewOpenAICodexStateRepository(integrationDB, integrationRedis)
	owners := []service.CodexTurnStateKey{createCodexStateFixture(t), createCodexStateFixture(t), createCodexStateFixture(t)}
	ids := []int64{owners[0].OwnerAccountID, owners[1].OwnerAccountID, owners[2].OwnerAccountID}
	now := time.Now().UTC().Truncate(time.Microsecond)
	for i, key := range owners {
		record, err := repo.BeginBusiness(ctx, key, fmt.Sprintf("live-filter-%d", i), now, now.Add(time.Minute))
		require.NoError(t, err)
		require.NotNil(t, record)
	}
	beforeStates, beforeLeases := codexBatchSnapshot(t, ctx, ids)
	assertOwners := func(expected ...int64) {
		t.Helper()
		records, err := repo.ListByAccounts(ctx, []int64{ids[2], ids[0], ids[1]})
		require.NoError(t, err)
		actual := make([]int64, 0, len(records))
		for _, record := range records {
			actual = append(actual, record.OwnerAccountID)
		}
		require.Equal(t, expected, actual)
	}
	assertOwners(ids[0], ids[1], ids[2])
	_, err := integrationDB.ExecContext(ctx, `UPDATE accounts SET extra=jsonb_set(extra,
		'{codex_turn_state,enabled}', 'false'::jsonb) WHERE id=$1`, ids[1])
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `UPDATE accounts SET extra=jsonb_set(extra,
		'{codex_turn_state_generation}', '"replacement-generation"'::jsonb) WHERE id=$1`, ids[2])
	require.NoError(t, err)
	assertOwners(ids[0])
	_, err = integrationDB.ExecContext(ctx, `UPDATE accounts SET type='apikey',
		extra=jsonb_set(extra, '{codex_turn_state,enabled}', 'true'::jsonb) WHERE id=$1`, ids[1])
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `UPDATE accounts SET platform='anthropic',
		extra=jsonb_set(extra, '{codex_turn_state_generation}', '"generation-1"'::jsonb) WHERE id=$1`, ids[2])
	require.NoError(t, err)
	assertOwners(ids[0])
	_, err = integrationDB.ExecContext(ctx, `UPDATE accounts SET type='oauth', deleted_at=NOW() WHERE id=$1`, ids[1])
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `UPDATE accounts SET platform='openai' WHERE id=$1`, ids[2])
	require.NoError(t, err)
	assertOwners(ids[0], ids[2])
	afterStates, afterLeases := codexBatchSnapshot(t, ctx, ids)
	require.Equal(t, beforeStates, afterStates)
	require.Equal(t, beforeLeases, afterLeases)
}

func TestCodexStateBatchPostgresEmptyIDsDoNotAccessStore(t *testing.T) {
	// sql.Open does not dial; closing immediately makes any attempted query fail
	// without contacting an address. Empty IDs must return before store access.
	closedDB, err := sql.Open("postgres", "")
	require.NoError(t, err)
	require.NoError(t, closedDB.Close())
	repo := NewOpenAICodexStateRepository(closedDB, integrationRedis)
	for _, ids := range [][]int64{nil, {}} {
		records, err := repo.ListByAccounts(context.Background(), ids)
		require.NoError(t, err)
		require.NotNil(t, records)
		require.Empty(t, records)
	}
	_, err = repo.ListByAccounts(context.Background(), []int64{1})
	require.ErrorContains(t, err, "database is closed", "the poisoned store must detect any non-empty read")
}
