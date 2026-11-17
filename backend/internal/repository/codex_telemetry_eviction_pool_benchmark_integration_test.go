//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// Compare the exact former predicate with the correlated parent lookup. Large,
// non-compressible turn state exposes work hidden by small/finished-only rows.
func BenchmarkCodexTelemetryCompletedRuntimeEvictionPlan(b *testing.B) {
	ctx := context.Background()
	tx, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(b, err)
	defer tx.Rollback()
	schema := "telemetry_eviction_bench_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	_, err = tx.ExecContext(ctx, `CREATE SCHEMA "`+schema+`"; SET LOCAL search_path TO "`+schema+`";
		SET LOCAL plan_cache_mode=force_generic_plan;
		CREATE TABLE codex_telemetry_activities (
			pool_id uuid NOT NULL, kind text NOT NULL, activity_key text NOT NULL,
			state jsonb NOT NULL, updated_at timestamptz NOT NULL,
			PRIMARY KEY(pool_id,kind,activity_key));
		CREATE INDEX runtime_age ON codex_telemetry_activities(pool_id,updated_at,kind,activity_key)
			WHERE kind IN ('turn','attempt','sampling')`)
	require.NoError(b, err)
	rng := rand.New(rand.NewPCG(17, 23))
	profile := make([]byte, 8*1024)
	for i := range profile {
		profile[i] = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"[rng.IntN(62)]
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO codex_telemetry_activities
		SELECT md5(p::text)::uuid,'turn','turn-'||n,jsonb_build_object('fixture',$1::text),NOW()
		FROM generate_series(1,32) p CROSS JOIN generate_series(1,128) n`, string(profile))
	require.NoError(b, err)
	_, err = tx.ExecContext(ctx, `INSERT INTO codex_telemetry_activities
		SELECT md5(p::text)::uuid,'attempt','attempt-'||lpad(n::text,4,'0'),jsonb_build_object('turn_key','turn-'||n),NOW()-INTERVAL '1 hour'
		FROM generate_series(1,32) p CROSS JOIN generate_series(1,3968) n;
		ANALYZE codex_telemetry_activities`)
	require.NoError(b, err)
	legacyCompleted := strings.ReplaceAll(codexTelemetryRuntimeCompleted, "parent.pool_id=$1::uuid AND ", "")
	legacyCompleted = strings.ReplaceAll(legacyCompleted, " OFFSET 0", "")
	for _, variant := range []struct{ name, predicate string }{
		{"legacy", legacyCompleted}, {"correlated", codexTelemetryRuntimeCompleted},
	} {
		name := "telemetry_eviction_bench_" + variant.name
		_, err := tx.ExecContext(ctx, `PREPARE `+name+`(uuid,int,text[],text[]) AS
			SELECT a.kind,a.activity_key FROM codex_telemetry_activities a
			WHERE a.pool_id=$1::uuid AND a.kind IN ('turn','attempt','sampling') AND `+variant.predicate+`
			AND NOT EXISTS (SELECT 1 FROM unnest($3::text[],$4::text[]) protected(kind,activity_key)
				WHERE protected.kind=a.kind AND protected.activity_key=a.activity_key)
			ORDER BY a.updated_at,a.kind COLLATE "C",a.activity_key COLLATE "C" LIMIT $2`)
		require.NoError(b, err)
		query := `EXECUTE ` + name + `(md5('1')::uuid,2,'{}','{}')`
		b.Run(variant.name, func(b *testing.B) {
			var plan []byte
			require.NoError(b, tx.QueryRowContext(ctx, `EXPLAIN (ANALYZE,BUFFERS,TIMING OFF,FORMAT JSON) `+query).Scan(&plan))
			var explained []struct {
				Plan          map[string]any `json:"Plan"`
				ExecutionTime float64        `json:"Execution Time"`
			}
			require.NoError(b, json.Unmarshal(plan, &explained))
			var inspect func(map[string]any)
			inspect = func(node map[string]any) {
				alias, _ := node["Alias"].(string)
				if alias == "parent" || strings.HasPrefix(alias, "parent_") {
					b.Logf("%s parent=%s rows=%v loops=%v index=%v", variant.name, node["Node Type"], node["Actual Rows"], node["Actual Loops"], node["Index Cond"])
				}
				children, _ := node["Plans"].([]any)
				for _, child := range children {
					inspect(child.(map[string]any))
				}
			}
			inspect(explained[0].Plan)
			b.Logf("%s explain_ms=%.3f shared_hits=%v shared_reads=%v", variant.name, explained[0].ExecutionTime, explained[0].Plan["Shared Hit Blocks"], explained[0].Plan["Shared Read Blocks"])
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				rows, err := tx.QueryContext(ctx, query)
				if err != nil {
					b.Fatal(err)
				}
				count := 0
				for rows.Next() {
					var kind, key string
					if err := rows.Scan(&kind, &key); err != nil {
						b.Fatal(err)
					}
					count++
				}
				err = rows.Err()
				_ = rows.Close()
				if err != nil {
					b.Fatal(err)
				}
				if count != 2 {
					b.Fatalf("victim count = %d, want 2", count)
				}
			}
		})
		_, err = tx.ExecContext(ctx, `DEALLOCATE `+name)
		require.NoError(b, err)
	}
}
