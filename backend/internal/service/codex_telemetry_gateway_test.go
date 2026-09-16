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

func TestCodexTelemetrySnapshotUsesFinalWireOnly(t *testing.T) {
	session := uuid.Must(uuid.NewV7()).String()
	thread := uuid.Must(uuid.NewV7()).String()
	turn := uuid.Must(uuid.NewV7()).String()
	parent := uuid.Must(uuid.NewV7()).String()
	parentTurn := uuid.Must(uuid.NewV7()).String()
	rootTurn := uuid.Must(uuid.NewV7()).String()
	metadata, err := json.Marshal(map[string]any{
		"request_kind": "turn", "session_id": session, "thread_id": thread, "turn_id": turn,
		"parent_thread_id": parent, "parent_turn_id": parentTurn, "root_turn_id": rootTurn,
		"sandbox": "workspace-write", "sandbox_mode": "workspace-write", "auto_review_enabled": false,
		"thread_source": "user", "turn_trigger": "composer", "agent_name": "/root",
	})
	require.NoError(t, err)
	body, err := json.Marshal(map[string]any{
		"model": "actual-model", "reasoning": map[string]string{"effort": "high"}, "service_tier": "priority",
		"client_metadata": map[string]string{openAIWSTurnMetadataHeader: string(metadata)},
		"input":           "PRIVATE PROMPT MUST NOT APPEAR IN SNAPSHOT",
	})
	require.NoError(t, err)
	headers := http.Header{
		"authorization": {"Bearer actual-token"}, "Chatgpt-Account-Id": {"actual-account"},
		"User-Agent": {"codex-tui/0.154.0 (Windows 10.0.26200; x86_64) WindowsTerminal"}, "originator": {"codex-tui"},
	}
	account := &Account{ID: 12, Name: "test", Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	profile := finalFingerprintCodexWireProfile(headers, body)
	input := codexTelemetryInputFromWire(account, headers, body, "http://proxy", false, profile)
	require.Equal(t, session, input.SessionID)
	require.Equal(t, thread, input.ThreadID)
	require.Equal(t, turn, input.TurnID)
	require.Equal(t, parent, input.ParentThreadID)
	require.Equal(t, parentTurn, input.ParentTurnID)
	require.Equal(t, rootTurn, input.RootTurnID)
	require.Equal(t, "actual-model", input.Model)
	require.Equal(t, "actual-token", input.AccessToken)
	require.Equal(t, "actual-account", input.ChatGPTAccountID)
	require.Equal(t, "0.154.0", input.Version)
	require.Equal(t, "high", input.Effort)
	require.Equal(t, "priority", input.ServiceTier)
	require.NotNil(t, input.AutoReviewEnabled)
	require.False(t, *input.AutoReviewEnabled)
	*profile.AutoReviewEnabled = true
	require.False(t, *input.AutoReviewEnabled, "snapshot must own the review flag")
	serialized, err := json.Marshal(input)
	require.NoError(t, err)
	require.NotContains(t, string(serialized), "PRIVATE PROMPT")
	require.NotContains(t, string(serialized), "actual-token")
	require.NotContains(t, string(serialized), "http://proxy")

	// A per-turn frame on a pooled connection must not inherit an old thread.
	oldThread := uuid.Must(uuid.NewV7()).String()
	headers.Set("thread-id", oldThread)
	ws := codexTelemetryInputFromWire(account, headers, body, "", true, profile)
	require.Equal(t, thread, ws.ThreadID)
	httpInput := codexTelemetryInputFromWire(account, headers, body, "", false, profile)
	require.Equal(t, oldThread, httpInput.ThreadID)
	missing := codexTelemetryInputFromWire(account, nil, []byte(`{"model":"m"}`), "", false, CodexWireProfile{})
	require.Empty(t, missing.SessionID)
	require.Empty(t, missing.ThreadID)
	require.Empty(t, missing.TurnID)
	require.Empty(t, missing.AccessToken)
	// An explicitly malformed final header is not repaired from another carrier.
	headers.Set("thread-id", "invalid")
	require.Empty(t, codexTelemetryInputFromWire(account, headers, body, "", false, profile).ThreadID)
}

func TestCodexTelemetryGatewayExcludesInternalAndNonOAuth(t *testing.T) {
	telemetry := NewCodexTelemetryService(nil)
	t.Cleanup(telemetry.Stop)
	svc := &OpenAIGatewayService{codexTelemetry: telemetry}
	oauth := &Account{ID: 12, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	for _, body := range []string{
		`{"request_kind":"prewarm"}`, `{"request_kind":"compaction"}`, `{"request_kind":"memory"}`,
		`{"generate":false}`, `{"model":"gpt-image-1"}`, `{"tools":[{"type":"image_generation"}]}`,
		`{"client_metadata":{"x-codex-turn-metadata":"{\"request_kind\":\"memory\"}"}}`,
	} {
		t.Run(body, func(t *testing.T) {
			require.Nil(t, svc.beginCodexTelemetryFromWire(context.Background(), oauth, nil, []byte(body), "", false))
		})
	}
	for _, account := range []*Account{nil, {ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}, {ID: 2, Platform: PlatformAnthropic, Type: AccountTypeOAuth}} {
		require.Nil(t, svc.beginCodexTelemetryFromWire(context.Background(), account, nil, []byte(`{"model":"gpt"}`), "", false))
	}
	require.Zero(t, telemetry.Observations(CodexTelemetryObservationQuery{}).Counters.Attempts)
}

func TestCodexTelemetryResultPreservesActualOutcomeAndUsage(t *testing.T) {
	now := time.Now()
	result := codexTelemetryResultFromResponse([]byte(`{"type":"response.done","response":{"id":"resp_actual","status":"incomplete","usage":{"input_tokens":21,"input_tokens_details":{"cached_tokens":7},"output_tokens":8,"output_tokens_details":{"reasoning_tokens":3}}}}`), "completed", 200, now, time.Time{})
	require.Equal(t, "failed", result.Status)
	require.Equal(t, "resp_actual", result.ResponseID)
	require.EqualValues(t, 21, result.InputTokens)
	require.EqualValues(t, 7, result.CachedInputTokens)
	require.EqualValues(t, 8, result.OutputTokens)
	require.EqualValues(t, 3, result.ReasoningOutputTokens)
	require.True(t, result.FirstTokenAt.IsZero())
	require.False(t, result.ExplicitClientInterrupt)
	for _, raw := range []string{`{"error":{"message":"secret"}}`, `{"status":"unknown"}`, `{"type":"error"}`} {
		require.Equal(t, "failed", codexTelemetryResultFromResponse([]byte(raw), "completed", 200, now, now).Status)
	}
	require.Equal(t, "failed", codexTelemetryResultFromResponse([]byte(`{"status":"completed"}`), "completed", 503, now, now).Status)
}
