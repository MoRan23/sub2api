package service

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestMemoryCodexTelemetryStoreTransactionRollbackAndIsolation(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryCodexTelemetryStore().(*MemoryCodexTelemetryStore)
	now := time.Now().UTC()
	key := CodexTelemetryPoolKey{OwnerAccountID: 42, OSFamily: "windows"}
	activityKey := CodexTelemetryActivityMapKey("turn", "one")
	var captured map[string]CodexTelemetryActivity
	batchID := uuid.NewString()
	proxyID := int64(7)
	payload := json.RawMessage(`{"input_tokens":5}`)
	pool, err := s.TransactPool(ctx, key, now, func(tx *CodexTelemetryPoolTransaction) error {
		captured = tx.Activities
		tx.Pool.LastBusinessAt = now
		tx.Activities[activityKey] = CodexTelemetryActivity{Kind: "turn", Key: "one", Data: json.RawMessage(`{"count":1}`), DueAt: now}
		tx.Batches = append(tx.Batches, CodexTelemetryBatch{Type: "analytics", Source: "observed", ID: batchID, ProxyID: &proxyID, Payload: payload})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if pool.Version != 1 || pool.LastBusinessAt != now {
		t.Fatalf("unexpected pool: %+v", pool)
	}
	pool.ID = "caller mutation"
	proxyID = 99
	payload[16] = '9'
	delete(captured, activityKey)
	rollback := errors.New("rollback")
	_, err = s.TransactPool(ctx, key, now.Add(time.Second), func(tx *CodexTelemetryPoolTransaction) error {
		if len(tx.Activities) != 1 {
			t.Fatal("callback retained mutable committed state")
		}
		activity := tx.Activities[activityKey]
		activity.Data[9] = '9'
		tx.Activities[activityKey] = activity
		tx.Pool.LastBusinessAt = now.Add(time.Hour)
		return rollback
	})
	if !errors.Is(err, rollback) {
		t.Fatalf("rollback error: %v", err)
	}
	_, err = s.TransactPool(ctx, key, now.Add(2*time.Second), func(tx *CodexTelemetryPoolTransaction) error {
		if string(tx.Activities[activityKey].Data) != `{"count":1}` || tx.Pool.Version != 1 || tx.Pool.LastBusinessAt != now || tx.Pool.ID == "caller mutation" {
			t.Fatalf("transaction leaked: %+v", tx)
		}
		tx.Pool.LastBusinessAt = now.Add(-time.Hour)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if s.pools[key].LastBusinessAt != now {
		t.Fatal("business time moved backwards")
	}
	claims, err := s.ClaimBatches(ctx, now, 5)
	if err != nil || len(claims) != 1 {
		t.Fatalf("claims=%d err=%v", len(claims), err)
	}
	if *claims[0].ProxyID != 7 || string(claims[0].Payload) != `{"input_tokens":5}` {
		t.Fatal("batch retained caller pointers")
	}
	claims[0].Payload[16] = '9'
	if string(s.batches[batchID].Payload) != `{"input_tokens":5}` {
		t.Fatal("claim exposed committed payload")
	}
}

func TestMemoryCodexTelemetryStoreLeasesAndUnknownOutcome(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryCodexTelemetryStore().(*MemoryCodexTelemetryStore)
	now := time.Now().UTC()
	key := CodexTelemetryPoolKey{OwnerAccountID: 42, OSFamily: "macos"}
	_, err := s.TransactPool(ctx, key, now, func(tx *CodexTelemetryPoolTransaction) error {
		tx.Batches = append(tx.Batches, CodexTelemetryBatch{Type: "analytics", Source: "observed", Payload: json.RawMessage(`{}`)})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	first, _ := s.ClaimBatches(ctx, now, 1)
	if len(first) != 1 {
		t.Fatal("initial claim missing")
	}
	second, _ := s.ClaimBatches(ctx, now.Add(30*time.Second), 1)
	if len(second) != 1 || first[0].ClaimID == second[0].ClaimID {
		t.Fatal("expired reservation not recovered with new owner")
	}
	if ok, _ := s.MarkSending(ctx, first[0].ID, first[0].ClaimID, first[0].PolicyEpoch, now.Add(30*time.Second)); ok {
		t.Fatal("stale owner started sending")
	}
	if ok, err := s.MarkSending(ctx, second[0].ID, second[0].ClaimID, second[0].PolicyEpoch, now.Add(31*time.Second)); err != nil || !ok {
		t.Fatalf("mark sending=%v err=%v", ok, err)
	}
	third, err := s.ClaimBatches(ctx, now.Add(61*time.Second), 1)
	if err != nil || len(third) != 0 {
		t.Fatalf("ambiguous send replayed: %v %v", third, err)
	}
	if s.batches[second[0].ID].Status != "unknown" {
		t.Fatal("lost send was not marked unknown")
	}
	if ok, _ := s.CompleteBatch(ctx, second[0].ID, second[0].ClaimID, CodexTelemetryBatchResult{Status: "sent"}, now.Add(62*time.Second)); ok {
		t.Fatal("late send overwrote unknown terminal state")
	}
}

func TestMemoryCodexTelemetryStorePolicyFenceAndDisabledModes(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryCodexTelemetryStore().(*MemoryCodexTelemetryStore)
	now := time.Now().UTC()
	key := CodexTelemetryPoolKey{OwnerAccountID: 42, OSFamily: "linux"}
	policy, _ := s.ReadPolicy(ctx)
	if policy.Epoch != 1 || !policy.Enabled || !policy.Simulation || !policy.Observation {
		t.Fatalf("bad defaults: %+v", policy)
	}
	_, err := s.TransactPool(ctx, key, now, func(tx *CodexTelemetryPoolTransaction) error {
		tx.Batches = append(tx.Batches, CodexTelemetryBatch{Type: "analytics", Source: "observed", Payload: json.RawMessage(`{}`)})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	claims, _ := s.ClaimBatches(ctx, now, 1)
	policy, err = s.SyncPolicy(ctx, true, false, false)
	if err != nil || policy.Epoch != 2 || !policy.Enabled {
		t.Fatalf("policy=%+v err=%v", policy, err)
	}
	if s.batches[claims[0].ID].Status != "cancelled" {
		t.Fatal("policy change failed to cancel claimed batch")
	}
	if ok, _ := s.MarkSending(ctx, claims[0].ID, claims[0].ClaimID, claims[0].PolicyEpoch, now); ok {
		t.Fatal("old policy started sending")
	}
	_, err = s.TransactPool(ctx, key, now, func(*CodexTelemetryPoolTransaction) error { t.Fatal("disabled callback executed"); return nil })
	if !errors.Is(err, ErrCodexTelemetryPolicyDisabled) {
		t.Fatalf("disabled transaction err=%v", err)
	}
	unchanged, _ := s.SyncPolicy(ctx, true, false, false)
	if unchanged.Epoch != policy.Epoch {
		t.Fatal("unchanged settings advanced epoch")
	}
	_, _ = s.SyncPolicy(ctx, true, true, true)
	if got, _ := s.ClaimBatches(ctx, now, 1); len(got) != 0 {
		t.Fatal("reenable replayed cancelled batch")
	}
}

func TestMemoryCodexTelemetryStoreDisablePreservesUncertainSend(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryCodexTelemetryStore().(*MemoryCodexTelemetryStore)
	now := time.Now().UTC()
	var ids []string
	for _, os := range []string{"windows", "macos", "linux"} {
		id := uuid.NewString()
		ids = append(ids, id)
		_, err := s.TransactPool(ctx, CodexTelemetryPoolKey{OwnerAccountID: 42, OSFamily: os}, now, func(tx *CodexTelemetryPoolTransaction) error {
			tx.Batches = append(tx.Batches, CodexTelemetryBatch{ID: id, Type: "analytics", Source: "observed", Payload: json.RawMessage(`{}`)})
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	claims, err := s.ClaimBatches(ctx, now, 2)
	if err != nil || len(claims) != 2 {
		t.Fatalf("claims=%v err=%v", claims, err)
	}
	sending, reserved := claims[0], claims[1]
	if ok, err := s.MarkSending(ctx, sending.ID, sending.ClaimID, sending.PolicyEpoch, now); err != nil || !ok {
		t.Fatalf("mark sending=%v err=%v", ok, err)
	}
	disabled, err := s.SyncPolicy(ctx, false, true, true)
	if err != nil {
		t.Fatal(err)
	}
	if disabled.Epoch != sending.PolicyEpoch+1 {
		t.Fatal("disable failed to advance publication fence")
	}
	if batch := s.batches[sending.ID]; batch.Status != "unknown" || batch.ErrorCode != "send_result_unknown" || !batch.LeaseUntil.IsZero() || batch.CompletedAt.IsZero() {
		t.Fatalf("in-flight send was falsely labelled cancelled: %+v", batch)
	}
	for _, id := range []string{reserved.ID, ids[2]} {
		if batch := s.batches[id]; batch.Status != "cancelled" || batch.ErrorCode != "policy_changed" {
			t.Fatalf("unsent batch not cancelled: %+v", batch)
		}
	}
	if ok, err := s.CompleteBatch(ctx, sending.ID, sending.ClaimID, CodexTelemetryBatchResult{Status: "sent"}, now); err != nil || ok {
		t.Fatalf("late result overwrote uncertainty: %v %v", ok, err)
	}
	if ok, err := s.MarkSending(ctx, reserved.ID, reserved.ClaimID, reserved.PolicyEpoch, now); err != nil || ok {
		t.Fatalf("old claim crossed disabled policy: %v %v", ok, err)
	}
	_, err = s.SyncPolicy(ctx, true, true, true)
	if err != nil {
		t.Fatal(err)
	}
	if batches, err := s.ClaimBatches(ctx, now, 10); err != nil || len(batches) != 0 {
		t.Fatalf("re-enable replayed old work: %v %v", batches, err)
	}
	if batch := s.batches[sending.ID]; batch.Status != "unknown" || batch.ErrorCode != "send_result_unknown" {
		t.Fatal("re-enable changed unknown outcome")
	}
}

func TestMemoryCodexTelemetryStoreConcurrentPoolAndClaim(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryCodexTelemetryStore().(*MemoryCodexTelemetryStore)
	now := time.Now().UTC()
	key := CodexTelemetryPoolKey{OwnerAccountID: 42, OSFamily: "windows", InstallationID: uuid.NewString()}
	activityKey := CodexTelemetryActivityMapKey("counter", "shared")
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.TransactPool(ctx, key, now, func(tx *CodexTelemetryPoolTransaction) error {
				var count int
				if prior, ok := tx.Activities[activityKey]; ok {
					if err := json.Unmarshal(prior.Data, &count); err != nil {
						return err
					}
				}
				data, _ := json.Marshal(count + 1)
				tx.Activities[activityKey] = CodexTelemetryActivity{Kind: "counter", Key: "shared", Data: data}
				tx.Batches = append(tx.Batches, CodexTelemetryBatch{Type: "analytics", Source: "observed", Payload: json.RawMessage(`{}`)})
				return nil
			})
			if err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if s.pools[key].Version != 20 || string(s.activities[key][activityKey].Data) != "20" {
		t.Fatal("concurrent transaction lost update")
	}
	claimed := make(chan CodexTelemetryBatch, 20)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			batches, err := s.ClaimBatches(ctx, now, 20)
			if err != nil {
				t.Error(err)
			}
			for _, batch := range batches {
				claimed <- batch
			}
		}()
	}
	wg.Wait()
	close(claimed)
	var initial []CodexTelemetryBatch
	for batch := range claimed {
		initial = append(initial, batch)
	}
	if len(initial) != 1 || initial[0].Sequence != 1 {
		t.Fatalf("same pool claimed concurrently: %+v", initial)
	}
	batch := initial[0]
	for sequence := int64(1); sequence <= 20; sequence++ {
		if batch.Sequence != sequence {
			t.Fatalf("out of order: got %d want %d", batch.Sequence, sequence)
		}
		if ok, err := s.CompleteBatch(ctx, batch.ID, batch.ClaimID, CodexTelemetryBatchResult{Status: "sent"}, now); err != nil || !ok {
			t.Fatalf("complete=%v err=%v", ok, err)
		}
		next, err := s.ClaimBatches(ctx, now, 20)
		if err != nil {
			t.Fatal(err)
		}
		if sequence < 20 {
			if len(next) != 1 {
				t.Fatalf("next count=%d", len(next))
			}
			batch = next[0]
		} else if len(next) != 0 {
			t.Fatal("all batches completed but queue not empty")
		}
	}
}

func TestMemoryCodexTelemetryStoreRejectedPayloadRollsBack(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryCodexTelemetryStore().(*MemoryCodexTelemetryStore)
	key := CodexTelemetryPoolKey{OwnerAccountID: 42, OSFamily: "linux"}
	now := time.Now().UTC()
	_, err := s.TransactPool(ctx, key, now, func(tx *CodexTelemetryPoolTransaction) error {
		tx.Batches = append(tx.Batches, CodexTelemetryBatch{Type: "analytics", Source: "observed", Payload: json.RawMessage(`{"nested":[{"Authorization":"secret"}]}`)})
		return nil
	})
	if !errors.Is(err, ErrCodexTelemetryInvalidState) || len(s.pools) != 0 || len(s.batches) != 0 {
		t.Fatalf("secret-bearing transaction committed: %v", err)
	}
	for _, data := range []string{`{"input_tokens":1,"output_tokens":2}`, `{"value":"opaque metric label"}`} {
		if err := ValidateCodexTelemetryStoredJSON(json.RawMessage(data)); err != nil {
			t.Errorf("valid summary %s: %v", data, err)
		}
	}
	for _, data := range []string{`{"access_token":"secret"}`, `{"nested":{"proxy-url":"secret"}}`, `{"messages":[]}`, `{"not":"json"`, `{"x":{"access_token":"secret"},"x":{}}`, `{"x":1,"x":2}`} {
		if err := ValidateCodexTelemetryStoredJSON(json.RawMessage(data)); err == nil {
			t.Errorf("accepted %s", data)
		}
	}
}

func TestMemoryCodexTelemetryStoreDueAndRetention(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryCodexTelemetryStore().(*MemoryCodexTelemetryStore)
	now := time.Now().UTC()
	key := CodexTelemetryPoolKey{OwnerAccountID: 42, OSFamily: "linux"}
	batchID := uuid.NewString()
	_, err := s.TransactPool(ctx, key, now, func(tx *CodexTelemetryPoolTransaction) error {
		tx.Activities[CodexTelemetryActivityMapKey("turn", "a")] = CodexTelemetryActivity{Kind: "turn", Key: "a", Data: json.RawMessage(`{}`), DueAt: now.Add(time.Minute)}
		tx.Batches = append(tx.Batches, CodexTelemetryBatch{Type: "analytics", Source: "observed", ID: batchID, Payload: json.RawMessage(`{}`), NotBefore: now.Add(time.Hour)})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if keys, _ := s.ListDuePools(ctx, now, 10); len(keys) != 0 {
		t.Fatal("future activity due early")
	}
	if keys, _ := s.ListDuePools(ctx, now.Add(time.Minute), 10); len(keys) != 1 || keys[0] != key {
		t.Fatal("due activity omitted")
	}
	if batches, _ := s.ClaimBatches(ctx, now, 10); len(batches) != 0 {
		t.Fatal("not-before ignored")
	}
	if err := s.Maintain(ctx, now.Add(24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if s.batches[batchID].Status != "dropped" {
		t.Fatal("expired unsent batch retained")
	}
	if err := s.Maintain(ctx, now.Add(8*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if len(s.batches) != 0 || len(s.pools) != 1 || len(s.activities[key]) != 1 {
		t.Fatal("retention removed durable pool or kept terminal batch")
	}
}

func TestMemoryCodexTelemetryStoreUnknownOSAndPublicationModes(t *testing.T) {
	for _, os := range []string{"windows", "unknown"} {
		for _, simulation := range []bool{false, true} {
			for _, observation := range []bool{false, true} {
				for _, source := range []string{"observed", "simulated", "mixed"} {
					name := os + "/" + source
					t.Run(name, func(t *testing.T) {
						ctx := context.Background()
						s := NewMemoryCodexTelemetryStore()
						_, _ = s.SyncPolicy(ctx, true, simulation, observation)
						_, err := s.TransactPool(ctx, CodexTelemetryPoolKey{OwnerAccountID: 42, OSFamily: os}, time.Now(), func(tx *CodexTelemetryPoolTransaction) error {
							tx.Batches = append(tx.Batches, CodexTelemetryBatch{Type: "metrics", Source: source, Payload: json.RawMessage(`{}`)})
							return nil
						})
						valid := (os != "unknown" || source == "observed") && ((source == "observed" && observation) || (source == "simulated" && simulation) || (source == "mixed" && simulation && observation))
						if valid && err != nil {
							t.Fatalf("mode %t/%t valid batch rejected: %v", simulation, observation, err)
						}
						if !valid && err == nil {
							t.Fatalf("mode %t/%t unsupported batch accepted", simulation, observation)
						}
					})
				}
			}
		}
	}
}

func TestMemoryCodexTelemetryStoreLateCompletionWithoutMaintenance(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryCodexTelemetryStore()
	now := time.Now().UTC()
	_, err := s.TransactPool(ctx, CodexTelemetryPoolKey{OwnerAccountID: 42, OSFamily: "linux"}, now, func(tx *CodexTelemetryPoolTransaction) error {
		tx.Batches = append(tx.Batches, CodexTelemetryBatch{Type: "metrics", Source: "observed", Payload: json.RawMessage(`{}`)})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	claims, _ := s.ClaimBatches(ctx, now, 1)
	batch := claims[0]
	if ok, err := s.CompleteBatch(ctx, batch.ID, batch.ClaimID, CodexTelemetryBatchResult{Status: "sent"}, now.Add(30*time.Second)); ok || err != nil {
		t.Fatalf("expired callback committed before maintenance: %v %v", ok, err)
	}
}

func TestMemoryCodexTelemetryStorePoolOrderAndParallelPools(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryCodexTelemetryStore()
	now := time.Now().UTC()
	firstKey := CodexTelemetryPoolKey{OwnerAccountID: 42, OSFamily: "windows"}
	secondKey := CodexTelemetryPoolKey{OwnerAccountID: 42, OSFamily: "linux"}
	_, err := s.TransactPool(ctx, firstKey, now, func(tx *CodexTelemetryPoolTransaction) error {
		tx.Batches = append(tx.Batches,
			CodexTelemetryBatch{Type: "analytics", Source: "simulated", Payload: json.RawMessage(`{}`), NotBefore: now.Add(time.Minute), Sequence: 99},
			CodexTelemetryBatch{Type: "metrics", Source: "observed", Payload: json.RawMessage(`{}`), Sequence: -1},
		)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.TransactPool(ctx, secondKey, now, func(tx *CodexTelemetryPoolTransaction) error {
		tx.Batches = append(tx.Batches, CodexTelemetryBatch{Type: "metrics", Source: "observed", Payload: json.RawMessage(`{}`)})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	claims, err := s.ClaimBatches(ctx, now, 10)
	if err != nil || len(claims) != 1 || claims[0].Pool.Key != secondKey || claims[0].Sequence != 3 {
		t.Fatalf("later batch bypassed delayed pool head: %+v err=%v", claims, err)
	}
	// Keep the second pool in flight while the first becomes due.
	if ok, _ := s.MarkSending(ctx, claims[0].ID, claims[0].ClaimID, claims[0].PolicyEpoch, now); !ok {
		t.Fatal("second pool send could not start")
	}
	claims, err = s.ClaimBatches(ctx, now.Add(time.Minute), 10)
	if err != nil || len(claims) != 1 || claims[0].Pool.Key != firstKey || claims[0].Sequence != 1 {
		t.Fatalf("first pool due head not claimed: %+v err=%v", claims, err)
	}
	first := claims[0]
	if next, _ := s.ClaimBatches(ctx, now.Add(time.Minute), 10); len(next) != 0 {
		t.Fatal("claim bypassed in-flight pool head")
	}
	if ok, _ := s.MarkSending(ctx, first.ID, first.ClaimID, first.PolicyEpoch, now.Add(time.Minute)); !ok {
		t.Fatal("first pool send could not start")
	}
	if next, _ := s.ClaimBatches(ctx, now.Add(89*time.Second), 10); len(next) != 0 {
		t.Fatal("claim bypassed sending pool head")
	}
	// An ambiguous send is terminal and releases the next batch without replay.
	next, err := s.ClaimBatches(ctx, now.Add(90*time.Second), 10)
	if err != nil || len(next) != 1 || next[0].Pool.Key != firstKey || next[0].Sequence != 2 {
		t.Fatalf("unknown send did not release next ordered batch: %+v err=%v", next, err)
	}
}
