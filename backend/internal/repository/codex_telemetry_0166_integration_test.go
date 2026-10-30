//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestCodexTelemetryPostgres0166SnapshotAndFrozenBatchRecovery(t *testing.T) {
	for _, current := range []bool{false, true} {
		name := "legacy"
		if current {
			name = "critical_path"
		}
		t.Run(name, func(t *testing.T) {
			store, key, now := telemetryStoreFixture(t)
			ctx := context.Background()
			params := map[string]any{"status": "completed", "duration_ms": 1000}
			metadata := map[string]any{"model": "gpt-6-astra", "version": "0.165.0"}
			fields := []string{"critical_path_duration_ms", "critical_path_inference_ms", "critical_path_auto_review_ms", "critical_path_user_wait_ms", "critical_path_tools_ms", "critical_path_overhead_ms", "critical_path_unknown_ms"}
			if current {
				params["critical_path_status"] = "disabled"
				for _, field := range fields {
					params[field] = nil
				}
				metadata["field_sources"] = map[string]string{"critical_path_status": "simulated"}
			}
			payload, err := json.Marshal(map[string]any{"events": []any{map[string]any{"event_type": "codex_turn_event", "event_params": params}}})
			require.NoError(t, err)
			profile, err := json.Marshal(metadata)
			require.NoError(t, err)
			batch := telemetryStoreBatch(uuid.NewString(), now)
			batch.Type, batch.Source, batch.Payload, batch.Metadata = "analytics", "simulated", payload, profile
			batch.ClientVersion = "0.165.0"
			const oldUnsealed = `{"explicit":true,"sealed":false,"profile":{"version":"0.165.0","simulation_enabled":true,"first_thread":true},"result":{"Status":"completed"}}`
			activity := telemetryStoreActivity("turn", "legacy-turn", oldUnsealed, now.Add(time.Minute))
			activityKey := service.CodexTelemetryActivityMapKey(activity.Kind, activity.Key)
			_, err = store.TransactPool(ctx, key, now, func(tx *service.CodexTelemetryPoolTransaction) error {
				tx.Activities[activityKey] = activity
				tx.Batches = append(tx.Batches, batch)
				return nil
			})
			require.NoError(t, err)
			restarted := NewCodexTelemetryStore(integrationDB)
			_, err = restarted.TransactPool(ctx, key, now.Add(time.Second), func(tx *service.CodexTelemetryPoolTransaction) error {
				require.JSONEq(t, oldUnsealed, string(tx.Activities[activityKey].Data), "restoration leaves unsealed facts for the runtime generator")
				return nil
			})
			require.NoError(t, err)
			claims, err := restarted.ClaimBatches(ctx, now.Add(time.Second), 1)
			require.NoError(t, err)
			require.Len(t, claims, 1)
			require.JSONEq(t, string(payload), string(claims[0].Payload))
			require.JSONEq(t, string(profile), string(claims[0].Metadata))
			// Recover an unsent claim across another process restart; neither
			// old payloads nor explicit nulls may be rewritten by the new code.
			reclaimed, err := NewCodexTelemetryStore(integrationDB).ClaimBatches(ctx, now.Add(33*time.Second), 1)
			require.NoError(t, err)
			require.Len(t, reclaimed, 1)
			require.Equal(t, claims[0].ID, reclaimed[0].ID)
			require.NotEqual(t, claims[0].ClaimID, reclaimed[0].ClaimID)
			require.Equal(t, claims[0].Payload, reclaimed[0].Payload)
			require.Equal(t, claims[0].Metadata, reclaimed[0].Metadata)
			require.NotContains(t, string(reclaimed[0].Payload), "field_sources")
			if !current {
				require.NotContains(t, string(reclaimed[0].Payload), "critical_path")
				require.NotContains(t, string(reclaimed[0].Metadata), "critical_path")
			}
		})
	}
}
