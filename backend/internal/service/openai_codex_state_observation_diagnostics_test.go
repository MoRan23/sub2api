package service

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestCodexTurnStateObservationDiagnosticsBindDeliveryAndSnapshot(t *testing.T) {
	for _, finishBeforeBind := range []bool{false, true} {
		for _, delivered := range []bool{false, true} {
			t.Run(testBoolName(finishBeforeBind)+"/"+testBoolName(delivered), func(t *testing.T) {
				isolateCodexTurnStateSummaryStore(t)
				now := time.Now()
				expiresAt := now.Add(time.Hour)
				token := codexStateTestToken(10, now.Add(-time.Minute))
				attempt := &CodexTurnStateAttempt{OSFamily: "windows", OwnerAccountID: 41, Model: "gpt-6-astra", AccountEnabled: true, accountType: "personal",
					Snapshot: CodexTurnStateSnapshot{Token: token, Source: "collector", Version: 17, ExpiresAt: expiresAt}}
				state := NewCodexTurnStateService(nil, nil, nil, nil)
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				noteOpenAICodexStatePatch(c, attempt, nil, nil)
				wire := populateCodexTurnStateObservation(c, nil, http.Header{"X-Codex-Turn-State": {token}}, nil, false)
				finish := func() {
					state.ObserveHeaders(attempt, http.Header{"X-Codex-Turn-State": {codexStateTestToken(11, now)}})
					require.NoError(t, state.Finish(context.Background(), attempt, delivered))
					finishOpenAICodexStateObservation(attempt)
				}
				if finishBeforeBind {
					finish()
					_, before := globalCodexTurnStateSummaryStore.snapshot([]int64{41})
					require.Empty(t, before, "a pending WS write is not a physical-send record")
				}
				bindCodexTurnStateSummarySequence(wire)
				if !finishBeforeBind {
					finish()
				}
				_, result := globalCodexTurnStateSummaryStore.snapshot([]int64{41})
				require.Len(t, result[41], 1)
				summary := result[41][0]
				require.NoError(t, uuid.Validate(summary.ObservationID))
				require.Equal(t, "injected", summary.OutboundAction)
				require.Equal(t, "collector", summary.OutboundSource)
				require.EqualValues(t, 17, summary.SnapshotVersion)
				require.Equal(t, &expiresAt, summary.SnapshotExpiresAt)
				require.NotNil(t, summary.RequestSentAt)
				require.False(t, summary.RequestSentAt.Before(now))
				require.False(t, summary.RequestSentAt.After(summary.ObservedAt))
				require.Equal(t, &delivered, summary.BusinessDelivered)
				require.Equal(t, 292, summary.OutboundLength)
				require.Equal(t, 312, summary.ResponseLength)
				encoded, err := json.Marshal(summary)
				require.NoError(t, err)
				require.NotContains(t, string(encoded), token)
				// Returned pointer fields must not mutate the process-local evidence.
				*summary.RequestSentAt = time.Time{}
				*summary.SnapshotExpiresAt = time.Time{}
				*summary.BusinessDelivered = !delivered
				_, unchanged := globalCodexTurnStateSummaryStore.snapshot([]int64{41})
				require.False(t, unchanged[41][0].RequestSentAt.IsZero())
				require.Equal(t, &expiresAt, unchanged[41][0].SnapshotExpiresAt)
				require.Equal(t, &delivered, unchanged[41][0].BusinessDelivered)
			})
		}
	}
}

func TestCodexTurnStateObservationDiagnosticsPassthroughReasons(t *testing.T) {
	for _, tc := range []struct {
		name, accountType, reason, want string
		enabled                         bool
	}{
		{name: "disabled", accountType: "personal", want: "cache_disabled"},
		{name: "unknown", enabled: true, want: "account_type_unknown"},
		{name: "missing", enabled: true, accountType: "personal", want: "cache_unavailable"},
		{name: "store_unavailable", enabled: true, accountType: "personal", reason: "maintenance_unavailable", want: "maintenance_unavailable"},
		{name: "excluded", enabled: true, accountType: "personal", reason: "model_excluded", want: "model_excluded"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			attempt := &CodexTurnStateAttempt{OSFamily: "windows", OwnerAccountID: 9, Model: "gpt-6-astra", AccountEnabled: tc.enabled, accountType: tc.accountType, MaintenanceReason: tc.reason}
			noteOpenAICodexStatePatch(c, attempt, nil, nil)
			wire := populateCodexTurnStateObservation(c, nil, http.Header{"X-Codex-Turn-State": {"guarded-client-token"}}, nil, false)
			require.Equal(t, tc.want, wire.value.MaintenanceReason)
			require.Equal(t, "passthrough", wire.value.Action)
			require.Equal(t, "client", wire.value.Source)
			require.Zero(t, wire.value.SnapshotVersion)
			require.Nil(t, wire.value.ExpiresAt)
		})
	}
}

func TestCodexTurnStateObservationDiagnosticsCollectorOmission(t *testing.T) {
	isolateCodexTurnStateSummaryStore(t)
	state := NewCodexTurnStateService(nil, nil, nil, nil)
	sentAt := time.Now().Add(-time.Second)
	id := uuid.NewString()
	state.recordCollectorObservation(&Account{ID: 7}, "gpt-6-astra", CodexTurnStateCollectResult{
		observationID: id, requestSentAt: sentAt,
		Observation: &CodexTurnStateSafeObservation{ObservedAt: time.Now(), TokenLength: 292, Shape: CodexTurnStateShapeTarget, ResponseSource: "header"},
	})
	_, result := globalCodexTurnStateSummaryStore.snapshot([]int64{7})
	require.Len(t, result[7], 1)
	summary := result[7][0]
	require.Equal(t, id, summary.ObservationID)
	require.Equal(t, "collector_omitted", summary.OutboundAction)
	require.Equal(t, "collector", summary.RequestSource)
	require.Equal(t, &sentAt, summary.RequestSentAt)
	require.Zero(t, summary.OutboundLength)
	require.Nil(t, summary.BusinessDelivered)
	require.Nil(t, summary.SnapshotExpiresAt)
	state.recordCollectorObservation(&Account{ID: 7}, "gpt-6-astra", CodexTurnStateCollectResult{})
	_, unchanged := globalCodexTurnStateSummaryStore.snapshot([]int64{7})
	require.Equal(t, result, unchanged)
}

func TestCodexTurnStateObservationDiagnosticsPendingOrFailedWSWritePreservesPrior(t *testing.T) {
	for _, written := range []bool{false, true} {
		t.Run(testBoolName(written), func(t *testing.T) {
			isolateCodexTurnStateSummaryStore(t)
			now := time.Now()
			globalCodexTurnStateSummaryStore.update(globalCodexTurnStateSummaryStore.nextSequence(), 7,
				CodexTurnStateObservation{OSFamily: "windows", Model: "gpt-6-astra", RequestSource: "collector", Action: "collector_omitted", ResponseLength: 292, ResponseShape: "target"}, true, now.Add(-time.Minute))
			_, prior := globalCodexTurnStateSummaryStore.snapshot([]int64{7})
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			state := NewCodexTurnStateService(nil, nil, nil, nil)
			attempt := &CodexTurnStateAttempt{OSFamily: "windows", OwnerAccountID: 7, Model: "gpt-6-astra", accountType: "personal"}
			noteOpenAICodexStatePatch(c, attempt, nil, nil)
			wire := populateCodexTurnStateObservation(c, nil, nil, []byte(`{"type":"response.create","model":"gpt-6-astra"}`), true)
			state.ObserveHeaders(attempt, http.Header{"X-Codex-Turn-State": {codexStateTestToken(11, now)}})
			require.NoError(t, state.Finish(context.Background(), attempt, written))
			finishOpenAICodexStateObservation(attempt)
			_, pending := globalCodexTurnStateSummaryStore.snapshot([]int64{7})
			require.Equal(t, prior, pending, "completion before successful WS write must not replace prior evidence")
			if !written {
				require.False(t, wire.logged)
				require.Nil(t, wire.value.RequestSentAt)
				return
			}
			bindCodexTurnStateSummarySequence(wire)
			_, completed := globalCodexTurnStateSummaryStore.snapshot([]int64{7})
			require.Equal(t, 312, completed[7][0].ResponseLength)
			require.Equal(t, "business", completed[7][0].RequestSource)
			require.NotNil(t, completed[7][0].RequestSentAt)
			require.Equal(t, &written, completed[7][0].BusinessDelivered)
		})
	}
}

func TestCodexTurnStateObservationDiagnosticsLogsOnlyBoundAttemptOnce(t *testing.T) {
	isolateCodexTurnStateSummaryStore(t)
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	state := NewCodexTurnStateService(nil, nil, nil, nil)
	now := time.Now()
	token := codexStateTestToken(10, now.Add(-time.Minute))
	attempt := &CodexTurnStateAttempt{OSFamily: "windows", OwnerAccountID: 7, Model: "gpt-6-astra", AccountEnabled: true, accountType: "personal",
		Generation: "never-log-generation", credentialEpoch: "never-log-epoch", id: "private-lease-id",
		Snapshot: CodexTurnStateSnapshot{Token: token, Source: "business", Version: 8, ExpiresAt: now.Add(time.Hour)}}
	noteOpenAICodexStatePatch(c, attempt, nil, nil)
	wire := populateCodexTurnStateObservation(c, nil, http.Header{"X-Codex-Turn-State": {token}}, nil, false)
	state.ObserveHeaders(attempt, http.Header{"X-Codex-Turn-State": {codexStateTestToken(11, now)}})
	require.NoError(t, state.Finish(context.Background(), attempt, false))
	finishOpenAICodexStateObservation(attempt)
	require.Empty(t, output.String(), "failed/pending WS write cannot emit physical-send completion")
	bindCodexTurnStateSummarySequence(wire)
	bindCodexTurnStateSummarySequence(wire)
	finishOpenAICodexStateObservation(attempt)
	require.Equal(t, 1, bytes.Count(output.Bytes(), []byte("openai_codex_turn_state_observation_completed")))
	var event map[string]any
	require.NoError(t, json.Unmarshal(output.Bytes(), &event))
	require.Equal(t, false, event["business_delivered"])
	require.Equal(t, "injected", event["outbound_action"])
	require.EqualValues(t, 292, event["outbound_length"])
	require.EqualValues(t, 8, event["snapshot_version"])
	require.NotEmpty(t, event["request_sent_at"])
	for _, forbidden := range []string{token, "never-log-generation", "never-log-epoch", "private-lease-id"} {
		require.NotContains(t, output.String(), forbidden)
	}
}

func testBoolName(value bool) string {
	if value {
		return "true"
	}
	return "false"
}
