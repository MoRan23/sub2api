package service

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFingerprintObservation0165TypedMetadataPrecedence(t *testing.T) {
	header := http.Header{openAIWSTurnMetadataHeader: {`{"history_initialization":"new","guardian_recorded_items":true,"analytics_enabled":true}`}}
	for _, test := range []struct {
		name, nested, status, history string
		value                         *bool
	}{
		{"false", `{"history_initialization":"cold_resume","guardian_recorded_items":false,"analytics_enabled":false}`, "valid", "cold_resume", boolPointer(false)},
		{"true", `{"history_initialization":"warm_fork","guardian_recorded_items":true,"analytics_enabled":true}`, "valid", "warm_fork", boolPointer(true)},
		{"invalid blocks fallback", `{"history_initialization":"unknown","guardian_recorded_items":"false","analytics_enabled":null}`, "invalid", "", nil},
		{"malformed carrier", `invalid`, "invalid", "", nil},
		{"missing falls back", `{}`, "valid", "new", boolPointer(true)},
	} {
		t.Run(test.name, func(t *testing.T) {
			var entry FingerprintObservationEntry
			body := codexWireTestBody(t, test.nested, nil)
			populateFingerprintObservationMetadata(&entry, header, body)
			require.Equal(t, test.status, entry.MetadataStatus.HistoryInitialization)
			require.Equal(t, test.status, entry.MetadataStatus.GuardianRecordedItems)
			require.Equal(t, test.status, entry.MetadataStatus.AnalyticsEnabled)
			require.Equal(t, test.history, string(entry.HistoryInitialization))
			require.Equal(t, test.value, entry.GuardianRecordedItems)
			require.Equal(t, test.value, entry.AnalyticsEnabled)
		})
	}
	var absent FingerprintObservationEntry
	populateFingerprintObservationMetadata(&absent, nil, []byte(`{"client_metadata":{"agent_name":"guardian","history_initialization":"new","guardian_recorded_items":true,"analytics_enabled":false}}`))
	require.Empty(t, absent.HistoryInitialization)
	require.Nil(t, absent.GuardianRecordedItems)
	require.Nil(t, absent.AnalyticsEnabled)
	require.Equal(t, "missing", absent.MetadataStatus.HistoryInitialization)
	require.Equal(t, "missing", absent.MetadataStatus.GuardianRecordedItems)
	require.Equal(t, "missing", absent.MetadataStatus.AnalyticsEnabled)
}

func TestFingerprintObservation0165SafeHandshake(t *testing.T) {
	for _, valid := range []bool{false, true} {
		metadata := map[string]any{"history_initialization": "cold_fork", "guardian_recorded_items": false, "analytics_enabled": true, "mcp_attribution": "must-not-retain"}
		if !valid {
			metadata["history_initialization"], metadata["guardian_recorded_items"], metadata["analytics_enabled"] = "must-not-retain", "must-not-retain", "must-not-retain"
		}
		raw, err := json.Marshal(metadata)
		require.NoError(t, err)
		frozen := cloneFingerprintObservationHeaders(http.Header{openAIWSTurnMetadataHeader: {string(raw)}})
		var entry FingerprintObservationEntry
		populateFingerprintObservationMetadata(&entry, frozen, nil)
		want := "invalid"
		if valid {
			want = "valid"
			require.Equal(t, CodexHistoryInitializationColdFork, entry.HistoryInitialization)
			require.Equal(t, boolPointer(false), entry.GuardianRecordedItems)
			require.Equal(t, boolPointer(true), entry.AnalyticsEnabled)
		}
		require.Equal(t, want, entry.MetadataStatus.HistoryInitialization)
		require.Equal(t, want, entry.MetadataStatus.GuardianRecordedItems)
		require.Equal(t, want, entry.MetadataStatus.AnalyticsEnabled)
		require.Nil(t, entry.MCPAttribution)
		require.Nil(t, entry.PromptCacheKey)
		encoded, err := json.Marshal(frozen)
		require.NoError(t, err)
		require.NotContains(t, string(encoded), "must-not-retain")
	}
}

func TestFingerprintObservation0165MCPAttribution(t *testing.T) {
	valid := `{"status":"complete","sources":[{"server_name":"must-not-retain","tool_name":"secret-tool","first_turn_id":"secret-turn"}]}`
	exact := `{"status":"none","padding":"` + strings.Repeat("x", fingerprintMCPAttributionLimit-len(`{"status":"none","padding":""}`)) + `"}`
	for _, test := range []struct {
		name, typ, status, reported string
		value                       any
		bytes                       int
	}{
		{"valid string", "string", "valid", "complete", valid, len(valid)},
		{"attribution error", "string", "valid", "attribution_error", `{"status":"attribution_error","error_reason":"must-not-retain"}`, 0},
		{"exact byte limit", "string", "valid", "none", exact, fingerprintMCPAttributionLimit},
		{"oversized", "string", "oversized", "", exact + " ", fingerprintMCPAttributionLimit + 1},
		{"much larger", "string", "oversized", "", strings.Repeat("x", fingerprintMCPAttributionLimit*2), fingerprintMCPAttributionLimit + 1},
		{"invalid JSON string", "string", "invalid", "", "must-not-retain", len("must-not-retain")},
		{"unknown producer status", "string", "invalid", "", `{"status":"must-not-retain"}`, 0},
		{"object instead of string", "object", "invalid", "", map[string]any{"status": "complete", "secret": "must-not-retain"}, 0},
		{"array", "array", "invalid", "", []any{"must-not-retain"}, 0},
		{"number", "number", "invalid", "", 42, 0},
		{"bool", "boolean", "invalid", "", false, 0},
		{"null", "null", "invalid", "", nil, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			body, err := json.Marshal(map[string]any{"client_metadata": map[string]any{"mcp_attribution": test.value}})
			require.NoError(t, err)
			original := append([]byte(nil), body...)
			var entry FingerprintObservationEntry
			populateFingerprintObservationMetadata(&entry, nil, body)
			require.True(t, entry.MCPAttribution.Present)
			require.Equal(t, test.typ, entry.MCPAttribution.Type)
			require.Equal(t, test.status, entry.MCPAttribution.Status)
			require.Equal(t, test.reported, entry.MCPAttribution.ReportedStatus)
			if test.bytes != 0 {
				require.Equal(t, test.bytes, *entry.MCPAttribution.Bytes)
			}
			encoded, err := json.Marshal(entry)
			require.NoError(t, err)
			require.NotContains(t, string(encoded), "must-not-retain")
			require.NotContains(t, string(encoded), "secret-tool")
			require.NotContains(t, string(encoded), "secret-turn")
			require.Equal(t, original, body)
		})
	}
	for _, body := range []string{`{}`, `{"mcp_attribution":"root-only"}`, `{"client_metadata":{"x-codex-turn-metadata":"{\"mcp_attribution\":\"nested-only\"}"}}`} {
		var entry FingerprintObservationEntry
		populateFingerprintObservationMetadata(&entry, http.Header{"mcp_attribution": {valid}}, []byte(body))
		require.Equal(t, &FingerprintMCPAttribution{Type: "missing", Status: "missing"}, entry.MCPAttribution)
	}
}

func TestFingerprintObservation0165PromptCacheKey(t *testing.T) {
	for _, test := range []struct {
		name, body, strategy string
		present              bool
	}{
		{"absent", `{}`, "absent", false},
		{"override", `{"prompt_cache_key":"private-cache-key"}`, "override", true},
		{"session", `{"prompt_cache_key":"` + codexWireTestSession + `","client_metadata":{"session_id":"` + codexWireTestSession + `"}}`, "session", true},
		{"null", `{"prompt_cache_key":null}`, "invalid", true},
		{"empty", `{"prompt_cache_key":""}`, "invalid", true},
		{"bool", `{"prompt_cache_key":false}`, "invalid", true},
		{"malformed", `{`, "invalid", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			var entry FingerprintObservationEntry
			populateFingerprintObservationMetadata(&entry, nil, []byte(test.body))
			require.Equal(t, &FingerprintPromptCacheKey{Present: test.present, Strategy: test.strategy}, entry.PromptCacheKey)
			encoded, err := json.Marshal(entry)
			require.NoError(t, err)
			require.NotContains(t, string(encoded), "private-cache-key")
		})
	}
}

func TestFingerprintObservation0165SnapshotOwnership(t *testing.T) {
	size := 2
	entry := FingerprintObservationEntry{
		GuardianRecordedItems: boolPointer(false), AnalyticsEnabled: boolPointer(false),
		MCPAttribution: &FingerprintMCPAttribution{Present: true, Type: "string", Bytes: &size, Status: "invalid"},
		PromptCacheKey: &FingerprintPromptCacheKey{Strategy: "absent"},
	}
	observer := &fingerprintObserver{ring: make([]FingerprintObservationEntry, 1)}
	observer.enabled.Store(true)
	observer.record(entry)
	*entry.GuardianRecordedItems, *entry.AnalyticsEnabled = true, true
	*entry.MCPAttribution.Bytes = 99
	entry.PromptCacheKey.Strategy = "override"
	snapshot := observer.snapshot(1)
	require.Equal(t, boolPointer(false), snapshot[0].GuardianRecordedItems)
	require.Equal(t, boolPointer(false), snapshot[0].AnalyticsEnabled)
	require.Equal(t, 2, *snapshot[0].MCPAttribution.Bytes)
	require.Equal(t, "absent", snapshot[0].PromptCacheKey.Strategy)
	*snapshot[0].MCPAttribution.Bytes = 101
	snapshot[0].PromptCacheKey.Strategy = "invalid"
	require.Equal(t, 2, *observer.snapshot(1)[0].MCPAttribution.Bytes)
	require.Equal(t, "absent", observer.snapshot(1)[0].PromptCacheKey.Strategy)
}
