package service

import (
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func newCodexStateIndexTestStore() *codexTurnStateSummaryStore {
	return &codexTurnStateSummaryStore{}
}

func codexStateIndexTestSequence(t *testing.T, o *codexTurnStateSummaryStore) uint64 {
	t.Helper()
	sequence := o.nextSequence()
	require.NotZero(t, sequence)
	return sequence
}

func codexStateIndexTestValue(model string, length int) CodexTurnStateObservation {
	return CodexTurnStateObservation{
		OSFamily: "windows",
		Model:    model, Action: "passthrough", OutboundLength: 292,
		ResponseLength: length, ResponseShape: "unknown", ResponseObservedShape: "team_target",
		ResponseCipherBlocks: 12, ResponseValidationReason: "account_type_unknown", ResponseSource: "metadata",
	}
}

func codexStateIndexTestJSON(t *testing.T, value CodexTurnStateModelObservation) map[string]any {
	t.Helper()
	encoded, err := json.Marshal(value)
	require.NoError(t, err)
	var fields map[string]any
	require.NoError(t, json.Unmarshal(encoded, &fields))
	return fields
}

func TestCodexStateObservationIndexRequiresFinishedRecordedSequence(t *testing.T) {
	for _, mode := range []string{"unfinished", "zero_sequence", "future_sequence", "zero_owner", "empty_model", "blank_model", "zero_time"} {
		t.Run(mode, func(t *testing.T) {
			o := newCodexStateIndexTestStore()
			now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
			sequence := codexStateIndexTestSequence(t, o)
			ownerID, finished := int64(7), true
			value := codexStateIndexTestValue("gpt-5.4", 332)
			switch mode {
			case "unfinished":
				finished = false
			case "zero_sequence":
				sequence = 0
			case "future_sequence":
				sequence++
			case "zero_owner":
				ownerID = 0
			case "empty_model":
				value.Model = ""
			case "blank_model":
				value.Model = " \t"
			case "zero_time":
				now = time.Time{}
			}
			o.update(sequence, ownerID, value, finished, now)
			enabled, observations := o.snapshot([]int64{0, 7})
			require.True(t, enabled)
			require.Empty(t, observations)
		})
	}
}

func TestCodexStateObservationIndexUsesObservedTimeAndSequenceThenSortsModels(t *testing.T) {
	o := newCodexStateIndexTestStore()
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	old := codexStateIndexTestSequence(t, o)
	middle := codexStateIndexTestSequence(t, o)
	newest := codexStateIndexTestSequence(t, o)
	observedAt := now.Add(3 * time.Second)
	o.update(newest, 7, codexStateIndexTestValue("z-model", 332), true, observedAt)
	o.update(old, 7, codexStateIndexTestValue("z-model", 312), true, now.Add(2*time.Second))
	o.update(middle, 7, codexStateIndexTestValue("z-model", 356), true, observedAt)
	_, observations := o.snapshot([]int64{7})
	require.Len(t, observations[7], 1)
	require.EqualValues(t, 332, codexStateIndexTestJSON(t, observations[7][0])["response_length"])
	// Completion time is primary even when an older physical request finishes last.
	observedAt = now.Add(4 * time.Second)
	o.update(old, 7, codexStateIndexTestValue("z-model", 356), true, observedAt)
	_, observations = o.snapshot([]int64{7})
	require.EqualValues(t, 356, codexStateIndexTestJSON(t, observations[7][0])["response_length"])
	tieWinner := codexStateIndexTestSequence(t, o)
	o.update(tieWinner, 7, codexStateIndexTestValue("z-model", 292), true, observedAt)
	o.update(old, 7, codexStateIndexTestValue("z-model", 312), true, observedAt)
	alpha := codexStateIndexTestSequence(t, o)
	o.update(alpha, 7, codexStateIndexTestValue("a-model", 332), true, now.Add(5*time.Second))
	_, observations = o.snapshot([]int64{7})
	require.Len(t, observations[7], 2)
	require.Equal(t, "a-model", observations[7][0].Model)
	require.Equal(t, "z-model", observations[7][1].Model)
	fields := codexStateIndexTestJSON(t, observations[7][1])
	require.EqualValues(t, 292, fields["response_length"])
	require.Equal(t, observedAt.Format(time.RFC3339Nano), fields["observed_at"])
}

func TestCodexStateObservationIndexIndependentOfFingerprintRingEvictionAndDisable(t *testing.T) {
	o := newCodexStateIndexTestStore()
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	sequence := codexStateIndexTestSequence(t, o)
	o.update(sequence, 7, codexStateIndexTestValue("gpt-5.4", 332), true, now)
	_, original := o.snapshot([]int64{7})
	fingerprint := &fingerprintObserver{ring: make([]FingerprintObservationEntry, fingerprintObservationCapacity)}
	fingerprint.mu.Lock()
	fingerprint.setEnabledLocked(true)
	fingerprint.mu.Unlock()
	oldRingSequence := fingerprint.record(FingerprintObservationEntry{AccountID: 7, Timestamp: now})
	require.NotZero(t, oldRingSequence)
	for range fingerprintObservationCapacity {
		require.NotZero(t, fingerprint.record(FingerprintObservationEntry{AccountID: 9, Timestamp: now}))
	}
	ring, _ := fingerprint.snapshotThrough(0)
	require.Len(t, ring, fingerprintObservationCapacity)
	for _, entry := range ring {
		require.NotEqual(t, oldRingSequence, entry.SequenceID)
	}
	fingerprint.mu.Lock()
	fingerprint.setEnabledLocked(false)
	fingerprint.mu.Unlock()
	require.Zero(t, fingerprint.record(FingerprintObservationEntry{AccountID: 7, Timestamp: now}))
	supported, afterDisable := o.snapshot([]int64{7})
	require.True(t, supported)
	require.Equal(t, original, afterDisable, "fingerprint eviction and disabling must not clear independent account summaries")
	o.update(sequence, 7, codexStateIndexTestValue("gpt-5.4", 332), true, now.Add(time.Minute))
	supported, observations := o.snapshot([]int64{7})
	require.True(t, supported)
	require.Len(t, observations[7], 1, "completion remains useful while fingerprint observation is disabled")
	require.Equal(t, "gpt-5.4", observations[7][0].Model)
	require.Equal(t, now.Add(time.Minute).Format(time.RFC3339Nano), codexStateIndexTestJSON(t, observations[7][0])["observed_at"])
}

func TestCodexStateObservationIndexEvictsLeastRecentlyUpdatedAt4096(t *testing.T) {
	o := newCodexStateIndexTestStore()
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	var firstSequence uint64
	for index := range 4096 {
		observedAt := now.Add(time.Duration(index) * time.Millisecond)
		sequence := codexStateIndexTestSequence(t, o)
		if index == 0 {
			firstSequence = sequence
		}
		o.update(sequence, 7, codexStateIndexTestValue(fmt.Sprintf("model-%04d", index), 332), true, observedAt)
	}
	// A genuinely newer result refreshes the oldest entry's recency.
	o.update(firstSequence, 7, codexStateIndexTestValue("model-0000", 356), true, now.Add(time.Minute))
	sequence := codexStateIndexTestSequence(t, o)
	o.update(sequence, 7, codexStateIndexTestValue("model-4096", 332), true, now.Add(2*time.Minute))
	_, observations := o.snapshot([]int64{7})
	require.Len(t, observations[7], 4096)
	models := make(map[string]bool, len(observations[7]))
	for _, value := range observations[7] {
		models[value.Model] = true
	}
	require.True(t, models["model-0000"])
	require.False(t, models["model-0001"], "the oldest remaining entry must be evicted")
	require.True(t, models["model-4096"])
}

func TestCodexStateObservationIndexReadsRefreshLRUButStaleResultsDoNot(t *testing.T) {
	o := newCodexStateIndexTestStore()
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	owners := make([]int64, 0, 4097)
	var secondSequence uint64
	for ownerID := int64(1); ownerID <= 4096; ownerID++ {
		owners = append(owners, ownerID)
		sequence := codexStateIndexTestSequence(t, o)
		if ownerID == 2 {
			secondSequence = sequence
		}
		o.update(sequence, ownerID, codexStateIndexTestValue("gpt-5.4", 332), true, now)
	}
	_, observed := o.snapshot([]int64{1})
	require.Len(t, observed[1], 1, "reading an owner refreshes its cached summary's recency")
	o.update(secondSequence, 2, codexStateIndexTestValue("gpt-5.4", 356), true, now.Add(-time.Minute))
	sequence := codexStateIndexTestSequence(t, o)
	o.update(sequence, 4097, codexStateIndexTestValue("gpt-5.4", 292), true, now.Add(time.Minute))
	owners = append(owners, 4097)
	_, observations := o.snapshot(owners)
	require.Len(t, observations, 4096)
	require.Len(t, observations[1], 1)
	require.NotContains(t, observations, int64(2), "a rejected stale result cannot save the least recently used entry from eviction")
	require.Len(t, observations[4097], 1)
}

func TestCodexStateObservationIndexSeparatesOwnersAndReturnsImmutableSnapshots(t *testing.T) {
	o := newCodexStateIndexTestStore()
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	first := codexStateIndexTestSequence(t, o)
	second := codexStateIndexTestSequence(t, o)
	o.update(first, 7, codexStateIndexTestValue("same-model", 292), true, now)
	o.update(second, 9, codexStateIndexTestValue("same-model", 332), true, now)
	_, observations := o.snapshot([]int64{9})
	require.NotContains(t, observations, int64(7))
	require.Len(t, observations[9], 1)
	require.EqualValues(t, 332, codexStateIndexTestJSON(t, observations[9][0])["response_length"])
	_, observations = o.snapshot([]int64{7, 9, 7, 123})
	require.Len(t, observations, 2)
	require.Len(t, observations[7], 1)
	require.Len(t, observations[9], 1)
	before, err := json.Marshal(observations)
	require.NoError(t, err)
	observations[7][0].Model = "caller-mutated"
	delete(observations, 9)
	_, fresh := o.snapshot([]int64{7, 9})
	after, err := json.Marshal(fresh)
	require.NoError(t, err)
	require.JSONEq(t, string(before), string(after))
	_, empty := o.snapshot(nil)
	require.Empty(t, empty, "empty owner selection must not mean all accounts")
}

func TestCodexStateObservationIndexSerializesOnlySafeSummary(t *testing.T) {
	o := newCodexStateIndexTestStore()
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	token := codexStateTestToken(12, now)
	sequence := codexStateIndexTestSequence(t, o)
	value := codexStateIndexTestValue("gpt-5.4", len(token))
	o.update(sequence, 7, value, true, now)
	_, observations := o.snapshot([]int64{7})
	require.Len(t, observations[7], 1)
	fields := codexStateIndexTestJSON(t, observations[7][0])
	require.Equal(t, "gpt-5.4", fields["model"])
	require.EqualValues(t, 332, fields["response_length"])
	require.Equal(t, "unknown", fields["response_shape"])
	require.Equal(t, "team_target", fields["response_observed_shape"])
	require.EqualValues(t, 12, fields["response_cipher_blocks"])
	require.Equal(t, "account_type_unknown", fields["response_validation_reason"])
	require.Equal(t, "metadata", fields["response_source"])
	require.EqualValues(t, 292, fields["outbound_length"])
	encoded, err := json.Marshal(observations)
	require.NoError(t, err)
	for _, secret := range []string{token, "private account name", "private-api-key", "encrypted_token", "access_token", "client_reported_installation_id"} {
		require.NotContains(t, string(encoded), secret)
	}
}

func TestCodexStateObservationIndexConcurrentOwnersRemainIsolated(t *testing.T) {
	o := newCodexStateIndexTestStore()
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	const owners, writersPerOwner, rounds = 8, 3, 40
	type expectedSummary struct {
		sequence uint64
		length   int
	}
	var expectedMu sync.Mutex
	expected := make(map[int64]expectedSummary, owners)
	sequences := make(chan uint64, owners*writersPerOwner*rounds)
	errors := make(chan error, owners*writersPerOwner)
	var writers sync.WaitGroup
	for ownerID := int64(1); ownerID <= owners; ownerID++ {
		for writer := range writersPerOwner {
			writers.Add(1)
			go func() {
				defer writers.Done()
				for range rounds {
					sequence := o.nextSequence()
					sequences <- sequence
					length := 292 + writer*20
					o.update(sequence, ownerID, codexStateIndexTestValue("shared-model", length), true, now)
					expectedMu.Lock()
					if sequence > expected[ownerID].sequence {
						expected[ownerID] = expectedSummary{sequence: sequence, length: length}
					}
					expectedMu.Unlock()
					supported, snapshot := o.snapshot([]int64{ownerID})
					if !supported || len(snapshot) != 1 || len(snapshot[ownerID]) != 1 || snapshot[ownerID][0].Model != "shared-model" {
						errors <- fmt.Errorf("owner %d received an incomplete or cross-owner snapshot", ownerID)
						return
					}
					// Concurrent consumers may edit their snapshot without touching
					// another reader or the stored per-owner summary.
					snapshot[ownerID][0].Model = "reader-local-change"
				}
			}()
		}
	}
	writers.Wait()
	close(sequences)
	close(errors)
	for err := range errors {
		require.NoError(t, err)
	}
	seen := make(map[uint64]bool, owners*writersPerOwner*rounds)
	for sequence := range sequences {
		require.NotZero(t, sequence)
		require.False(t, seen[sequence], "actual-send sequences must remain unique across concurrent owners")
		seen[sequence] = true
	}
	require.Len(t, seen, owners*writersPerOwner*rounds)
	ids := make([]int64, 0, owners)
	for ownerID := int64(1); ownerID <= owners; ownerID++ {
		ids = append(ids, ownerID)
	}
	supported, snapshot := o.snapshot(ids)
	require.True(t, supported)
	require.Len(t, snapshot, owners)
	for ownerID, latest := range expected {
		require.Len(t, snapshot[ownerID], 1)
		require.Equal(t, "shared-model", snapshot[ownerID][0].Model)
		require.EqualValues(t, latest.length, codexStateIndexTestJSON(t, snapshot[ownerID][0])["response_length"])
	}
}
