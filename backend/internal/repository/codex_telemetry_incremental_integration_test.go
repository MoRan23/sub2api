//go:build integration

package repository

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestCodexTelemetryIncrementalDueOrderMatchesMemory(t *testing.T) {
	store, key, now := telemetryStoreFixture(t)
	keys := []string{"中", "z", "é", "A", "😀", "a", "Z", "ä"}
	_, err := store.TransactPool(context.Background(), key, now, func(tx *service.CodexTelemetryPoolTransaction) error {
		for _, name := range keys {
			require.NoError(t, tx.Activities.Put(telemetryStoreActivity("turn", name, `{}`, now)))
		}
		sort.Strings(keys)
		for _, limit := range []int{0, 3} {
			rows, err := tx.Activities.ListActiveTurns(now, limit)
			require.NoError(t, err)
			var got []string
			for _, row := range rows {
				got = append(got, row.Key)
			}
			want := keys
			if limit > 0 {
				want = want[:limit]
			}
			require.Equal(t, want, got, "SQL ties must match Go byte ordering for every database locale")
		}
		return nil
	})
	require.NoError(t, err)
}

func TestCodexTelemetryIncrementalReadsAndStagedQueries(t *testing.T) {
	store, key, now := telemetryStoreFixture(t)
	_, err := store.TransactPool(context.Background(), key, now, func(tx *service.CodexTelemetryPoolTransaction) error {
		activities := tx.Activities
		_, found, err := activities.Get("turn", "due-a")
		require.NoError(t, err)
		require.False(t, found)
		_, found, err = activities.Get("turn", "")
		require.NoError(t, err)
		require.False(t, found, "an empty lookup key is a miss")
		a := telemetryStoreActivity("turn", "due-a", `{"inflight":{}}`, now)
		require.NoError(t, activities.Put(a))
		a.Data[0] = '['
		loaded, found, err := activities.Get("turn", "due-a")
		require.NoError(t, err)
		require.True(t, found, "Put must replace a negative cache entry")
		require.JSONEq(t, `{"inflight":{}}`, string(loaded.Data), "Put must detach caller bytes")
		loaded.Data[0] = '['
		for _, item := range []service.CodexTelemetryActivity{
			telemetryStoreActivity("turn", "due-b", `{"inflight":{"request":"2026-01-01T00:00:00Z"}}`, now),
			telemetryStoreActivity("turn", "future", `{}`, now.Add(time.Hour)),
			telemetryStoreActivity("turn", "undated", `{}`, time.Time{}),
			telemetryStoreActivity("turn", "closed", `{"sealed":true,"inflight":{"request":"2026-01-01T00:00:00Z"}}`, now),
			telemetryStoreActivity("sampling", "sample", `{}`, time.Time{}),
			telemetryStoreActivity("thread", "marker", `{"initialized":true}`, time.Time{}),
		} {
			require.NoError(t, activities.Put(item))
		}
		many, err := activities.GetMany([]service.CodexTelemetryActivityKey{
			{Kind: "turn", Key: "due-a"}, {Kind: "turn", Key: "due-b"},
			{Kind: "turn", Key: "due-a"}, {Kind: "turn", Key: "missing"},
		})
		require.NoError(t, err)
		require.Len(t, many, 2, "duplicates and missing keys must not create extra entries")
		require.JSONEq(t, `{"inflight":{}}`, string(many[service.CodexTelemetryActivityMapKey("turn", "due-a")].Data), "Get must detach returned bytes")
		many[service.CodexTelemetryActivityMapKey("turn", "due-b")].Data[0] = '['
		loaded, _, err = activities.Get("turn", "due-b")
		require.NoError(t, err)
		require.JSONEq(t, `{"inflight":{"request":"2026-01-01T00:00:00Z"}}`, string(loaded.Data), "GetMany must detach returned bytes")
		due, err := activities.ListActiveTurns(now, 1)
		require.NoError(t, err)
		require.Len(t, due, 1)
		require.Equal(t, "due-a", due[0].Key)
		due[0].Data[0] = '['
		loaded, _, err = activities.Get("turn", "due-a")
		require.NoError(t, err)
		require.JSONEq(t, `{"inflight":{}}`, string(loaded.Data), "query results must detach cached bytes")
		due, err = activities.ListActiveTurns(now, 0)
		require.NoError(t, err)
		require.Len(t, due, 2, "due scan includes the boundary but excludes future, undated and sealed turns")
		active, err := activities.ListActiveTurns(time.Time{}, 0)
		require.NoError(t, err)
		require.Len(t, active, 4, "policy scans include undated active turns")
		occupancy, err := activities.RuntimeOccupancy()
		require.NoError(t, err)
		require.Equal(t, 7, occupancy, "only active turns reserve their inflight slots")
		require.NoError(t, activities.Put(telemetryStoreActivity("turn", "due-b", `{"sealed":true,"inflight":{"request":"2026-01-01T00:00:00Z"}}`, now)))
		require.NoError(t, activities.Delete("turn", "future"))
		occupancy, err = activities.RuntimeOccupancy()
		require.NoError(t, err)
		require.Equal(t, 5, occupancy, "aggregates must include staged updates and deletes")
		_, found, err = activities.Get("turn", "future")
		require.NoError(t, err)
		require.False(t, found)
		return nil
	})
	require.NoError(t, err)
}

func TestCodexTelemetryIncrementalExpiryEligibilityAndCache(t *testing.T) {
	store, key, now := telemetryStoreFixture(t)
	ctx := context.Background()
	cutoff := now.Add(-7 * 24 * time.Hour)
	removed := []service.CodexTelemetryActivity{
		telemetryStoreActivity("sampling", "equal", `{}`, time.Time{}),
		telemetryStoreActivity("attempt", "finished", `{"finished":true,"turn_key":"active"}`, time.Time{}),
		telemetryStoreActivity("turn", "sealed", `{"sealed":true}`, time.Time{}),
		telemetryStoreActivity("turn", "incomplete", `{"incomplete":true}`, time.Time{}),
		telemetryStoreActivity("attempt", "sealed-parent", `{"turn_key":"sealed"}`, time.Time{}),
		telemetryStoreActivity("attempt", "incomplete-parent", `{"turn_key":"incomplete"}`, time.Time{}),
		telemetryStoreActivity("attempt", "orphan", `{"turn_key":"absent"}`, time.Time{}),
	}
	retained := []service.CodexTelemetryActivity{
		telemetryStoreActivity("turn", "active", `{"inflight":{"request":"2026-01-01T00:00:00Z"}}`, cutoff),
		telemetryStoreActivity("attempt", "live", `{"turn_key":"active"}`, time.Time{}),
		telemetryStoreActivity("thread", "marker", `{"initialized":true}`, time.Time{}),
		telemetryStoreActivity("metrics", "marker", `{}`, time.Time{}),
		telemetryStoreActivity("policy", "marker", `{"epoch":1}`, time.Time{}),
	}
	_, err := store.TransactPool(ctx, key, cutoff, func(tx *service.CodexTelemetryPoolTransaction) error {
		for _, items := range [][]service.CodexTelemetryActivity{removed, retained} {
			for _, item := range items {
				if err := tx.Activities.Put(item); err != nil {
					return err
				}
			}
		}
		return nil
	})
	require.NoError(t, err)
	_, err = store.TransactPool(ctx, key, cutoff.Add(time.Microsecond), func(tx *service.CodexTelemetryPoolTransaction) error {
		return tx.Activities.Put(telemetryStoreActivity("sampling", "newer", `{}`, time.Time{}))
	})
	require.NoError(t, err)
	_, err = store.TransactPool(ctx, key, now, func(tx *service.CodexTelemetryPoolTransaction) error {
		for _, item := range removed {
			_, found, err := tx.Activities.Get(item.Kind, item.Key)
			require.NoError(t, err)
			require.True(t, found, "prime the positive cache before SQL deletion")
		}
		count, err := tx.Activities.DeleteExpiredRuntime(cutoff)
		require.NoError(t, err)
		require.Equal(t, len(removed), count, "expiry must include updated_at exactly at cutoff")
		for _, item := range removed {
			_, found, err := tx.Activities.Get(item.Kind, item.Key)
			require.NoError(t, err)
			require.False(t, found, "DELETE RETURNING must invalidate %s/%s", item.Kind, item.Key)
		}
		for _, item := range retained {
			_, found, err := tx.Activities.Get(item.Kind, item.Key)
			require.NoError(t, err)
			require.True(t, found, "expiry must retain %s/%s", item.Kind, item.Key)
		}
		_, found, err := tx.Activities.Get("sampling", "newer")
		require.NoError(t, err)
		require.True(t, found)
		count, err = tx.Activities.DeleteExpiredRuntime(cutoff)
		require.NoError(t, err)
		require.Zero(t, count)
		occupancy, err := tx.Activities.RuntimeOccupancy()
		require.NoError(t, err)
		require.Equal(t, 4, occupancy)
		require.NoError(t, tx.Activities.Put(removed[0]))
		_, found, err = tx.Activities.Get(removed[0].Kind, removed[0].Key)
		require.NoError(t, err)
		require.True(t, found, "a returned deletion must allow an explicit replacement")
		return nil
	})
	require.NoError(t, err)
}

func TestCodexTelemetryIncrementalEvictionOrderAndProtection(t *testing.T) {
	store, key, now := telemetryStoreFixture(t)
	ctx := context.Background()
	_, err := store.TransactPool(ctx, key, now.Add(-time.Second), func(tx *service.CodexTelemetryPoolTransaction) error {
		return tx.Activities.Put(telemetryStoreActivity("sampling", "oldest", `{}`, time.Time{}))
	})
	require.NoError(t, err)
	_, err = store.TransactPool(ctx, key, now, func(tx *service.CodexTelemetryPoolTransaction) error {
		for _, item := range []service.CodexTelemetryActivity{
			telemetryStoreActivity("turn", "sealed", `{"sealed":true}`, time.Time{}),
			telemetryStoreActivity("turn", "incomplete", `{"incomplete":true}`, time.Time{}),
			telemetryStoreActivity("turn", "live", `{}`, time.Time{}),
			telemetryStoreActivity("attempt", "A", `{"turn_key":"sealed"}`, time.Time{}),
			telemetryStoreActivity("attempt", "a", `{"turn_key":"incomplete"}`, time.Time{}),
			telemetryStoreActivity("attempt", "z", `{"turn_key":"missing"}`, time.Time{}),
			telemetryStoreActivity("attempt", "live", `{"turn_key":"live"}`, time.Time{}),
			telemetryStoreActivity("sampling", "sample", `{}`, time.Time{}),
		} {
			if err := tx.Activities.Put(item); err != nil {
				return err
			}
		}
		return nil
	})
	require.NoError(t, err)
	_, err = store.TransactPool(ctx, key, now.Add(time.Second), func(tx *service.CodexTelemetryPoolTransaction) error {
		return tx.Activities.Put(telemetryStoreActivity("sampling", "newest", `{}`, time.Time{}))
	})
	require.NoError(t, err)
	_, err = store.TransactPool(ctx, key, now.Add(time.Minute), func(tx *service.CodexTelemetryPoolTransaction) error {
		protected := []service.CodexTelemetryActivityKey{{Kind: "attempt", Key: "A"}}
		assertPresent := func(kind, key string, want bool) {
			_, found, err := tx.Activities.Get(kind, key)
			require.NoError(t, err)
			require.Equal(t, want, found, "%s/%s", kind, key)
		}
		for _, name := range []string{"A", "a", "z", "live"} {
			assertPresent("attempt", name, true)
		}
		count, err := tx.Activities.EvictCompletedRuntime(0, nil)
		require.NoError(t, err)
		require.Zero(t, count)
		for _, step := range []struct {
			limit int
			kind  string
			keys  []string
		}{
			{1, "sampling", []string{"oldest"}},
			{2, "attempt", []string{"a", "z"}},
			{1, "sampling", []string{"sample"}},
			{2, "turn", []string{"incomplete", "sealed"}},
		} {
			count, err = tx.Activities.EvictCompletedRuntime(step.limit, protected)
			require.NoError(t, err)
			require.Equal(t, step.limit, count)
			for _, name := range step.keys {
				assertPresent(step.kind, name, false)
			}
			assertPresent("attempt", "A", true)
			assertPresent("sampling", "newest", true)
			assertPresent("turn", "live", true)
			assertPresent("attempt", "live", true)
		}
		count, err = tx.Activities.EvictCompletedRuntime(100, nil)
		require.NoError(t, err)
		require.Equal(t, 2, count, "removing protection permits the orphaned attempt and newest sample")
		assertPresent("attempt", "A", false)
		occupancy, err := tx.Activities.RuntimeOccupancy()
		require.NoError(t, err)
		require.Equal(t, 2, occupancy)
		return nil
	})
	require.NoError(t, err)
}

func TestCodexTelemetryIncrementalMalformedRuntimeState(t *testing.T) {
	for _, test := range []struct {
		name, kind, data string
		aggregateFails   bool
	}{
		{"sealed_string", "turn", `{"sealed":"true"}`, true},
		{"incomplete_number", "turn", `{"incomplete":1}`, true},
		{"inflight_array", "turn", `{"inflight":[]}`, true},
		{"inflight_boolean", "turn", `{"inflight":true}`, true},
		{"inflight_string", "turn", `{"inflight":"invalid"}`, true},
		{"finished_string", "attempt", `{"finished":"false"}`, true},
		{"finished_array", "attempt", `{"finished":[]}`, true},
		{"turn_key_number", "attempt", `{"turn_key":1}`, true},
		{"turn_array", "turn", `[]`, true},
		{"attempt_boolean", "attempt", `true`, true},
		{"last_seen_invalid", "turn", `{"last_seen":"invalid"}`, false},
		{"inflight_time_invalid", "turn", `{"inflight":{"request":"invalid"}}`, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, key, now := telemetryStoreFixture(t)
			ctx := context.Background()
			pool, err := store.TransactPool(ctx, key, now, func(*service.CodexTelemetryPoolTransaction) error { return nil })
			require.NoError(t, err)
			_, err = integrationDB.ExecContext(ctx, `INSERT INTO codex_telemetry_activities(pool_id,kind,activity_key,state,updated_at)
				VALUES($1::uuid,$2,'malformed',$3::jsonb,$4)`, pool.ID, test.kind, test.data, now)
			require.NoError(t, err)
			_, err = store.TransactPool(ctx, key, now, func(tx *service.CodexTelemetryPoolTransaction) error {
				_, _, err := tx.Activities.Get(test.kind, "malformed")
				return err
			})
			require.ErrorIs(t, err, service.ErrCodexTelemetryInvalidState, "point reads must validate full runtime types")
			_, err = store.TransactPool(ctx, key, now, func(tx *service.CodexTelemetryPoolTransaction) error {
				return tx.Activities.Put(telemetryStoreActivity(test.kind, "new", test.data, time.Time{}))
			})
			require.ErrorIs(t, err, service.ErrCodexTelemetryInvalidState, "writes must validate full runtime types")
			if test.kind == "turn" {
				_, err = store.TransactPool(ctx, key, now, func(tx *service.CodexTelemetryPoolTransaction) error {
					_, err := tx.Activities.ListActiveTurns(time.Time{}, 0)
					return err
				})
				require.ErrorIs(t, err, service.ErrCodexTelemetryInvalidState, "selected turns must validate full runtime types")
			}
			_, err = store.TransactPool(ctx, key, now, func(tx *service.CodexTelemetryPoolTransaction) error {
				_, err := tx.Activities.RuntimeOccupancy()
				return err
			})
			if !test.aggregateFails {
				require.NoError(t, err, "aggregate validation concerns fields consumed by SQL")
				return
			}
			require.ErrorIs(t, err, service.ErrCodexTelemetryInvalidState)
			for _, operation := range []func(service.CodexTelemetryActivityTransaction) error{
				func(a service.CodexTelemetryActivityTransaction) error {
					_, err := a.DeleteExpiredRuntime(now)
					return err
				},
				func(a service.CodexTelemetryActivityTransaction) error {
					_, err := a.EvictCompletedRuntime(1, nil)
					return err
				},
			} {
				_, err = store.TransactPool(ctx, key, now, func(tx *service.CodexTelemetryPoolTransaction) error { return operation(tx.Activities) })
				require.ErrorIs(t, err, service.ErrCodexTelemetryInvalidState, "destructive aggregates must reject malformed consumed fields")
			}
		})
	}
}

func TestCodexTelemetryIncrementalRollbackAfterAggregateFlush(t *testing.T) {
	store, key, now := telemetryStoreFixture(t)
	ctx := context.Background()
	priorDue := now.Add(time.Hour)
	pool, err := store.TransactPool(ctx, key, now.Add(-time.Hour), func(tx *service.CodexTelemetryPoolTransaction) error {
		if err := tx.Activities.Put(telemetryStoreActivity("turn", "original", `{"inflight":{}}`, priorDue)); err != nil {
			return err
		}
		return tx.Activities.Put(telemetryStoreActivity("sampling", "original", `{}`, time.Time{}))
	})
	require.NoError(t, err)
	batchID := uuid.NewString()
	abort := errors.New("abort after SQL aggregate flush")
	_, err = store.TransactPool(ctx, key, now, func(tx *service.CodexTelemetryPoolTransaction) error {
		tx.Pool.LastBusinessAt = now
		for _, item := range []service.CodexTelemetryActivity{
			telemetryStoreActivity("turn", "original", `{"inflight":{"one":"2026-01-01T00:00:00Z","two":"2026-01-01T00:00:00Z"}}`, now),
			telemetryStoreActivity("attempt", "new", `{"turn_key":"missing"}`, time.Time{}),
			telemetryStoreActivity("sampling", "new", `{}`, time.Time{}),
		} {
			require.NoError(t, tx.Activities.Put(item))
		}
		require.NoError(t, tx.Activities.Delete("sampling", "original"))
		occupancy, err := tx.Activities.RuntimeOccupancy()
		require.NoError(t, err)
		require.Equal(t, 5, occupancy)
		due, err := tx.Activities.ListActiveTurns(now, 128)
		require.NoError(t, err)
		require.Len(t, due, 1)
		removed, err := tx.Activities.DeleteExpiredRuntime(now)
		require.NoError(t, err)
		require.Equal(t, 2, removed)
		tx.Batches = append(tx.Batches, telemetryStoreBatch(batchID, now))
		return abort
	})
	require.ErrorIs(t, err, abort)
	var version int64
	var due time.Time
	var business sql.NullTime
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT version,next_due_at,last_business_at
		FROM codex_telemetry_pools WHERE id=$1::uuid`, pool.ID).Scan(&version, &due, &business))
	require.Equal(t, pool.Version, version)
	require.True(t, priorDue.Equal(due))
	require.False(t, business.Valid)
	var original []byte
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT state FROM codex_telemetry_activities
		WHERE pool_id=$1::uuid AND kind='turn' AND activity_key='original'`, pool.ID).Scan(&original))
	require.JSONEq(t, `{"inflight":{}}`, string(original))
	var count int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT count(*) FROM codex_telemetry_activities
		WHERE pool_id=$1::uuid AND activity_key='original'`, pool.ID).Scan(&count))
	require.Equal(t, 2, count, "flushed row updates and deletes must roll back")
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT count(*) FROM codex_telemetry_activities
		WHERE pool_id=$1::uuid AND activity_key='new'`, pool.ID).Scan(&count))
	require.Zero(t, count)
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT count(*) FROM codex_telemetry_batches WHERE id=$1::uuid`, batchID).Scan(&count))
	require.Zero(t, count)
}

func TestCodexTelemetryIncrementalRestoredWritesPreserveOriginalTimestamp(t *testing.T) {
	store, key, now := telemetryStoreFixture(t)
	ctx := context.Background()
	originalTime, originalDue := now.Add(-time.Hour), now.Add(time.Hour)
	pool, err := store.TransactPool(ctx, key, originalTime, func(tx *service.CodexTelemetryPoolTransaction) error {
		return tx.Activities.Put(telemetryStoreActivity("sampling", "original", `{"count":1}`, originalDue))
	})
	require.NoError(t, err)
	for _, operation := range []string{"staged_update", "flushed_update", "staged_delete", "flushed_delete", "expired"} {
		t.Run(operation, func(t *testing.T) {
			_, err := store.TransactPool(ctx, key, now, func(tx *service.CodexTelemetryPoolTransaction) error {
				original, found, err := tx.Activities.Get("sampling", "original")
				require.NoError(t, err)
				require.True(t, found)
				require.True(t, originalTime.Equal(original.UpdatedAt))
				switch operation {
				case "staged_update", "flushed_update":
					changed := original
					changed.Data = []byte(`{"count":2}`)
					changed.DueAt = now.Add(-time.Minute)
					require.NoError(t, tx.Activities.Put(changed))
				case "staged_delete", "flushed_delete":
					require.NoError(t, tx.Activities.Delete(original.Kind, original.Key))
				case "expired":
					removed, err := tx.Activities.DeleteExpiredRuntime(originalTime)
					require.NoError(t, err)
					require.Equal(t, 1, removed)
					_, found, err := tx.Activities.Get(original.Kind, original.Key)
					require.NoError(t, err)
					require.False(t, found)
				}
				if operation == "flushed_update" || operation == "flushed_delete" {
					_, err := tx.Activities.RuntimeOccupancy()
					require.NoError(t, err)
				}
				require.NoError(t, tx.Activities.Put(original))
				if operation == "flushed_update" || operation == "flushed_delete" || operation == "expired" {
					count, err := tx.Activities.RuntimeOccupancy()
					require.NoError(t, err)
					require.Equal(t, 1, count)
				}
				restored, found, err := tx.Activities.Get(original.Kind, original.Key)
				require.NoError(t, err)
				require.True(t, found)
				require.Equal(t, original.Data, restored.Data)
				require.True(t, originalTime.Equal(restored.UpdatedAt), "restoring the original value must retain its age")
				require.True(t, originalDue.Equal(restored.DueAt))
				return nil
			})
			require.NoError(t, err)
			var updated, due, nextDue time.Time
			require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT a.updated_at,a.due_at,p.next_due_at
				FROM codex_telemetry_activities a JOIN codex_telemetry_pools p ON p.id=a.pool_id
				WHERE a.pool_id=$1::uuid AND a.kind='sampling' AND a.activity_key='original'`, pool.ID).Scan(&updated, &due, &nextDue))
			require.True(t, originalTime.Equal(updated))
			require.True(t, originalDue.Equal(due))
			require.True(t, originalDue.Equal(nextDue))
		})
	}
}
