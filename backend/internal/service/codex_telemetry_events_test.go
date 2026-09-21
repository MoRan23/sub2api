package service

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func codexTelemetryEventTestProfile() codexTelemetryProfile {
	return codexTelemetryProfile{
		client: codexTelemetryClient{
			localID: 42, accessToken: "test-secret-do-not-serialize", accountID: "upstream-account",
			userAgent:  "codex-tui/0.154.0 (Ubuntu 24.04.4; x86_64) xterm-256color (codex-tui; 0.154.0)",
			originator: "codex-tui", version: "0.154.0",
		},
		sessionID: "actual-session", threadID: "actual-thread", turnID: "actual-turn", rootTurnID: "actual-root-turn",
		model: "gpt-6-astra", effort: "high", serviceTier: "default", started: time.Unix(1700000000, 0),
		firstThread: true, dynamicTool: true, command: true, fileChange: true,
		simulationEnabled: true, observationEnabled: true, scenarioSeed: "test-pool-seed", samplingCount: 1,
		input: CodexTelemetryInput{
			ParentThreadID: "actual-parent-thread", ParentTurnID: "actual-parent-turn", RootTurnID: "actual-root-turn",
			ForkedFromThreadID: "actual-fork-source", ThreadSource: "subagent", SubagentKind: "review",
			TurnTrigger: "collaboration", AgentName: "review-agent", SandboxMode: "workspace_write", ApprovalPolicy: "on-request", ApprovalsReviewer: "auto_review",
		},
	}
}

func TestCodexTelemetryAnalyticsEventContract(t *testing.T) {
	profile := codexTelemetryEventTestProfile()
	initial := codexInitializationEvents(profile)
	require.Len(t, initial, 1, "title completions are deferred until the simulated turn is sealed")
	root := initial[0].EventParams
	require.Equal(t, "codex_thread_initialized", initial[0].EventType)
	require.Equal(t, "actual-session", root["session_id"])
	require.Equal(t, "actual-thread", root["thread_id"])
	require.Equal(t, "actual-parent-thread", root["parent_thread_id"])
	require.Equal(t, "actual-fork-source", root["forked_from_thread_id"])
	require.Equal(t, "subagent", root["thread_source"])
	require.Equal(t, "review", root["subagent_source"])
	finished := codexTerminalEvents(profile, codexTelemetryTerminal{status: "completed", finished: profile.started.Add(10 * time.Second)})
	guardian := finished[0].EventParams
	require.Equal(t, "guardian_review", guardian["thread_source"])
	require.Equal(t, "actual-thread", guardian["parent_thread_id"])
	guardianID, ok := guardian["thread_id"].(string)
	require.True(t, ok)
	require.NoError(t, uuid.Validate(guardianID))
	require.NotEqual(t, profile.threadID, guardian["thread_id"])
	guardianClient, ok := guardian["app_server_client"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "in_process", guardianClient["rpc_transport"])
	title := finished[2].EventParams
	require.Equal(t, "thread_title", title["thread_source"])
	require.Equal(t, title["thread_id"], title["session_id"])
	require.Nil(t, title["parent_thread_id"])
	titleID, ok := title["thread_id"].(string)
	require.True(t, ok)
	require.NoError(t, uuid.Validate(titleID))
	require.Equal(t, true, finished[3].EventParams["ephemeral"])
	require.Nil(t, finished[3].EventParams["parent_turn_id"])
	require.Nil(t, finished[3].EventParams["parent_thread_id"])
	require.Equal(t, profile.model, title["model"], "do not force a Luna title model")

	result := codexTelemetryTerminal{
		status: "completed", finished: profile.started.Add(2 * time.Second),
		firstEvent: profile.started.Add(100 * time.Millisecond), firstToken: profile.started.Add(300 * time.Millisecond),
		body: []byte(`{"response":{"id":"resp_actual","service_tier":"priority","usage":{"input_tokens":70,"output_tokens":30,"input_tokens_details":{"cached_tokens":20},"output_tokens_details":{"reasoning_tokens":5}}}}`),
	}
	terminal := codexTerminalEvents(profile, result)
	require.Len(t, terminal, 9+codexSimulatedHookCount(profile))
	counts := map[string]int{"codex_hook_run": 0}
	for _, event := range append(initial, terminal...) {
		counts[event.EventType]++
		encoded, err := json.Marshal(event)
		require.NoError(t, err)
		require.NotContains(t, string(encoded), "test-secret-do-not-serialize")
	}
	require.Equal(t, map[string]int{
		"codex_thread_initialized": 3, "codex_turn_event": 2, "codex_command_execution_event": 1,
		"codex_dynamic_tool_call_event": 1, "codex_file_change_event": 1, "codex_accepted_line_fingerprints": 1, "codex_hook_run": codexSimulatedHookCount(profile),
		"codex_guardian_review": 1,
	}, counts)
	turn := terminal[len(terminal)-1].EventParams
	for key, expected := range map[string]any{
		"session_id": "actual-session", "thread_id": "actual-thread", "turn_id": "actual-turn", "root_turn_id": "actual-root-turn",
		"parent_thread_id": "actual-parent-thread", "parent_turn_id": "actual-parent-turn", "forked_from_thread_id": "actual-fork-source",
		"sandbox_policy": "workspace_write", "approval_policy": "on-request", "thread_source": "subagent", "turn_trigger": "collaboration",
		"agent_name": "review-agent", "subagent_source": "review", "service_tier": "priority", "status": "completed",
		"input_tokens": int64(70), "output_tokens": int64(30), "total_tokens": int64(100), "cached_input_tokens": int64(20), "reasoning_output_tokens": int64(5),
		"duration_ms": int64(2000), "before_first_sampling_ms": int64(0),
	} {
		require.Equal(t, expected, turn[key], key)
	}
	require.Nil(t, turn["explicit_client_interrupt_requested_at_ms"])
	require.Equal(t, int64(2000), turn["sampling_ms"].(int64)+turn["tool_blocking_ms"].(int64), "simulated phases partition the turn once")
	for _, event := range terminal {
		switch event.EventType {
		case "codex_command_execution_event", "codex_dynamic_tool_call_event", "codex_file_change_event":
			require.Equal(t, "resp_actual", event.EventParams["originating_response_id"])
			require.Equal(t, "actual-parent-thread", event.EventParams["parent_thread_id"])
			require.Equal(t, "actual-parent-turn", event.EventParams["parent_turn_id"])
		case "codex_accepted_line_fingerprints":
			require.Nil(t, event.EventParams["repo_hash"])
			require.Empty(t, event.EventParams["line_fingerprints"])
		case "codex_hook_run":
			require.Equal(t, "Stop", event.EventParams["hook_name"])
		}
	}
}

func TestCodexTelemetryAnalyticsReviewDisabledAndMissingIdentity(t *testing.T) {
	profile := codexTelemetryEventTestProfile()
	disabled := false
	profile.input.AutoReviewEnabled = &disabled
	initial := codexInitializationEvents(profile)
	require.Len(t, initial, 1)
	for _, event := range initial {
		require.NotEqual(t, "guardian_review", event.EventParams["thread_source"])
	}
	turn := codexMainTurnEvent(profile, codexTelemetryTerminal{status: "completed", finished: profile.started.Add(time.Second)}).EventParams
	require.Equal(t, false, turn["guardian_v2_enabled"], "unknown required state is a simulated default, not a measured flag")
	require.Equal(t, "auto_review", turn["approvals_reviewer"], "explicit reviewer survives disabled auto-review")
	profile.firstThread = false
	require.Empty(t, codexInitializationEvents(profile))
	profile.firstThread = true
	profile.turnID = ""
	require.Len(t, codexInitializationEvents(profile), 1, "thread initialization does not require a main turn ID")
	require.Empty(t, codexTerminalEvents(profile, codexTelemetryTerminal{status: "completed"}), "do not fabricate the missing main turn identity")
	profile.sessionID = ""
	require.Empty(t, codexInitializationEvents(profile))
	profile = codexTelemetryEventTestProfile()
	profile.threadID = ""
	require.Empty(t, codexInitializationEvents(profile))
	require.Empty(t, codexTerminalEvents(profile, codexTelemetryTerminal{status: "completed"}))
}

func TestCodexTelemetryAnalyticsAttemptCountsDoNotLeakIntoSyntheticTitle(t *testing.T) {
	for _, attempts := range []int{0, 1, 3} {
		profile := codexTelemetryEventTestProfile()
		profile.attemptCount = attempts
		profile.samplingCount = 2
		profile.clientRetryCount = 1
		turn := codexMainTurnEvent(profile, codexTelemetryTerminal{status: "completed", finished: profile.started.Add(time.Second)}).EventParams
		require.Equal(t, 2, turn["sampling_request_count"])
		require.Equal(t, 1, turn["sampling_retry_count"], "physical requests do not prove client retries")
		title := codexTitleTurnEvent(profile, "synthetic-title-thread").EventParams
		require.Equal(t, 1, title["sampling_request_count"])
		require.Equal(t, 0, title["sampling_retry_count"])
	}
}

func TestCodexTelemetryAnalyticsFailureIsNotAnExplicitInterrupt(t *testing.T) {
	profile := codexTelemetryEventTestProfile()
	profile.dynamicTool, profile.command, profile.fileChange = false, false, false
	profile.firstThread = false
	for _, status := range []string{"failed", "interrupted", "incomplete", "completed"} {
		t.Run(status, func(t *testing.T) {
			events := codexTerminalEvents(profile, codexTelemetryTerminal{status: status, httpStatus: 503, finished: profile.started.Add(time.Second)})
			require.Len(t, events, codexSimulatedHookCount(profile)+1)
			for _, event := range events[:len(events)-1] {
				require.Equal(t, "Stop", event.EventParams["hook_name"])
			}
			turn := events[len(events)-1].EventParams
			require.Equal(t, status, turn["status"])
			require.Equal(t, 503, turn["codex_error_http_status_code"])
			require.Nil(t, turn["explicit_client_interrupt_requested_at_ms"])
			require.Equal(t, int64(0), turn["before_first_sampling_ms"])
			require.Equal(t, int64(1000), turn["sampling_ms"])
		})
	}
	events := codexTerminalEvents(profile, codexTelemetryTerminal{status: "interrupted", explicitClientInterrupt: true, finished: profile.started.Add(time.Second)})
	for _, event := range events[:len(events)-1] {
		require.Equal(t, "Interrupt", event.EventParams["hook_name"])
	}
	require.Equal(t, profile.started.Add(time.Second).UnixMilli(), events[len(events)-1].EventParams["explicit_client_interrupt_requested_at_ms"])
	require.Empty(t, codexResponseID(nil), "do not fabricate response IDs")
	tool := codexDynamicToolEvent(profile, nil)
	require.Nil(t, tool.EventParams["originating_response_id"])
}

func TestCodexTelemetryAnalyticsDesktopAndCLIIdentity(t *testing.T) {
	for _, test := range []struct {
		name, userAgent, version, product, appVersion, transport, os, osVersion, arch string
	}{
		{"desktop", "Codex Desktop/0.154.0 (Mac OS 26.6.2; arm64) (Codex Desktop; 2026.909.1)", "0.154.0", "Codex Desktop", "2026.909.1", "stdio", "macos", "26.6.2", "aarch64"},
		{"cli", "codex-tui/0.154.0 (Ubuntu 24.04.4; x86_64) xterm-256color (codex-tui; 0.154.0)", "0.154.0", "codex-tui", "0.154.0", "in_process", "linux", "24.04.4", "x86_64"},
		{"windows", "codex-tui/0.153.0 (Windows 10.0.26200; x86_64)", "0.153.0", "codex-tui", "0.153.0", "in_process", "windows", "10.0.26200", "x86_64"},
	} {
		t.Run(test.name, func(t *testing.T) {
			profile := codexTelemetryEventTestProfile()
			profile.client.userAgent, profile.client.version = test.userAgent, test.version
			app := codexAppServerClient(profile)
			require.Equal(t, test.product, app["product_client_id"])
			require.Equal(t, test.product, codexClientName(profile))
			require.Equal(t, test.appVersion, app["client_version"])
			require.Equal(t, test.transport, app["rpc_transport"])
			require.Equal(t, map[string]any{"codex_rs_version": test.version, "runtime_os": test.os, "runtime_os_version": test.osVersion, "runtime_arch": test.arch}, codexRuntime(profile))
		})
	}
}

func TestCodexTelemetrySimulationPermissionAndEvidenceBoundaries(t *testing.T) {
	for _, sandbox := range []string{"", "read_only", "seccomp", "seatbelt", "windows_restricted", "external_sandbox"} {
		profile := codexTelemetryEventTestProfile()
		profile.input.SandboxMode, profile.input.Sandbox = "", sandbox
		result := codexTelemetryTerminal{status: "completed", finished: profile.started.Add(time.Second)}
		require.False(t, codexSimulatesFileChange(profile), sandbox)
		for _, event := range codexTerminalEvents(profile, result) {
			require.NotEqual(t, "codex_file_change_event", event.EventType, sandbox)
			require.NotEqual(t, "codex_accepted_line_fingerprints", event.EventType, sandbox)
		}
	}
	profile := codexTelemetryEventTestProfile()
	profile.input.SandboxMode, profile.input.Sandbox = "workspace_write", "seccomp"
	require.Equal(t, "workspace_write", codexTelemetrySandboxPolicy(profile))
	require.True(t, codexSimulatesFileChange(profile))
	profile.simulationEnabled = false
	require.Empty(t, codexInitializationEvents(profile))
	require.Empty(t, codexTerminalEvents(profile, codexTelemetryTerminal{status: "completed"}), "a completed response cannot prove client turn completion")
	profile.simulationEnabled = true
	profile.input.OSFamily = "unknown"
	require.Empty(t, codexInitializationEvents(profile), "do not guess an OS-specific simulation")
}

func TestCodexTelemetryActivityIdentityAndTimeAreStable(t *testing.T) {
	profile := codexTelemetryEventTestProfile()
	result := codexTelemetryTerminal{status: "completed", finished: profile.started.Add(5 * time.Second)}
	first, second := codexTerminalEvents(profile, result), codexTerminalEvents(profile, result)
	require.Equal(t, first, second, "retries and restarts must not regenerate activities")
	for _, event := range first {
		if completed, ok := event.EventParams["completed_at_ms"].(int64); ok {
			require.LessOrEqual(t, completed, result.finished.UnixMilli())
			require.GreaterOrEqual(t, event.EventParams["started_at_ms"].(int64), profile.started.UnixMilli())
		}
	}
	for _, test := range []struct{ ua, os, version string }{
		{"codex-tui/0.155.1 (Arch Linux; x86_64)", "linux", ""},
		{"codex-tui/0.155.1 (Darwin 24.0; arm64)", "macos", "24.0"},
	} {
		profile.client.userAgent = test.ua
		runtime := codexRuntime(profile)
		require.Equal(t, test.os, runtime["runtime_os"])
		require.Equal(t, test.version, runtime["runtime_os_version"])
	}
}
