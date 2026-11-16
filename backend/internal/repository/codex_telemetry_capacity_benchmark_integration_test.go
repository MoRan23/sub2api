//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

// Retained completed turns carry realistic, non-compressible profile strings.
// Capacity checks must not repeatedly detoast those profiles on a small mutation.
func BenchmarkCodexTelemetryRuntimeCapacity(b *testing.B) {
	for _, size := range []int{32, 4096} {
		b.Run(fmt.Sprintf("states_%d", size), func(b *testing.B) {
			store, key, now := telemetryStoreFixture(b)
			ctx := context.Background()
			pool, err := store.TransactPool(ctx, key, now, func(*service.CodexTelemetryPoolTransaction) error { return nil })
			require.NoError(b, err)
			rng := rand.New(rand.NewPCG(17, 23))
			payload := make([]byte, 16*1024)
			for i := range payload {
				payload[i] = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"[rng.IntN(62)]
			}
			state, err := json.Marshal(map[string]any{"sealed": true, "inflight": map[string]any{}, "profile": map[string]any{"fixture": string(payload)}})
			require.NoError(b, err)
			_, err = integrationDB.ExecContext(ctx, `INSERT INTO codex_telemetry_activities(pool_id,kind,activity_key,state,updated_at)
				SELECT $1::uuid,'turn','retained-'||n,$2::jsonb,$3 FROM generate_series(1,$4) n`, pool.ID, state, now, size)
			require.NoError(b, err)
			_, err = integrationDB.ExecContext(ctx, `ANALYZE codex_telemetry_activities`)
			require.NoError(b, err)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_, err = store.TransactPool(ctx, key, now.Add(time.Duration(i)*time.Millisecond), func(tx *service.CodexTelemetryPoolTransaction) error {
					if _, err := tx.Activities.DeleteExpiredRuntime(now.Add(-7 * 24 * time.Hour)); err != nil {
						return err
					}
					if _, err := tx.Activities.RuntimeOccupancy(); err != nil {
						return err
					}
					return tx.Activities.Put(telemetryStoreActivity("sampling", "current", fmt.Sprintf(`{"iteration":%d}`, i), time.Time{}))
				})
				if err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
