package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestCodex0171MetadataBudget(t *testing.T) {
	t.Parallel()
	metadata := func(padding int, remotes string) string {
		return `{"agent_name":"` + strings.Repeat("a", padding) + `","workspaces":{"/a":{"associated_remote_urls":` + remotes + `}}}`
	}
	t.Run("below_budget_unchanged", func(t *testing.T) {
		full := metadata(20, `{"origin":"https://example.com/a"}`)
		actual, err := boundCodexTurnMetadata(full)
		require.NoError(t, err)
		require.Equal(t, full, actual)
	})
	t.Run("exact_8096", func(t *testing.T) {
		full := metadata(0, `{"origin":"a"}`)
		full = metadata(codexTurnMetadataBudget-len(full), `{"origin":"a"}`)
		actual, err := boundCodexTurnMetadata(full)
		require.NoError(t, err)
		require.Len(t, actual, codexTurnMetadataBudget)
		require.Equal(t, full, actual)
	})
	t.Run("8097_removes_whole_remote", func(t *testing.T) {
		full := metadata(0, `{"origin":"a"}`)
		full = metadata(codexTurnMetadataBudget-len(full)+1, `{"origin":"a"}`)
		actual, err := boundCodexTurnMetadata(full)
		require.NoError(t, err)
		require.LessOrEqual(t, len(actual), codexTurnMetadataBudget)
		require.False(t, gjson.Get(actual, "workspaces./a.associated_remote_urls").Exists())
	})
	t.Run("no_workspaces_preserved", func(t *testing.T) {
		full := `{"agent_name":"` + strings.Repeat("a", 9000) + `"}`
		actual, err := boundCodexTurnMetadata(full)
		require.NoError(t, err)
		require.Equal(t, full, actual)
	})
	t.Run("base_over_budget_preserved", func(t *testing.T) {
		full := metadata(9000, `{"origin":"a"}`)
		actual, err := boundCodexTurnMetadata(full)
		require.NoError(t, err)
		require.Equal(t, full, actual)
	})
	t.Run("origin_before_shorter_other", func(t *testing.T) {
		remotes := `{"other":"a","origin":"` + strings.Repeat("b", 50) + `"}`
		full := metadata(0, remotes)
		full = metadata(codexTurnMetadataBudget-len(full)+len(`,"other":"a"`), remotes)
		actual, err := boundCodexTurnMetadata(full)
		require.NoError(t, err)
		require.Len(t, actual, codexTurnMetadataBudget)
		require.Equal(t, strings.Repeat("b", 50), gjson.Get(actual, "workspaces./a.associated_remote_urls.origin").String())
		require.False(t, gjson.Get(actual, "workspaces./a.associated_remote_urls.other").Exists())
	})
	t.Run("empty_maps_and_nonremote_fields_preserved", func(t *testing.T) {
		full := `{"agent_name":"` + strings.Repeat("x", 8050) + `","workspaces":{"/empty":{"associated_remote_urls":{}},"/a":{"v":false,"associated_remote_urls":{"origin":"` + strings.Repeat("z", 800) + `"}}}}`
		actual, err := boundCodexTurnMetadata(full)
		require.NoError(t, err)
		// Non-remote content already exceeds the budget, so the complete value survives.
		require.Equal(t, full, actual)
	})
	t.Run("unicode_size_and_stable_order", func(t *testing.T) {
		full := `{"agent_name":"` + strings.Repeat("x", 7950) + `","workspaces":{"/z":{"associated_remote_urls":{"origin":"` + strings.Repeat("z", 100) + `"}},"/a":{"associated_remote_urls":{"long":"\u4e2d\ud83d\ude80","b":"ab","a":"ab"}}}}`
		actual, err := boundCodexTurnMetadata(full)
		require.NoError(t, err)
		require.LessOrEqual(t, len(actual), codexTurnMetadataBudget)
		require.True(t, json.Valid([]byte(actual)))
		for range 20 {
			again, repeatErr := boundCodexTurnMetadata(full)
			require.NoError(t, repeatErr)
			require.Equal(t, actual, again)
		}
	})
	t.Run("body_header_independent_and_input_immutable", func(t *testing.T) {
		profile := ParseCodexWireProfile(metadata(7600, `{"origin":"`+strings.Repeat("a", 300)+`"}`))
		profile.ToolNamespacesInfoAllowed = true
		profile.ToolNamespacesInfo = json.RawMessage(`{"tools":"` + strings.Repeat("t", 250) + `"}`)
		original := bytes.Clone(profile.Workspaces)
		header, err := profile.MarshalNestedJSON(false)
		require.NoError(t, err)
		body, err := profile.MarshalNestedJSON(true)
		require.NoError(t, err)
		require.True(t, gjson.Get(header, "workspaces./a.associated_remote_urls.origin").Exists())
		require.False(t, gjson.Get(body, "workspaces./a.associated_remote_urls.origin").Exists())
		require.True(t, gjson.Get(body, "tool_namespaces_info").Exists())
		require.False(t, gjson.Get(header, "tool_namespaces_info").Exists())
		require.Equal(t, original, []byte(profile.Workspaces))
	})
	t.Run("legacy_extensions_reserved", func(t *testing.T) {
		full := `{"extension":"` + strings.Repeat("z", 7900) + `","workspaces":{"/a":{"associated_remote_urls":{"origin":"` + strings.Repeat("x", 250) + `"}}}}`
		actual, err := rewriteOpenAICodexTurnMetadataProjection(full, openAICodexMetadataProjection{}, false)
		require.NoError(t, err)
		require.Equal(t, strings.Repeat("z", 7900), gjson.Get(actual, "extension").String())
		require.LessOrEqual(t, len(actual), codexTurnMetadataBudget)
		require.False(t, gjson.Get(actual, "workspaces./a.associated_remote_urls.origin").Exists())
	})
}

func TestCodex0171WSTraceContext(t *testing.T) {
	t.Parallel()
	const parent = "00-0123456789abcdef0123456789abcdef-0123456789abcdef-01"
	for _, tc := range []struct {
		name, parent, state string
		parentOK, stateOK   bool
	}{
		{"valid", parent, "vendor=value,1tenant@system=other", true, true},
		{"zero_trace", "00-00000000000000000000000000000000-0123456789abcdef-01", "a=b", false, false},
		{"zero_span", "00-0123456789abcdef0123456789abcdef-0000000000000000-01", "", false, false},
		{"uppercase", strings.ToUpper(parent), "", false, false},
		{"version_ff", "ff" + parent[2:], "", false, false},
		{"version_zero_suffix", parent + "-suffix", "", false, false},
		{"future_version", "01" + parent[2:] + "-extension", "a=b", true, true},
		{"state_without_parent", "", "a=b", false, false},
		{"duplicate_state", parent, "a=b,a=c", true, false},
		{"state_too_large", parent, "a=" + strings.Repeat("v", 511), true, false},
		{"bad_state_key", parent, "A=b", true, false},
		{"bad_state_value", parent, "a=b=c", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			headers := make(http.Header)
			if tc.parent != "" {
				headers.Set("traceparent", tc.parent)
			}
			if tc.state != "" {
				headers.Set("tracestate", tc.state)
			}
			value := parseOpenAIWSTraceContext(headers)
			require.Equal(t, tc.parentOK, value.parent != "")
			require.Equal(t, tc.stateOK, value.state != "")
		})
	}
	t.Run("duplicate_parent_dropped", func(t *testing.T) {
		value := parseOpenAIWSTraceContext(http.Header{"Traceparent": {parent, parent}, "Tracestate": {"a=b"}})
		require.Empty(t, value.parent)
		require.Empty(t, value.state)
		require.Equal(t, fingerprintMetadataInvalid, value.observation.TraceparentStatus)
	})
	t.Run("validated_once_and_snapshot_redacted", func(t *testing.T) {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
		c.Request.Header.Set("traceparent", parent)
		c.Request.Header.Set("tracestate", "vendor=value")
		headers := make(http.Header)
		copyOpenAIWSTraceContext(headers, c)
		c.Request.Header.Set("traceparent", "invalid-after-capture")
		reconnect := make(http.Header)
		copyOpenAIWSTraceContext(reconnect, c)
		require.Equal(t, parent, reconnect.Get("traceparent"))
		snapshot := cloneFingerprintObservationHeaders(headers)
		require.Empty(t, snapshot.Get("traceparent"))
		require.Empty(t, snapshot.Get("tracestate"))
		require.Equal(t, fingerprintMetadataValid, fingerprintWSHandshakeTraceContext(snapshot).TraceparentStatus)
		plan := OpenAIOAuthIdentityPlan{}
		digest := openAIWSOutboundIdentityPlanDigest(headers, plan)
		headers.Set("traceparent", "00-abcdef0123456789abcdef0123456789-abcdef0123456789-00")
		require.Equal(t, digest, openAIWSOutboundIdentityPlanDigest(headers, plan))
	})
}

func TestCodex0171WSHeadersContainOnlyValidatedTrace(t *testing.T) {
	svc := &OpenAIGatewayService{}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
	const parent = "00-0123456789abcdef0123456789abcdef-0123456789abcdef-01"
	c.Request.Header.Set("traceparent", parent)
	c.Request.Header.Set("tracestate", "vendor=value")
	account := &Account{ID: 171, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	require.NoError(t, json.Unmarshal([]byte(`{"header_override_enabled":true,"header_overrides":{"traceparent":"spoofed","tracestate":"spoofed=value","x-sub2-observation-traceparent-status":"valid","x-sub2-observation-tracestate-status":"valid","x-sub2-observation-metadata-bytes":"999"}}`), &account.Credentials))
	headers, _, err := svc.buildOpenAIWSHeadersWithBody(context.Background(), c, account, "test-token",
		OpenAIWSProtocolDecision{Transport: OpenAIUpstreamTransportResponsesWebsocketV2}, true, "", "", "", nil, false)
	require.NoError(t, err)
	require.Equal(t, parent, headers.Get("traceparent"))
	require.Equal(t, "vendor=value", headers.Get("tracestate"))
	for name := range headers {
		require.False(t, strings.HasPrefix(strings.ToLower(name), "x-sub2-observation-"))
	}
	require.Len(t, headerValuesCaseInsensitive(headers, "traceparent"), 1)
	require.Len(t, headerValuesCaseInsensitive(headers, "tracestate"), 1)
}

func TestCodex0171MetadataBudgetObservation(t *testing.T) {
	t.Parallel()
	raw := `{"workspaces":{"/a":{"associated_remote_urls":{"origin":"https://example.com/repo"}}}}`
	headers := http.Header{"X-Codex-Turn-Metadata": {raw}}
	carriers := fingerprintFinalMetadataCarriers(headers, nil)
	observed := observeFingerprintMetadataBudget(headers, carriers)
	require.Equal(t, len(raw), *observed.HeaderBytes)
	require.Nil(t, observed.BodyBytes)
	require.Nil(t, observed.RemotesTrimmed)
	snapshot := cloneFingerprintObservationHeaders(headers)
	require.NotContains(t, snapshot.Get(openAIWSTurnMetadataHeader), "https://example.com/repo")
	fromSnapshot := observeFingerprintMetadataBudget(snapshot, fingerprintFinalMetadataCarriers(snapshot, nil))
	require.Equal(t, observed, fromSnapshot)
	original := json.RawMessage(gjson.Get(raw, "workspaces").Raw)
	require.Nil(t, observeCodexMetadataRemotesTrimmed(original, snapshot, fingerprintFinalMetadataCarriers(snapshot, nil)))
	require.False(t, *observeCodexMetadataRemotesTrimmed(original, headers, carriers))
	trimmed := http.Header{"X-Codex-Turn-Metadata": {`{"workspaces":{"/a":{}}}`}}
	require.True(t, *observeCodexMetadataRemotesTrimmed(original, trimmed, fingerprintFinalMetadataCarriers(trimmed, nil)))
	require.Nil(t, observeFingerprintMetadataBudget(nil, nil))
	entry := FingerprintObservationEntry{MetadataBudget: observed, TraceContext: &FingerprintTraceContext{TraceparentStatus: fingerprintMetadataValid}}
	cloneFingerprintObservationMetadata(&entry)
	*entry.MetadataBudget.HeaderBytes = 0
	entry.TraceContext.TraceparentStatus = fingerprintMetadataMissing
	require.Equal(t, len(raw), *observed.HeaderBytes)
}

func TestCodex0171WSComparablePreservesResponseFields(t *testing.T) {
	t.Parallel()
	base := []byte(`{"model":"gpt-5.6-sol","input":[]}`)
	want, err := normalizeOpenAIWSPayloadWithoutInputAndPreviousResponseID(base)
	require.NoError(t, err)
	for _, fields := range []string{"", `,"request_country":null,"access_programs":[]`, `,"request_country":"JP","access_programs":["program"]`} {
		payload := []byte(`{"model":"gpt-5.6-sol","input":[]` + fields + `}`)
		before := bytes.Clone(payload)
		actual, compareErr := normalizeOpenAIWSPayloadWithoutInputAndPreviousResponseID(payload)
		require.NoError(t, compareErr)
		require.Equal(t, want, actual)
		require.Equal(t, before, payload)
	}
	// Invalid UTF-8 enters the existing map decoder fallback; its response-only fields must also be excluded.
	fallback := []byte("{\"model\":\"gpt-5.6-sol\",\"input\":[],\"request_country\":\"\xff\",\"access_programs\":[]}")
	actual, err := normalizeOpenAIWSPayloadWithoutInputAndPreviousResponseID(fallback)
	require.NoError(t, err)
	require.Equal(t, want, actual)
}

func BenchmarkCodex0171MetadataBudget(b *testing.B) {
	for _, size := range []int{200, 8000, 32000} {
		b.Run(fmt.Sprint(size), func(b *testing.B) {
			full := `{"workspaces":{"/repo":{"associated_remote_urls":{"origin":"` + strings.Repeat("a", size) + `","upstream":"https://example.com/upstream"}}}}`
			b.ReportAllocs()
			for b.Loop() {
				if _, err := boundCodexTurnMetadata(full); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
