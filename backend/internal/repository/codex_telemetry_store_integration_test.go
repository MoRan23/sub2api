//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func telemetryStoreFixture(t *testing.T) (service.CodexTelemetryStore, service.CodexTelemetryPoolKey, time.Time) {
	t.Helper()
	ctx := context.Background()
	store := NewCodexTelemetryStore(integrationDB)
	old, err := store.ReadPolicy(ctx)
	require.NoError(t, err)
	// The isolated migration harness seeds the public master setting. Remove
	// public preferences temporarily so direct SyncPolicy calls in store tests
	// exercise their explicit arguments; production always honors persisted UI
	// settings. Restore every original row, including its timestamp and ID.
	type settingRow struct {
		id         int64
		key, value string
		updatedAt  time.Time
	}
	rows, err := integrationDB.QueryContext(ctx, `SELECT id,key,value,updated_at FROM settings WHERE key IN ($1,$2,$3)`,
		service.SettingKeyCodexTelemetryEnabled, service.SettingKeyCodexTelemetrySimulationEnabled, service.SettingKeyCodexTelemetryObservationEnabled)
	require.NoError(t, err)
	var prior []settingRow
	for rows.Next() {
		var row settingRow
		require.NoError(t, rows.Scan(&row.id, &row.key, &row.value, &row.updatedAt))
		prior = append(prior, row)
	}
	require.NoError(t, rows.Err())
	require.NoError(t, rows.Close())
	clearPublic := func() {
		_, err := integrationDB.ExecContext(context.Background(), `DELETE FROM settings WHERE key IN ($1,$2,$3)`,
			service.SettingKeyCodexTelemetryEnabled, service.SettingKeyCodexTelemetrySimulationEnabled, service.SettingKeyCodexTelemetryObservationEnabled)
		require.NoError(t, err)
	}
	t.Cleanup(func() {
		clearPublic()
		_, err := store.SyncPolicy(context.Background(), old.Enabled, old.Simulation, old.Observation)
		require.NoError(t, err)
		for _, row := range prior {
			_, err := integrationDB.ExecContext(context.Background(), `INSERT INTO settings(id,key,value,updated_at) VALUES($1,$2,$3,$4)`, row.id, row.key, row.value, row.updatedAt)
			require.NoError(t, err)
		}
	})
	clearPublic()
	_, err = store.SyncPolicy(ctx, true, true, true)
	require.NoError(t, err)
	var owner int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO accounts(name,platform,type,credentials,extra)
		VALUES($1,'openai','oauth','{}','{}') RETURNING id`, t.Name()).Scan(&owner))
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), `DELETE FROM accounts WHERE id=$1`, owner)
	})
	return store, service.CodexTelemetryPoolKey{OwnerAccountID: owner, OSFamily: "windows", InstallationID: uuid.NewString()}, time.Now().UTC().Truncate(time.Microsecond)
}

func telemetryStoreActivity(kind, key, data string, due time.Time) service.CodexTelemetryActivity {
	return service.CodexTelemetryActivity{Kind: kind, Key: key, Data: json.RawMessage(data), DueAt: due}
}

func telemetryStoreBatch(id string, at time.Time) service.CodexTelemetryBatch {
	return service.CodexTelemetryBatch{ID: id, Type: "metrics", Source: "observed", CreatedAt: at, NotBefore: at,
		UserAgent: "codex_cli_rs/0.155.1 (Windows 10.0.26200; x86_64)", Originator: "codex_cli_rs", ClientVersion: "0.155.1",
		Payload: json.RawMessage(`{"resourceMetrics":[]}`), Metadata: json.RawMessage(`{"model":"gpt-6-astra"}`)}
}

func telemetryStoreState(t *testing.T, batchID string) (status, errorCode, claimID string) {
	t.Helper()
	require.NoError(t, integrationDB.QueryRowContext(context.Background(), `SELECT status,error_code,COALESCE(claim_id::text,'')
		FROM codex_telemetry_batches WHERE id=$1::uuid`, batchID).Scan(&status, &errorCode, &claimID))
	return
}

func telemetryStoreSetPublic(t *testing.T, key, value string) {
	t.Helper()
	_, err := integrationDB.ExecContext(context.Background(), `INSERT INTO settings(key,value) VALUES($1,$2)
		ON CONFLICT(key) DO UPDATE SET value=EXCLUDED.value,updated_at=NOW()`, key, value)
	require.NoError(t, err)
}

func TestCodexTelemetryPostgresReadPolicyRepairsMissedSettingsCallback(t *testing.T) {
	store, key, now := telemetryStoreFixture(t)
	ctx := context.Background()
	before, err := store.ReadPolicy(ctx)
	require.NoError(t, err)
	var ids []string
	for _, os := range []string{"windows", "macos", "linux"} {
		other := key
		other.OSFamily = os
		id := uuid.NewString()
		ids = append(ids, id)
		_, err := store.TransactPool(ctx, other, now, func(tx *service.CodexTelemetryPoolTransaction) error {
			tx.Batches = append(tx.Batches, telemetryStoreBatch(id, now))
			return nil
		})
		require.NoError(t, err)
	}
	claimed, err := store.ClaimBatches(ctx, now, 2)
	require.NoError(t, err)
	require.Len(t, claimed, 2)
	ok, err := store.MarkSending(ctx, claimed[0].ID, claimed[0].ClaimID, claimed[0].PolicyEpoch, now)
	require.NoError(t, err)
	require.True(t, ok)
	// Simulate successful public-settings commit followed by a failed runtime
	// callback. The next periodic read must fence all queued/claimed/sending data.
	telemetryStoreSetPublic(t, service.SettingKeyCodexTelemetryEnabled, "false")
	recovered, err := NewCodexTelemetryStore(integrationDB).ReadPolicy(ctx)
	require.NoError(t, err)
	require.Equal(t, before.Epoch+1, recovered.Epoch)
	require.False(t, recovered.Enabled)
	require.True(t, recovered.Simulation, "missing public subsetting keeps default enabled")
	require.True(t, recovered.Observation)
	for _, id := range ids {
		status, reason, _ := telemetryStoreState(t, id)
		if id == claimed[0].ID {
			require.Equal(t, "unknown", status)
			require.Equal(t, "send_result_unknown", reason)
		} else {
			require.Equal(t, "cancelled", status)
			require.Equal(t, "policy_changed", reason)
		}
	}
	again, err := store.ReadPolicy(ctx)
	require.NoError(t, err)
	require.Equal(t, recovered, again, "reconciliation must be idempotent")
}

func TestCodexTelemetryPostgresPublicSettingsMismatchFencesAllProducers(t *testing.T) {
	store, key, now := telemetryStoreFixture(t)
	ctx := context.Background()
	_, err := store.TransactPool(ctx, key, now, func(tx *service.CodexTelemetryPoolTransaction) error {
		tx.Batches = append(tx.Batches, telemetryStoreBatch(uuid.NewString(), now))
		return nil
	})
	require.NoError(t, err)
	claimed, err := store.ClaimBatches(ctx, now, 1)
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	telemetryStoreSetPublic(t, service.SettingKeyCodexTelemetryObservationEnabled, "false")
	_, err = store.ClaimBatches(ctx, now, 1)
	require.ErrorIs(t, err, service.ErrCodexTelemetryPolicyChanged)
	ok, err := store.MarkSending(ctx, claimed[0].ID, claimed[0].ClaimID, claimed[0].PolicyEpoch, now)
	require.False(t, ok)
	require.ErrorIs(t, err, service.ErrCodexTelemetryPolicyChanged)
	_, err = store.TransactPool(ctx, key, now, func(*service.CodexTelemetryPoolTransaction) error {
		t.Fatal("stale policy admitted producer")
		return nil
	})
	require.ErrorIs(t, err, service.ErrCodexTelemetryPolicyChanged)
	// Shared reads fail closed without trying a shared-to-exclusive upgrade.
	var private string
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT value FROM settings WHERE key=$1`, codexTelemetryRuntimePolicyKey).Scan(&private))
	var unreconciled service.CodexTelemetryPolicy
	require.NoError(t, json.Unmarshal([]byte(private), &unreconciled))
	require.True(t, unreconciled.Observation)
	repaired, err := store.ReadPolicy(ctx)
	require.NoError(t, err)
	require.False(t, repaired.Observation)
	require.Equal(t, unreconciled.Epoch+1, repaired.Epoch)
}

func TestCodexTelemetryPostgresStaleSettingsCallbackCannotRevertCommittedPolicy(t *testing.T) {
	store, _, _ := telemetryStoreFixture(t)
	ctx := context.Background()
	before, err := store.ReadPolicy(ctx)
	require.NoError(t, err)
	telemetryStoreSetPublic(t, service.SettingKeyCodexTelemetryEnabled, "false")
	p, err := store.SyncPolicy(ctx, true, true, true)
	require.NoError(t, err)
	require.False(t, p.Enabled, "stale enable callback overrode committed disable")
	require.Equal(t, before.Epoch+1, p.Epoch)
	telemetryStoreSetPublic(t, service.SettingKeyCodexTelemetryEnabled, "true")
	telemetryStoreSetPublic(t, service.SettingKeyCodexTelemetrySimulationEnabled, "false")
	telemetryStoreSetPublic(t, service.SettingKeyCodexTelemetryObservationEnabled, "true")
	actual, err := store.SyncPolicy(ctx, false, true, false)
	require.NoError(t, err)
	require.True(t, actual.Enabled)
	require.False(t, actual.Simulation)
	require.True(t, actual.Observation)
	require.Equal(t, p.Epoch+1, actual.Epoch)
	again, err := store.SyncPolicy(ctx, false, true, false)
	require.NoError(t, err)
	require.Equal(t, actual, again, "repeated stale callback advanced generation")
}

func TestCodexTelemetryPostgresPublicSettingsDefaultsAndMalformedValues(t *testing.T) {
	store, _, _ := telemetryStoreFixture(t)
	ctx := context.Background()
	private, err := store.SyncPolicy(ctx, false, false, true)
	require.NoError(t, err)
	unchanged, err := store.ReadPolicy(ctx)
	require.NoError(t, err)
	require.Equal(t, private, unchanged, "all missing public settings must preserve private policy")
	telemetryStoreSetPublic(t, service.SettingKeyCodexTelemetrySimulationEnabled, "false")
	partial, err := store.ReadPolicy(ctx)
	require.NoError(t, err)
	require.True(t, partial.Enabled)
	require.False(t, partial.Simulation)
	require.True(t, partial.Observation)
	telemetryStoreSetPublic(t, service.SettingKeyCodexTelemetryEnabled, "invalid")
	malformed, err := store.ReadPolicy(ctx)
	require.NoError(t, err)
	require.False(t, malformed.Enabled, "malformed public value must fail closed")
	telemetryStoreSetPublic(t, service.SettingKeyCodexTelemetryEnabled, "  ")
	empty, err := store.ReadPolicy(ctx)
	require.NoError(t, err)
	require.True(t, empty.Enabled, "empty persisted setting follows SettingService default")
}

func TestCodexTelemetryPostgresPoolConvergenceAndAtomicRollback(t *testing.T) {
	store, key, now := telemetryStoreFixture(t)
	ctx := context.Background()
	first, err := store.TransactPool(ctx, key, now, func(tx *service.CodexTelemetryPoolTransaction) error {
		tx.Pool.LastBusinessAt = now
		a := telemetryStoreActivity("process", "startup", `{"count":1}`, time.Time{})
		tx.Activities[service.CodexTelemetryActivityMapKey(a.Kind, a.Key)] = a
		return nil
	})
	require.NoError(t, err)
	require.EqualValues(t, 1, first.Version)
	const writers = 12
	var wg sync.WaitGroup
	errs := make(chan error, writers)
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Independent repositories represent separate application instances.
			_, err := NewCodexTelemetryStore(integrationDB).TransactPool(ctx, key, now.Add(time.Minute), func(tx *service.CodexTelemetryPoolTransaction) error {
				a := tx.Activities[service.CodexTelemetryActivityMapKey("process", "startup")]
				var current struct {
					Count int `json:"count"`
				}
				if err := json.Unmarshal(a.Data, &current); err != nil {
					return err
				}
				current.Count++
				a.Data, _ = json.Marshal(current)
				tx.Activities[service.CodexTelemetryActivityMapKey(a.Kind, a.Key)] = a
				return nil
			})
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	restarted := NewCodexTelemetryStore(integrationDB)
	loaded, err := restarted.TransactPool(ctx, key, now.Add(time.Hour), func(tx *service.CodexTelemetryPoolTransaction) error {
		require.JSONEq(t, fmt.Sprintf(`{"count":%d}`, writers+1), string(tx.Activities[service.CodexTelemetryActivityMapKey("process", "startup")].Data))
		require.Equal(t, first.Seed, tx.Pool.Seed)
		require.Equal(t, now, tx.Pool.LastBusinessAt, "maintenance must not extend real business activity")
		return nil
	})
	require.NoError(t, err)
	require.Equal(t, first.ID, loaded.ID)
	require.EqualValues(t, writers+2, loaded.Version)
	for _, os := range []string{"macos", "linux"} {
		other := key
		other.OSFamily = os
		p, err := restarted.TransactPool(ctx, other, now, func(*service.CodexTelemetryPoolTransaction) error { return nil })
		require.NoError(t, err)
		require.NotEqual(t, first.ID, p.ID)
		require.NotEqual(t, first.Seed, p.Seed)
	}
	missing := key
	missing.InstallationID = ""
	withoutID, err := restarted.TransactPool(ctx, missing, now, func(*service.CodexTelemetryPoolTransaction) error { return nil })
	require.NoError(t, err)
	require.NotEqual(t, first.ID, withoutID.ID)
	batchID := uuid.NewString()
	_, err = store.TransactPool(ctx, key, now, func(tx *service.CodexTelemetryPoolTransaction) error {
		tx.Batches = append(tx.Batches, telemetryStoreBatch(batchID, now))
		delete(tx.Activities, service.CodexTelemetryActivityMapKey("process", "startup"))
		return errors.New("abort this transaction")
	})
	require.Error(t, err)
	var count int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT count(*) FROM codex_telemetry_batches WHERE id=$1`, batchID).Scan(&count))
	require.Zero(t, count)
	_, err = store.TransactPool(ctx, key, now, func(tx *service.CodexTelemetryPoolTransaction) error {
		require.Contains(t, tx.Activities, service.CodexTelemetryActivityMapKey("process", "startup"))
		tx.Batches = append(tx.Batches, telemetryStoreBatch(batchID, now))
		a := telemetryStoreActivity("turn", "unsafe", `{"nested":{"access_token":"must-not-persist"}}`, time.Time{})
		tx.Activities[service.CodexTelemetryActivityMapKey(a.Kind, a.Key)] = a
		return nil
	})
	require.Error(t, err)
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT count(*) FROM codex_telemetry_batches WHERE id=$1`, batchID).Scan(&count))
	require.Zero(t, count)
}

func TestCodexTelemetryPostgresDueActivityAndBatchSealAreAtomic(t *testing.T) {
	store, key, now := telemetryStoreFixture(t)
	ctx := context.Background()
	_, err := store.TransactPool(ctx, key, now, func(tx *service.CodexTelemetryPoolTransaction) error {
		a := telemetryStoreActivity("metrics", "window", `{"count":3}`, now.Add(time.Minute))
		tx.Activities[service.CodexTelemetryActivityMapKey(a.Kind, a.Key)] = a
		return nil
	})
	require.NoError(t, err)
	due, err := store.ListDuePools(ctx, now.Add(59*time.Second), 100)
	require.NoError(t, err)
	require.NotContains(t, due, key)
	due, err = store.ListDuePools(ctx, now.Add(time.Minute), 100)
	require.NoError(t, err)
	require.Contains(t, due, key)
	batchID := uuid.NewString()
	_, err = store.TransactPool(ctx, key, now.Add(time.Minute), func(tx *service.CodexTelemetryPoolTransaction) error {
		delete(tx.Activities, service.CodexTelemetryActivityMapKey("metrics", "window"))
		tx.Batches = append(tx.Batches, telemetryStoreBatch(batchID, now.Add(time.Minute)))
		return nil
	})
	require.NoError(t, err)
	due, err = store.ListDuePools(ctx, now.Add(time.Minute), 100)
	require.NoError(t, err)
	require.NotContains(t, due, key)
	claimed, err := store.ClaimBatches(ctx, now.Add(time.Minute), 10)
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	require.Equal(t, batchID, claimed[0].ID)
}

func TestCodexTelemetryPostgresClaimsRecoveryAndUncertainSend(t *testing.T) {
	store, key, now := telemetryStoreFixture(t)
	ctx := context.Background()
	ids := []string{uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()}
	for i, os := range []string{"windows", "macos", "linux", "unknown"} {
		other := key
		other.OSFamily = os
		_, err := store.TransactPool(ctx, other, now, func(tx *service.CodexTelemetryPoolTransaction) error {
			tx.Batches = append(tx.Batches, telemetryStoreBatch(ids[i], now))
			return nil
		})
		require.NoError(t, err)
	}
	var wg sync.WaitGroup
	claims := make(chan []service.CodexTelemetryBatch, 2)
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			batches, err := NewCodexTelemetryStore(integrationDB).ClaimBatches(ctx, now, 3)
			claims <- batches
			errs <- err
		}()
	}
	wg.Wait()
	close(claims)
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	byID := make(map[string]service.CodexTelemetryBatch)
	for batches := range claims {
		for _, b := range batches {
			require.NotContains(t, byID, b.ID)
			byID[b.ID] = b
		}
	}
	require.Len(t, byID, 4)
	sending := byID[ids[0]]
	ok, err := store.MarkSending(ctx, sending.ID, sending.ClaimID, sending.PolicyEpoch, now.Add(time.Second))
	require.NoError(t, err)
	require.True(t, ok)
	ok, err = store.MarkSending(ctx, sending.ID, sending.ClaimID, sending.PolicyEpoch, now.Add(2*time.Second))
	require.NoError(t, err)
	require.False(t, ok, "a duplicate callback cannot dispatch twice")
	reclaimed, err := NewCodexTelemetryStore(integrationDB).ClaimBatches(ctx, now.Add(32*time.Second), 10)
	require.NoError(t, err)
	require.Len(t, reclaimed, 3, "sending is not safe to replay; only unsent claims recover")
	status, code, _ := telemetryStoreState(t, sending.ID)
	require.Equal(t, "unknown", status)
	require.Equal(t, "send_result_unknown", code)
	for _, b := range reclaimed {
		require.NotEqual(t, byID[b.ID].ClaimID, b.ClaimID)
		ok, err := store.MarkSending(ctx, b.ID, byID[b.ID].ClaimID, b.PolicyEpoch, now.Add(33*time.Second))
		require.NoError(t, err)
		require.False(t, ok)
		ok, err = store.CompleteBatch(ctx, b.ID, b.ClaimID, service.CodexTelemetryBatchResult{Status: "skipped", ErrorCode: "proxy_unavailable"}, now.Add(33*time.Second))
		require.NoError(t, err)
		require.True(t, ok)
	}
	ok, err = store.CompleteBatch(ctx, sending.ID, sending.ClaimID, service.CodexTelemetryBatchResult{Status: "sent", HTTPStatus: 200}, now.Add(34*time.Second))
	require.NoError(t, err)
	require.False(t, ok)
}

func TestCodexTelemetryPostgresPoolBatchOrderAcrossInstances(t *testing.T) {
	store, key, now := telemetryStoreFixture(t)
	ctx := context.Background()
	firstID, secondID := uuid.NewString(), uuid.NewString()
	_, err := store.TransactPool(ctx, key, now, func(tx *service.CodexTelemetryPoolTransaction) error {
		first := telemetryStoreBatch(firstID, now)
		first.NotBefore = now.Add(time.Second)
		tx.Batches = append(tx.Batches, first, telemetryStoreBatch(secondID, now))
		return nil
	})
	require.NoError(t, err)
	claimed, err := store.ClaimBatches(ctx, now, 10)
	require.NoError(t, err)
	require.Empty(t, claimed, "an earlier not-yet-due initialization cannot be overtaken")
	var wg sync.WaitGroup
	results := make(chan []service.CodexTelemetryBatch, 2)
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			batches, err := NewCodexTelemetryStore(integrationDB).ClaimBatches(ctx, now.Add(time.Second), 10)
			results <- batches
			errs <- err
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	all := make([]service.CodexTelemetryBatch, 0)
	for result := range results {
		all = append(all, result...)
	}
	require.Len(t, all, 1, "only the earliest batch in a pool may be claimed across instances")
	require.Equal(t, firstID, all[0].ID)
	ok, err := store.MarkSending(ctx, all[0].ID, all[0].ClaimID, all[0].PolicyEpoch, now.Add(2*time.Second))
	require.NoError(t, err)
	require.True(t, ok)
	claimed, err = store.ClaimBatches(ctx, now.Add(3*time.Second), 10)
	require.NoError(t, err)
	require.Empty(t, claimed, "sending initialization must finish before subsequent events")
	claimed, err = store.ClaimBatches(ctx, now.Add(33*time.Second), 10)
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	require.Equal(t, secondID, claimed[0].ID)
	require.Greater(t, claimed[0].Sequence, all[0].Sequence)
}

func TestCodexTelemetryPostgresPolicyFenceAndDisabledNoReplay(t *testing.T) {
	store, key, now := telemetryStoreFixture(t)
	ctx := context.Background()
	before, err := store.ReadPolicy(ctx)
	require.NoError(t, err)
	batchID := uuid.NewString()
	_, err = store.TransactPool(ctx, key, now, func(tx *service.CodexTelemetryPoolTransaction) error {
		tx.Batches = append(tx.Batches, telemetryStoreBatch(batchID, now))
		return nil
	})
	require.NoError(t, err)
	claimed, err := store.ClaimBatches(ctx, now, 10)
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	off, err := store.SyncPolicy(ctx, false, true, true)
	require.NoError(t, err)
	require.Equal(t, before.Epoch+1, off.Epoch)
	unchanged, err := store.SyncPolicy(ctx, false, true, true)
	require.NoError(t, err)
	require.Equal(t, off.Epoch, unchanged.Epoch)
	called := false
	_, err = store.TransactPool(ctx, key, now, func(*service.CodexTelemetryPoolTransaction) error { called = true; return nil })
	require.ErrorIs(t, err, service.ErrCodexTelemetryPolicyDisabled)
	require.False(t, called)
	ok, err := store.MarkSending(ctx, batchID, claimed[0].ClaimID, before.Epoch, now.Add(time.Second))
	require.NoError(t, err)
	require.False(t, ok)
	_, err = store.SyncPolicy(ctx, true, true, true)
	require.NoError(t, err)
	claimAgain, err := store.ClaimBatches(ctx, now.Add(time.Second), 10)
	require.NoError(t, err)
	require.Empty(t, claimAgain)
	status, code, _ := telemetryStoreState(t, batchID)
	require.Equal(t, "cancelled", status)
	require.Equal(t, "policy_changed", code)
	_, err = store.TransactPool(ctx, key, now, func(tx *service.CodexTelemetryPoolTransaction) error {
		b := telemetryStoreBatch(uuid.NewString(), now)
		b.PolicyEpoch = before.Epoch
		tx.Batches = append(tx.Batches, b)
		return nil
	})
	require.ErrorIs(t, err, service.ErrCodexTelemetryPolicyChanged)
	_, err = store.SyncPolicy(ctx, true, false, false)
	require.NoError(t, err)
	_, err = store.TransactPool(ctx, key, now, func(*service.CodexTelemetryPoolTransaction) error { return nil })
	require.ErrorIs(t, err, service.ErrCodexTelemetryPolicyDisabled)
}

func TestCodexTelemetryPostgresPolicyChangeFencesConcurrentPublication(t *testing.T) {
	store, key, now := telemetryStoreFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	inside := make(chan struct{})
	release := make(chan struct{})
	producerDone := make(chan error, 1)
	policyDone := make(chan error, 1)
	batchID := uuid.NewString()
	go func() {
		_, err := store.TransactPool(ctx, key, now, func(tx *service.CodexTelemetryPoolTransaction) error {
			close(inside)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-release:
			}
			tx.Batches = append(tx.Batches, telemetryStoreBatch(batchID, now))
			return nil
		})
		producerDone <- err
	}()
	select {
	case <-inside:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	go func() {
		_, err := NewCodexTelemetryStore(integrationDB).SyncPolicy(ctx, false, true, true)
		policyDone <- err
	}()
	select {
	case err := <-policyDone:
		t.Fatalf("policy change crossed an uncommitted producer fence: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	require.NoError(t, <-producerDone)
	require.NoError(t, <-policyDone)
	status, code, _ := telemetryStoreState(t, batchID)
	require.Equal(t, "cancelled", status)
	require.Equal(t, "policy_changed", code)
	claimed, err := store.ClaimBatches(ctx, now.Add(time.Second), 10)
	require.NoError(t, err)
	require.Empty(t, claimed)
}

func TestCodexTelemetryPostgresPolicyChangeKeepsStartedSendsUnknown(t *testing.T) {
	store, key, now := telemetryStoreFixture(t)
	ctx := context.Background()
	ids := []string{uuid.NewString(), uuid.NewString(), uuid.NewString()}
	for i, os := range []string{"windows", "macos", "linux"} {
		other := key
		other.OSFamily = os
		_, err := store.TransactPool(ctx, other, now, func(tx *service.CodexTelemetryPoolTransaction) error {
			tx.Batches = append(tx.Batches, telemetryStoreBatch(ids[i], now))
			return nil
		})
		require.NoError(t, err)
	}
	claimed, err := store.ClaimBatches(ctx, now, 2)
	require.NoError(t, err)
	require.Len(t, claimed, 2)
	require.Equal(t, ids[0], claimed[0].ID)
	ok, err := store.MarkSending(ctx, claimed[0].ID, claimed[0].ClaimID, claimed[0].PolicyEpoch, now.Add(time.Second))
	require.NoError(t, err)
	require.True(t, ok)
	_, err = store.SyncPolicy(ctx, false, true, true)
	require.NoError(t, err)
	status, reason, _ := telemetryStoreState(t, ids[0])
	require.Equal(t, "unknown", status, "a policy change cannot establish whether a dispatched request reached the receiver")
	require.Equal(t, "send_result_unknown", reason)
	for _, id := range ids[1:] {
		status, reason, _ := telemetryStoreState(t, id)
		require.Equal(t, "cancelled", status)
		require.Equal(t, "policy_changed", reason)
	}
	_, err = store.SyncPolicy(ctx, true, true, true)
	require.NoError(t, err)
	again, err := store.ClaimBatches(ctx, now.Add(2*time.Second), 10)
	require.NoError(t, err)
	require.Empty(t, again, "re-enabling cannot replay cancelled or uncertain transmissions")
	ok, err = store.CompleteBatch(ctx, claimed[0].ID, claimed[0].ClaimID,
		service.CodexTelemetryBatchResult{Status: "sent", HTTPStatus: 202}, now.Add(2*time.Second))
	require.NoError(t, err)
	require.False(t, ok, "a late callback must not overwrite the fenced unknown result")
	status, reason, _ = telemetryStoreState(t, ids[0])
	require.Equal(t, "unknown", status)
	require.Equal(t, "send_result_unknown", reason)
}

func TestCodexTelemetryPostgresExpirationRetentionAndUnknownOS(t *testing.T) {
	store, key, now := telemetryStoreFixture(t)
	ctx := context.Background()
	expiredID := uuid.NewString()
	_, err := store.TransactPool(ctx, key, now, func(tx *service.CodexTelemetryPoolTransaction) error {
		b := telemetryStoreBatch(expiredID, now.Add(-24*time.Hour))
		tx.Batches = append(tx.Batches, b)
		return nil
	})
	require.NoError(t, err)
	claimed, err := store.ClaimBatches(ctx, now, 10)
	require.NoError(t, err)
	require.Empty(t, claimed)
	status, code, _ := telemetryStoreState(t, expiredID)
	require.Equal(t, "dropped", status)
	require.Equal(t, "batch_expired", code)
	require.NoError(t, store.Maintain(ctx, now.Add(7*24*time.Hour)))
	var count int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT count(*) FROM codex_telemetry_batches WHERE id=$1`, expiredID).Scan(&count))
	require.Zero(t, count)
	unknown := key
	unknown.OSFamily = "unknown"
	_, err = store.TransactPool(ctx, unknown, now, func(tx *service.CodexTelemetryPoolTransaction) error {
		tx.Batches = append(tx.Batches, telemetryStoreBatch(uuid.NewString(), now))
		return nil
	})
	require.NoError(t, err)
	_, err = store.TransactPool(ctx, unknown, now, func(tx *service.CodexTelemetryPoolTransaction) error {
		b := telemetryStoreBatch(uuid.NewString(), now)
		b.Source = "simulated"
		tx.Batches = append(tx.Batches, b)
		return nil
	})
	require.ErrorIs(t, err, service.ErrCodexTelemetryInvalidState)
}
