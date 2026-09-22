//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// Exercise the upgrade boundary through actual PostgreSQL transactions and
// independent runtime instances. All senders are local; no telemetry leaves the test.
func TestCodexTelemetryPostgresMetricV1UpgradeAcrossInstancesAndRestart(t *testing.T) {
	store, key, now := telemetryStoreFixture(t)
	ctx := context.Background()
	t.Setenv("CODEX_TELEMETRY_ENABLED", "true")
	_, err := store.SyncPolicy(ctx, true, false, true)
	require.NoError(t, err)
	input := service.CodexTelemetryInput{
		AccountID: key.OwnerAccountID, OwnerAccountID: key.OwnerAccountID,
		OSFamily: key.OSFamily, CredentialOS: key.OSFamily, AuthorizationGeneration: "compatibility-authorization",
		InstallationID: key.InstallationID, ManagedInstallation: true,
		AccessToken: "local-test-token", ChatGPTAccountID: "local-test-workspace",
		UserAgent: "codex_cli_rs/0.155.1 (Windows 10.0.26200; x86_64)", Originator: "codex_cli_rs", Version: "0.155.1",
		SessionID: uuid.NewString(), ThreadID: uuid.NewString(), TurnID: uuid.NewString(), SamplingID: uuid.NewString(),
		Model: "gpt-6-astra", StartedAt: now,
	}
	accounts := telemetryRuntimeAccounts{account: &service.Account{
		ID: key.OwnerAccountID, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Status: service.StatusActive,
		OpenAIOAuthOSProfiles: &service.OpenAIOAuthOSProfiles{DefaultOS: key.OSFamily, Profiles: map[string]service.OpenAIOAuthOSProfile{
			key.OSFamily: {OSFamily: key.OSFamily, InstallationID: key.InstallationID, UserAgent: input.UserAgent},
		}},
	}, slots: map[string]*service.OpenAIOAuthOSCredential{key.OSFamily: {
		OwnerAccountID: key.OwnerAccountID, OSFamily: key.OSFamily, AuthorizationGeneration: input.AuthorizationGeneration,
		Revision: 1, Status: service.OpenAIOAuthAuthorizationAuthorized,
		Credentials: map[string]any{"access_token": input.AccessToken, "chatgpt_account_id": input.ChatGPTAccountID},
	}}}
	newRuntime := func() *service.CodexTelemetryService {
		runtime := service.NewCodexTelemetryService(nil)
		runtime.SetSender(func(context.Context, *http.Request, service.CodexTelemetryInput, bool) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("{}"))}, nil
		})
		runtime.SetPersistence(NewCodexTelemetryStore(integrationDB), accounts, nil)
		t.Cleanup(runtime.Stop)
		return runtime
	}
	finish := func(attempt *service.CodexTelemetryAttempt, count uint64) {
		buckets := make([]uint64, 35) // Official millisecond boundaries plus +Inf.
		buckets[1] = count
		attempt.Finish(service.CodexTelemetryResult{
			Status: "completed", HTTPStatus: http.StatusOK, DeliveryStatus: "delivered", FinishedAt: time.Now(),
			EventMetrics: []service.CodexTelemetryEventMetric{{Kind: "response.output_text.delta", Success: true,
				Count: count, WaitCount: count, WaitSumMS: float64(count), WaitMinMS: 1, WaitMaxMS: 1, WaitBuckets: buckets}},
		})
	}
	waitFinished := func(want int) {
		require.Eventually(t, func() bool {
			var count int
			err := integrationDB.QueryRowContext(ctx, `SELECT count(*) FROM codex_telemetry_activities a
				JOIN codex_telemetry_pools p ON p.id=a.pool_id WHERE p.owner_account_id=$1
				AND a.kind='attempt' AND (a.state->>'finished')::boolean`, key.OwnerAccountID).Scan(&count)
			return err == nil && count == want
		}, 8*time.Second, 20*time.Millisecond)
	}
	readSnapshot := func() map[string]any {
		var data []byte
		require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT a.state FROM codex_telemetry_activities a
			JOIN codex_telemetry_pools p ON p.id=a.pool_id WHERE p.owner_account_id=$1
			AND a.kind='metrics' AND a.activity_key='current'`, key.OwnerAccountID).Scan(&data))
		var snapshot map[string]any
		require.NoError(t, json.Unmarshal(data, &snapshot))
		return snapshot
	}
	writeSnapshot := func(snapshot map[string]any, due time.Time) {
		data, err := json.Marshal(snapshot)
		require.NoError(t, err)
		_, err = store.TransactPool(ctx, key, time.Now(), func(tx *service.CodexTelemetryPoolTransaction) error {
			a := telemetryStoreActivity("metrics", "current", string(data), due)
			tx.Activities[service.CodexTelemetryActivityMapKey(a.Kind, a.Key)] = a
			return nil
		})
		require.NoError(t, err)
	}

	first := newRuntime()
	attempt := first.Begin(ctx, input)
	require.NotNil(t, attempt)
	finish(attempt, 2)
	waitFinished(1)
	first.Stop()
	snapshot := readSnapshot()
	states := snapshot["states"].([]any)
	require.Len(t, states, 1)
	state := states[0].(map[string]any)
	collectedAt := state["collected_at"]
	clients := snapshot["clients"]
	pending := state["pending"].([]any)
	require.Len(t, pending, 2)
	for _, raw := range pending {
		metric := raw.(map[string]any)
		var attributes []any
		for _, attribute := range metric["attributes"].([]any) {
			if attribute.(map[string]any)["key"] != "kind" {
				attributes = append(attributes, attribute)
			}
		}
		metric["attributes"] = attributes
	}
	legacyNames := []string{"codex.app_server.codex_home.size_bytes", "codex.sqlite.logs.write.bytes", "codex.sqlite.logs.write.max_entry_bytes"}
	for _, name := range legacyNames {
		buckets := make([]uint64, 16)
		buckets[12] = 1
		pending = append(pending, map[string]any{"name": name, "kind": "histogram", "unit": "", "attributes": []any{},
			"started": now, "finished": now, "count": 1, "sum": 4096, "min": 4096, "max": 4096, "buckets": buckets})
	}
	state["pending"], snapshot["version"] = pending, 1
	writeSnapshot(snapshot, now.Add(time.Minute))

	// A batch sealed by the old binary is immutable even though its distribution
	// uses obsolete boundaries. Mark it sent through the real claim/CAS protocol.
	oldBatchID := uuid.NewString()
	_, err = store.TransactPool(ctx, key, now, func(tx *service.CodexTelemetryPoolTransaction) error {
		batch := telemetryStoreBatch(oldBatchID, now)
		batch.Payload = json.RawMessage(`{"resourceMetrics":[{"scopeMetrics":[{"metrics":[{"name":"codex.sqlite.logs.write.bytes","histogram":{"dataPoints":[{"count":"1","sum":4096,"explicitBounds":[0,5,10,25,50,75,100,250,500,750,1000,2500,5000,7500,10000],"bucketCounts":["0","0","0","0","0","0","0","0","0","0","0","0","1","0","0","0"]}]}}]}]}]}`)
		tx.Batches = append(tx.Batches, batch)
		return nil
	})
	require.NoError(t, err)
	claimed, err := store.ClaimBatches(ctx, time.Now(), 10)
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	require.Equal(t, oldBatchID, claimed[0].ID)
	ok, err := store.MarkSending(ctx, oldBatchID, claimed[0].ClaimID, claimed[0].PolicyEpoch, time.Now())
	require.NoError(t, err)
	require.True(t, ok)
	ok, err = store.CompleteBatch(ctx, oldBatchID, claimed[0].ClaimID, service.CodexTelemetryBatchResult{Status: "sent", HTTPStatus: 200}, time.Now())
	require.NoError(t, err)
	require.True(t, ok)
	var oldPayload string
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT payload::text FROM codex_telemetry_batches WHERE id=$1`, oldBatchID).Scan(&oldPayload))

	second, third := newRuntime(), newRuntime()
	var wg sync.WaitGroup
	for i, runtime := range []*service.CodexTelemetryService{second, third} {
		next := input
		next.ThreadID, next.TurnID, next.SamplingID, next.StartedAt = uuid.NewString(), uuid.NewString(), uuid.NewString(), time.Now()
		attempt := runtime.Begin(ctx, next)
		require.NotNil(t, attempt)
		wg.Add(1)
		go func(count uint64) { defer wg.Done(); finish(attempt, count) }(uint64(i + 3))
	}
	wg.Wait()
	waitFinished(3)
	second.Stop()
	third.Stop()
	snapshot = readSnapshot()
	require.EqualValues(t, 2, snapshot["version"])
	require.Equal(t, "legacy_byte_histogram_discarded", snapshot["compatibility_reason"])
	require.Equal(t, clients, snapshot["clients"], "upgrade must retain durable startup deduplication")
	states = snapshot["states"].([]any)
	require.Len(t, states, 1, "instances must share one aggregate partition")
	state = states[0].(map[string]any)
	require.Equal(t, collectedAt, state["collected_at"], "restart must retain the delta window")
	require.EqualValues(t, 3, state["attempts"])
	pending = state["pending"].([]any)
	byKind := map[string]float64{}
	for _, raw := range pending {
		metric := raw.(map[string]any)
		require.NotContains(t, legacyNames, metric["name"])
		if metric["name"] != "codex.sse_event" {
			continue
		}
		for _, rawAttribute := range metric["attributes"].([]any) {
			attribute := rawAttribute.(map[string]any)
			if attribute["key"] == "kind" {
				byKind[attribute["value"].(map[string]any)["stringValue"].(string)] += metric["sum"].(float64)
			}
		}
	}
	require.Equal(t, map[string]float64{"unknown": 2, "response.output_text.delta": 7}, byKind)

	// Advance only the stored metric collection window; a new process seals it
	// without any synthetic business request or reliance on Redis notification.
	state["collected_at"] = time.Now().Add(-61 * time.Second)
	writeSnapshot(snapshot, time.Now().Add(-time.Second))
	fourth := newRuntime()
	require.Eventually(t, func() bool {
		var count int
		err := integrationDB.QueryRowContext(ctx, `SELECT count(*) FROM codex_telemetry_batches b
			JOIN codex_telemetry_pools p ON p.id=b.pool_id WHERE p.owner_account_id=$1 AND b.type='metrics' AND b.id<>$2`, key.OwnerAccountID, oldBatchID).Scan(&count)
		return err == nil && count == 1
	}, 8*time.Second, 20*time.Millisecond)
	fourth.Stop()
	var payload []byte
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT b.payload FROM codex_telemetry_batches b
		JOIN codex_telemetry_pools p ON p.id=b.pool_id WHERE p.owner_account_id=$1 AND b.type='metrics' AND b.id<>$2`, key.OwnerAccountID, oldBatchID).Scan(&payload))
	require.NotContains(t, string(payload), "legacy_byte_histogram_discarded", "migration diagnostics are local only")
	var events, waits int64
	for _, metric := range gjson.GetBytes(payload, "resourceMetrics.0.scopeMetrics.0.metrics").Array() {
		require.NotContains(t, legacyNames, metric.Get("name").String())
		switch metric.Get("name").String() {
		case "codex.sse_event":
			for _, point := range metric.Get("sum.dataPoints").Array() {
				events += point.Get("asInt").Int()
			}
		case "codex.sse_event.duration_ms":
			for _, point := range metric.Get("histogram.dataPoints").Array() {
				waits += point.Get("count").Int()
			}
		}
	}
	require.EqualValues(t, 9, events, "old and new observations must not be lost or double counted")
	require.EqualValues(t, 9, waits, "unchanged duration buckets must survive migration and sealing")
	var finalOldPayload, finalOldStatus string
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT payload::text,status FROM codex_telemetry_batches WHERE id=$1`, oldBatchID).Scan(&finalOldPayload, &finalOldStatus))
	require.Equal(t, oldPayload, finalOldPayload)
	require.Equal(t, "sent", finalOldStatus)
}
