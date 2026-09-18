package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

const environmentDiagnosticTestSession = "019539f0-8c00-7001-8000-000000000001"
const environmentDiagnosticTestThread = "019539f0-8c00-7001-8000-000000000002"
const environmentDiagnosticTestTurn = "019539f0-8c00-7001-8000-000000000003"

func environmentDiagnosticTestLogger(t *testing.T) (*gin.Context, *observer.ObservedLogs) {
	t.Helper()
	core, logs := observer.New(zap.InfoLevel)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(
		logger.IntoContext(context.Background(), zap.New(core).With(zap.String("request_id", "diagnostic-request"))),
	)
	return c, logs
}

func environmentDiagnosticTestState(t *testing.T) ([]byte, *RequestTimezoneState) {
	t.Helper()
	message := timezoneTestMessage(timezoneTestEnvironment("Asia/Shanghai", "2026-09-18"))
	message["internal_chat_message_metadata_passthrough"] = map[string]any{"content_item_kinds": []string{"user.text"}}
	metadata := timezoneTestBody(t, map[string]any{
		"session_id": environmentDiagnosticTestSession, "thread_id": environmentDiagnosticTestThread,
		"turn_id": environmentDiagnosticTestTurn, "request_kind": "turn", "thread_source": "memory_consolidation",
	})
	body := timezoneTestBody(t, map[string]any{
		"input": []any{message},
		"client_metadata": map[string]any{
			openAIWSTurnMetadataHeader: string(metadata), "x-openai-subagent": "memory_consolidation",
		},
	})
	prepared, state := PrepareOpenAIRequestTimezone(body, timezoneTestPolicy(), timezoneTestAcceptedAt(), false, false)
	require.Nil(t, state.Inbound, "diagnostics must not require fingerprint collection")
	require.Len(t, state.projectionSources, 1)
	require.NotNil(t, state.projectionSources[0].occurrence.environmentDiagnostic)
	return prepared, state
}

func TestOpenAIEnvironmentDiagnosticLogWithFingerprintDisabled(t *testing.T) {
	previous := globalFingerprintObserver.enabled.Swap(false)
	t.Cleanup(func() { globalFingerprintObserver.enabled.Store(previous) })
	for _, transport := range []string{"http", "ws"} {
		t.Run(transport, func(t *testing.T) {
			c, logs := environmentDiagnosticTestLogger(t)
			body, state := environmentDiagnosticTestState(t)
			SetRequestTimezoneState(c, state)
			account := newOpenAIIdentityPathOAuthAccount(71)
			headers := http.Header{"Session-Id": {"019539f0-8c00-7001-8000-000000000004"}}
			svc := &OpenAIGatewayService{}
			for attempt := 0; attempt < 2; attempt++ {
				if transport == "http" {
					svc.recordFingerprintObservationFromContextWithBody(c, account, headers, body)
				} else {
					svc.recordFingerprintObservationWSFrame(c, account, state, body, headers, nil)
				}
			}
			entries := logs.FilterMessage("openai.environment_source_diagnostic").All()
			require.Len(t, entries, 2, "physical retries must remain independently diagnosable")
			fields := entries[0].ContextMap()
			require.Equal(t, "diagnostic-request", fields["request_id"])
			require.Equal(t, int64(71), fields["account_id"])
			require.Equal(t, transport, fields["transport"])
			require.Equal(t, "complete", fields["scan_status"])
			require.Equal(t, false, fields["truncated"])
			require.Equal(t, "memory_consolidation", fields["thread_source"])
			wantSession := environmentDiagnosticTestSession
			if transport == "http" {
				wantSession = headers.Get("session-id")
			}
			require.Equal(t, wantSession, fields["observed_session_id"])
			require.Equal(t, environmentDiagnosticTestThread, fields["observed_thread_id"])
			require.Equal(t, environmentDiagnosticTestTurn, fields["observed_turn_id"])
			candidates := fields["candidates"].([]openAIEnvironmentDiagnosticCandidate)
			require.Len(t, candidates, 1)
			require.False(t, candidates[0].Eligible)
			require.Equal(t, "skipped", candidates[0].ConversionStatus)
			require.Equal(t, "Asia/Shanghai", candidates[0].Timezone)
			require.Equal(t, "2026-09-18", candidates[0].CurrentDate)
			require.NotNil(t, candidates[0].Diagnostic)
		})
	}
}

func TestOpenAIEnvironmentDiagnosticLogGuardsAndHealthySources(t *testing.T) {
	c, logs := environmentDiagnosticTestLogger(t)
	body, state := environmentDiagnosticTestState(t)
	svc, account := &OpenAIGatewayService{}, newOpenAIIdentityPathOAuthAccount(72)
	svc.logOpenAIEnvironmentSourceDiagnostic(nil, account, state, body, nil, "http")
	svc.logOpenAIEnvironmentSourceDiagnostic(c, nil, state, body, nil, "http")
	svc.logOpenAIEnvironmentSourceDiagnostic(c, newOpenAIIdentityPathAPIKeyAccount(72), state, body, nil, "http")
	svc.logOpenAIEnvironmentSourceDiagnostic(c, account, nil, body, nil, "http")
	svc.logOpenAIEnvironmentSourceDiagnostic(c, account, state, nil, nil, "http")
	svc.logOpenAIEnvironmentSourceDiagnostic(c, account, &RequestTimezoneState{}, body, nil, "http")
	healthyBody := timezoneTestBody(t, map[string]any{"input": timezoneTestInput(timezoneTestEnvironment("Asia/Shanghai", "2026-09-18"))})
	healthyBody, healthy := PrepareOpenAIRequestTimezone(healthyBody, timezoneTestPolicy(), timezoneTestAcceptedAt(), false, false)
	svc.logOpenAIEnvironmentSourceDiagnostic(c, account, healthy, healthyBody, nil, "http")
	require.Zero(t, logs.FilterMessage("openai.environment_source_diagnostic").Len())
	// Earlier calls without a selected account or a body must not consume output.
	svc.logOpenAIEnvironmentSourceDiagnostic(c, account, state, body, nil, "http")
	require.Equal(t, 1, logs.FilterMessage("openai.environment_source_diagnostic").Len())
}

func TestOpenAIEnvironmentDiagnosticLogMappedPathsAndLimits(t *testing.T) {
	c, logs := environmentDiagnosticTestLogger(t)
	body, state := environmentDiagnosticTestState(t)
	originalPath := state.projectionSources[0].occurrence.item.Path
	mapped := RemapRequestTimezoneState(state, map[string]string{originalPath: "input.8.content.0.text"})
	svc, account := &OpenAIGatewayService{}, newOpenAIIdentityPathOAuthAccount(73)
	svc.logOpenAIEnvironmentSourceDiagnostic(c, account, mapped, body, nil, "ws")
	fields := logs.All()[0].ContextMap()
	candidate := fields["candidates"].([]openAIEnvironmentDiagnosticCandidate)[0]
	require.Equal(t, originalPath, candidate.SourcePath)
	require.Equal(t, "input.8.content.0.text", candidate.Path)
	for i := 1; i < openAIRequestTimezoneItemLimit+3; i++ {
		source := state.projectionSources[0]
		source.occurrence.item.Path = fmt.Sprintf("input.%d.content.0.text", i)
		state.projectionSources = append(state.projectionSources, source)
	}
	svc.logOpenAIEnvironmentSourceDiagnostic(c, account, state, body, nil, "ws")
	fields = logs.All()[1].ContextMap()
	require.Len(t, fields["candidates"], openAIRequestTimezoneItemLimit)
	require.Equal(t, true, fields["truncated"])
	require.Equal(t, int64(3), fields["omitted_candidates"])
	incomplete := &RequestTimezoneState{projectionScanStatus: "limited", AcceptedAt: state.AcceptedAt, Target: state.Target}
	svc.logOpenAIEnvironmentSourceDiagnostic(c, account, incomplete, body, nil, "http")
	fields = logs.All()[2].ContextMap()
	require.Equal(t, true, fields["scan_incomplete"])
	require.Equal(t, true, fields["truncated"])
	require.Empty(t, fields["candidates"])
}

func TestOpenAIEnvironmentDiagnosticLogDoesNotLeakContentOrCredentials(t *testing.T) {
	c, logs := environmentDiagnosticTestLogger(t)
	message := timezoneTestMessage("<environment_context>\n<cwd>/private/secret-workspace</cwd>\n<timezone>secret-invalid-timezone</timezone>\n<current_date>secretdate</current_date>\n</environment_context>")
	message["internal_chat_message_metadata_passthrough"] = map[string]any{
		"content_item_kinds": []string{"secret-arbitrary-kind"}, "authorization": "secret-metadata-credential",
	}
	metadata := timezoneTestBody(t, map[string]any{
		"session_id": "secret-invalid-session", "thread_id": "secret-invalid-thread", "turn_id": "secret-invalid-turn",
		"thread_source": strings.Repeat("secret-source", 20), "subagent_kind": "secret\nsubagent", "turn_trigger": "secret trigger",
	})
	body := timezoneTestBody(t, map[string]any{
		"input":         []any{message, map[string]any{"role": "user", "content": "secret-user-body"}},
		"authorization": "secret-body-credential",
		"client_metadata": map[string]any{
			openAIWSTurnMetadataHeader: string(metadata), "x-openai-subagent": "secret\nsubagent",
		},
	})
	_, state := PrepareOpenAIRequestTimezone(body, timezoneTestPolicy(), timezoneTestAcceptedAt(), false, false)
	headers := http.Header{"Authorization": {"Bearer secret-header-credential"}, "Session-Id": {environmentDiagnosticTestSession}, "Thread-Id": {environmentDiagnosticTestThread}}
	svc := &OpenAIGatewayService{}
	svc.logOpenAIEnvironmentSourceDiagnostic(c, newOpenAIIdentityPathOAuthAccount(74), state, body, headers, "ws")
	entries := logs.FilterMessage("openai.environment_source_diagnostic").All()
	require.Len(t, entries, 1)
	fields := entries[0].ContextMap()
	require.Empty(t, fields["observed_session_id"], "WS cannot borrow stale handshake IDs")
	require.Empty(t, fields["observed_thread_id"])
	require.Empty(t, fields["observed_turn_id"])
	encoded, err := json.Marshal(fields)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "secret")
	require.NotContains(t, string(encoded), "/private/")
	for _, candidate := range fields["candidates"].([]openAIEnvironmentDiagnosticCandidate) {
		require.Empty(t, candidate.Timezone)
		require.Empty(t, candidate.CurrentDate)
	}
}

func TestOpenAIEnvironmentDiagnosticHeaderUUIDRejectsMalformedAliases(t *testing.T) {
	headers := http.Header{"Session-Id": {environmentDiagnosticTestSession}, "Session_id": {"not-a-uuid"}}
	require.Empty(t, openAIEnvironmentDiagnosticHeaderUUID(headers, environmentDiagnosticTestSession, "session-id", "session_id"))
	require.Empty(t, openAIEnvironmentDiagnosticHeaderUUID(http.Header{"Session-Id": {environmentDiagnosticTestSession, environmentDiagnosticTestThread}}, environmentDiagnosticTestSession, "session-id"))
}
