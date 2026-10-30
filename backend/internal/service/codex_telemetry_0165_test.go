package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCodexTelemetryAuxiliaryHeadersUseFinalWire(t *testing.T) {
	account := &Account{ID: 11}
	for _, test := range []struct {
		name           string
		headers        http.Header
		sku, residency string
	}{
		{"absent", http.Header{}, "", ""},
		{"actual", http.Header{"x-openai-product-sku": {"tpp"}, "x-openai-internal-codex-residency": {"us"}}, "tpp", "us"},
		{"conflicting", http.Header{"X-OpenAI-Product-Sku": {"tpp", "aeon"}}, "", ""},
		{"invalid", http.Header{"X-OpenAI-Product-Sku": {"tpp\r\nInjected: yes"}}, "", ""},
		{"oversized", http.Header{"X-OpenAI-Product-Sku": {strings.Repeat("a", 257)}}, "", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			for _, headers := range []http.Header{test.headers, cloneFingerprintObservationHeaders(test.headers)} {
				input := codexTelemetryInputFromWire(account, headers, []byte(`{"product_sku":"guessed","model":"example"}`), "", false, CodexWireProfile{})
				require.Equal(t, test.sku, input.ProductSKU)
				require.Equal(t, test.residency, input.CodexResidency)
			}
		})
	}
	headers := http.Header{}
	headers.Set("X-OpenAI-Product-Sku", "frozen")
	physical := cloneFingerprintObservationHeaders(headers)
	headers[http.CanonicalHeaderKey("X-OpenAI-Product-Sku")][0] = "changed"
	require.Equal(t, "frozen", codexTelemetryAuxiliaryHeader(physical, "X-OpenAI-Product-Sku"))
}

func telemetryAuxiliaryTestSend(t *testing.T, profile codexTelemetryProfile, metrics bool) http.Header {
	t.Helper()
	s := &CodexTelemetryService{analyticsURL: "https://example.test/events", metricsURL: "https://example.test/metrics"}
	var headers http.Header
	code, reason, cancelled := s.send(context.Background(), func(_ context.Context, request *http.Request, _ CodexTelemetryInput, _ bool) (*http.Response, error) {
		headers = request.Header.Clone()
		return &http.Response{StatusCode: 202, Body: io.NopCloser(strings.NewReader("{}"))}, nil
	}, codexTelemetryJob{profile: profile, metrics: metrics, body: []byte(`{}`)})
	require.Equal(t, 202, code)
	require.Empty(t, reason)
	require.False(t, cancelled)
	return headers
}

func TestCodexTelemetryAuxiliaryHeadersStayFrozenInBatches(t *testing.T) {
	profile := codexTelemetryEventTestProfile()
	profile.input.ProductSKU, profile.input.CodexResidency = "tpp", "us"
	tx := &CodexTelemetryPoolTransaction{}
	for _, sku := range []string{"tpp", "aeon"} {
		profile.input.ProductSKU = sku
		require.NoError(t, appendRuntimeAnalytics(tx, profile, []codexAnalyticsEvent{codexMainTurnEvent(profile, codexTelemetryTerminal{})}, profile.started))
	}
	profile.input.ProductSKU, profile.input.CodexResidency = "newer", ""
	for index, batch := range tx.Batches {
		restored, err := unmarshalCodexTelemetryProfile(batch.Metadata)
		require.NoError(t, err)
		headers := telemetryAuxiliaryTestSend(t, restored, false)
		require.Equal(t, []string{"tpp", "aeon"}[index], headers.Get("X-OpenAI-Product-Sku"))
		require.Equal(t, "us", headers.Get("X-OpenAI-Internal-Codex-Residency"))
		require.Empty(t, headers.Get("Version"))
		require.NotContains(t, string(batch.Payload), "product_sku")
		metrics := telemetryAuxiliaryTestSend(t, restored, true)
		require.Empty(t, metrics.Get("X-OpenAI-Product-Sku"))
		require.Empty(t, metrics.Get("X-OpenAI-Internal-Codex-Residency"))
		require.Equal(t, "OTel-OTLP-Exporter-Rust/0.31.0", metrics.Get("User-Agent"))
	}
	var old map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(tx.Batches[0].Metadata, &old))
	delete(old, "product_sku")
	delete(old, "codex_residency")
	encoded, err := json.Marshal(old)
	require.NoError(t, err)
	restored, err := unmarshalCodexTelemetryProfile(encoded)
	require.NoError(t, err)
	headers := telemetryAuxiliaryTestSend(t, restored, false)
	require.Empty(t, headers.Get("X-OpenAI-Product-Sku"))
	require.Empty(t, headers.Get("X-OpenAI-Internal-Codex-Residency"))
}

func TestCodexTelemetryHydrationPreservesAuxiliaryHeaders(t *testing.T) {
	s, _, _, job := telemetryPersistedTransportFixture(t)
	job.profile.input.ProductSKU, job.profile.input.CodexResidency = "frozen", "us"
	require.Empty(t, s.hydrateTelemetryTransport(context.Background(), &job))
	require.Equal(t, "frozen", job.profile.input.ProductSKU)
	require.Equal(t, "us", job.profile.input.CodexResidency)
}

func telemetryRequireEventKeys(t *testing.T, event codexAnalyticsEvent, fields string) {
	t.Helper()
	keys := make([]string, 0, len(event.EventParams))
	for key := range event.EventParams {
		keys = append(keys, key)
	}
	require.ElementsMatch(t, strings.Fields(fields), keys, event.EventType)
}

// Independent key sets copied from Codex 0.165.0 analytics/events.rs, including
// its flattened critical-path fields (unchanged in 0.166.0). These prevent
// compatibility fields from silently leaking into the official schema.
func TestCodexTelemetry0165AnalyticsSchemaAndProvenance(t *testing.T) {
	profile := codexTelemetryEventTestProfile()
	turn := codexMainTurnEvent(profile, codexTelemetryTerminal{status: "completed", finished: profile.started.Add(time.Second), httpStatus: 200})
	telemetryRequireEventKeys(t, turn, `thread_id session_id turn_id active_plugin_ids_at_turn_start voice_session_id root_turn_id turn_trigger codex_turn_source submission_type app_server_client runtime ephemeral thread_source initialization_mode subagent_source parent_thread_id model model_provider sandbox_policy reasoning_effort reasoning_summary service_tier approval_policy approvals_reviewer guardian_v2_enabled multi_agent_version sandbox_network_access collaboration_mode personality workspace_kind num_input_images image_preparations is_first_turn status explicit_client_interrupt_requested_at_ms turn_error codex_error_kind codex_error_http_status_code usage_limit_window_minutes steer_count total_tool_call_count shell_command_count file_change_count mcp_tool_call_count dynamic_tool_call_count subagent_tool_call_count web_search_count image_generation_count input_tokens cached_input_tokens cache_write_input_tokens output_tokens reasoning_output_tokens total_tokens before_first_sampling_ms sampling_ms compaction_ms between_sampling_overhead_ms tool_blocking_ms after_last_sampling_ms sampling_request_count sampling_retry_count tools_change_count critical_path_status critical_path_duration_ms critical_path_inference_ms critical_path_auto_review_ms critical_path_user_wait_ms critical_path_tools_ms critical_path_overhead_ms critical_path_unknown_ms duration_ms started_at completed_at`)
	for _, field := range []string{"active_plugin_ids_at_turn_start", "voice_session_id", "usage_limit_window_minutes"} {
		require.Contains(t, turn.EventParams, field)
		require.Nil(t, turn.EventParams[field])
	}
	require.Equal(t, "disabled", turn.EventParams["multi_agent_version"])
	require.Equal(t, 0, turn.EventParams["tools_change_count"])
	const base = `thread_id session_id turn_id root_turn_id item_id cell_id parent_call_id originating_response_id subsequent_response_id app_server_client runtime thread_source subagent_source parent_thread_id tool_name tool_event_type started_at_ms completed_at_ms duration_ms execution_duration_ms review_count guardian_review_count user_review_count final_approval_outcome terminal_status failure_kind requested_additional_permissions requested_network_access `
	command := codexCommandEvent(profile, nil)
	telemetryRequireEventKeys(t, command, base+`sandbox_backend model_slug reasoning_effort plugin_id script_path command_execution_source exit_code command_total_action_count command_read_action_count command_list_files_action_count command_search_action_count command_unknown_action_count`)
	telemetryRequireEventKeys(t, codexDynamicToolEvent(profile, nil), base+`dynamic_tool_name success output_content_item_count output_text_item_count output_image_item_count output_audio_item_count`)
	telemetryRequireEventKeys(t, codexFileChangeEvent(profile, nil), base+`file_change_count file_add_count file_update_count file_delete_count file_move_count`)
	require.Nil(t, command.EventParams["tool_event_type"])
	require.Nil(t, command.EventParams["sandbox_backend"])
	require.Equal(t, profile.model, command.EventParams["model_slug"])
	require.Equal(t, profile.effort, command.EventParams["reasoning_effort"])
	guardian := codexGuardianReviewEvent(profile)
	telemetryRequireEventKeys(t, guardian, `session_id app_server_client runtime guardian_context_mode thread_id turn_id review_id target_item_id approval_request_source reviewed_action reviewed_action_truncated decision terminal_status failure_reason attempt_count risk_level user_authorization outcome guardian_thread_id guardian_session_kind guardian_model guardian_reasoning_effort guardian_default_review_model_id guardian_catalog_contains_auto_review guardian_review_model_overridden guardian_review_model_override guardian_model_provider_id had_prior_review_context review_timeout_ms tool_call_count time_to_first_token_ms completion_latency_ms started_at completed_at input_tokens cached_input_tokens cache_write_input_tokens output_tokens reasoning_output_tokens total_tokens`)
	telemetryRequireEventKeys(t, codexInitializationEvents(profile)[0], `thread_id session_id app_server_client runtime model ephemeral is_worktree thread_source initialization_mode subagent_source parent_thread_id forked_from_thread_id created_at`)
	telemetryRequireEventKeys(t, codexHookEvent(profile, "Stop"), `thread_id turn_id product_client_id model_slug hook_name hook_source handler_type execution_mode status`)
	telemetryRequireEventKeys(t, codexAcceptedLinesEvent(profile), `event_type turn_id thread_id product_surface model_slug completed_at repo_hash accepted_added_lines accepted_deleted_lines line_fingerprints`)
	require.Contains(t, guardian.EventParams, "guardian_context_mode")
	require.Nil(t, guardian.EventParams["guardian_context_mode"])
	tx := &CodexTelemetryPoolTransaction{}
	require.NoError(t, appendRuntimeAnalytics(tx, profile, []codexAnalyticsEvent{turn, command}, profile.started))
	restored, err := unmarshalCodexTelemetryProfile(tx.Batches[0].Metadata)
	require.NoError(t, err)
	require.Equal(t, "mixed", tx.Batches[0].Source)
	observer := &CodexTelemetryService{}
	entry := observer.newObservationLocked(restored, 1, "analytics", nil, 1)
	require.True(t, entry.ContainsSimulated)
	require.Equal(t, map[string]string{"multi_agent_version": "simulated", "tools_change_count": "simulated", "critical_path_status": "simulated", "model_slug": "observed", "reasoning_effort": "observed"}, entry.FieldSources)
	require.NotContains(t, string(tx.Batches[0].Payload), "field_sources")
	require.Equal(t, "actual-parent-turn", restored.input.ParentTurnID)
	require.Equal(t, "actual-fork-source", restored.input.ForkedFromThreadID)
	require.Equal(t, "review-agent", restored.input.AgentName)
	profile.model, profile.effort = "", ""
	unknown := codexCommandEvent(profile, nil)
	require.Nil(t, unknown.EventParams["model_slug"])
	require.Nil(t, unknown.EventParams["reasoning_effort"])
	profile.simulationEnabled = false
	require.Empty(t, codexTerminalEvents(profile, codexTelemetryTerminal{}))
	observed := codexMainTurnEvent(profile, codexTelemetryTerminal{})
	require.NotContains(t, observed.EventParams, "multi_agent_version")
	require.NotContains(t, observed.EventParams, "tools_change_count")
}
