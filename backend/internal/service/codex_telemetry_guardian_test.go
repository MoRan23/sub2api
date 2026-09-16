package service

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestCodexTelemetryReviewFactsAreIndependent(t *testing.T) {
	for _, test := range []struct {
		name      string
		auto      *bool
		v2        *bool
		reviewer  string
		expected  string
		simulates bool
	}{
		{"unknown", nil, nil, "", "", true},
		{"auto-review-off", boolPointer(false), nil, "", "", false},
		{"auto-review-on", boolPointer(true), nil, "", "auto_review", true},
		{"v2-on-auto-off", boolPointer(false), boolPointer(true), "auto_review", "auto_review", false},
		{"v2-off-auto-on", boolPointer(true), boolPointer(false), "auto_review", "auto_review", true},
		{"v2-on-user-reviewer", nil, boolPointer(true), "user", "user", false},
		{"explicit-reviewer-wins", boolPointer(true), nil, "user", "user", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			profile := codexTelemetryEventTestProfile()
			profile.input.AutoReviewEnabled, profile.input.GuardianV2Enabled = test.auto, test.v2
			profile.input.ApprovalsReviewer = test.reviewer
			turn := codexMainTurnEvent(profile, codexTelemetryTerminal{status: "completed"}).EventParams
			if test.expected == "" {
				require.NotContains(t, turn, "approvals_reviewer")
			} else {
				require.Equal(t, test.expected, turn["approvals_reviewer"])
			}
			if test.v2 == nil {
				require.NotContains(t, turn, "guardian_v2_enabled")
			} else {
				require.Equal(t, *test.v2, turn["guardian_v2_enabled"])
			}
			require.Equal(t, test.simulates, codexSimulatesGuardian(profile))
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

func TestCodexTelemetryGuardianRequestsKeepOnlyActualEventsAndMeasurements(t *testing.T) {
	for _, test := range []struct {
		name, source, subagent, header string
		guardian                       bool
	}{
		{"review-source", "guardian_review", "", "", true},
		{"classifier-source", "guardian_classifier", "", "", true},
		{"review-subagent", "", "guardian", "", true},
		{"classifier-subagent", "", "guardian_classifier", "", true},
		{"review-header", "", "", "guardian", true},
		{"classifier-header", "", "", "guardian_classifier", true},
		{"header-with-other-subagent", "user", "review", "guardian", true},
		{"ordinary-request", "user", "review", "", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			s, calls := telemetryCaptureService(t)
			s.randIntN = func(int) int { return 0 } // ordinary control simulates every optional behavior
			gateway := &OpenAIGatewayService{codexTelemetry: s}
			account := &Account{ID: 12, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
			session, thread, turn := uuid.Must(uuid.NewV7()).String(), uuid.Must(uuid.NewV7()).String(), uuid.Must(uuid.NewV7()).String()
			parent := uuid.Must(uuid.NewV7()).String()
			metadata, err := json.Marshal(map[string]any{
				"session_id": session, "thread_id": thread, "turn_id": turn, "parent_thread_id": parent,
				"thread_source": test.source, "subagent_kind": test.subagent, "auto_review_enabled": true,
			})
			require.NoError(t, err)
			body, err := json.Marshal(map[string]any{"model": "actual-model", "client_metadata": map[string]string{openAIWSTurnMetadataHeader: string(metadata)}})
			require.NoError(t, err)
			headers := http.Header{"Authorization": {"Bearer token"}, "Chatgpt-Account-Id": {"account"}, "X-Openai-Subagent": {test.header}}
			attempt := gateway.beginCodexTelemetryFromWire(context.Background(), account, headers, body, "", true)
			require.NotNil(t, attempt, "Guardian physical requests are not excluded from actual telemetry")
			started := attempt.profile.started
			attempt.Finish(CodexTelemetryResult{Status: "failed", HTTPStatus: 503, InputTokens: 9, OutputTokens: 2,
				FirstEventAt: started.Add(10 * time.Millisecond), FirstTokenAt: started.Add(20 * time.Millisecond), FinishedAt: started.Add(time.Second)})
			s.flushMetrics(time.Now())
			telemetryWaitDrained(t, s)
			var events []codexAnalyticsEvent
			metricNames := map[string]bool{}
			for _, sent := range calls() {
				if sent.metrics {
					for _, descriptor := range codexMetricDescriptors {
						metricNames[descriptor.name] = metricNames[descriptor.name] || codexMetricsTestMetric(sent.body, descriptor.name).Exists()
					}
					req := codexMetricsTestMetric(sent.body, "codex.websocket.request")
					if req.Exists() {
						require.Equal(t, "false", codexMetricsTestAttribute(req.Get("sum.dataPoints.0"), "success"))
					}
					continue
				}
				var payload struct {
					Events []codexAnalyticsEvent `json:"events"`
				}
				require.NoError(t, json.Unmarshal(sent.body, &payload))
				events = append(events, payload.Events...)
			}
			if test.guardian {
				require.Len(t, events, 2, "only the actual thread initialization and terminal turn remain")
				require.Equal(t, "codex_thread_initialized", events[0].EventType)
				require.Equal(t, "codex_turn_event", events[1].EventType)
				for _, event := range events {
					require.Equal(t, session, event.EventParams["session_id"])
					require.Equal(t, thread, event.EventParams["thread_id"])
					require.Equal(t, parent, event.EventParams["parent_thread_id"])
				}
				main := events[1].EventParams
				require.Equal(t, turn, main["turn_id"])
				require.Equal(t, "failed", main["status"])
				require.EqualValues(t, 9, main["input_tokens"])
				require.EqualValues(t, 2, main["output_tokens"])
				require.Zero(t, main["total_tool_call_count"])
				require.Nil(t, main["turn_trigger"], "a missing trigger must not become a user composer event")
				if test.source == "" {
					require.Nil(t, main["thread_source"])
				}
				for name, exists := range metricNames {
					if !exists {
						continue
					}
					require.Contains(t, []string{"codex.turn.e2e_duration_ms", "codex.turn.ttft.duration_ms", "codex.turn.ttfm.duration_ms", "codex.thread.started", "codex.turn.network_proxy", "codex.websocket.request", "codex.websocket.request.duration_ms", "codex.websocket.event", "codex.websocket.event.duration_ms"}, name)
				}
			} else {
				require.Len(t, events, 13)
				require.True(t, metricNames["codex.hooks.run"])
				require.True(t, metricNames["codex.tool.unified_exec"])
				require.True(t, metricNames["codex.process.start"])
			}
			require.True(t, metricNames["codex.turn.e2e_duration_ms"])
			require.True(t, metricNames["codex.websocket.request"])
			observations := s.Observations(CodexTelemetryObservationQuery{})
			require.EqualValues(t, 1, observations.Counters.Attempts)
			for _, item := range observations.Items {
				require.Equal(t, !test.guardian, item.ContainsSimulated)
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
	turn := codexMainTurnEvent(attempt.turn.profile, codexTelemetryTerminal{status: "completed"}).EventParams
	require.Equal(t, true, turn["guardian_v2_enabled"])
	require.Equal(t, "auto_review", turn["approvals_reviewer"])
}

func TestCodexTelemetryGuardianDoesNotConsumeOrdinaryStartup(t *testing.T) {
	store := newCodexTelemetryMetricStore()
	profile := codexMetricsTestProfile()
	profile.input.ThreadSource = "guardian_review"
	require.Empty(t, store.touch(profile))
	store.record(profile, codexTelemetryTerminal{status: "completed", finished: profile.started.Add(time.Second)})
	profile.input.ThreadSource = "user"
	require.Len(t, store.touch(profile), 1, "a Guardian request did not simulate a user's startup")
	store.record(profile, codexTelemetryTerminal{status: "completed", finished: profile.started.Add(time.Second)})
	require.Len(t, store.flush(profile.started.Add(time.Minute)), 2, "actual Guardian samples remain separate from user simulations")
}
