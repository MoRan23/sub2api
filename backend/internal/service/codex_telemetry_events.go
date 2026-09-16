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
	return codexAnalyticsEvent{EventType: eventType, EventParams: params, client: profile.client}
}

// Initialization needs actual session/thread identity, but does not invent a
// main turn ID when a client did not send one.
func codexInitializationEvents(profile codexTelemetryProfile) []codexAnalyticsEvent {
	if profile.sessionID == "" || profile.threadID == "" {
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
	if codexSimulatesGuardian(profile) {
		events = append(events, codexThreadInitialized(profile, codexThreadSpec{
			threadID: uuid.NewString(), sessionID: profile.sessionID, parentThreadID: profile.threadID,
			source: "guardian_review", subagentSource: "guardian", model: "codex-auto-review",
		}))
	}
	if profile.firstThread && codexSimulatesClientBehavior(profile) {
		titleID := uuid.NewString()
		events = append(events, codexThreadInitialized(profile, codexThreadSpec{
			threadID: titleID, sessionID: titleID, source: "thread_title", model: "gpt-5.6-luna", ephemeral: true,
		}), codexTitleTurnEvent(profile, titleID))
	}
	return events
}

func codexThreadInitialized(profile codexTelemetryProfile, spec codexThreadSpec) codexAnalyticsEvent {
	appServer := codexAppServerClient(profile)
	if spec.source == "guardian_review" {
		appServer["rpc_transport"], appServer["experimental_api_enabled"] = "in_process", nil
	}
	params := map[string]any{
		"app_server_client": appServer, "created_at": profile.started.Unix(),
		"ephemeral": spec.ephemeral, "forked_from_thread_id": spec.forkedFromID, "initialization_mode": "new",
		"model": spec.model, "parent_thread_id": spec.parentThreadID, "runtime": codexRuntime(profile),
		"session_id": spec.sessionID, "subagent_source": spec.subagentSource, "thread_id": spec.threadID,
		"thread_source": codexOptionalString(spec.source),
	}
	return newCodexAnalyticsEvent(profile, "codex_thread_initialized", params)
}

func codexTitleTurnEvent(profile codexTelemetryProfile, threadID string) codexAnalyticsEvent {
	duration := int64(800 + simulatedInt(profile.turnID+":title", 1800))
	turnID := uuid.NewString()
	title := profile
	title.sessionID, title.threadID, title.rootTurnID = threadID, threadID, turnID
	title.dynamicTool, title.command, title.fileChange = false, false, false
	title.attemptCount = 1
	reviewDisabled := false
	// Synthetic title identity must not inherit a real subagent's ancestry.
	title.input = CodexTelemetryInput{AutoReviewEnabled: &reviewDisabled, ThreadSource: "thread_title", TurnTrigger: "thread_title"}
	params := codexTurnEventBase(title, codexTurnSpec{threadID, turnID, "gpt-5.6-luna", "low"})
	params["approval_policy"], params["approvals_reviewer"] = "never", "user"
	params["before_first_sampling_ms"], params["sampling_ms"] = duration/2, duration/2
	params["completed_at"], params["duration_ms"] = profile.started.Unix()+duration/1000, duration
	params["ephemeral"], params["is_first_turn"] = true, true
	params["sandbox_policy"], params["status"] = "read_only", "completed"
	params["reasoning_summary"], params["workspace_kind"] = nil, nil
	return newCodexAnalyticsEvent(profile, "codex_turn_event", params)
}

func codexTerminalEvents(profile codexTelemetryProfile, result codexTelemetryTerminal) []codexAnalyticsEvent {
	if profile.sessionID == "" || profile.threadID == "" || profile.turnID == "" {
		return nil
	}
	if !codexSimulatesClientBehavior(profile) {
		return []codexAnalyticsEvent{codexMainTurnEvent(profile, result)}
	}
	events := make([]codexAnalyticsEvent, 0, 9)
	if profile.command && profile.dynamicTool {
		events = append(events, codexCommandEvent(profile, result.body))
	}
	if profile.dynamicTool {
		events = append(events, codexDynamicToolEvent(profile, result.body))
	}
	if profile.fileChange {
		events = append(events, codexFileChangeEvent(profile, result.body), codexAcceptedLinesEvent(profile))
	}
	for range 4 {
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
	if reviewer := codexTelemetryApprovalsReviewer(profile); reviewer != "" {
		params["approvals_reviewer"] = reviewer
	}
	params["completed_at"], params["duration_ms"] = finished.Unix(), elapsedMillis(profile.started, finished, finished)
	params["before_first_sampling_ms"], params["sampling_ms"] = nil, nil
	if !result.firstEvent.IsZero() {
		params["before_first_sampling_ms"] = elapsedMillis(profile.started, result.firstEvent, finished)
		params["sampling_ms"] = elapsedMillis(result.firstEvent, finished, finished)
	}
	if !result.firstToken.IsZero() {
		params["sampling_ms"] = elapsedMillis(result.firstToken, finished, finished)
	}
	params["after_last_sampling_ms"], params["between_sampling_overhead_ms"] = 0, 0
	params["status"], params["service_tier"] = result.status, firstNonEmptyString(response.Get("service_tier").String(), profile.serviceTier)
	params["sandbox_policy"] = firstNonEmptyString(profile.input.Sandbox, profile.input.SandboxMode, "workspace_write")
	if !codexSimulatesClientBehavior(profile) {
		params["approval_policy"] = codexOptionalString(profile.input.ApprovalPolicy)
		params["sandbox_policy"] = codexOptionalString(firstNonEmptyString(profile.input.Sandbox, profile.input.SandboxMode))
	}
	params["explicit_client_interrupt_requested_at_ms"] = nil
	if result.explicitClientInterrupt {
		params["explicit_client_interrupt_requested_at_ms"] = finished.UnixMilli()
	}
	if result.httpStatus >= 400 {
		params["codex_error_http_status_code"] = result.httpStatus
	}
	setCodexTurnUsage(params, response)
	return newCodexAnalyticsEvent(profile, "codex_turn_event", params)
}

func codexTurnEventBase(profile codexTelemetryProfile, spec codexTurnSpec) map[string]any {
	dynamicCount, commandCount, fileCount := boolInt(profile.dynamicTool), boolInt(profile.command && profile.dynamicTool), boolInt(profile.fileChange)
	if !codexSimulatesClientBehavior(profile) {
		dynamicCount, commandCount, fileCount = 0, 0, 0
	}
	requestCount := max(profile.attemptCount, 1)
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
		"sampling_retry_count": requestCount - 1, "sandbox_network_access": nil, "service_tier": profile.serviceTier,
		"session_id": profile.sessionID, "shell_command_count": commandCount, "started_at": profile.started.Unix(),
		"steer_count": 0, "subagent_source": codexOptionalString(profile.input.SubagentKind), "subagent_tool_call_count": 0, "submission_type": nil,
		"agent_name": codexOptionalString(profile.input.AgentName),
		"thread_id":  spec.threadID, "thread_source": codexOptionalString(codexTelemetryThreadSource(profile)), "tool_blocking_ms": 0,
		"total_tokens": 0, "total_tool_call_count": dynamicCount + fileCount, "turn_error": nil,
		"turn_id": spec.turnID, "turn_trigger": firstNonEmptyString(profile.input.TurnTrigger, "composer"), "web_search_count": 0, "workspace_kind": "projectless",
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

func codexDynamicToolEvent(profile codexTelemetryProfile, terminal []byte) codexAnalyticsEvent {
	status := "completed"
	if profile.command && simulatedInt(profile.turnID+":command-status", 10) == 0 {
		status = "failed"
	}
	params := codexToolEventBase(profile, codexToolSpec{terminal, uuid.NewString(), int64(200 + simulatedInt(profile.turnID+":dynamic", 1800)), status})
	params["dynamic_tool_name"], params["tool_name"], params["success"] = "exec", "exec", status == "completed"
	for _, key := range []string{"output_audio_item_count", "output_content_item_count", "output_image_item_count", "output_text_item_count"} {
		params[key] = nil
	}
	return newCodexAnalyticsEvent(profile, "codex_dynamic_tool_call_event", params)
}

func codexCommandEvent(profile codexTelemetryProfile, terminal []byte) codexAnalyticsEvent {
	duration := int64(100 + simulatedInt(profile.turnID+":command-duration", 1200))
	status, exitCode, failure := "completed", 0, any(nil)
	if simulatedInt(profile.turnID+":command-status", 10) == 0 {
		status, exitCode, failure = "failed", 1, "tool_error"
	}
	params := codexToolEventBase(profile, codexToolSpec{terminal, uuid.NewString(), duration, status})
	params["cell_id"], params["command_execution_source"] = "1", "unifiedExecStartup"
	params["exit_code"], params["failure_kind"], params["tool_name"] = exitCode, failure, "unified_exec"
	params["plugin_id"], params["script_path"] = nil, nil
	setCodexCommandCounts(params, simulatedInt(profile.turnID+":command-kind", 4))
	return newCodexAnalyticsEvent(profile, "codex_command_execution_event", params)
}

func setCodexCommandCounts(params map[string]any, kind int) {
	params["command_total_action_count"] = 1
	for index, key := range []string{"command_read_action_count", "command_list_files_action_count", "command_search_action_count", "command_unknown_action_count"} {
		params[key] = boolInt(index == kind)
	}
}

func codexFileChangeEvent(profile codexTelemetryProfile, terminal []byte) codexAnalyticsEvent {
	total, kind := 1+simulatedInt(profile.turnID+":file-total", 3), simulatedInt(profile.turnID+":file-kind", 4)
	params := codexToolEventBase(profile, codexToolSpec{terminal, uuid.NewString(), int64(500 + simulatedInt(profile.turnID+":file-duration", 4000)), "completed"})
	for index, key := range []string{"file_add_count", "file_update_count", "file_delete_count", "file_move_count"} {
		params[key] = total * boolInt(index == kind)
	}
	params["file_change_count"], params["tool_name"] = total, "apply_patch"
	return newCodexAnalyticsEvent(profile, "codex_file_change_event", params)
}

func codexAcceptedLinesEvent(profile codexTelemetryProfile) codexAnalyticsEvent {
	return newCodexAnalyticsEvent(profile, "codex_accepted_line_fingerprints", map[string]any{
		"accepted_added_lines": 1 + simulatedInt(profile.turnID+":added", 120), "accepted_deleted_lines": simulatedInt(profile.turnID+":deleted", 24),
		"completed_at": time.Now().Unix(), "event_type": "codex.accepted_line_fingerprints",
		"line_fingerprints": []any{}, "model_slug": profile.model, "product_surface": "codex",
		"repo_hash": nil, "thread_id": profile.threadID, "turn_id": profile.turnID,
	})
}

func codexToolEventBase(profile codexTelemetryProfile, spec codexToolSpec) map[string]any {
	completed := time.Now()
	return map[string]any{
		"app_server_client": codexAppServerClient(profile), "cell_id": spec.itemID, "completed_at_ms": completed.UnixMilli(),
		"duration_ms": spec.duration, "execution_duration_ms": spec.duration, "failure_kind": nil,
		"final_approval_outcome": "unknown", "guardian_review_count": 0, "item_id": spec.itemID,
		"originating_response_id": codexOptionalString(codexResponseID(spec.terminal)), "parent_call_id": nil,
		"parent_thread_id": codexOptionalString(profile.input.ParentThreadID), "parent_turn_id": codexOptionalString(profile.input.ParentTurnID),
		"requested_additional_permissions": false, "requested_network_access": false,
		"review_count": 0, "root_turn_id": codexOptionalString(profile.rootTurnID), "runtime": codexRuntime(profile),
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

// Simulation policy is separate from observed feature state. An unspecified
// auto-review flag may allow a synthetic reviewer, but cannot prove V2 is active.
func codexSimulatesGuardian(profile codexTelemetryProfile) bool {
	return codexSimulatesClientBehavior(profile) &&
		profile.input.ApprovalsReviewer != "user" &&
		(profile.input.AutoReviewEnabled == nil || *profile.input.AutoReviewEnabled)
}

func codexSimulatesClientBehavior(profile codexTelemetryProfile) bool {
	for _, value := range []string{profile.input.ThreadSource, profile.input.SubagentKind, profile.input.OpenAISubagent} {
		switch strings.ToLower(strings.TrimSpace(value)) {
		case "guardian", "guardian_review", "guardian_classifier":
			return false
		}
	}
	return true
}

func codexTelemetryThreadSource(profile codexTelemetryProfile) string {
	if !codexSimulatesClientBehavior(profile) {
		return profile.input.ThreadSource
	}
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
	_, _, osName, osVersion, arch := codexUserAgentParts(profile.client.userAgent, profile.client.version)
	runtimeOS := strings.ToLower(osName)
	switch runtimeOS {
	case "mac os":
		runtimeOS = "macos"
	case "ubuntu", "debian", "arch linux":
		runtimeOS = "linux"
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
	for _, name := range []string{"Mac OS", "Windows", "Ubuntu", "Linux", "Debian", "Arch Linux"} {
		if strings.HasPrefix(platform, name+" ") {
			return name, strings.TrimSpace(strings.TrimPrefix(platform, name))
		}
	}
	parts := strings.SplitN(platform, " ", 2)
	if len(parts) == 2 {
		return parts[0], parts[1]
	}
	return platform, "Unknown"
}
