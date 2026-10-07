package service

// CodexTelemetrySignalCoverage describes collection capability, not a recorded
// event. In particular, understanding a metric schema never proves a sample
// was collected. Fixed diagnostics keep client contents and credentials out of
// the observations API.
// unavailable means the current exporter does not support that signal type;
// awaiting_source means the metric schema is known but no current entry point
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
			Signal: "skill_invocation", Type: "logs", Status: "unavailable",
			EventNames: []string{"codex.skill_invocation"},
			Reason:     "client_skill_event_not_on_responses_wire",
		},
		{
			Signal: "auth_storage", Type: "metrics", Status: "awaiting_source",
			EventNames: []string{
				"codex.auth_storage.operation", "codex.auth_storage.duration",
				"codex.auth_storage.refresh_persist", "codex.auth_storage.refresh_persist.duration",
			},
			Reason: "client_auth_storage_not_on_responses_wire",
		},
	}
}
