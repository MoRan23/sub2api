//go:build integration

package repository

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	dbmigrations "github.com/Wei-Shaw/sub2api/migrations"
	"github.com/stretchr/testify/require"
)

func TestMigration306CodexReasoningEffortText(t *testing.T) {
	ctx := context.Background()
	tx := testTx(t)
	// A transaction-local legacy table isolates the migration replay from other tests.
	_, err := tx.ExecContext(ctx, `CREATE TEMP TABLE usage_logs (
 id INTEGER PRIMARY KEY, reasoning_effort VARCHAR(20), requested_reasoning_effort VARCHAR(20)
) ON COMMIT DROP`)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `INSERT INTO usage_logs VALUES (1, 'max', 'xhigh'), (2, NULL, NULL)`)
	require.NoError(t, err)
	migration, err := dbmigrations.FS.ReadFile("306_codex_reasoning_effort_text.sql")
	require.NoError(t, err)
	for range 2 {
		_, err = tx.ExecContext(ctx, string(migration))
		require.NoError(t, err)
	}
	var actualType string
	for _, column := range []string{"reasoning_effort", "requested_reasoning_effort"} {
		require.NoError(t, tx.QueryRowContext(ctx, `SELECT format_type(atttypid, atttypmod)
FROM pg_attribute WHERE attrelid = 'usage_logs'::regclass AND attname = $1`, column).Scan(&actualType))
		require.Equal(t, "text", actualType)
	}
	var effective, requested string
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT reasoning_effort, requested_reasoning_effort FROM usage_logs WHERE id = 1`).Scan(&effective, &requested))
	require.Equal(t, "max", effective)
	require.Equal(t, "xhigh", requested)
	var nullsPreserved bool
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT reasoning_effort IS NULL AND requested_reasoning_effort IS NULL FROM usage_logs WHERE id = 2`).Scan(&nullsPreserved))
	require.True(t, nullsPreserved)
	custom := "provider_" + strings.Repeat("custom_effort_", 16)
	_, err = tx.ExecContext(ctx, `INSERT INTO usage_logs VALUES (3, $1, $2), (4, 'disabled', '18446744073709551615')`, custom, custom)
	require.NoError(t, err)
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT reasoning_effort, requested_reasoning_effort FROM usage_logs WHERE id = 3`).Scan(&effective, &requested))
	require.Equal(t, custom, effective)
	require.Equal(t, custom, requested)
}

func TestGatewayCacheOpenAIResponseEffortRoundTripAndIsolation(t *testing.T) {
	ctx := context.Background()
	rdb := testRedis(t)
	cache := NewGatewayCache(rdb).(service.OpenAIResponseEffortCache)
	state := service.OpenAIResponseEffortState{
		Version: 1, AccountID: 23, Identity: "test-only-identity", Override: true,
		Effort:    service.OpenAIEffortValue{Type: "number", Value: "18446744073709551615"},
		ExpiresAt: time.Now().Add(time.Minute).UnixMilli(),
	}
	require.NoError(t, cache.SetOpenAIResponseEffort(ctx, 5, 7, "resp_effort_shared", state, time.Minute))
	got, found, err := cache.GetOpenAIResponseEffort(ctx, 5, 7, "resp_effort_shared")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, state, got)
	for _, scope := range [][2]int64{{6, 7}, {5, 8}, {6, 8}} {
		_, found, err := cache.GetOpenAIResponseEffort(ctx, scope[0], scope[1], "resp_effort_shared")
		require.NoError(t, err)
		require.False(t, found)
	}
	// A new process-local store restores the small state from Redis unchanged.
	restarted := service.NewOpenAIWSStateStore(NewGatewayCache(rdb)).(service.OpenAIResponseEffortStore)
	got, found, err = restarted.GetResponseEffort(ctx, 5, 7, "resp_effort_shared")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, state, got)
	for _, value := range []string{"disabled", "provider_custom_value_with_more_than_twenty_characters"} {
		state.Effort = service.OpenAIEffortValue{Type: "string", Value: value}
		require.NoError(t, cache.SetOpenAIResponseEffort(ctx, 5, 7, "resp_effort_"+value, state, time.Minute))
		got, found, err = cache.GetOpenAIResponseEffort(ctx, 5, 7, "resp_effort_"+value)
		require.NoError(t, err)
		require.True(t, found)
		require.Equal(t, value, got.Effort.Value)
	}
}

func TestGatewayCacheOpenAIResponseEffortCorruptionAndExpiry(t *testing.T) {
	ctx := context.Background()
	rdb := testRedis(t)
	cache := NewGatewayCache(rdb).(service.OpenAIResponseEffortCache)
	for index, value := range []string{`{"version":`, `[]`, strings.Repeat(" ", 4097)} {
		responseID := fmt.Sprintf("resp_bad_effort_%d", index)
		key := "openai:response-effort:v1:5:7:" + responseID
		require.NoError(t, rdb.Set(ctx, key, value, time.Minute).Err())
		_, found, err := cache.GetOpenAIResponseEffort(ctx, 5, 7, responseID)
		require.NoError(t, err)
		require.False(t, found)
	}
	store := service.NewOpenAIWSStateStore(NewGatewayCache(rdb)).(service.OpenAIResponseEffortStore)
	state := service.OpenAIResponseEffortState{AccountID: 23, Identity: "test-only-identity", Override: true, Effort: service.OpenAIEffortValue{Type: "string", Value: "disabled"}}
	require.NoError(t, store.BindResponseEffort(ctx, 5, 7, "resp_expiring_effort", state, 300*time.Millisecond))
	_, found, err := store.GetResponseEffort(ctx, 5, 7, "resp_expiring_effort")
	require.NoError(t, err)
	require.True(t, found)
	restarted := service.NewOpenAIWSStateStore(NewGatewayCache(rdb)).(service.OpenAIResponseEffortStore)
	_, found, err = restarted.GetResponseEffort(ctx, 5, 7, "resp_expiring_effort")
	require.NoError(t, err)
	require.True(t, found)
	require.Eventually(t, func() bool {
		_, localFound, localErr := store.GetResponseEffort(ctx, 5, 7, "resp_expiring_effort")
		_, restoredFound, restoredErr := restarted.GetResponseEffort(ctx, 5, 7, "resp_expiring_effort")
		_, redisFound, redisErr := cache.GetOpenAIResponseEffort(ctx, 5, 7, "resp_expiring_effort")
		return localErr == nil && restoredErr == nil && redisErr == nil && !localFound && !restoredFound && !redisFound
	}, 3*time.Second, 20*time.Millisecond)
}
