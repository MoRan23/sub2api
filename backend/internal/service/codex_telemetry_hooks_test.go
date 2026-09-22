package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCodexTelemetryLifecycleHooksRespectSubagentSource(t *testing.T) {
	for _, test := range []struct {
		name, source, kind, header, hook string
		interrupt                        bool
	}{
		{name: "root", source: "user", hook: "Stop"},
		{name: "root-interrupt", source: "user", interrupt: true, hook: "Interrupt"},
		{name: "unknown-root", hook: "Stop"},
		{name: "feature", source: "automation", hook: "Stop"},
		{name: "spawn-kind", source: "subagent", kind: "thread_spawn", hook: "SubagentStop"},
		{name: "spawn-header", header: "collab_spawn", hook: "SubagentStop"},
		{name: "spawn-header-fallback", source: "subagent", kind: "collab_spawn", header: "collab_spawn", hook: "SubagentStop"},
		{name: "spawn-both", source: "subagent", kind: "thread_spawn", header: "collab_spawn", hook: "SubagentStop"},
		{name: "normalized-spawn", source: " SubAgent ", kind: " Thread_Spawn ", header: " Collab_Spawn ", hook: "SubagentStop"},
		{name: "spawn-interrupt", source: "subagent", kind: "thread_spawn", interrupt: true},
		{name: "review", source: "subagent", kind: "review"},
		{name: "compact", header: "compact"},
		{name: "unknown-kind", kind: "custom-subagent"},
		{name: "unknown-header", header: "custom-subagent"},
		{name: "missing-kind", source: "subagent"},
		{name: "missing-kind-interrupt", source: "subagent", interrupt: true},
		{name: "memory-source", source: "memory_consolidation"},
		{name: "memory-kind", kind: "memory_consolidation"},
		{name: "memory-header", header: "memory_consolidation", interrupt: true},
		{name: "conflicting-kind-header", source: "subagent", kind: "thread_spawn", header: "review"},
		{name: "conflicting-header-kind", source: "subagent", kind: "review", header: "collab_spawn"},
		{name: "conflicting-root-source", source: "user", kind: "thread_spawn"},
		{name: "guardian-source", source: "guardian_review"},
		{name: "guardian-header", source: "subagent", kind: "thread_spawn", header: "guardian"},
	} {
		t.Run(test.name, func(t *testing.T) {
			profile := codexTelemetryEventTestProfile()
			profile.firstThread, profile.dynamicTool, profile.command, profile.fileChange = false, false, false, false
			configuredCount := codexSimulatedHookCount(profile)
			profile.input.ThreadSource, profile.input.SubagentKind, profile.input.OpenAISubagent = test.source, test.kind, test.header
			require.Positive(t, configuredCount, "fixture must exercise configured hooks")
			events := codexTerminalEvents(profile, codexTelemetryTerminal{
				status: "completed", finished: profile.started.Add(time.Second), explicitClientInterrupt: test.interrupt,
			})
			var hookNames []string
			for _, event := range events {
				if event.EventType == "codex_hook_run" {
					hookNames = append(hookNames, event.EventParams["hook_name"].(string))
				}
			}
			if test.hook == "" {
				require.Empty(t, hookNames)
			} else {
				require.Len(t, hookNames, configuredCount)
				for _, name := range hookNames {
					require.Equal(t, test.hook, name)
				}
			}
		})
	}
}

func TestCodexTelemetryLifecycleHooksRequireSimulation(t *testing.T) {
	for _, test := range []struct {
		name       string
		simulation bool
		os         string
	}{
		{name: "disabled", os: "linux"},
		{name: "unknown-os", simulation: true, os: "unknown"},
	} {
		t.Run(test.name, func(t *testing.T) {
			profile := codexTelemetryEventTestProfile()
			profile.input.ThreadSource, profile.input.SubagentKind = "user", ""
			profile.simulationEnabled, profile.input.OSFamily = test.simulation, test.os
			name, count := codexSimulatedLifecycleHook(profile, false)
			require.Empty(t, name)
			require.Zero(t, count)
		})
	}
}

func TestCodexTelemetryNetworkPermissionAndTitle(t *testing.T) {
	for _, test := range []struct {
		name, mode, sandbox, policy string
		network                     bool
	}{
		{name: "full-access", mode: "danger-full-access", policy: "full_access", network: true},
		{name: "normalized-full-access", mode: "full_access", policy: "full_access", network: true},
		{name: "sandbox-policy-fallback", sandbox: "full_access", policy: "full_access", network: true},
		{name: "read-only", mode: "read-only", policy: "read_only"},
		{name: "workspace-write", mode: "workspace_write", policy: "workspace_write"},
		{name: "external-sandbox", mode: "external-sandbox", policy: "external_sandbox"},
		{name: "unknown", policy: "read_only"},
		{name: "unknown-mode", mode: "custom", policy: "read_only"},
		{name: "no-backend-is-not-full-access", sandbox: "none", policy: "read_only"},
		{name: "mode-precedes-backend", mode: "read_only", sandbox: "full_access", policy: "read_only"},
	} {
		t.Run(test.name, func(t *testing.T) {
			profile := codexTelemetryEventTestProfile()
			profile.input.SandboxMode, profile.input.Sandbox = test.mode, test.sandbox
			turn := codexMainTurnEvent(profile, codexTelemetryTerminal{status: "completed", finished: profile.started.Add(time.Second)})
			require.Equal(t, test.policy, turn.EventParams["sandbox_policy"])
			require.Equal(t, test.network, turn.EventParams["sandbox_network_access"])
			title := codexTitleTurnEvent(profile, "title-thread")
			require.Equal(t, "read_only", title.EventParams["sandbox_policy"])
			require.Equal(t, false, title.EventParams["sandbox_network_access"])
		})
	}
}
