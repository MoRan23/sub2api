//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestCodexTelemetryCompletedRuntimeParentPoolIsolation(t *testing.T) {
	for _, operation := range []string{"expiry", "eviction"} {
		t.Run(operation, func(t *testing.T) {
			store, key, now := telemetryStoreFixture(t)
			ctx := context.Background()
			otherKey := key
			otherKey.InstallationID = uuid.NewString()
			_, err := store.TransactPool(ctx, otherKey, now, func(tx *service.CodexTelemetryPoolTransaction) error {
				// An active turn in another pool must not retain this pool's
				// orphan or closed-parent attempts with the same turn key.
				for _, name := range []string{"orphan", "closed"} {
					if err := tx.Activities.Put(telemetryStoreActivity("turn", name, `{}`, time.Time{})); err != nil {
						return err
					}
				}
				return nil
			})
			require.NoError(t, err)
			_, err = store.TransactPool(ctx, key, now, func(tx *service.CodexTelemetryPoolTransaction) error {
				for _, item := range []service.CodexTelemetryActivity{
					telemetryStoreActivity("turn", "live", `{}`, time.Time{}),
					telemetryStoreActivity("turn", "closed", `{"sealed":true}`, time.Time{}),
					telemetryStoreActivity("attempt", "live", `{"turn_key":"live"}`, time.Time{}),
					telemetryStoreActivity("attempt", "orphan", `{"turn_key":"orphan"}`, time.Time{}),
					telemetryStoreActivity("attempt", "closed", `{"turn_key":"closed"}`, time.Time{}),
					telemetryStoreActivity("attempt", "finished", `{"turn_key":"live","finished":true}`, time.Time{}),
				} {
					if err := tx.Activities.Put(item); err != nil {
						return err
					}
				}
				return nil
			})
			require.NoError(t, err)
			_, err = store.TransactPool(ctx, key, now.Add(time.Minute), func(tx *service.CodexTelemetryPoolTransaction) error {
				var removed int
				var err error
				if operation == "expiry" {
					removed, err = tx.Activities.DeleteExpiredRuntime(now)
				} else {
					removed, err = tx.Activities.EvictCompletedRuntime(10, nil)
				}
				require.NoError(t, err)
				require.Equal(t, 4, removed)
				for _, name := range []string{"orphan", "closed", "finished", "live"} {
					_, found, err := tx.Activities.Get("attempt", name)
					require.NoError(t, err)
					require.Equal(t, name == "live", found, name)
				}
				return nil
			})
			require.NoError(t, err)
			_, err = store.TransactPool(ctx, otherKey, now.Add(time.Minute), func(tx *service.CodexTelemetryPoolTransaction) error {
				for _, name := range []string{"orphan", "closed"} {
					_, found, err := tx.Activities.Get("turn", name)
					require.NoError(t, err)
					require.True(t, found)
				}
				return nil
			})
			require.NoError(t, err)
		})
	}
}

func TestCodexTelemetryCompletedRuntimeGenericPlanBoundsParentPool(t *testing.T) {
	tx := testTx(t)
	ctx := context.Background()
	// A private schema keeps the plan's statistics and fixture independent of
	// other integration tests. Use the production key/index shapes.
	schema := "telemetry_eviction_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	_, err := tx.ExecContext(ctx, `CREATE SCHEMA "`+schema+`"; SET LOCAL search_path TO "`+schema+`";
		SET LOCAL plan_cache_mode=force_generic_plan;
		CREATE TABLE codex_telemetry_activities (
			pool_id uuid NOT NULL, kind text NOT NULL, activity_key text NOT NULL,
			state jsonb NOT NULL, updated_at timestamptz NOT NULL,
			PRIMARY KEY(pool_id,kind,activity_key));
		CREATE INDEX runtime_age ON codex_telemetry_activities(pool_id,updated_at,kind,activity_key)
			WHERE kind IN ('turn','attempt','sampling');
		INSERT INTO codex_telemetry_activities
		SELECT md5(p::text)::uuid,'turn','turn-'||n,jsonb_build_object('fixture',repeat('x',256)),NOW()
		FROM generate_series(1,128) p CROSS JOIN generate_series(1,128) n;
		INSERT INTO codex_telemetry_activities
		SELECT md5('1')::uuid,'attempt','attempt-'||lpad(n::text,4,'0'),jsonb_build_object('turn_key','turn-'||n),NOW()-INTERVAL '1 hour'
		FROM generate_series(1,256) n;
		ANALYZE codex_telemetry_activities`)
	require.NoError(t, err)
	// Leading unfinished attempts force the parent check to execute; a fixture
	// containing only finished attempts can hide an unbounded hashed SubPlan.
	query := `SELECT a.kind,a.activity_key FROM codex_telemetry_activities a
		WHERE a.pool_id=$1::uuid AND a.kind IN ('turn','attempt','sampling')
		AND ` + codexTelemetryRuntimeCompleted + `
		AND NOT EXISTS (SELECT 1 FROM unnest($3::text[],$4::text[]) protected(kind,activity_key)
			WHERE protected.kind=a.kind AND protected.activity_key=a.activity_key)
		ORDER BY a.updated_at,a.kind COLLATE "C",a.activity_key COLLATE "C" LIMIT $2`
	_, err = tx.ExecContext(ctx, `PREPARE telemetry_eviction_plan(uuid,int,text[],text[]) AS `+query)
	require.NoError(t, err)
	// PREPARE is connection scoped, whereas this fixture rolls back at cleanup.
	defer func() { _, _ = tx.ExecContext(ctx, `DEALLOCATE telemetry_eviction_plan`) }()
	var raw []byte
	require.NoError(t, tx.QueryRowContext(ctx, `EXPLAIN (ANALYZE,BUFFERS,TIMING OFF,FORMAT JSON)
		EXECUTE telemetry_eviction_plan(md5('1')::uuid,2,'{}','{}')`).Scan(&raw))
	var plans []struct {
		Plan map[string]any `json:"Plan"`
	}
	require.NoError(t, json.Unmarshal(raw, &plans))
	require.Len(t, plans, 1)
	parentScans := 0
	executedParentScans := 0
	var inspect func(map[string]any)
	inspect = func(node map[string]any) {
		alias, _ := node["Alias"].(string)
		if alias == "parent" || strings.HasPrefix(alias, "parent_") {
			parentScans++
			encoded, err := json.Marshal(node)
			if err != nil {
				t.Fatal(err)
			}
			// Covers an index condition as well as a bitmap child's index
			// condition or a scan filter; do not force a particular plan node.
			poolBound := strings.Contains(string(encoded), "pool_id = $1") || strings.Contains(string(encoded), "pool_id = a.pool_id")
			require.True(t, poolBound, "parent scan must be bounded by the current pool, directly or through the correlated row: %s", encoded)
			require.Contains(t, string(encoded), "activity_key = COALESCE", "parent scan must seek the referenced turn, not hash every turn in the pool: %s", encoded)
			if loops, ok := node["Actual Loops"].(float64); ok && loops > 0 {
				executedParentScans++
				require.LessOrEqual(t, node["Actual Rows"].(float64), float64(1))
			}
		}
		children, _ := node["Plans"].([]any)
		for _, child := range children {
			inspect(child.(map[string]any))
		}
	}
	inspect(plans[0].Plan)
	require.Positive(t, parentScans)
	require.Positive(t, executedParentScans)
	require.Equal(t, float64(2), plans[0].Plan["Actual Rows"])
	t.Logf("pool_bounded_eviction_plan=%s", raw)
	// Verify the same predicate also plans correctly in retention, where $2
	// is a cutoff timestamp rather than a victim limit.
	_, err = tx.ExecContext(ctx, `PREPARE telemetry_expiry_plan(uuid,timestamptz) AS
		SELECT a.kind,a.activity_key FROM codex_telemetry_activities a WHERE a.pool_id=$1::uuid
		AND a.kind IN ('turn','attempt','sampling') AND a.updated_at <= $2 AND `+codexTelemetryRuntimeCompleted)
	require.NoError(t, err)
	defer func() { _, _ = tx.ExecContext(ctx, `DEALLOCATE telemetry_expiry_plan`) }()
	require.NoError(t, tx.QueryRowContext(ctx, fmt.Sprintf(`EXPLAIN (FORMAT JSON)
		EXECUTE telemetry_expiry_plan(md5('1')::uuid,'%s')`, time.Now().UTC().Format(time.RFC3339))).Scan(&raw))
	require.NoError(t, json.Unmarshal(raw, &plans))
	inspect(plans[0].Plan)
}
