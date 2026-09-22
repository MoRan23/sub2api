package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func isolateCodexTurnStateSummaryStore(t *testing.T) {
	t.Helper()
	previous := globalCodexTurnStateSummaryStore
	globalCodexTurnStateSummaryStore = &codexTurnStateSummaryStore{}
	SetFingerprintObservationEnabled(false)
	t.Cleanup(func() {
		SetFingerprintObservationEnabled(false)
		globalCodexTurnStateSummaryStore = previous
	})
}

func recordCodexStatusObservation(t *testing.T, _ int64, ownerID int64, value CodexTurnStateObservation, observedAt time.Time) {
	t.Helper()
	sequence := globalCodexTurnStateSummaryStore.nextSequence()
	require.NotZero(t, sequence)
	globalCodexTurnStateSummaryStore.update(sequence, ownerID, value, true, observedAt)
}

func TestCodexTurnStateStatusObservationsRemainIndependentOfDisabledCache(t *testing.T) {
	isolateCodexTurnStateSummaryStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	ownerOne, ownerTwo := codexStateBatchOwner(1, false), codexStateBatchOwner(2, false)
	accounts := &codexStateBatchAccounts{accounts: map[int64]*Account{
		1: ownerOne, 2: ownerTwo, 3: codexStateBatchOwner(3, false), 11: codexStateBatchShadow(11, 1),
	}}
	records := &codexStateBatchRecords{records: []CodexTurnStateRecord{
		{OSFamily: "windows", OwnerAccountID: 1, Generation: CodexTurnStateGenerationForAccount(ownerOne), Model: "cached-model", EncryptedToken: "private-encrypted-token", ExpiresAt: now.Add(20 * time.Minute), TokenLength: 292, CipherBlocks: 10, Shape: "target", Source: "business"},
	}}
	beforeRecords := slices.Clone(records.records)
	state := NewCodexTurnStateService(records, accounts, nil, codexStateTestCollector(func(context.Context, CodexTurnStateCollectRequest) (CodexTurnStateCollectResult, error) {
		t.Fatal("status observations must not schedule collection")
		return CodexTurnStateCollectResult{}, nil
	}))
	state.now = func() time.Time { return now }
	state.modelPolicy = &codexStateBatchPolicy{models: []string{"z-observed", "cached-model", "a-observed"}}
	before, err := state.GetStatus(ctx, 1)
	require.NoError(t, err)
	require.False(t, before.Enabled)
	require.Empty(t, before.Observations)
	require.Len(t, before.Models, 1)
	recordCodexStatusObservation(t, 11, 1, CodexTurnStateObservation{
		OSFamily: "windows",
		Model:    "z-observed", Action: "passthrough", Source: "client", OutboundLength: 312,
		ResponseLength: 312, ResponseShape: "suspect", ResponseObservedShape: CodexTurnStateObservedPersonalExtended,
		ResponseCipherBlocks: 11, ResponseSource: "metadata",
	}, now.Add(-3*time.Second))
	recordCodexStatusObservation(t, 1, 1, CodexTurnStateObservation{
		OSFamily: "windows",
		Model:    "a-observed", Action: "passthrough", ResponseLength: 292, ResponseShape: "target",
		ResponseObservedShape: CodexTurnStateObservedPersonalTarget, ResponseCipherBlocks: 10, ResponseSource: "header",
	}, now.Add(-2*time.Second))
	recordCodexStatusObservation(t, 2, 2, CodexTurnStateObservation{
		OSFamily: "windows",
		Model:    "other-owner-model", Action: "passthrough", ResponseLength: 356, ResponseShape: "unknown",
		ResponseObservedShape: CodexTurnStateObservedTeamBusinessExtended, ResponseCipherBlocks: 13,
		ResponseValidationReason: "unexpected_shape", ResponseSource: "metadata",
	}, now.Add(-time.Second))
	batch, err := state.GetStatuses(ctx, []int64{1, 11, 2, 3})
	require.NoError(t, err)
	require.Equal(t, []string{"z-observed", "cached-model", "a-observed"}, batch.Models, "cache-policy order is independent of sorted observation models")
	for _, id := range []int64{1, 11, 2, 3} {
		status, readErr := state.GetStatus(ctx, id)
		require.NoError(t, readErr)
		require.Equal(t, status, batch.Items[fmt.Sprint(id)], "single and batch reads must expose the same local summary")
		require.False(t, status.Enabled)
		require.True(t, status.ObservationEnabled)
		require.Equal(t, "instance", status.ObservationScope)
	}
	observed := batch.Items["1"].Observations
	require.Len(t, observed, 2)
	require.Equal(t, []string{"a-observed", "z-observed"}, []string{observed[0].Model, observed[1].Model})
	require.Equal(t, 292, observed[0].ResponseLength)
	require.Equal(t, "header", observed[0].ResponseSource)
	require.Equal(t, 312, observed[1].OutboundLength)
	require.Equal(t, 11, observed[1].ResponseCipherBlocks)
	require.Equal(t, now.Add(-3*time.Second), observed[1].ObservedAt)
	require.Equal(t, observed, batch.Items["11"].Observations, "Spark reads the credential owner's observations")
	require.Equal(t, before.Models, batch.Items["1"].Models, "passive responses do not become cached model statuses")
	require.Equal(t, before.Models, batch.Items["11"].Models)
	require.Len(t, batch.Items["2"].Observations, 1)
	require.Equal(t, "other-owner-model", batch.Items["2"].Observations[0].Model)
	require.Equal(t, "unexpected_shape", batch.Items["2"].Observations[0].ResponseValidationReason)
	require.Equal(t, []CodexTurnStateModelObservation{}, batch.Items["3"].Observations)
	require.Empty(t, batch.Items["2"].Models)
	require.Empty(t, batch.Items["3"].Models)
	serialized, err := json.Marshal(batch)
	require.NoError(t, err)
	for _, secret := range []string{"private-account-credential", "private-encrypted-token", "private-generation", "encrypted_token", "access_token", "credentials"} {
		require.NotContains(t, string(serialized), secret)
	}
	// Callers cannot mutate either another account's result or the global index.
	batch.Items["1"].Observations[0].Model = "caller-mutated"
	require.Equal(t, "a-observed", batch.Items["11"].Observations[0].Model)
	again, err := state.GetStatus(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, "a-observed", again.Observations[0].Model)
	require.Equal(t, beforeRecords, records.records)
	requirePassiveWSNoMaintenance(t, state)

	SetFingerprintObservationEnabled(false)
	off, err := state.GetStatuses(ctx, []int64{1, 11, 2, 3})
	require.NoError(t, err)
	for id, status := range off.Items {
		require.True(t, status.ObservationEnabled)
		require.Equal(t, "instance", status.ObservationScope)
		if id == "1" {
			require.Equal(t, again.Observations, status.Observations)
		} else {
			require.Equal(t, batch.Items[id].Observations, status.Observations)
		}
	}
	require.Empty(t, SnapshotFingerprintObservations(0))
	require.Equal(t, before.Models, off.Items["1"].Models)
	SetFingerprintObservationEnabled(true)
	reenabled, err := state.GetStatus(ctx, 1)
	require.NoError(t, err)
	require.True(t, reenabled.ObservationEnabled)
	require.Equal(t, again.Observations, reenabled.Observations, "full fingerprint observation does not control lightweight account summaries")
	require.Equal(t, beforeRecords, records.records)
	requirePassiveWSNoMaintenance(t, state)
}

func TestCodexTurnStateStatusObservationUsesFrozenOwnerAcrossBindOrdering(t *testing.T) {
	for _, finishBeforeBind := range []bool{false, true} {
		t.Run(fmt.Sprintf("finish_before_bind_%t", finishBeforeBind), func(t *testing.T) {
			isolateCodexTurnStateSummaryStore(t)
			ctx := context.Background()
			owner, shadow := codexStateBatchOwner(1, false), codexStateBatchShadow(11, 1)
			accounts := &codexStateBatchAccounts{accounts: map[int64]*Account{1: owner, 11: shadow, 2: codexStateBatchOwner(2, false)}}
			records := &codexStateBatchRecords{}
			state := NewCodexTurnStateService(records, accounts, nil, nil)
			state.modelPolicy = &codexStateBatchPolicy{models: []string{"observed-final-model"}}
			attempt, err := prepareCodexStateTest(state, ctx, shadow, "observed-final-model")
			require.NoError(t, err)
			require.NotNil(t, attempt)
			require.False(t, attempt.Enabled)
			require.Equal(t, owner.ID, attempt.OwnerAccountID)
			require.Empty(t, attempt.Snapshot.Token)
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			noteOpenAICodexStatePatch(c, attempt, nil, nil)
			entry := FingerprintObservationEntry{AccountID: shadow.ID, EventKind: FingerprintObservationEventHTTP, Timestamp: time.Now().Add(-10 * time.Minute)}
			observation := populateCodexTurnStateObservation(c, &entry, http.Header{"X-Codex-Turn-State": {"private-client-state"}}, nil, false)
			token := makeCodexWSStateTestToken(10, time.Now().Add(-time.Minute))
			var observedNotBefore, observedNotAfter time.Time
			finish := func() {
				observedNotBefore = time.Now()
				state.ObserveHeaders(attempt, http.Header{"X-Codex-Turn-State": {token}})
				observedNotAfter = time.Now()
				require.Empty(t, attempt.candidates, "a disabled cache must retain only the safe response summary")
				require.NoError(t, state.Finish(ctx, attempt, true))
				finishOpenAICodexStateObservation(attempt)
			}
			if finishBeforeBind {
				finish()
			}
			bindCodexTurnStateSummarySequence(observation)
			bindCodexTurnStateSummarySequence(observation)
			if !finishBeforeBind {
				finish()
			}
			batch, err := state.GetStatuses(ctx, []int64{1, 11, 2})
			require.NoError(t, err)
			require.Len(t, batch.Items["1"].Observations, 1, "the physical shadow ID must not replace the owner frozen by Prepare")
			result := batch.Items["1"].Observations[0]
			require.Equal(t, "observed-final-model", result.Model)
			require.Equal(t, 292, result.ResponseLength)
			require.Equal(t, "target", result.ResponseShape)
			require.Equal(t, CodexTurnStateObservedPersonalTarget, result.ResponseObservedShape)
			require.Equal(t, 10, result.ResponseCipherBlocks)
			require.Equal(t, "header", result.ResponseSource)
			require.Equal(t, len("private-client-state"), result.OutboundLength)
			require.False(t, result.ObservedAt.Before(observedNotBefore), "observation time must be when the state was received, not its signed timestamp or request start")
			require.False(t, result.ObservedAt.After(observedNotAfter))
			require.Equal(t, batch.Items["1"].Observations, batch.Items["11"].Observations)
			require.Empty(t, batch.Items["2"].Observations)
			require.Empty(t, batch.Items["1"].Models)
			require.Empty(t, records.records)
			require.Empty(t, SnapshotFingerprintObservations(0))
			requirePassiveWSNoMaintenance(t, state)
			serialized, err := json.Marshal(batch)
			require.NoError(t, err)
			for _, secret := range []string{token, "private-client-state", "private-account-credential", "private-generation", "access_token"} {
				require.NotContains(t, string(serialized), secret)
			}
		})
	}
}

func TestCodexTurnStateStatusObservationFingerprintSwitchDoesNotDiscardSummary(t *testing.T) {
	isolateCodexTurnStateSummaryStore(t)
	SetFingerprintObservationEnabled(true)
	ctx := context.Background()
	owner := codexStateBatchOwner(1, false)
	accounts := &codexStateBatchAccounts{accounts: map[int64]*Account{1: owner}}
	records := &codexStateBatchRecords{}
	state := NewCodexTurnStateService(records, accounts, nil, nil)
	state.modelPolicy = &codexStateBatchPolicy{models: []string{"old-window-model", "new-window-model"}}
	attempt, err := prepareCodexStateTest(state, ctx, owner, "old-window-model")
	require.NoError(t, err)
	require.NotNil(t, attempt)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	noteOpenAICodexStatePatch(c, attempt, nil, nil)
	entry := FingerprintObservationEntry{AccountID: owner.ID, EventKind: FingerprintObservationEventHTTP}
	observation := populateCodexTurnStateObservation(c, &entry, nil, nil, false)
	sequence := globalFingerprintObserver.record(entry)
	require.NotZero(t, sequence)
	bindCodexTurnStateObservationSequence(observation, sequence)
	SetFingerprintObservationEnabled(false)
	// Closing full fingerprint observation must not suppress an actual send's
	// response summary, even when that response arrives after the switch change.
	bindCodexTurnStateSummarySequence(observation)
	state.ObserveHeaders(attempt, http.Header{"X-Codex-Turn-State": {makeCodexWSStateTestToken(10, time.Now().Add(-time.Minute))}})
	require.NoError(t, state.Finish(ctx, attempt, true))
	finishOpenAICodexStateObservation(attempt)
	status, err := state.GetStatus(ctx, owner.ID)
	require.NoError(t, err)
	require.True(t, status.ObservationEnabled)
	require.Len(t, status.Observations, 1)
	require.Equal(t, "old-window-model", status.Observations[0].Model)
	require.Empty(t, SnapshotFingerprintObservations(0), "summary publication must not repopulate the disabled full fingerprint ring")
	SetFingerprintObservationEnabled(true)
	recordCodexStatusObservation(t, owner.ID, owner.ID, CodexTurnStateObservation{
		OSFamily: "windows",
		Model:    "new-window-model", Action: "passthrough", ResponseLength: 292, ResponseShape: "target", ResponseSource: "header",
	}, time.Now())
	status, err = state.GetStatus(ctx, owner.ID)
	require.NoError(t, err)
	require.Len(t, status.Observations, 2)
	require.Equal(t, "new-window-model", status.Observations[0].Model)
	require.Equal(t, "old-window-model", status.Observations[1].Model)
	require.Empty(t, SnapshotFingerprintObservations(0))
	require.Empty(t, records.records)
	requirePassiveWSNoMaintenance(t, state)
}

func TestCodexTurnStateStatusObservationNoStateDoesNotReplacePriorResponse(t *testing.T) {
	for _, withPrior := range []bool{false, true} {
		for _, delivered := range []bool{false, true} {
			t.Run(fmt.Sprintf("prior_%t_delivered_%t", withPrior, delivered), func(t *testing.T) {
				isolateCodexTurnStateSummaryStore(t)
				ctx := context.Background()
				owner := codexStateBatchOwner(1, false)
				accounts := &codexStateBatchAccounts{accounts: map[int64]*Account{1: owner}}
				records := &codexStateBatchRecords{}
				state := NewCodexTurnStateService(records, accounts, nil, nil)
				state.modelPolicy = &codexStateBatchPolicy{models: []string{"final-model"}}
				if withPrior {
					recordCodexStatusObservation(t, owner.ID, owner.ID, CodexTurnStateObservation{
						OSFamily: "windows",
						Model:    "final-model", Action: "passthrough", ResponseLength: 356, ResponseShape: "unknown",
						ResponseObservedShape: CodexTurnStateObservedTeamBusinessExtended,
						ResponseCipherBlocks:  13, ResponseValidationReason: "unexpected_shape", ResponseSource: "metadata",
					}, time.Now().Add(-time.Minute))
				}
				before, err := state.GetStatus(ctx, owner.ID)
				require.NoError(t, err)
				attempt, err := prepareCodexStateTest(state, ctx, owner, "final-model")
				require.NoError(t, err)
				require.NotNil(t, attempt)
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				noteOpenAICodexStatePatch(c, attempt, nil, nil)
				entry := FingerprintObservationEntry{AccountID: owner.ID, EventKind: FingerprintObservationEventHTTP}
				observation := populateCodexTurnStateObservation(c, &entry, nil, nil, false)
				bindCodexTurnStateSummarySequence(observation)
				// A successful response without state and a failed/abandoned send both
				// finish their wire lifecycle without a token observation timestamp.
				require.NoError(t, state.Finish(ctx, attempt, delivered))
				finishOpenAICodexStateObservation(attempt)
				require.True(t, attempt.SafeObservation().ObservedAt.IsZero())
				after, err := state.GetStatus(ctx, owner.ID)
				require.NoError(t, err)
				require.Equal(t, before.Observations, after.Observations, "finishing without a state must not overwrite the last real response summary")
				if withPrior {
					require.Len(t, after.Observations, 1)
					require.Equal(t, 356, after.Observations[0].ResponseLength)
				} else {
					require.Equal(t, []CodexTurnStateModelObservation{}, after.Observations)
				}
				require.Empty(t, records.records)
				requirePassiveWSNoMaintenance(t, state)
			})
		}
	}
}
