package service

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func newCodexStateIndexTestObserver() *fingerprintObserver {
	o := &fingerprintObserver{ring: make([]FingerprintObservationEntry, fingerprintObservationCapacity)}
	o.mu.Lock()
	o.setEnabledLocked(true)
	o.mu.Unlock()
	return o
}

func codexStateIndexTestSequence(t *testing.T, o *fingerprintObserver, ownerID int64, observedAt time.Time) uint64 {
	t.Helper()
	sequence := o.record(FingerprintObservationEntry{AccountID: ownerID, Timestamp: observedAt})
	require.NotZero(t, sequence)
	return sequence
}

func codexStateIndexTestValue(model string, length int) CodexTurnStateObservation {
	return CodexTurnStateObservation{
		Model: model, Action: "passthrough", OutboundLength: 292,
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
	for _, mode := range []string{"unfinished", "zero_sequence", "future_sequence", "zero_window", "future_window", "zero_owner", "empty_model", "blank_model", "disabled"} {
		t.Run(mode, func(t *testing.T) {
			o := newCodexStateIndexTestObserver()
			now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
			sequence := codexStateIndexTestSequence(t, o, 7, now)
			window := o.codexStateObservationWindow()
			ownerID, finished := int64(7), true
			value := codexStateIndexTestValue("gpt-5.4", 332)
			switch mode {
			case "unfinished":
				finished = false
			case "zero_sequence":
				sequence = 0
			case "future_sequence":
				sequence++
			case "zero_window":
				window = 0
			case "future_window":
				window++
			case "zero_owner":
				ownerID = 0
			case "empty_model":
				value.Model = ""
			case "blank_model":
				value.Model = " \t"
			case "disabled":
				o.mu.Lock()
				o.setEnabledLocked(false)
				o.mu.Unlock()
			}
			o.updateCodexTurnStateObservation(sequence, ownerID, value, finished, now, window)
			enabled, observations := o.codexStateObservations([]int64{0, 7})
			require.Equal(t, mode != "disabled", enabled)
			require.Empty(t, observations)
		})
	}
}

func TestCodexStateObservationIndexUsesObservedTimeAndSequenceThenSortsModels(t *testing.T) {
	o := newCodexStateIndexTestObserver()
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	old := codexStateIndexTestSequence(t, o, 7, now)
	middle := codexStateIndexTestSequence(t, o, 7, now.Add(time.Second))
	newest := codexStateIndexTestSequence(t, o, 7, now.Add(2*time.Second))
	observedAt := now.Add(3 * time.Second)
	o.updateCodexTurnStateObservation(newest, 7, codexStateIndexTestValue("z-model", 332), true, observedAt, o.codexStateObservationWindow())
	o.updateCodexTurnStateObservation(old, 7, codexStateIndexTestValue("z-model", 312), true, now.Add(2*time.Second), o.codexStateObservationWindow())
	o.updateCodexTurnStateObservation(middle, 7, codexStateIndexTestValue("z-model", 356), true, observedAt, o.codexStateObservationWindow())
	_, observations := o.codexStateObservations([]int64{7})
	require.Len(t, observations[7], 1)
	require.EqualValues(t, 332, codexStateIndexTestJSON(t, observations[7][0])["response_length"])
	// Completion time is primary even when an older physical request finishes last.
	observedAt = now.Add(4 * time.Second)
	o.updateCodexTurnStateObservation(old, 7, codexStateIndexTestValue("z-model", 356), true, observedAt, o.codexStateObservationWindow())
	_, observations = o.codexStateObservations([]int64{7})
	require.EqualValues(t, 356, codexStateIndexTestJSON(t, observations[7][0])["response_length"])
	tieWinner := codexStateIndexTestSequence(t, o, 7, observedAt)
	o.updateCodexTurnStateObservation(tieWinner, 7, codexStateIndexTestValue("z-model", 292), true, observedAt, o.codexStateObservationWindow())
	o.updateCodexTurnStateObservation(old, 7, codexStateIndexTestValue("z-model", 312), true, observedAt, o.codexStateObservationWindow())
	alpha := codexStateIndexTestSequence(t, o, 7, now.Add(5*time.Second))
	o.updateCodexTurnStateObservation(alpha, 7, codexStateIndexTestValue("a-model", 332), true, now.Add(5*time.Second), o.codexStateObservationWindow())
	_, observations = o.codexStateObservations([]int64{7})
	require.Len(t, observations[7], 2)
	require.Equal(t, "a-model", observations[7][0].Model)
	require.Equal(t, "z-model", observations[7][1].Model)
	fields := codexStateIndexTestJSON(t, observations[7][1])
	require.EqualValues(t, 292, fields["response_length"])
	require.Equal(t, observedAt.Format(time.RFC3339Nano), fields["observed_at"])
}

func TestCodexStateObservationIndexAcceptsFinishedRequestAfterRingEviction(t *testing.T) {
	o := newCodexStateIndexTestObserver()
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	sequence := codexStateIndexTestSequence(t, o, 7, now)
	for range fingerprintObservationCapacity {
		codexStateIndexTestSequence(t, o, 9, now)
	}
	ring, _ := o.snapshotThrough(0)
	require.Len(t, ring, fingerprintObservationCapacity)
	for _, entry := range ring {
		require.NotEqual(t, sequence, entry.SequenceID)
	}
	o.updateCodexTurnStateObservation(sequence, 7, codexStateIndexTestValue("gpt-5.4", 332), true, now.Add(time.Minute), o.codexStateObservationWindow())
	enabled, observations := o.codexStateObservations([]int64{7})
	require.True(t, enabled)
	require.Len(t, observations[7], 1, "completion remains useful after the smaller request ring rolls over")
	require.Equal(t, "gpt-5.4", observations[7][0].Model)
}

func TestCodexStateObservationIndexEvictsLeastRecentlyUpdatedAt4096(t *testing.T) {
	o := newCodexStateIndexTestObserver()
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	var firstSequence uint64
	for index := range 4096 {
		observedAt := now.Add(time.Duration(index) * time.Millisecond)
		sequence := codexStateIndexTestSequence(t, o, 7, observedAt)
		if index == 0 {
			firstSequence = sequence
		}
		o.updateCodexTurnStateObservation(sequence, 7, codexStateIndexTestValue(fmt.Sprintf("model-%04d", index), 332), true, observedAt, o.codexStateObservationWindow())
	}
	// A genuinely newer result refreshes the oldest entry's recency.
	o.updateCodexTurnStateObservation(firstSequence, 7, codexStateIndexTestValue("model-0000", 356), true, now.Add(time.Minute), o.codexStateObservationWindow())
	sequence := codexStateIndexTestSequence(t, o, 7, now.Add(2*time.Minute))
	o.updateCodexTurnStateObservation(sequence, 7, codexStateIndexTestValue("model-4096", 332), true, now.Add(2*time.Minute), o.codexStateObservationWindow())
	_, observations := o.codexStateObservations([]int64{7})
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
	o := newCodexStateIndexTestObserver()
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	owners := make([]int64, 0, 4097)
	var secondSequence uint64
	for ownerID := int64(1); ownerID <= 4096; ownerID++ {
		owners = append(owners, ownerID)
		sequence := codexStateIndexTestSequence(t, o, ownerID, now)
		if ownerID == 2 {
			secondSequence = sequence
		}
		o.updateCodexTurnStateObservation(sequence, ownerID, codexStateIndexTestValue("gpt-5.4", 332), true, now, o.codexStateObservationWindow())
	}
	_, observed := o.codexStateObservations([]int64{1})
	require.Len(t, observed[1], 1, "reading an owner refreshes its cached summary's recency")
	o.updateCodexTurnStateObservation(secondSequence, 2, codexStateIndexTestValue("gpt-5.4", 356), true, now.Add(-time.Minute), o.codexStateObservationWindow())
	sequence := codexStateIndexTestSequence(t, o, 4097, now.Add(time.Minute))
	o.updateCodexTurnStateObservation(sequence, 4097, codexStateIndexTestValue("gpt-5.4", 292), true, now.Add(time.Minute), o.codexStateObservationWindow())
	owners = append(owners, 4097)
	_, observations := o.codexStateObservations(owners)
	require.Len(t, observations, 4096)
	require.Len(t, observations[1], 1)
	require.NotContains(t, observations, int64(2), "a rejected stale result cannot save the least recently used entry from eviction")
	require.Len(t, observations[4097], 1)
}

func TestCodexStateObservationIndexDisableClearsAndFencesOldCompletions(t *testing.T) {
	o := newCodexStateIndexTestObserver()
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	oldWindow := o.codexStateObservationWindow()
	require.NotZero(t, oldWindow)
	old := codexStateIndexTestSequence(t, o, 7, now)
	o.updateCodexTurnStateObservation(old, 7, codexStateIndexTestValue("old-model", 332), true, now, oldWindow)
	o.mu.Lock()
	o.setEnabledLocked(false)
	o.mu.Unlock()
	enabled, observations := o.codexStateObservations([]int64{7})
	require.False(t, enabled)
	require.Empty(t, observations)
	require.Zero(t, o.codexStateObservationWindow())
	o.updateCodexTurnStateObservation(old, 7, codexStateIndexTestValue("old-model", 356), true, now.Add(time.Minute), oldWindow)
	o.mu.Lock()
	o.setEnabledLocked(true)
	o.mu.Unlock()
	o.updateCodexTurnStateObservation(old, 7, codexStateIndexTestValue("old-model", 356), true, now.Add(2*time.Minute), oldWindow)
	enabled, observations = o.codexStateObservations([]int64{7})
	require.True(t, enabled)
	require.Empty(t, observations, "re-enabling cannot resurrect a pre-disable physical request")
	fresh := codexStateIndexTestSequence(t, o, 7, now.Add(3*time.Minute))
	require.Greater(t, fresh, old)
	o.updateCodexTurnStateObservation(fresh, 7, codexStateIndexTestValue("late-recorded-old-model", 356), true, now.Add(3*time.Minute), oldWindow)
	_, observations = o.codexStateObservations([]int64{7})
	require.Empty(t, observations, "a request frozen before disable cannot publish even when its send was recorded after re-enable")
	require.Greater(t, o.codexStateObservationWindow(), oldWindow)
	o.updateCodexTurnStateObservation(fresh, 7, codexStateIndexTestValue("new-model", 292), true, now.Add(3*time.Minute), o.codexStateObservationWindow())
	_, observations = o.codexStateObservations([]int64{7})
	require.Len(t, observations[7], 1)
	require.Equal(t, "new-model", observations[7][0].Model)
}

func TestCodexStateObservationIndexSeparatesOwnersAndReturnsImmutableSnapshots(t *testing.T) {
	o := newCodexStateIndexTestObserver()
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	first := codexStateIndexTestSequence(t, o, 7, now)
	second := codexStateIndexTestSequence(t, o, 9, now)
	o.updateCodexTurnStateObservation(first, 7, codexStateIndexTestValue("same-model", 292), true, now, o.codexStateObservationWindow())
	o.updateCodexTurnStateObservation(second, 9, codexStateIndexTestValue("same-model", 332), true, now, o.codexStateObservationWindow())
	_, observations := o.codexStateObservations([]int64{9})
	require.NotContains(t, observations, int64(7))
	require.Len(t, observations[9], 1)
	require.EqualValues(t, 332, codexStateIndexTestJSON(t, observations[9][0])["response_length"])
	_, observations = o.codexStateObservations([]int64{7, 9, 7, 123})
	require.Len(t, observations, 2)
	require.Len(t, observations[7], 1)
	require.Len(t, observations[9], 1)
	before, err := json.Marshal(observations)
	require.NoError(t, err)
	observations[7][0].Model = "caller-mutated"
	delete(observations, 9)
	_, fresh := o.codexStateObservations([]int64{7, 9})
	after, err := json.Marshal(fresh)
	require.NoError(t, err)
	require.JSONEq(t, string(before), string(after))
	_, empty := o.codexStateObservations(nil)
	require.Empty(t, empty, "empty owner selection must not mean all accounts")
}

func TestCodexStateObservationIndexSerializesOnlySafeSummary(t *testing.T) {
	o := newCodexStateIndexTestObserver()
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	token := codexStateTestToken(12, now)
	sequence := o.record(FingerprintObservationEntry{
		AccountID: 7, AccountName: "private account name", Timestamp: now,
		ClientReportedInstallationID: token, APIKeyName: "private-api-key",
	})
	require.NotZero(t, sequence)
	value := codexStateIndexTestValue("gpt-5.4", len(token))
	o.updateCodexTurnStateObservation(sequence, 7, value, true, now, o.codexStateObservationWindow())
	_, observations := o.codexStateObservations([]int64{7})
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
