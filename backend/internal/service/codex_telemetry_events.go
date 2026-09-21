package service

import (
	"crypto/sha256"
	"encoding/binary"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/tidwall/gjson"
)

// Analytics follows the Codex event vocabulary. The tool, title and hook events
// below are simulated; they must never be used as request fingerprint evidence.
type codexAnalyticsEvent struct {
	EventType   string         `json:"event_type"`
	EventParams map[string]any `json:"event_params"`
	client      codexTelemetryClient
	source      string
}

type codexThreadSpec struct {
	threadID       string
	sessionID      string
	parentThreadID any
	forkedFromID   any
	source         string
	subagentSource any
	model          string
	ephemeral      bool
}

type codexTurnSpec struct {
	threadID string
	turnID   string
	model    string
	effort   string
}

type codexToolSpec struct {
	terminal []byte
	itemID   string
	duration int64
	status   string
}

func newCodexAnalyticsEvent(profile codexTelemetryProfile, eventType string, params map[string]any) codexAnalyticsEvent {
	return codexAnalyticsEvent{EventType: eventType, EventParams: params, client: profile.client, source: "simulated"}
}

// Initialization needs actual session/thread identity, but does not invent a
// main turn ID when a client did not send one.
func codexInitializationEvents(profile codexTelemetryProfile) []codexAnalyticsEvent {
	if profile.sessionID == "" || profile.threadID == "" || !codexSimulatesClientBehavior(profile) {
		return nil
	}
	events := make([]codexAnalyticsEvent, 0, 4)
	if profile.firstThread {
		events = append(events, codexThreadInitialized(profile, codexThreadSpec{
			threadID: profile.threadID, sessionID: profile.sessionID,
			parentThreadID: codexOptionalString(profile.input.ParentThreadID),
			forkedFromID:   codexOptionalString(profile.input.ForkedFromThreadID),
			source:         codexTelemetryThreadSource(profile),
			subagentSource: codexOptionalString(profile.input.SubagentKind), model: profile.model,
		}))
	}
	return events
}

func codexThreadInitialized(profile codexTelemetryProfile, spec codexThreadSpec) codexAnalyticsEvent {
	appServer := codexAppServerClient(profile)
	if spec.source == "guardian_review" {
		appServer["rpc_transport"], appServer["experimental_api_enabled"] = "in_process", nil
	}
	initializationMode := "new"
	if spec.forkedFromID != nil {
		initializationMode = "forked"
	}
	params := map[string]any{
		"app_server_client": appServer, "created_at": profile.started.Unix(),
		"ephemeral": spec.ephemeral, "forked_from_thread_id": spec.forkedFromID, "initialization_mode": initializationMode,
		"is_worktree": nil,
		"model":       spec.model, "parent_thread_id": spec.parentThreadID, "runtime": codexRuntime(profile),
		"session_id": spec.sessionID, "subagent_source": spec.subagentSource, "thread_id": spec.threadID,
		"thread_source": codexOptionalString(spec.source),
	}
	return newCodexAnalyticsEvent(profile, "codex_thread_initialized", params)
}

func codexTitleTurnEvent(profile codexTelemetryProfile, threadID string) codexAnalyticsEvent {
	duration := codexActivityDuration(profile, "title", 800, 1800)
	turnID := codexActivityID(profile, "title-turn")
	title := profile
	title.sessionID, title.threadID, title.rootTurnID = threadID, threadID, turnID
	title.dynamicTool, title.command, title.fileChange = false, false, false
	title.attemptCount, title.samplingCount, title.clientRetryCount = 1, 1, 0
	reviewDisabled := false
	// Synthetic title identity must not inherit a real subagent's ancestry.
	title.input.ParentThreadID, title.input.ParentTurnID, title.input.ForkedFromThreadID = "", "", ""
	title.input.AutoReviewEnabled, title.input.ThreadSource, title.input.TurnTrigger = &reviewDisabled, "thread_title", "thread_title"
	params := codexTurnEventBase(title, codexTurnSpec{threadID, turnID, profile.model, profile.effort})
	params["approval_policy"], params["approvals_reviewer"] = "never", "user"
	params["before_first_sampling_ms"], params["sampling_ms"] = duration/2, duration/2
	completed := codexActivityCompletedAt(profile)
	params["started_at"], params["completed_at"], params["duration_ms"] = completed.Add(-time.Duration(duration)*time.Millisecond).Unix(), completed.Unix(), duration
	params["ephemeral"], params["is_first_turn"] = true, true
	params["sandbox_policy"], params["status"] = "read_only", "completed"
	params["reasoning_summary"], params["workspace_kind"] = nil, nil
	return newCodexAnalyticsEvent(profile, "codex_turn_event", params)
}

func codexTerminalEvents(profile codexTelemetryProfile, result codexTelemetryTerminal) []codexAnalyticsEvent {
	if profile.sessionID == "" || profile.threadID == "" || profile.turnID == "" || !codexSimulatesClientBehavior(profile) {
		return nil
	}
	profile.ended = result.finished
	events := make([]codexAnalyticsEvent, 0, 9)
	if codexSimulatesGuardian(profile) {
		events = append(events, codexThreadInitialized(profile, codexThreadSpec{
			threadID: codexActivityID(profile, "guardian-thread"), sessionID: profile.sessionID, parentThreadID: profile.threadID,
			source: "guardian_review", subagentSource: "guardian", model: "codex-auto-review",
		}), codexGuardianReviewEvent(profile))
	}
	if profile.firstThread {
		titleID := codexActivityID(profile, "title-thread")
		events = append(events, codexThreadInitialized(profile, codexThreadSpec{
			threadID: titleID, sessionID: titleID, source: "thread_title", model: profile.model, ephemeral: true,
		}), codexTitleTurnEvent(profile, titleID))
	}
	if profile.command && profile.dynamicTool {
		events = append(events, codexCommandEvent(profile, result.body))
	}
	if profile.dynamicTool {
		events = append(events, codexDynamicToolEvent(profile, result.body))
	}
	if codexSimulatesFileChange(profile) {
		events = append(events, codexFileChangeEvent(profile, result.body), codexAcceptedLinesEvent(profile))
	}
	for range codexSimulatedHookCount(profile) {
		events = append(events, codexHookEvent(profile, result.explicitClientInterrupt))
	}
	return append(events, codexMainTurnEvent(profile, result))
}

func codexMainTurnEvent(profile codexTelemetryProfile, result codexTelemetryTerminal) codexAnalyticsEvent {
	finished := result.finished
	if finished.IsZero() {
		finished = time.Now()
	}
	params := codexTurnEventBase(profile, codexTurnSpec{profile.threadID, profile.turnID, profile.model, profile.effort})
	response := codexTerminalResponse(result.body)
	params["approval_policy"] = firstNonEmptyString(profile.input.ApprovalPolicy, "on-request")
	params["approvals_reviewer"] = codexSimulatedReviewer(profile)
	params["completed_at"], params["duration_ms"] = finished.Unix(), elapsedMillis(profile.started, finished, finished)
	// These are a virtual client phase profile, not client-side measurements.
	// Partition the known wall time exactly once so phases cannot overlap.
	wall := elapsedMillis(profile.started, finished, finished)
	toolBlocking := codexSimulatedToolDuration(profile)
	if toolBlocking > wall {
		toolBlocking = wall
	}
	params["before_first_sampling_ms"], params["sampling_ms"] = int64(0), wall-toolBlocking
	params["tool_blocking_ms"] = toolBlocking
	params["after_last_sampling_ms"], params["between_sampling_overhead_ms"] = 0, 0
	params["status"], params["service_tier"] = result.status, profile.serviceTier
	if profile.observationEnabled {
		params["service_tier"] = firstNonEmptyString(response.Get("service_tier").String(), profile.serviceTier)
	}
	params["sandbox_policy"] = codexTelemetrySandboxPolicy(profile)
	params["explicit_client_interrupt_requested_at_ms"] = nil
	if result.explicitClientInterrupt {
		params["explicit_client_interrupt_requested_at_ms"] = finished.UnixMilli()
	}
	if profile.observationEnabled && result.httpStatus >= 400 {
		params["codex_error_http_status_code"] = result.httpStatus
	}
	if profile.observationEnabled {
		setCodexTurnUsage(params, response)
	}
	event := newCodexAnalyticsEvent(profile, "codex_turn_event", params)
	if profile.observationEnabled && (response.Get("usage").Exists() || result.httpStatus != 0) {
		event.source = "mixed"
	}
	return event
}

func codexTurnEventBase(profile codexTelemetryProfile, spec codexTurnSpec) map[string]any {
	dynamicCount, commandCount, fileCount := boolInt(profile.dynamicTool), boolInt(profile.command && profile.dynamicTool), boolInt(codexSimulatesFileChange(profile))
	if !codexSimulatesClientBehavior(profile) {
		dynamicCount, commandCount, fileCount = 0, 0, 0
	}
	requestCount := max(profile.samplingCount, 1)
	params := map[string]any{
		"app_server_client": codexAppServerClient(profile), "cache_write_input_tokens": 0,
		"cached_input_tokens": 0, "codex_error_http_status_code": nil, "codex_error_kind": nil,
		"codex_turn_source": nil, "collaboration_mode": "default", "compaction_ms": 0,
		"dynamic_tool_call_count": dynamicCount, "ephemeral": false, "file_change_count": fileCount,
		"image_generation_count": 0, "image_preparations": []any{},
		"initialization_mode": "new", "input_tokens": 0, "is_first_turn": profile.firstThread,
		"mcp_tool_call_count": 0, "model": spec.model, "model_provider": "openai", "num_input_images": 0,
		"output_tokens": 0, "parent_thread_id": codexOptionalString(profile.input.ParentThreadID), "personality": "pragmatic",
		"parent_turn_id": codexOptionalString(profile.input.ParentTurnID), "forked_from_thread_id": codexOptionalString(profile.input.ForkedFromThreadID),
		"reasoning_effort": spec.effort, "reasoning_output_tokens": 0, "reasoning_summary": "detailed",
		"root_turn_id": codexOptionalString(profile.rootTurnID), "runtime": codexRuntime(profile), "sampling_request_count": requestCount,
		"sampling_retry_count": max(profile.clientRetryCount, 0), "sandbox_network_access": false, "service_tier": profile.serviceTier,
		"session_id": profile.sessionID, "shell_command_count": commandCount, "started_at": profile.started.Unix(),
		"steer_count": 0, "subagent_source": codexOptionalString(profile.input.SubagentKind), "subagent_tool_call_count": 0, "submission_type": nil,
		"agent_name": codexOptionalString(profile.input.AgentName),
		"thread_id":  spec.threadID, "thread_source": codexOptionalString(codexTelemetryThreadSource(profile)), "tool_blocking_ms": 0,
		"total_tokens": 0, "total_tool_call_count": dynamicCount + fileCount, "turn_error": nil,
		"turn_id": spec.turnID, "turn_trigger": firstNonEmptyString(profile.input.TurnTrigger, "composer"), "web_search_count": 0, "workspace_kind": nil,
		"guardian_v2_enabled": false, "approvals_reviewer": codexSimulatedReviewer(profile),
	}
	if profile.input.GuardianV2Enabled != nil {
		params["guardian_v2_enabled"] = *profile.input.GuardianV2Enabled
	}
	if !codexSimulatesClientBehavior(profile) {
		params["turn_trigger"] = codexOptionalString(profile.input.TurnTrigger)
	}
	return params
}

func setCodexTurnUsage(params map[string]any, response gjson.Result) {
	usage := response.Get("usage")
	input, output := usage.Get("input_tokens").Int(), usage.Get("output_tokens").Int()
	total := usage.Get("total_tokens").Int()
	if total == 0 {
		total = input + output
	}
	params["input_tokens"], params["output_tokens"], params["total_tokens"] = input, output, total
	params["cached_input_tokens"] = usage.Get("input_tokens_details.cached_tokens").Int()
	params["reasoning_output_tokens"] = usage.Get("output_tokens_details.reasoning_tokens").Int()
}

func codexHookEvent(profile codexTelemetryProfile, explicitInterrupt bool) codexAnalyticsEvent {
	hookName := "Stop"
	if explicitInterrupt {
		hookName = "Interrupt"
	}
	return newCodexAnalyticsEvent(profile, "codex_hook_run", map[string]any{
		"execution_mode": "sync", "handler_type": "mcp_tool", "hook_name": hookName,
		"hook_source": "plugin", "model_slug": profile.model, "product_client_id": codexClientName(profile),
		"status": "completed", "thread_id": profile.threadID, "turn_id": profile.turnID,
	})
}

func codexGuardianTarget(profile codexTelemetryProfile) (string, map[string]any) {
	if profile.dynamicTool && profile.command {
		return codexActivityID(profile, "command"), map[string]any{"type": "unified_exec", "sandbox_permissions": "use_default", "additional_permissions": nil, "tty": false}
	}
	return codexActivityID(profile, "file-change"), map[string]any{"type": "apply_patch"}
}

func codexGuardianReviewEvent(profile codexTelemetryProfile) codexAnalyticsEvent {
	completed := codexActivityCompletedAt(profile)
	if codexSimulatesFileChange(profile) {
		completed = completed.Add(-time.Duration(codexActivityDuration(profile, "file-duration", 500, 4000)) * time.Millisecond)
	}
	if profile.dynamicTool {
		completed = completed.Add(-time.Duration(codexActivityDuration(profile, "dynamic", 200, 1800)) * time.Millisecond)
	}
	duration := codexActivityDuration(profile, "guardian", 100, 800)
	target, action := codexGuardianTarget(profile)
	return newCodexAnalyticsEvent(profile, "codex_guardian_review", map[string]any{
		"session_id": profile.sessionID, "app_server_client": codexAppServerClient(profile), "runtime": codexRuntime(profile),
		"thread_id": profile.threadID, "turn_id": profile.turnID, "review_id": codexActivityID(profile, "guardian-review"),
		"target_item_id": target, "approval_request_source": "main_turn", "reviewed_action": action, "reviewed_action_truncated": false,
		"decision": "approved", "terminal_status": "approved", "failure_reason": nil, "attempt_count": 1,
		"risk_level": nil, "user_authorization": nil, "outcome": nil, "guardian_thread_id": codexActivityID(profile, "guardian-thread"),
		"guardian_session_kind": "trunk_new", "guardian_model": "codex-auto-review", "guardian_reasoning_effort": nil,
		"guardian_default_review_model_id": "codex-auto-review", "guardian_catalog_contains_auto_review": nil,
		"guardian_review_model_overridden": false, "guardian_review_model_override": nil, "guardian_model_provider_id": "openai",
		"had_prior_review_context": false, "review_timeout_ms": 20000, "tool_call_count": 0,
		"time_to_first_token_ms": nil, "completion_latency_ms": duration,
		"started_at": completed.Add(-time.Duration(duration) * time.Millisecond).Unix(), "completed_at": completed.Unix(),
		"input_tokens": nil, "cached_input_tokens": nil, "cache_write_input_tokens": nil, "output_tokens": nil,
		"reasoning_output_tokens": nil, "total_tokens": nil,
	})
}

func codexDynamicToolEvent(profile codexTelemetryProfile, terminal []byte) codexAnalyticsEvent {
	status := "completed"
	if profile.command && simulatedInt(codexScenarioKey(profile)+":command-status", 10) == 0 {
		status = "failed"
	}
	params := codexToolEventBase(profile, codexToolSpec{terminal, codexActivityID(profile, "dynamic-tool"), codexActivityDuration(profile, "dynamic", 200, 1800), status})
	params["dynamic_tool_name"], params["tool_name"], params["success"] = "exec", "exec", status == "completed"
	for _, key := range []string{"output_audio_item_count", "output_content_item_count", "output_image_item_count", "output_text_item_count"} {
		params[key] = nil
	}
	return newCodexAnalyticsEvent(profile, "codex_dynamic_tool_call_event", params)
}

func codexCommandEvent(profile codexTelemetryProfile, terminal []byte) codexAnalyticsEvent {
	duration := codexActivityDuration(profile, "command-duration", 100, 1200)
	status, exitCode, failure := "completed", 0, any(nil)
	if simulatedInt(codexScenarioKey(profile)+":command-status", 10) == 0 {
		status, exitCode, failure = "failed", 1, "tool_error"
	}
	params := codexToolEventBase(profile, codexToolSpec{terminal, codexActivityID(profile, "command"), duration, status})
	params["cell_id"], params["command_execution_source"] = "1", "unifiedExecStartup"
	params["exit_code"], params["failure_kind"], params["tool_name"] = exitCode, failure, "unified_exec"
	params["plugin_id"], params["script_path"] = nil, nil
	setCodexCommandCounts(params, simulatedInt(codexScenarioKey(profile)+":command-kind", 4))
	return newCodexAnalyticsEvent(profile, "codex_command_execution_event", params)
}

func setCodexCommandCounts(params map[string]any, kind int) {
	params["command_total_action_count"] = 1
	for index, key := range []string{"command_read_action_count", "command_list_files_action_count", "command_search_action_count", "command_unknown_action_count"} {
		params[key] = boolInt(index == kind)
	}
}

func codexFileChangeEvent(profile codexTelemetryProfile, terminal []byte) codexAnalyticsEvent {
	total, kind := 1+simulatedInt(codexScenarioKey(profile)+":file-total", 3), simulatedInt(codexScenarioKey(profile)+":file-kind", 4)
	params := codexToolEventBase(profile, codexToolSpec{terminal, codexActivityID(profile, "file-change"), codexActivityDuration(profile, "file-duration", 500, 4000), "completed"})
	for index, key := range []string{"file_add_count", "file_update_count", "file_delete_count", "file_move_count"} {
		params[key] = total * boolInt(index == kind)
	}
	params["file_change_count"], params["tool_name"] = total, "apply_patch"
	return newCodexAnalyticsEvent(profile, "codex_file_change_event", params)
}

func codexAcceptedLinesEvent(profile codexTelemetryProfile) codexAnalyticsEvent {
	return newCodexAnalyticsEvent(profile, "codex_accepted_line_fingerprints", map[string]any{
		"accepted_added_lines": 1 + simulatedInt(codexScenarioKey(profile)+":added", 120), "accepted_deleted_lines": simulatedInt(codexScenarioKey(profile)+":deleted", 24),
		"completed_at": codexActivityCompletedAt(profile).Unix(), "event_type": "codex.accepted_line_fingerprints",
		"line_fingerprints": []any{}, "model_slug": profile.model, "product_surface": "codex",
		"repo_hash": nil, "thread_id": profile.threadID, "turn_id": profile.turnID,
	})
}

func codexToolEventBase(profile codexTelemetryProfile, spec codexToolSpec) map[string]any {
	completed := codexActivityCompletedAt(profile)
	if codexSimulatesFileChange(profile) && spec.itemID != codexActivityID(profile, "file-change") {
		completed = completed.Add(-time.Duration(codexActivityDuration(profile, "file-duration", 500, 4000)) * time.Millisecond)
	}
	if maxDuration := elapsedMillis(profile.started, completed, completed); spec.duration > maxDuration {
		spec.duration = maxDuration
	}
	guardianTarget, _ := codexGuardianTarget(profile)
	guardianCount := boolInt(codexSimulatesGuardian(profile) && spec.itemID == guardianTarget)
	approvalOutcome := "unknown"
	if guardianCount > 0 {
		approvalOutcome = "guardian_approved"
	}
	return map[string]any{
		"app_server_client": codexAppServerClient(profile), "cell_id": spec.itemID, "completed_at_ms": completed.UnixMilli(),
		"duration_ms": spec.duration, "execution_duration_ms": spec.duration, "failure_kind": nil,
		"final_approval_outcome": approvalOutcome, "guardian_review_count": guardianCount, "item_id": spec.itemID,
		"originating_response_id": codexOptionalString(codexResponseID(spec.terminal)), "parent_call_id": nil,
		"parent_thread_id": codexOptionalString(profile.input.ParentThreadID), "parent_turn_id": codexOptionalString(profile.input.ParentTurnID),
		"requested_additional_permissions": false, "requested_network_access": false,
		"review_count": guardianCount, "root_turn_id": codexOptionalString(profile.rootTurnID), "runtime": codexRuntime(profile),
		"session_id": profile.sessionID, "started_at_ms": completed.Add(-time.Duration(spec.duration) * time.Millisecond).UnixMilli(),
		"subagent_source": codexOptionalString(profile.input.SubagentKind), "subsequent_response_id": nil, "terminal_status": spec.status,
		"thread_id": profile.threadID, "thread_source": firstNonEmptyString(profile.input.ThreadSource, "user"), "turn_id": profile.turnID, "user_review_count": 0,
	}
}

func codexTerminalResponse(terminal []byte) gjson.Result {
	root := gjson.ParseBytes(terminal)
	if response := root.Get("response"); response.Exists() {
		return response
	}
	return root
}

func codexResponseID(terminal []byte) string {
	return codexTerminalResponse(terminal).Get("id").String()
}

// Simulation policy is separate from observed feature state. A simulated
// reviewer requires a simulated action and cannot contradict an observed flag.
func codexSimulatesGuardian(profile codexTelemetryProfile) bool {
	return codexSimulatesClientBehavior(profile) && (profile.dynamicTool && profile.command || codexSimulatesFileChange(profile)) &&
		codexSimulatedReviewer(profile) == "auto_review" && profile.input.ApprovalPolicy != "never" &&
		(profile.input.AutoReviewEnabled == nil || *profile.input.AutoReviewEnabled)
}

func codexSimulatesClientBehavior(profile codexTelemetryProfile) bool {
	if !profile.simulationEnabled || codexTelemetryOSFamily(profile) == "" {
		return false
	}
	for _, value := range []string{profile.input.ThreadSource, profile.input.SubagentKind, profile.input.OpenAISubagent} {
		switch strings.ToLower(strings.TrimSpace(value)) {
		case "guardian", "guardian_review", "guardian_classifier":
			return false
		}
	}
	return true
}

func codexTelemetryThreadSource(profile codexTelemetryProfile) string {
	return firstNonEmptyString(profile.input.ThreadSource, "user")
}

func codexTelemetryApprovalsReviewer(profile codexTelemetryProfile) string {
	if reviewer := profile.input.ApprovalsReviewer; reviewer == "user" || reviewer == "auto_review" {
		return reviewer
	}
	// Codex computes auto_review_enabled as an eligible approval policy AND
	// reviewer=auto_review. True proves the reviewer; false cannot identify it.
	if profile.input.AutoReviewEnabled != nil && *profile.input.AutoReviewEnabled {
		return "auto_review"
	}
	return ""
}

func codexTelemetryProfileSource(profile codexTelemetryProfile) string {
	if profile.simulationEnabled {
		if profile.observationEnabled {
			return "mixed"
		}
		return "simulated"
	}
	return "observed"
}

func codexScenarioKey(profile codexTelemetryProfile) string {
	return profile.scenarioSeed + ":" + profile.threadID + ":" + profile.turnID
}

func codexActivityID(profile codexTelemetryProfile, activity string) string {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte(codexScenarioKey(profile)+":"+activity)).String()
}

func codexActivityCompletedAt(profile codexTelemetryProfile) time.Time {
	if !profile.ended.IsZero() {
		return profile.ended
	}
	// Without a frozen completion time, activity has zero duration. Never emit
	// a future completion at initialization or depend on callback wall time.
	return profile.started
}

func codexActivityDuration(profile codexTelemetryProfile, activity string, base, span int) int64 {
	duration := int64(base + simulatedInt(codexScenarioKey(profile)+":"+activity, span))
	budget := elapsedMillis(profile.started, codexActivityCompletedAt(profile), profile.started)
	switch activity {
	case "dynamic", "file-duration", "guardian":
		// Guardian review precedes the two possible tool activities; reserve a
		// non-overlapping third of the turn for each.
		budget /= 3
	case "command-duration":
		// A shell command is nested in its dynamic exec wrapper.
		budget = codexActivityDuration(profile, "dynamic", 200, 1800)
	}
	return min(duration, budget)
}

func codexTelemetrySandboxPolicy(profile codexTelemetryProfile) string {
	for _, value := range []string{profile.input.SandboxMode, profile.input.Sandbox} {
		switch value {
		case "read_only", "read-only":
			return "read_only"
		case "workspace_write", "workspace-write":
			return "workspace_write"
		case "full_access", "danger-full-access":
			return "full_access"
		case "external_sandbox", "external-sandbox":
			return "external_sandbox"
		}
	}
	return "read_only"
}

func codexSimulatesFileChange(profile codexTelemetryProfile) bool {
	policy := codexTelemetrySandboxPolicy(profile)
	return codexSimulatesClientBehavior(profile) && profile.fileChange && (policy == "workspace_write" || policy == "full_access")
}

func codexSimulatedReviewer(profile codexTelemetryProfile) string {
	if reviewer := codexTelemetryApprovalsReviewer(profile); reviewer != "" {
		return reviewer
	}
	if profile.input.ApprovalPolicy == "never" || (profile.input.AutoReviewEnabled != nil && !*profile.input.AutoReviewEnabled) {
		return "user"
	}
	if simulatedInt(profile.scenarioSeed+":reviewer", 2) == 0 {
		return "auto_review"
	}
	return "user"
}

func codexSimulatedHookCount(profile codexTelemetryProfile) int {
	if !codexSimulatesClientBehavior(profile) {
		return 0
	}
	// A pool has a stable simulated hook configuration. Counts must agree in
	// analytics, the feature flag, and OTLP instead of hard-coding four hooks.
	return simulatedInt(profile.scenarioSeed+":registered-hooks", 3)
}

func codexSimulatedHookDuration(profile codexTelemetryProfile, index int) int64 {
	return codexActivityDuration(profile, "hook", 1, 25)
}

func codexSimulatedToolDuration(profile codexTelemetryProfile) int64 {
	var duration int64
	if profile.dynamicTool {
		duration += codexActivityDuration(profile, "dynamic", 200, 1800)
	}
	if codexSimulatesFileChange(profile) {
		duration += codexActivityDuration(profile, "file-duration", 500, 4000)
	}
	if codexSimulatesGuardian(profile) {
		duration += codexActivityDuration(profile, "guardian", 100, 800)
	}
	return duration
}

func codexTelemetryOSFamily(profile codexTelemetryProfile) string {
	if profile.input.OSFamily != "" {
		switch strings.ToLower(profile.input.OSFamily) {
		case "windows", "macos", "linux":
			return strings.ToLower(profile.input.OSFamily)
		}
		return ""
	}
	_, _, name, _, _ := codexUserAgentParts(profile.client.userAgent, profile.client.version)
	switch strings.ToLower(name) {
	case "windows":
		return "windows"
	case "mac os", "macos", "darwin":
		return "macos"
	case "linux", "ubuntu", "debian", "arch linux", "arch", "fedora", "centos", "rhel", "alpine", "opensuse", "nixos", "rocky linux", "red hat enterprise linux":
		return "linux"
	}
	return ""
}

func codexTelemetryShell(profile codexTelemetryProfile) string {
	if shell := strings.ToLower(strings.TrimSpace(profile.input.Shell)); shell != "" {
		return shell
	}
	switch codexTelemetryOSFamily(profile) {
	case "windows":
		return "powershell"
	case "macos":
		return "zsh"
	case "linux":
		return "bash"
	}
	return ""
}

func codexOptionalString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func simulatedInt(seed string, limit int) int {
	if limit <= 1 {
		return 0
	}
	sum := sha256.Sum256([]byte(seed))
	return int(binary.BigEndian.Uint64(sum[:8]) % uint64(limit))
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func elapsedMillis(start, end, fallback time.Time) int64 {
	if start.IsZero() {
		return 0
	}
	if end.IsZero() {
		end = fallback
	}
	milliseconds := end.Sub(start).Milliseconds()
	if milliseconds < 0 {
		return 0
	}
	return milliseconds
}

func codexAppServerClient(profile codexTelemetryProfile) map[string]any {
	client, version, _, _, _ := codexUserAgentParts(profile.client.userAgent, profile.client.version)
	transport := "in_process"
	if strings.EqualFold(client, "Codex Desktop") {
		transport = "stdio"
	}
	return map[string]any{"product_client_id": client, "client_name": client, "client_version": version, "rpc_transport": transport, "experimental_api_enabled": true}
}

func codexRuntime(profile codexTelemetryProfile) map[string]any {
	_, _, _, osVersion, arch := codexUserAgentParts(profile.client.userAgent, profile.client.version)
	runtimeOS := codexTelemetryOSFamily(profile)
	switch strings.ToLower(arch) {
	case "arm64":
		arch = "aarch64"
	case "amd64", "x64":
		arch = "x86_64"
	}
	return map[string]any{"codex_rs_version": profile.client.version, "runtime_os": runtimeOS, "runtime_os_version": osVersion, "runtime_arch": arch}
}

func codexClientName(profile codexTelemetryProfile) string {
	name, _, _, _, _ := codexUserAgentParts(profile.client.userAgent, profile.client.version)
	return name
}

func codexUserAgentParts(userAgent, fallbackVersion string) (client, appVersion, osName, osVersion, arch string) {
	client = strings.TrimSpace(strings.SplitN(userAgent, "/", 2)[0])
	appVersion = fallbackVersion
	open, closeIndex := strings.Index(userAgent, "("), strings.Index(userAgent, ")")
	if open >= 0 && closeIndex > open {
		platform := strings.SplitN(userAgent[open+1:closeIndex], ";", 2)
		osName, osVersion = splitCodexOS(strings.TrimSpace(platform[0]))
		if len(platform) == 2 {
			arch = strings.TrimSpace(platform[1])
		}
	}
	if lastOpen := strings.LastIndex(userAgent, "("); lastOpen > open && strings.HasSuffix(userAgent, ")") {
		app := strings.SplitN(userAgent[lastOpen+1:len(userAgent)-1], ";", 2)
		if len(app) == 2 {
			appVersion = strings.TrimSpace(app[1])
		}
	}
	return
}

func splitCodexOS(platform string) (string, string) {
	for _, name := range []string{"Red Hat Enterprise Linux", "Rocky Linux", "Arch Linux", "Mac OS", "macOS", "Windows", "Ubuntu", "Linux", "Debian", "Darwin", "Fedora", "CentOS", "Alpine", "openSUSE", "NixOS"} {
		if strings.EqualFold(platform, name) {
			return name, ""
		}
		if len(platform) > len(name) && strings.EqualFold(platform[:len(name)], name) && platform[len(name)] == ' ' {
			return name, strings.TrimSpace(platform[len(name):])
		}
	}
	parts := strings.SplitN(platform, " ", 2)
	if len(parts) == 2 {
		return parts[0], parts[1]
	}
	return platform, ""
}
