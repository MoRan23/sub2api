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

func TestCodexTelemetry0170PostgresReplayV3UpgradeV4Restart(t *testing.T) {
	t.Setenv("CODEX_TELEMETRY_ENABLED", "true")
	store, key, now := telemetryStoreFixture(t)
	runtime := telemetry0169Runtime(t, telemetry0169NoSendStore{store})
	input := service.CodexTelemetryInput{
		AccountID: key.OwnerAccountID, OwnerAccountID: key.OwnerAccountID, CredentialOS: key.OSFamily,
		AuthorizationGeneration: "fixture-generation", OSFamily: key.OSFamily, InstallationID: key.InstallationID,
		UserAgent: "codex_cli_rs/0.170.0 (Windows 10.0.26200; x86_64)", Originator: "codex_cli_rs", Version: "0.170.0",
		AccessToken: "fixture-only-never-sent", ChatGPTAccountID: "fixture-only-account",
		SessionID: uuid.NewString(), ThreadID: uuid.NewString(), TurnID: uuid.NewString(), SamplingID: uuid.NewString(),
		Model: "gpt-6-astra", StartedAt: now,
	}
	old := runtime.Begin(context.Background(), input)
	require.NotNil(t, old)
	old.Finish(service.CodexTelemetryResult{Status: "completed", HTTPStatus: 200, DeliveryStatus: "delivered", FinishedAt: now.Add(time.Millisecond)})
	require.Eventually(t, func() bool {
		_, snapshot, err := telemetry0169ReadMetrics(key)
		if err != nil {
			return false
		}
		for _, state := range snapshot.States {
			if state.Attempts > 0 {
				return true
			}
		}
		return false
	}, 5*time.Second, 10*time.Millisecond)
	runtime.Stop()
	// Model an unsealed v3 aggregate produced before replay collection existed.
	_, err := store.TransactPool(context.Background(), key, now, func(tx *service.CodexTelemetryPoolTransaction) error {
		activity, exists, err := tx.Activities.Get("metrics", "current")
		if err != nil {
			return err
		}
		require.True(t, exists)
		var snapshot map[string]json.RawMessage
		if err := json.Unmarshal(activity.Data, &snapshot); err != nil {
			return err
		}
		snapshot["version"] = json.RawMessage("3")
		activity.Data, err = json.Marshal(snapshot)
		if err != nil {
			return err
		}
		return tx.Activities.Put(activity)
	})
	require.NoError(t, err)

	for run := 0; run < 2; run++ {
		restarted := telemetry0169Runtime(t, telemetry0169NoSendStore{NewCodexTelemetryStore(integrationDB)})
		input.TurnID, input.SamplingID, input.StartedAt = uuid.NewString(), uuid.NewString(), time.Now()
		attempt := restarted.Begin(context.Background(), input)
		require.NotNil(t, attempt)
		result := service.CodexTelemetryResult{
			Status: "completed", HTTPStatus: 200, DeliveryStatus: "delivered", FinishedAt: time.Now(),
			ReasoningReplay: &service.CodexReasoningReplayEvidence{Report: "prefix_mismatch_reported", RemovedCounts: service.CodexReasoningRemovalCounts{PrefixMismatch: 3}},
		}
		attempt.Finish(result)
		attempt.Finish(result)
		require.Eventually(t, func() bool {
			_, snapshot, err := telemetry0169ReadMetrics(key)
			if err != nil || snapshot.Version != 4 {
				return false
			}
			var count float64
			for _, state := range snapshot.States {
				for _, metric := range state.Pending {
					if metric.Name == "codex.responses.reasoning_replay" {
						count += metric.Sum
					}
				}
			}
			return count == float64(run+1)
		}, 5*time.Second, 10*time.Millisecond)
		restarted.Stop()
	}
	raw, snapshot, err := telemetry0169ReadMetrics(key)
	require.NoError(t, err)
	require.Equal(t, 4, snapshot.Version)
	require.NotContains(t, string(raw), "fixture-only-never-sent")
	require.NotContains(t, string(raw), "input_transformations")
	var decoded struct {
		States []struct {
			Pending []struct {
				Name       string
				Count      uint64
				Sum        float64
				Buckets    []uint64
				Attributes []struct {
					Key   string
					Value struct {
						StringValue string `json:"stringValue"`
					}
				}
			}
		}
	}
	require.NoError(t, json.Unmarshal(raw, &decoded))
	reasons := map[string]uint64{}
	for _, state := range decoded.States {
		for _, metric := range state.Pending {
			if metric.Name != "codex.responses.reasoning_items_removed" {
				continue
			}
			require.Len(t, metric.Buckets, 11)
			for _, attribute := range metric.Attributes {
				if attribute.Key == "reason" {
					reasons[attribute.Value.StringValue] += metric.Count
					if attribute.Value.StringValue == "prefix_mismatch" {
						require.Equal(t, float64(6), metric.Sum)
					} else {
						require.Zero(t, metric.Sum)
					}
				}
			}
		}
	}
	require.Equal(t, map[string]uint64{"organization_mismatch": 2, "end_user_mismatch": 2, "model_mismatch": 2, "prefix_mismatch": 2, "turn_boundary": 2, "other": 2}, reasons)
}
