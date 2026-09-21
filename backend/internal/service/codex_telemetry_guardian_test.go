package service

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCodexTelemetryReviewFactsAreIndependent(t *testing.T) {
	for _, test := range []struct {
		name               string
		auto, v2           *bool
		reviewer, expected string
		simulates          bool
	}{
		{"auto-review-off", boolPointer(false), nil, "", "", false},
		{"auto-review-on", boolPointer(true), nil, "", "auto_review", true},
		{"v2-on-auto-off", boolPointer(false), boolPointer(true), "auto_review", "auto_review", false},
		{"v2-off-auto-on", boolPointer(true), boolPointer(false), "auto_review", "auto_review", true},
		{"v2-on-user-reviewer", nil, boolPointer(true), "user", "user", false},
		{"explicit-reviewer-wins", boolPointer(true), nil, "user", "user", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			profile := codexTelemetryEventTestProfile()
			profile.input.AutoReviewEnabled, profile.input.GuardianV2Enabled, profile.input.ApprovalsReviewer = test.auto, test.v2, test.reviewer
			require.Equal(t, test.expected, codexTelemetryApprovalsReviewer(profile), "unknown facts remain unknown even when simulation fills required fields")
			turn := codexMainTurnEvent(profile, codexTelemetryTerminal{status: "completed", finished: profile.started.Add(time.Second)})
			if test.expected != "" {
				require.Equal(t, test.expected, turn.EventParams["approvals_reviewer"])
			}
			if test.v2 == nil {
				require.Equal(t, false, turn.EventParams["guardian_v2_enabled"])
				require.Equal(t, "simulated", turn.source)
			} else {
				require.Equal(t, *test.v2, turn.EventParams["guardian_v2_enabled"])
			}
			require.Equal(t, test.simulates, codexSimulatesGuardian(profile))
			profile.dynamicTool, profile.fileChange = false, false
			require.False(t, codexSimulatesGuardian(profile), "a reviewer requires an eligible activity")
		})
	}
}

func TestCodexTelemetryReviewSnapshotReadsFinalCarriers(t *testing.T) {
	account := &Account{ID: 12, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	for _, test := range []struct {
		name, body, header, reviewer string
		v2                           *bool
	}{
		{"missing", `{}`, `{}`, "", nil},
		{"header", `{}`, `{"guardian_v2_enabled":"true","approvals_reviewer":"auto_review"}`, "auto_review", boolPointer(true)},
		{"body-flat", `{"guardian_v2_enabled":false,"approvals_reviewer":"user"}`, `{}`, "user", boolPointer(false)},
		{"body-metadata-priority", `{"client_metadata":{"x-codex-turn-metadata":"{\"guardian_v2_enabled\":false,\"approvals_reviewer\":\"auto_review\"}"}}`, `{"guardian_v2_enabled":"true","approvals_reviewer":"user"}`, "auto_review", boolPointer(false)},
		{"string-extra", `{"x-codex-turn-metadata":{"guardian_v2_enabled":"true","approvals_reviewer":"user"}}`, `{}`, "user", boolPointer(true)},
		{"malformed-is-unknown", `{"guardian_v2_enabled":"invalid","approvals_reviewer":"invalid"}`, `{"guardian_v2_enabled":"true","approvals_reviewer":"user"}`, "", nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			headers := http.Header{}
			headers.Set(openAIWSTurnMetadataHeader, test.header)
			body := []byte(test.body)
			input := codexTelemetryInputFromWire(account, headers, body, "", false, finalFingerprintCodexWireProfile(headers, body))
			require.Equal(t, test.reviewer, input.ApprovalsReviewer)
			require.Equal(t, test.v2, input.GuardianV2Enabled)
			require.Nil(t, input.AutoReviewEnabled, "V2 and reviewer must not create an auto-review flag")
		})
	}
}

func TestCodexTelemetryGuardianRequestsKeepOnlyActualMeasurements(t *testing.T) {
	for _, test := range []struct{ name, source, subagent, header string }{
		{"review-source", "guardian_review", "", ""}, {"classifier-source", "guardian_classifier", "", ""},
		{"review-subagent", "", "guardian", ""}, {"classifier-subagent", "", "guardian_classifier", ""},
		{"review-header", "", "", "guardian"}, {"classifier-header", "", "", "guardian_classifier"},
	} {
		t.Run(test.name, func(t *testing.T) {
			profile := codexTelemetryEventTestProfile()
			profile.input.ThreadSource, profile.input.SubagentKind, profile.input.OpenAISubagent = test.source, test.subagent, test.header
			result := codexTelemetryTerminal{status: "failed", httpStatus: 503, finished: profile.started.Add(time.Second), result: CodexTelemetryResult{EventCount: 2, FailedEventCount: 1}}
			require.Empty(t, codexInitializationEvents(profile), "a request does not prove app-server initialized a thread")
			require.Empty(t, codexTerminalEvents(profile, result), "a Guardian response does not prove its client turn completed")
			store := newCodexTelemetryMetricStore()
			store.touch(profile)
			store.recordAttempt(profile, result)
			store.record(profile, result)
			batches := store.flush(profile.started.Add(2 * time.Minute))
			require.Len(t, batches, 1)
			require.Equal(t, "observed", batches[0].source)
			for _, name := range batches[0].names {
				require.Equal(t, "codex.sse_event", name)
			}
		})
	}
}

func TestCodexTelemetryBeginCopiesIndependentReviewFlags(t *testing.T) {
	s, _ := telemetryCaptureService(t)
	input := telemetryTestInput()
	input.AutoReviewEnabled, input.GuardianV2Enabled = boolPointer(false), boolPointer(true)
	input.ApprovalsReviewer = "auto_review"
	attempt := s.Begin(context.Background(), input)
	require.NotNil(t, attempt)
	*input.AutoReviewEnabled, *input.GuardianV2Enabled = true, false
	require.False(t, *attempt.profile.input.AutoReviewEnabled)
	require.True(t, *attempt.profile.input.GuardianV2Enabled)
	turn := codexMainTurnEvent(attempt.profile, codexTelemetryTerminal{status: "completed"}).EventParams
	require.Equal(t, true, turn["guardian_v2_enabled"])
	require.Equal(t, "auto_review", turn["approvals_reviewer"])
}

func TestCodexTelemetryGuardianDoesNotConsumeOrdinaryStartup(t *testing.T) {
	store, profile := newCodexTelemetryMetricStore(), codexMetricsTestProfile()
	profile.input.ThreadSource = "guardian_review"
	store.touch(profile)
	require.Empty(t, store.clients)
	profile.input.ThreadSource = "user"
	store.touch(profile)
	require.Len(t, store.clients, 1)
	batches := store.flush(profile.started.Add(time.Minute))
	require.Len(t, batches, 1)
	require.True(t, codexMetricsTestMetric(batches[0].body, "codex.process.start").Exists())
}
