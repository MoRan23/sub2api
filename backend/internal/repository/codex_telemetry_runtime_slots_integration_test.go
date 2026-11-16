//go:build integration

package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	dbmigrations "github.com/Wei-Shaw/sub2api/migrations"
	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

func TestCodexTelemetryRuntimeSlotsMigration(t *testing.T) {
	tx := testTx(t)
	ctx := context.Background()
	schema := "telemetry_slots_" + uuid.NewString()
	_, err := tx.ExecContext(ctx, `CREATE SCHEMA "`+schema+`"; SET LOCAL search_path TO "`+schema+`";
		CREATE TABLE codex_telemetry_activities(kind text, state jsonb);
		INSERT INTO codex_telemetry_activities VALUES
		('turn','{"inflight":{"one":"2026-01-01T00:00:00Z","two":"2026-01-01T00:00:00Z"}}'),
		('turn','{"sealed":true,"inflight":{"one":"2026-01-01T00:00:00Z"}}'),
		('thread','{"initialized":true}')`)
	require.NoError(t, err)
	body, err := dbmigrations.FS.ReadFile("288_codex_telemetry_runtime_slots.sql")
	require.NoError(t, err)
	for range 2 {
		_, err = tx.ExecContext(ctx, string(body))
		require.NoError(t, err)
	}
	var slots int
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT sum(runtime_slots) FROM codex_telemetry_activities`).Scan(&slots))
	require.Equal(t, 4, slots)
	// A legacy writer that knows nothing about the new column stays correct.
	_, err = tx.ExecContext(ctx, `UPDATE codex_telemetry_activities SET state='{"incomplete":true}' WHERE runtime_slots=3`)
	require.NoError(t, err)
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT sum(runtime_slots) FROM codex_telemetry_activities`).Scan(&slots))
	require.Equal(t, 2, slots)
}

func TestCodexTelemetryRuntimeSlotsShapes(t *testing.T) {
	for _, tc := range []struct {
		kind, state string
		want        int
	}{
		{"turn", `{}`, 1}, {"turn", `null`, 1},
		{"turn", `{"inflight":{"a":"bad-time","b":null}}`, 3},
		{"turn", `{"sealed":true,"inflight":{"a":null}}`, 1},
		{"turn", `{"incomplete":true,"inflight":{"a":null}}`, 1},
		{"turn", `{"sealed":null,"incomplete":null,"inflight":null}`, 1},
		{"turn", `[]`, -1}, {"turn", `{"sealed":"true"}`, -1},
		{"turn", `{"incomplete":1}`, -1}, {"turn", `{"inflight":[]}`, -1},
		{"attempt", `{"finished":false,"turn_key":"t"}`, 1},
		{"attempt", `{"finished":null,"turn_key":null}`, 1},
		{"attempt", `true`, -1}, {"attempt", `{"finished":1}`, -1},
		{"attempt", `{"turn_key":[]}`, -1}, {"sampling", `[]`, 1},
	} {
		t.Run(tc.kind+tc.state, func(t *testing.T) {
			var got int
			require.NoError(t, integrationDB.QueryRow(`SELECT codex_telemetry_runtime_slots($1,$2::jsonb)`, tc.kind, tc.state).Scan(&got))
			require.Equal(t, tc.want, got)
		})
	}
	var marker sql.NullInt64
	require.NoError(t, integrationDB.QueryRow(`SELECT codex_telemetry_runtime_slots('thread','{}')`).Scan(&marker))
	require.False(t, marker.Valid)
}

func TestCodexTelemetryRuntimeSlotsChangesRollbackAndInstances(t *testing.T) {
	store, key, now := telemetryStoreFixture(t)
	ctx := context.Background()
	pool, err := store.TransactPool(ctx, key, now, func(tx *service.CodexTelemetryPoolTransaction) error {
		if err := tx.Activities.Put(telemetryStoreActivity("turn", "t", `{"inflight":{"a":"2026-01-01T00:00:00Z"}}`, time.Time{})); err != nil {
			return err
		}
		count, err := tx.Activities.RuntimeOccupancy()
		require.NoError(t, err)
		require.Equal(t, 2, count)
		if err := tx.Activities.Put(telemetryStoreActivity("turn", "t", `{"sealed":true}`, time.Time{})); err != nil {
			return err
		}
		count, err = tx.Activities.RuntimeOccupancy()
		require.NoError(t, err)
		require.Equal(t, 1, count)
		return nil
	})
	require.NoError(t, err)
	rolledBack := errors.New("test rollback")
	_, err = store.TransactPool(ctx, key, now, func(tx *service.CodexTelemetryPoolTransaction) error {
		require.NoError(t, tx.Activities.Delete("turn", "t"))
		count, err := tx.Activities.RuntimeOccupancy()
		require.NoError(t, err)
		require.Zero(t, count)
		return rolledBack
	})
	require.ErrorIs(t, err, rolledBack)
	var wg sync.WaitGroup
	errs := make(chan error, 12)
	for i := range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			other := NewCodexTelemetryStore(integrationDB)
			_, err := other.TransactPool(ctx, key, now, func(tx *service.CodexTelemetryPoolTransaction) error {
				if _, err := tx.Activities.RuntimeOccupancy(); err != nil {
					return err
				}
				return tx.Activities.Put(telemetryStoreActivity("sampling", fmt.Sprint(i), `{}`, time.Time{}))
			})
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	_, err = store.TransactPool(ctx, key, now, func(tx *service.CodexTelemetryPoolTransaction) error {
		count, err := tx.Activities.RuntimeOccupancy()
		require.NoError(t, err)
		require.Equal(t, 13, count)
		removed, err := tx.Activities.DeleteExpiredRuntime(now.Add(time.Hour))
		require.NoError(t, err)
		require.Equal(t, 13, removed)
		count, err = tx.Activities.RuntimeOccupancy()
		require.NoError(t, err)
		require.Zero(t, count)
		return nil
	})
	require.NoError(t, err)
	var remaining int
	require.NoError(t, integrationDB.QueryRow(`SELECT count(*) FROM codex_telemetry_activities WHERE pool_id=$1`, pool.ID).Scan(&remaining))
	require.Zero(t, remaining)
}

func TestCodexTelemetryRuntimeSlotsReuseWithinTransaction(t *testing.T) {
	_, key, now := telemetryStoreFixture(t)
	connector, err := pq.NewConnector(integrationDSN)
	require.NoError(t, err)
	counts := &telemetryBenchCounters{}
	db := sql.OpenDB(telemetryBenchConnector{connector, counts})
	t.Cleanup(func() { _ = db.Close() })
	store := NewCodexTelemetryStore(db)
	_, err = store.TransactPool(context.Background(), key, now, func(tx *service.CodexTelemetryPoolTransaction) error {
		require.NoError(t, tx.Activities.Put(telemetryStoreActivity("turn", "t", `{"sealed":true}`, time.Time{})))
		n, err := tx.Activities.RuntimeOccupancy()
		require.NoError(t, err)
		require.Equal(t, 1, n)
		before := counts.queries.Load()
		n, err = tx.Activities.RuntimeOccupancy()
		require.NoError(t, err)
		require.Equal(t, 1, n)
		require.Equal(t, before, counts.queries.Load(), "an unchanged transaction must reuse its capacity summary")

		removed, err := tx.Activities.DeleteExpiredRuntime(now.Add(time.Hour))
		require.NoError(t, err)
		require.Equal(t, 1, removed)
		before = counts.queries.Load()
		n, err = tx.Activities.RuntimeOccupancy()
		require.NoError(t, err)
		require.Zero(t, n)
		require.Equal(t, before, counts.queries.Load(), "completed deletions must adjust the existing summary without another scan")

		require.NoError(t, tx.Activities.Put(telemetryStoreActivity("thread", "marker", `{}`, time.Time{})))
		before = counts.queries.Load()
		n, err = tx.Activities.RuntimeOccupancy()
		require.NoError(t, err)
		require.Zero(t, n)
		require.Equal(t, before+1, counts.queries.Load(), "a marker write only flushes the changed row, without a runtime scan")
		return nil
	})
	require.NoError(t, err)
}
