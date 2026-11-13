//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// Keep the real PostgreSQL producer and maintenance paths while preventing the
// integration fixture from ever dispatching telemetry to an external endpoint.
type telemetry0169NoSendStore struct{ service.CodexTelemetryStore }

func (s telemetry0169NoSendStore) ClaimBatches(context.Context, time.Time, int) ([]service.CodexTelemetryBatch, error) {
	return nil, nil
}

type telemetry0169BlockedStore struct {
	telemetry0169NoSendStore
	once    sync.Once
	entered chan struct{}
	release chan struct{}
	done    chan error
}

func (s *telemetry0169BlockedStore) TransactPool(ctx context.Context, key service.CodexTelemetryPoolKey, now time.Time, fn func(*service.CodexTelemetryPoolTransaction) error) (*service.CodexTelemetryPool, error) {
	blocked := false
	s.once.Do(func() {
		blocked = true
		close(s.entered)
		select {
		case <-s.release:
		case <-ctx.Done():
		}
	})
	pool, err := s.CodexTelemetryStore.TransactPool(ctx, key, now, fn)
	if blocked {
		s.done <- err
	}
	return pool, err
}

func telemetry0169Runtime(t *testing.T, store service.CodexTelemetryStore) *service.CodexTelemetryService {
	t.Helper()
	runtime := service.NewCodexTelemetryService(nil)
	t.Cleanup(runtime.Stop)
	require.NoError(t, runtime.SetStore(store))
	return runtime
}

func telemetry0169ConnectInput(key service.CodexTelemetryPoolKey) service.CodexTelemetryConnectInput {
	return service.CodexTelemetryConnectInput{
		AccountID: key.OwnerAccountID, OwnerAccountID: key.OwnerAccountID,
		OSFamily: key.OSFamily, CredentialOS: key.OSFamily, InstallationID: key.InstallationID,
		AuthorizationGeneration: "fixture-generation", CredentialRevision: 1,
		SessionID: uuid.NewString(), Route: "wss://example.invalid/v1/responses#proxy=0",
		UserAgent: "codex_cli_rs/0.169.0 (Windows 10.0.26200; x86_64)", Originator: "codex_cli_rs",
		Version: "0.169.0", Model: "gpt-6-astra", ProductSKU: "codex", CodexResidency: "us",
	}
}

type telemetry0169MetricSnapshot struct {
	Version int `json:"version"`
	States  []struct {
		Turns    int `json:"turns"`
		Attempts int `json:"attempts"`
		Pending  []struct {
			Name       string           `json:"name"`
			Count      uint64           `json:"count"`
			Sum        float64          `json:"sum"`
			Attributes []map[string]any `json:"attributes"`
		} `json:"pending"`
	} `json:"states"`
}

func telemetry0169ReadMetrics(key service.CodexTelemetryPoolKey) ([]byte, telemetry0169MetricSnapshot, error) {
	var raw []byte
	err := integrationDB.QueryRowContext(context.Background(), `SELECT a.state FROM codex_telemetry_activities a
		JOIN codex_telemetry_pools p ON p.id=a.pool_id WHERE p.owner_account_id=$1 AND p.os_family=$2
		AND p.installation_id=$3 AND a.kind='metrics' AND a.activity_key='current'`,
		key.OwnerAccountID, key.OSFamily, key.InstallationID).Scan(&raw)
	var snapshot telemetry0169MetricSnapshot
	if err == nil {
		err = json.Unmarshal(raw, &snapshot)
	}
	return raw, snapshot, err
}

func telemetry0169ConnectCount(snapshot telemetry0169MetricSnapshot) uint64 {
	var count uint64
	for _, state := range snapshot.States {
		for _, metric := range state.Pending {
			if metric.Name == "codex.websocket.connect" {
				count += metric.Count
			}
		}
	}
	return count
}

func TestCodexTelemetry0169PostgresConnectSnapshotRestartAndSealedRetry(t *testing.T) {
	t.Setenv("CODEX_TELEMETRY_ENABLED", "true")
	store, key, now := telemetryStoreFixture(t)
	runtime := telemetry0169Runtime(t, telemetry0169NoSendStore{store})
	input := telemetry0169ConnectInput(key)
	factory := runtime.PrepareConnect(input)
	require.NotNil(t, factory)
	observer := factory()
	require.NotNil(t, observer)
	observer.Finish(true)
	observer.Finish(false) // A repeated callback must not add a second Dial.
	require.Eventually(t, func() bool {
		_, snapshot, err := telemetry0169ReadMetrics(key)
		return err == nil && telemetry0169ConnectCount(snapshot) == 1
	}, 5*time.Second, 10*time.Millisecond)
	runtime.Stop()

	_, before, err := telemetry0169ReadMetrics(key)
	require.NoError(t, err)
	require.Equal(t, 3, before.Version)
	for _, state := range before.States {
		require.Zero(t, state.Turns)
		require.Zero(t, state.Attempts, "physical Dial must not invent a model attempt")
	}

	// A fresh service must restore and extend the same v3 aggregate, rather than
	// resetting it or requiring a new snapshot/database schema version.
	restarted := telemetry0169Runtime(t, telemetry0169NoSendStore{NewCodexTelemetryStore(integrationDB)})
	factory = restarted.PrepareConnect(input)
	require.NotNil(t, factory)
	factory().Finish(false)
	require.Eventually(t, func() bool {
		_, snapshot, err := telemetry0169ReadMetrics(key)
		return err == nil && telemetry0169ConnectCount(snapshot) == 2
	}, 5*time.Second, 10*time.Millisecond)
	restarted.Stop()
	raw, restored, err := telemetry0169ReadMetrics(key)
	require.NoError(t, err)
	require.Equal(t, 3, restored.Version)
	require.Contains(t, string(raw), "codex.websocket.connect.duration_ms")
	var nonMetrics int
	require.NoError(t, integrationDB.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM codex_telemetry_activities a
		JOIN codex_telemetry_pools p ON p.id=a.pool_id WHERE p.owner_account_id=$1
		AND a.kind NOT IN ('policy','metrics')`, key.OwnerAccountID).Scan(&nonMetrics))
	require.Zero(t, nonMetrics, "Dial measurements must not create thread/turn/sampling activities")

	// Advance only the isolated aggregate's due time; no minute-long test sleep.
	_, err = store.TransactPool(context.Background(), key, now, func(tx *service.CodexTelemetryPoolTransaction) error {
		activity, found, err := tx.Activities.Get("metrics", "current")
		if err != nil || !found {
			return err
		}
		var value map[string]any
		if err := json.Unmarshal(activity.Data, &value); err != nil {
			return err
		}
		for _, state := range value["states"].([]any) {
			state.(map[string]any)["collected_at"] = now.Add(-2 * time.Minute).Format(time.RFC3339Nano)
		}
		activity.Data, err = json.Marshal(value)
		if err != nil {
			return err
		}
		activity.DueAt = now.Add(-time.Minute)
		return tx.Activities.Put(activity)
	})
	require.NoError(t, err)
	flusher := telemetry0169Runtime(t, telemetry0169NoSendStore{store})
	var batchID string
	var sealed, metadata []byte
	require.Eventually(t, func() bool {
		err := integrationDB.QueryRowContext(context.Background(), `SELECT b.id::text,b.payload,b.metadata
			FROM codex_telemetry_batches b JOIN codex_telemetry_pools p ON p.id=b.pool_id
			WHERE p.owner_account_id=$1 AND b.type='metrics' ORDER BY b.sequence DESC LIMIT 1`,
			key.OwnerAccountID).Scan(&batchID, &sealed, &metadata)
		return err == nil
	}, 5*time.Second, 10*time.Millisecond)
	flusher.Stop()
	require.Contains(t, string(sealed), "codex.websocket.connect.duration_ms")
	require.Contains(t, string(metadata), `"product_sku"`)
	require.Contains(t, string(metadata), `"codex_residency"`)
	claimedAt := time.Now().UTC()
	claims, err := store.ClaimBatches(context.Background(), claimedAt, 10)
	require.NoError(t, err)
	require.Len(t, claims, 1)
	require.Equal(t, batchID, claims[0].ID)
	require.Equal(t, sealed, []byte(claims[0].Payload))
	require.Equal(t, metadata, []byte(claims[0].Metadata))
	// Lease recovery by a fresh repository must resend the frozen payload and
	// metadata byte-for-byte, without rebuilding them using current generators.
	retry, err := NewCodexTelemetryStore(integrationDB).ClaimBatches(context.Background(), claimedAt.Add(31*time.Second), 10)
	require.NoError(t, err)
	require.Len(t, retry, 1)
	require.NotEqual(t, claims[0].ClaimID, retry[0].ClaimID)
	require.Equal(t, sealed, []byte(retry[0].Payload))
	require.Equal(t, metadata, []byte(retry[0].Metadata))
}

func TestCodexTelemetry0169PostgresStaleConnectEpochRollsBack(t *testing.T) {
	t.Setenv("CODEX_TELEMETRY_ENABLED", "true")
	store, key, _ := telemetryStoreFixture(t)
	blocked := &telemetry0169BlockedStore{telemetry0169NoSendStore: telemetry0169NoSendStore{store},
		entered: make(chan struct{}), release: make(chan struct{}), done: make(chan error, 1)}
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(blocked.release) }) }
	t.Cleanup(release)
	runtime := telemetry0169Runtime(t, blocked)
	input := telemetry0169ConnectInput(key)
	factory := runtime.PrepareConnect(input)
	require.NotNil(t, factory)
	factory().Finish(true)
	select {
	case <-blocked.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("connect mutation did not reach PostgreSQL")
	}
	// Another instance changes policy after the observation was queued. The
	// producer remains enabled locally; only the database epoch fence protects it.
	_, err := store.SyncPolicy(context.Background(), true, false, true)
	require.NoError(t, err)
	release()
	select {
	case err := <-blocked.done:
		require.ErrorIs(t, err, service.ErrCodexTelemetryPolicyChanged)
	case <-time.After(5 * time.Second):
		t.Fatal("stale connect mutation did not complete")
	}
	var pools int
	require.NoError(t, integrationDB.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM codex_telemetry_pools WHERE owner_account_id=$1`, key.OwnerAccountID).Scan(&pools))
	require.Zero(t, pools, "rejected observation must roll back pool and activity creation")
	require.NoError(t, runtime.SetStore(blocked))
	factory = runtime.PrepareConnect(input)
	require.NotNil(t, factory)
	factory().Finish(true)
	require.Eventually(t, func() bool {
		_, snapshot, err := telemetry0169ReadMetrics(key)
		return err == nil && telemetry0169ConnectCount(snapshot) == 1
	}, 5*time.Second, 10*time.Millisecond)
}

func TestCodexTelemetry0169PostgresTurnCriticalPathNulls(t *testing.T) {
	t.Setenv("CODEX_TELEMETRY_ENABLED", "true")
	store, key, now := telemetryStoreFixture(t)
	runtime := telemetry0169Runtime(t, telemetry0169NoSendStore{store})
	connect := telemetry0169ConnectInput(key)
	input := service.CodexTelemetryInput{
		AccountID: key.OwnerAccountID, OwnerAccountID: key.OwnerAccountID, CredentialOS: key.OSFamily,
		AuthorizationGeneration: "fixture-generation", OSFamily: key.OSFamily, InstallationID: key.InstallationID,
		UserAgent: connect.UserAgent, Originator: connect.Originator, Version: connect.Version,
		AccessToken: "fixture-only-not-sent", ChatGPTAccountID: "fixture-only-account",
		SessionID: uuid.NewString(), ThreadID: uuid.NewString(), TurnID: uuid.NewString(), SamplingID: uuid.NewString(),
		Model: "gpt-6-astra", Effort: "xhigh", TurnTrigger: "user", StartedAt: now,
	}
	attempt := runtime.Begin(context.Background(), input)
	require.NotNil(t, attempt)
	endTurn := true
	attempt.Finish(service.CodexTelemetryResult{Status: "completed", HTTPStatus: 200, DeliveryStatus: "delivered", FinishedAt: now.Add(time.Millisecond), EndTurn: &endTurn})
	require.Eventually(t, func() bool {
		var finished int
		err := integrationDB.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM codex_telemetry_activities a
			JOIN codex_telemetry_pools p ON p.id=a.pool_id WHERE p.owner_account_id=$1
			AND a.kind='attempt' AND a.state->>'finished'='true'`, key.OwnerAccountID).Scan(&finished)
		return err == nil && finished == 1
	}, 5*time.Second, 10*time.Millisecond)
	runtime.Stop()
	// A fresh process seals the persisted, completed turn when the next explicit
	// turn arrives. Event schema comes from the current generator, not old JSON.
	restarted := telemetry0169Runtime(t, telemetry0169NoSendStore{NewCodexTelemetryStore(integrationDB)})
	input.TurnID, input.SamplingID, input.StartedAt = uuid.NewString(), uuid.NewString(), time.Now()
	next := restarted.Begin(context.Background(), input)
	require.NotNil(t, next)
	next.Finish(service.CodexTelemetryResult{Status: "completed", HTTPStatus: 200, DeliveryStatus: "delivered", FinishedAt: time.Now(), EndTurn: &endTurn})
	var payload, metadata []byte
	require.Eventually(t, func() bool {
		return integrationDB.QueryRowContext(context.Background(), `SELECT b.payload,b.metadata FROM codex_telemetry_batches b
			JOIN codex_telemetry_pools p ON p.id=b.pool_id WHERE p.owner_account_id=$1 AND b.type='analytics'
			AND b.payload::text LIKE '%critical_path_sampling_ms%' ORDER BY b.sequence DESC LIMIT 1`, key.OwnerAccountID).Scan(&payload, &metadata) == nil
	}, 5*time.Second, 10*time.Millisecond)
	restarted.Stop()
	var decoded struct {
		Events []struct {
			Type   string                     `json:"event_type"`
			Params map[string]json.RawMessage `json:"event_params"`
		} `json:"events"`
	}
	require.NoError(t, json.Unmarshal(payload, &decoded))
	turns := 0
	for _, event := range decoded.Events {
		if event.Type != "codex_turn_event" {
			continue
		}
		turns++
		require.JSONEq(t, `"disabled"`, string(event.Params["critical_path_status"]))
		for _, field := range []string{"critical_path_sampling_ms", "critical_path_compaction_ms", "critical_path_auto_review_overhead_ms"} {
			require.Equal(t, json.RawMessage("null"), event.Params[field], field)
		}
	}
	require.Equal(t, 2, turns, "main and title turns both receive the current schema")
	require.Contains(t, string(metadata), `"critical_path_status": "simulated"`)
	require.NotContains(t, string(payload), "fixture-only-not-sent")
	require.NotContains(t, string(metadata), "fixture-only-not-sent")
}
