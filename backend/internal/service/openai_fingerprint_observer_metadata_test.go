package service

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func fingerprintMetadataTestBody(t *testing.T, metadata map[string]any) []byte {
	t.Helper()
	nested, err := json.Marshal(metadata)
	require.NoError(t, err)
	body, err := json.Marshal(map[string]any{"client_metadata": map[string]any{openAIWSTurnMetadataHeader: string(nested)}})
	require.NoError(t, err)
	return body
}

func fingerprintMetadataTestFunction(name string) map[string]any {
	return map[string]any{
		"name": name, "direct": false, "deferred": true, "code_mode_name": "tools." + name,
		"source":    map[string]any{"kind": "mcp", "server_name": "local-search", "credentials": "must-not-retain"},
		"arguments": "must-not-retain", "description": "must-not-retain", "output": "must-not-retain",
	}
}

func fingerprintMetadataTestInventory() map[string]any {
	return map[string]any{
		"tools": map[string]any{
			"name": "Tools", "functions": map[string]any{"search": fingerprintMetadataTestFunction("search")},
			"arbitrary": "must-not-retain",
		},
	}
}

func TestFingerprintObservationMetadataFinalWire(t *testing.T) {
	metadata := map[string]any{
		"request_kind": "compaction", "history_ingest_requested": false,
		"compaction":           DefaultCodexCompactionTurnMetadata(CodexCompactionImplementationRemoteV2),
		"tool_namespaces_info": fingerprintMetadataTestInventory(),
	}
	body := fingerprintMetadataTestBody(t, metadata)
	original := append([]byte{}, body...)
	entry := buildFingerprintObservationEntry(nil, newOpenAIOAuthPinAccount(1, nil), installationIDResolution{},
		http.Header{openAIWSTurnMetadataHeader: []string{`{"request_kind":"turn","history_ingest_requested":true}`}},
		body, OpenAICodexTurnIdentity{}, false, true)
	require.Equal(t, CodexWireRequestCompaction, entry.RequestKind)
	require.NotNil(t, entry.HistoryIngestRequested)
	require.False(t, *entry.HistoryIngestRequested)
	require.Equal(t, CodexCompactionImplementationRemoteV2, entry.Compaction.Implementation)
	require.Equal(t, &FingerprintMetadataStatus{"valid", "valid", "valid", "valid"}, entry.MetadataStatus)
	require.Len(t, entry.ToolNamespacesInfo, 1)
	function := entry.ToolNamespacesInfo[0].Functions[0]
	require.Equal(t, "tools", entry.ToolNamespacesInfo[0].Namespace)
	require.Equal(t, "search", function.Function)
	require.False(t, function.Direct)
	require.True(t, function.Deferred)
	require.Equal(t, "tools.search", *function.CodeModeName)
	require.Equal(t, FingerprintToolSource{Kind: "mcp", ServerName: "local-search"}, function.Source)
	encoded, err := json.Marshal(entry)
	require.NoError(t, err)
	require.Contains(t, string(encoded), `"history_ingest_requested":false`)
	require.NotContains(t, string(encoded), "must-not-retain")
	require.Equal(t, original, body, "observation must not rewrite the outbound body")
}

func TestFingerprintObservationMetadataCarrierPrecedence(t *testing.T) {
	tests := []struct {
		name, body, header, want, status string
	}{
		{"body only", `{"client_metadata":{"x-codex-turn-metadata":"{\"request_kind\":\"turn\"}"}}`, "", "turn", "valid"},
		{"compatibility body", `{"x-codex-turn-metadata":{"request_kind":"memory"}}`, `{"request_kind":"turn"}`, "memory", "valid"},
		{"header only", `{}`, `{"request_kind":"prewarm"}`, "prewarm", "valid"},
		{"header fills absent field", `{"client_metadata":{"x-codex-turn-metadata":"{}"}}`, `{"request_kind":"turn"}`, "turn", "valid"},
		{"invalid body field blocks header", `{"client_metadata":{"x-codex-turn-metadata":"{\"request_kind\":false}"}}`, `{"request_kind":"turn"}`, "", "invalid"},
		{"invalid body carrier blocks header", `{"client_metadata":{"x-codex-turn-metadata":"invalid"}}`, `{"request_kind":"turn"}`, "", "invalid"},
		{"canonical carrier requires string", `{"client_metadata":{"x-codex-turn-metadata":{"request_kind":"turn"}}}`, `{"request_kind":"turn"}`, "", "invalid"},
		{"invalid header", `{}`, "invalid", "", "invalid"},
		{"no default request kind", `{}`, "", "", "missing"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			headers := http.Header{}
			if tt.header != "" {
				headers.Set(openAIWSTurnMetadataHeader, tt.header)
			}
			var entry FingerprintObservationEntry
			populateFingerprintObservationMetadata(&entry, headers, []byte(tt.body))
			require.Equal(t, tt.want, string(entry.RequestKind))
			require.Equal(t, tt.status, entry.MetadataStatus.RequestKind)
		})
	}
}

func TestFingerprintObservationMetadataDoesNotUseInboundOrPlan(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/responses", strings.NewReader(string(fingerprintMetadataTestBody(t, map[string]any{"request_kind": "memory"}))))
	c.Request.Header.Set(openAIWSTurnMetadataHeader, `{"request_kind":"memory","history_ingest_requested":true}`)
	SetOpenAIOAuthIdentityPlan(c, OpenAIOAuthIdentityPlan{WireProfile: CodexWireProfile{
		RequestKind: CodexWireRequestMemory, HistoryIngestRequested: boolPointer(true),
		Compaction: marshalCodexCompactionMetadata(DefaultCodexCompactionTurnMetadata(CodexCompactionImplementationResponses)),
	}})
	entry := buildFingerprintObservationEntry(c, newOpenAIOAuthPinAccount(1, nil), installationIDResolution{}, nil,
		[]byte(`{"type":"response.create"}`), OpenAICodexTurnIdentity{}, false, false)
	require.Empty(t, entry.RequestKind)
	require.Nil(t, entry.HistoryIngestRequested)
	require.Nil(t, entry.Compaction)
	require.Nil(t, entry.ToolNamespacesInfo)
	require.Equal(t, &FingerprintMetadataStatus{"missing", "missing", "missing", "missing"}, entry.MetadataStatus)
}

func TestFingerprintObservationMetadataWSHandshakeSnapshot(t *testing.T) {
	metadata := map[string]any{
		"request_kind": "compaction", "history_ingest_requested": false,
		"compaction": DefaultCodexCompactionTurnMetadata(CodexCompactionImplementationRemoteV2),
		"unrelated":  "must-not-retain",
	}
	encoded, err := json.Marshal(metadata)
	require.NoError(t, err)
	headers := http.Header{
		"Authorization":            []string{"must-not-retain"},
		openAIWSTurnMetadataHeader: []string{string(encoded)},
	}
	frozen := cloneFingerprintObservationHeaders(headers)
	var entry FingerprintObservationEntry
	populateFingerprintObservationMetadata(&entry, frozen, []byte(`{"type":"response.create"}`))
	require.Equal(t, CodexWireRequestCompaction, entry.RequestKind)
	require.NotNil(t, entry.HistoryIngestRequested)
	require.False(t, *entry.HistoryIngestRequested)
	require.Equal(t, CodexCompactionImplementationRemoteV2, entry.Compaction.Implementation)
	encoded, err = json.Marshal(frozen)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "must-not-retain")
	header := headers[openAIWSTurnMetadataHeader][0]
	require.Contains(t, header, "must-not-retain", "snapshot must not mutate physical headers")

	for _, field := range []string{"request_kind", "history_ingest_requested", "compaction"} {
		metadata[field] = map[string]any{"invalid": "must-not-retain"}
	}
	encoded, _ = json.Marshal(metadata)
	frozen = cloneFingerprintObservationHeaders(http.Header{openAIWSTurnMetadataHeader: []string{string(encoded)}})
	entry = FingerprintObservationEntry{}
	populateFingerprintObservationMetadata(&entry, frozen, nil)
	require.Equal(t, &FingerprintMetadataStatus{"invalid", "invalid", "invalid", "missing"}, entry.MetadataStatus)
	encoded, _ = json.Marshal(frozen)
	require.NotContains(t, string(encoded), "must-not-retain")
}

func TestFingerprintObservationMetadataWSRecorderFreezesProtocolAndDropsCredentials(t *testing.T) {
	SetFingerprintObservationEnabled(false)
	t.Cleanup(func() { SetFingerprintObservationEnabled(false) })
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/responses", nil)
	account := newOpenAIOAuthPinAccount(19008, nil)
	account.Credentials = map[string]any{"access_token": "must-not-retain", "refresh_token": "must-not-retain"}
	setFingerprintObservationOutboundIdentity(c, OpenAICodexTurnIdentity{
		SessionID: fingerprintObserverSessionV7, ThreadID: fingerprintObserverSessionV7,
	})
	recorder := freezeFingerprintObservationWSHandshake(c, account)
	require.NotNil(t, recorder)
	account.Platform, account.Type = "changed", "changed"
	account.Credentials["access_token"] = "changed-secret-must-not-retain"
	clearFingerprintObservationOutboundIdentity(c)
	SetFingerprintObservationEnabled(true)
	recorder(http.Header{
		"Session-Id": []string{fingerprintObserverSessionV7}, "Thread-Id": []string{fingerprintObserverSessionV7},
		openAIWSTurnMetadataHeader: []string{`{"request_kind":"turn","history_ingest_requested":false}`},
	})
	entries := SnapshotFingerprintObservations(1)
	require.Len(t, entries, 1)
	require.Equal(t, fingerprintObserverSessionV7, entries[0].SessionID)
	require.Equal(t, fingerprintObserverSessionV7, entries[0].ThreadID)
	require.Equal(t, CodexWireRequestTurn, entries[0].RequestKind)
	require.NotNil(t, entries[0].HistoryIngestRequested)
	require.False(t, *entries[0].HistoryIngestRequested)
	encoded, err := json.Marshal(entries[0])
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "must-not-retain")
}

func TestFingerprintObservationMetadataWSRecorderUsesAPIKeyPlan(t *testing.T) {
	SetFingerprintObservationEnabled(false)
	SetFingerprintObservationEnabled(true)
	t.Cleanup(func() { SetFingerprintObservationEnabled(false) })
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/responses", nil)
	account := &Account{ID: 19009, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	// A stale request-local marker must not override an API-key plan; the same
	// authority boundary is used by the normal HTTP observation path.
	setFingerprintObservationOutboundIdentity(c, OpenAICodexTurnIdentity{
		SessionID: fingerprintObserverThreadV7, ThreadID: fingerprintObserverThreadV7,
	})
	SetOpenAIOAuthIdentityPlan(c, OpenAIOAuthIdentityPlan{TurnIdentityEnabled: true, TurnIdentity: OpenAICodexTurnIdentity{
		SessionID: fingerprintObserverSessionV7, ThreadID: fingerprintObserverSessionV7,
	}})
	recorder := freezeFingerprintObservationWSHandshake(c, account)
	require.NotNil(t, recorder)
	ClearOpenAIOAuthIdentityPlan(c)
	clearFingerprintObservationOutboundIdentity(c)
	recorder(http.Header{
		"Session-Id": []string{fingerprintObserverSessionV7}, "Thread-Id": []string{fingerprintObserverSessionV7},
	})
	entries := SnapshotFingerprintObservations(1)
	require.Len(t, entries, 1)
	require.Equal(t, fingerprintObserverSessionV7, entries[0].SessionID)
	require.Equal(t, fingerprintObserverSessionV7, entries[0].ThreadID)
}

func TestFingerprintObservationMetadataMalformedValues(t *testing.T) {
	for _, value := range []any{nil, "false", 0, map[string]any{}} {
		var entry FingerprintObservationEntry
		populateFingerprintObservationMetadata(&entry,
			http.Header{openAIWSTurnMetadataHeader: []string{`{"history_ingest_requested":true}`}},
			fingerprintMetadataTestBody(t, map[string]any{"history_ingest_requested": value}))
		require.Nil(t, entry.HistoryIngestRequested)
		require.Equal(t, "invalid", entry.MetadataStatus.HistoryIngestRequested)
	}
	for _, value := range []any{nil, false, []any{}, map[string]any{"trigger": "manual"}} {
		var entry FingerprintObservationEntry
		populateFingerprintObservationMetadata(&entry, nil, fingerprintMetadataTestBody(t, map[string]any{"compaction": value}))
		require.Nil(t, entry.Compaction)
		require.Equal(t, "invalid", entry.MetadataStatus.Compaction)
	}
}

func TestFingerprintObservationMetadataToolInventoryValidation(t *testing.T) {
	for _, field := range []string{"name", "functions"} {
		inventory := fingerprintMetadataTestInventory()
		delete(inventory["tools"].(map[string]any), field)
		encoded, _ := json.Marshal(inventory)
		result, status := parseFingerprintToolNamespaces(encoded)
		require.Nil(t, result)
		require.Equal(t, "invalid", status)
	}
	for _, field := range []string{"name", "direct", "deferred", "source"} {
		inventory := fingerprintMetadataTestInventory()
		function := inventory["tools"].(map[string]any)["functions"].(map[string]any)["search"].(map[string]any)
		function[field] = nil
		encoded, _ := json.Marshal(inventory)
		result, status := parseFingerprintToolNamespaces(encoded)
		require.Nil(t, result)
		require.Equal(t, "invalid", status)
	}
	for _, source := range []any{map[string]any{"kind": "mcp"}, map[string]any{"kind": "other"}, "mcp"} {
		function := fingerprintMetadataTestFunction("search")
		function["source"] = source
		encoded, _ := json.Marshal(function)
		truncated := false
		_, valid := parseFingerprintToolFunction("search", encoded, &truncated)
		require.False(t, valid)
	}
	function := fingerprintMetadataTestFunction("search")
	function["source"] = map[string]any{"kind": "harness", "server_name": "must-not-retain"}
	function["code_mode_name"] = nil
	encoded, _ := json.Marshal(function)
	truncated := false
	result, valid := parseFingerprintToolFunction("key", encoded, &truncated)
	require.True(t, valid)
	require.Empty(t, result.Source.ServerName)
	require.Nil(t, result.CodeModeName)
}

func TestFingerprintObservationMetadataInventoryLimitsAndOrder(t *testing.T) {
	inventory := make(map[string]any)
	for i := 0; i < 65; i++ {
		functions := make(map[string]any)
		for j := 0; j < 5; j++ {
			functions[fmt.Sprintf("f%03d", j)] = fingerprintMetadataTestFunction(fmt.Sprintf("f%d", j))
		}
		inventory[fmt.Sprintf("n%03d", i)] = map[string]any{"name": fmt.Sprintf("Namespace%d", i), "functions": functions}
	}
	encoded, err := json.Marshal(inventory)
	require.NoError(t, err)
	result, status := parseFingerprintToolNamespaces(encoded)
	require.Equal(t, "truncated", status)
	require.Len(t, result, 64)
	count := 0
	for i, namespace := range result {
		require.Equal(t, fmt.Sprintf("n%03d", i), namespace.Namespace)
		for j, function := range namespace.Functions {
			require.Equal(t, fmt.Sprintf("f%03d", j), function.Function)
			count++
		}
	}
	require.Equal(t, 256, count)
	function := fingerprintMetadataTestFunction(strings.Repeat("界", 257))
	encoded, _ = json.Marshal(function)
	truncated := false
	item, valid := parseFingerprintToolFunction(strings.Repeat("键", 257), encoded, &truncated)
	require.True(t, valid)
	require.True(t, truncated)
	require.Equal(t, 256, utf8.RuneCountInString(item.Name))
	require.Equal(t, 256, utf8.RuneCountInString(item.Function))
	require.Equal(t, 256, utf8.RuneCountInString(*item.CodeModeName))
}

func TestFingerprintObservationMetadataSnapshotOwnership(t *testing.T) {
	observer := &fingerprintObserver{ring: make([]FingerprintObservationEntry, 2)}
	observer.enabled.Store(true)
	var entry FingerprintObservationEntry
	populateFingerprintObservationMetadata(&entry, nil, fingerprintMetadataTestBody(t, map[string]any{
		"request_kind": "compaction", "history_ingest_requested": false,
		"compaction":           DefaultCodexCompactionTurnMetadata(CodexCompactionImplementationRemoteV2),
		"tool_namespaces_info": fingerprintMetadataTestInventory(),
	}))
	observer.record(entry)
	mutate := func(value *FingerprintObservationEntry) {
		value.MetadataStatus.Compaction = "invalid"
		*value.HistoryIngestRequested = true
		value.Compaction.Trigger = "auto"
		value.ToolNamespacesInfo[0].Name = "changed"
		value.ToolNamespacesInfo[0].Functions[0].Source.ServerName = "changed"
		*value.ToolNamespacesInfo[0].Functions[0].CodeModeName = "changed"
	}
	want := observer.snapshot(1)[0]
	mutate(&entry)
	require.True(t, reflect.DeepEqual(want, observer.snapshot(1)[0]), "record must own its nested state")
	snapshot := observer.snapshot(1)
	mutate(&snapshot[0])
	require.True(t, reflect.DeepEqual(want, observer.snapshot(1)[0]), "snapshot mutations must not change the ring")
}
