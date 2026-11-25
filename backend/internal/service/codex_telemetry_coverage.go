package service

// CodexTelemetrySignalCoverage describes collection capability, not a recorded
// event. In particular, understanding a metric schema never proves a sample
// was collected. Fixed diagnostics keep client contents and credentials out of
// the observations API.
// unavailable means the current exporter does not support that signal type;
// awaiting_source means the signal schema is known but no current entry point
// receives the client's measurements. Both statuses mean uncollected.
type CodexTelemetrySignalCoverage struct {
	Signal     string   `json:"signal"`
	Type       string   `json:"type"`
	Status     string   `json:"status"`
	EventNames []string `json:"event_names"`
	Reason     string   `json:"reason"`
}

func codexTelemetrySignalCoverage() []CodexTelemetrySignalCoverage {
	// Allocate each response independently so callers cannot alter subsequent
	// coverage diagnostics. These capabilities do not change with policy toggles.
	return []CodexTelemetrySignalCoverage{
		{
			Signal: "turn_critical_path", Type: "metrics", Status: "awaiting_source",
			EventNames: []string{"codex.turn.critical_path.capture", "codex.turn.critical_path.duration_ms", "codex.turn.critical_path.component_ms"},
			Reason:     "client_critical_path_not_on_responses_wire",
		},
		{
			Signal: "tool_declaration_updates", Type: "metrics", Status: "awaiting_source",
			EventNames: []string{"codex.tools.exec_entry_updates", "codex.tools.exec_update_bytes"},
			Reason:     "client_tool_declaration_updates_not_on_responses_wire",
		},
		{
			Signal: "skill_invocation", Type: "logs", Status: "unavailable",
			EventNames: []string{"codex.skill_invocation"},
			Reason:     "client_skill_event_not_on_responses_wire",
		},
		{
			Signal: "auth_storage", Type: "metrics", Status: "awaiting_source",
			EventNames: []string{
				"codex.auth_storage.operation", "codex.auth_storage.duration",
				"codex.auth_storage.refresh_persist", "codex.auth_storage.refresh_persist.duration",
				"codex.auth_storage.lock_acquire", "codex.auth_storage.lock_wait.duration", "codex.auth_storage.lock_hold.duration",
			},
			Reason: "client_auth_storage_not_on_responses_wire",
		},
		{
			Signal: "websocket_continuation", Type: "metrics", Status: "awaiting_source",
			EventNames: []string{"codex.websocket.continuation"},
			Reason:     "client_websocket_continuation_not_on_responses_wire",
		},
		{
			Signal: "used_dictation", Type: "analytics", Status: "awaiting_source",
			EventNames: []string{"codex_turn_event"},
			Reason:     "client_dictation_source_not_on_responses_wire",
		},
		{
			Signal: "guardian_v2_classification", Type: "analytics", Status: "awaiting_source",
			EventNames: []string{"codex_guardian_v2_classification"},
			Reason:     "client_guardian_v2_classification_not_on_responses_wire",
		},
		{
			Signal: "exec_command_timing", Type: "metrics", Status: "awaiting_source",
			EventNames: []string{"codex.exec_command.duration_total_ms", "codex.exec_command.calls"},
			Reason:     "client_exec_command_timing_not_on_responses_wire",
		},
		{
			Signal: "multi_agent_spawn", Type: "metrics", Status: "awaiting_source",
			EventNames: []string{"codex.multi_agent.spawn.failure", "codex.multi_agent.spawn.phase.duration_ms"},
			Reason:     "client_multi_agent_spawn_not_on_responses_wire",
		},
		{
			Signal: "additional_context", Type: "metrics", Status: "awaiting_source",
			EventNames: []string{"codex.additional_context", "codex.additional_context.value_bytes"},
			Reason:     "client_additional_context_not_on_responses_wire",
		},
		{
			Signal: "tool_registrations", Type: "metrics", Status: "awaiting_source",
			EventNames: []string{"codex.tools.registered"},
			Reason:     "client_tool_registrations_not_on_responses_wire",
		},
		{
			Signal: "mcp_binding_catalog", Type: "metrics", Status: "awaiting_source",
			EventNames: []string{"codex.mcp.binding_catalog.raw_definition_json_bytes"},
			Reason:     "client_mcp_binding_catalog_not_on_responses_wire",
		},
		{
			Signal: "model_search_discovery", Type: "metrics", Status: "awaiting_source",
			EventNames: []string{"codex.model_search.discovery"},
			Reason:     "client_model_search_discovery_not_on_responses_wire",
		},
		{
			Signal: "artifact_operation_metrics", Type: "metrics", Status: "awaiting_source",
			EventNames: []string{"codex.artifact.operation.started", "codex.artifact.operation.expected_output_count"},
			Reason:     "client_artifact_operation_not_on_responses_wire",
		},
		{
			Signal: "artifact_operation_analytics", Type: "analytics", Status: "awaiting_source",
			EventNames: []string{"codex_artifact_operation"},
			Reason:     "client_artifact_operation_not_on_responses_wire",
		},
	}
}
