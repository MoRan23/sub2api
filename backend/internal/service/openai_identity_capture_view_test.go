package service

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// This is the installation capture decoder used before the shared body view.
// Keep it separate from the optimized capture: Decode accepts the first JSON
// value and repairs invalid UTF-8, while the other identity parsers are strict.
func legacyOpenAIIdentityCaptureInstallationID(c *gin.Context, body []byte) string {
	var decoded map[string]any
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.UseNumber()
	if len(body) > 0 && decoder.Decode(&decoded) == nil {
		return extractClientInstallationID(c, decoded)
	}
	return extractClientInstallationID(c, nil)
}

func TestOpenAIIdentityCaptureViewInstallationCompatibility(t *testing.T) {
	nestedBody := func(metadata string) []byte {
		encoded, err := json.Marshal(metadata)
		require.NoError(t, err)
		return []byte(`{"client_metadata":{"x-codex-turn-metadata":` + string(encoded) + `}}`)
	}
	tests := []struct {
		name         string
		body         []byte
		directHeader string
		turnHeader   string
		want         string
	}{
		{name: "direct header wins", body: []byte(`{"client_metadata":{"x-codex-installation-id":"body"}}`), directHeader: " header ", turnHeader: `{"installation_id":"nested-header"}`, want: "header"},
		{name: "direct body before nested header", body: []byte(`{"client_metadata":{"x-codex-installation-id":" body "}}`), turnHeader: `{"installation_id":"nested-header"}`, want: "body"},
		{name: "nested header before nested body", body: nestedBody(`{"installation_id":"nested-body"}`), turnHeader: `{"installation_id":" nested-header "}`, want: "nested-header"},
		{name: "nested body fallback", body: nestedBody(`{"installation_id":" nested-body "}`), want: "nested-body"},
		{name: "trailing JSON accepted for installation", body: []byte(`{"client_metadata":{"x-codex-installation-id":"first"}} {"client_metadata":{"x-codex-installation-id":"second"}}`), want: "first"},
		{name: "trailing garbage accepted for installation", body: []byte(`{"client_metadata":{"x-codex-installation-id":"first"}} trailing`), want: "first"},
		{name: "invalid UTF8 repaired for installation", body: []byte("{\"client_metadata\":{\"x-codex-installation-id\":\"from-\xff\"}}"), want: "from-\ufffd"},
		{name: "invalid UTF8 elsewhere accepted for installation", body: []byte("{\"input\":\"\xff\",\"client_metadata\":{\"x-codex-installation-id\":\"body\"}}"), want: "body"},
		{name: "last escaped direct key wins", body: []byte(`{"client_metadata":{"x-codex-installation-id":"first","x-codex-installation-\u0069d":"last"}}`), want: "last"},
		{name: "last escaped container wins", body: []byte(`{"client_metadata":{"x-codex-installation-id":"first"},"client_metad\u0061ta":{"x-codex-installation-id":"last"}}`), want: "last"},
		{name: "last null container clears earlier object", body: []byte(`{"client_metadata":{"x-codex-installation-id":"first"},"client_metad\u0061ta":null}`), turnHeader: `{"installation_id":"header"}`, want: "header"},
		{name: "last non-string direct key falls back", body: []byte(`{"client_metadata":{"x-codex-installation-id":"first","x-codex-installation-\u0069d":7,"x-codex-turn-metadata":"{\"installation_id\":\"nested\"}"}}`), want: "nested"},
		{name: "last null direct key falls back", body: []byte(`{"client_metadata":{"x-codex-installation-id":"first","x-codex-installation-\u0069d":null}}`), turnHeader: `{"installation_id":"header"}`, want: "header"},
		{name: "last escaped nested key wins", body: nestedBody(`{"installation_id":"first","installation_\u0069d":"last"}`), want: "last"},
		{name: "last null nested key clears earlier value", body: nestedBody(`{"installation_id":"first","installation_\u0069d":null}`)},
		{name: "object turn carrier is not installation string", body: []byte(`{"client_metadata":{"x-codex-turn-metadata":{"installation_id":"ignored"}}}`)},
		{name: "root installation fields ignored", body: []byte(`{"installation_id":"ignored","x-codex-installation-id":"ignored","x-codex-turn-metadata":{"installation_id":"ignored"}}`)},
		{name: "large outer number preserved by UseNumber", body: []byte(`{"input":{"unknown":1e1000},"client_metadata":{"x-codex-installation-id":"body"}}`), want: "body"},
		{name: "nested overflow rejects nested installation", body: nestedBody(`{"installation_id":"ignored","unknown":1e1000}`)},
		{name: "header overflow falls back to nested body", body: nestedBody(`{"installation_id":"body"}`), turnHeader: `{"installation_id":"ignored","unknown":1e1000}`, want: "body"},
		{name: "malformed first object ignores body", body: []byte(`{"client_metadata":{"x-codex-installation-id":"ignored"},}`), turnHeader: `{"installation_id":"header"}`, want: "header"},
		{name: "null body uses header", body: []byte(`null`), turnHeader: `{"installation_id":"header"}`, want: "header"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			if tt.directHeader != "" {
				c.Request.Header.Set(codexInstallationIDKey, tt.directHeader)
			}
			if tt.turnHeader != "" {
				c.Request.Header.Set(openAIWSTurnMetadataHeader, tt.turnHeader)
			}
			originalBody := bytes.Clone(tt.body)
			originalHeaders := c.Request.Header.Clone()
			legacy := legacyOpenAIIdentityCaptureInstallationID(c, tt.body)
			require.Equal(t, tt.want, legacy, "fixture must describe the previous decoder contract")
			capture := CaptureOpenAIOAuthIdentity(c, tt.body, "")
			require.Equal(t, legacy, capture.ClientInstallationID)
			require.Equal(t, originalBody, tt.body)
			require.Equal(t, originalHeaders, c.Request.Header)
		})
	}
}

func TestOpenAIIdentityCaptureViewMetadataContainerPresence(t *testing.T) {
	for _, tt := range []struct {
		name    string
		body    string
		invalid int
	}{
		{name: "absent", body: `{}`},
		{name: "object", body: `{"client_metadata":{}}`},
		{name: "null", body: `{"client_metadata":null}`, invalid: 1},
		{name: "string", body: `{"client_metadata":"opaque"}`, invalid: 1},
		{name: "array", body: `{"client_metadata":[]}`, invalid: 1},
		{name: "number", body: `{"client_metadata":7}`, invalid: 1},
		{name: "boolean", body: `{"client_metadata":false}`, invalid: 1},
		{name: "last escaped null", body: `{"client_metadata":{},"client_metad\u0061ta":null}`, invalid: 1},
		{name: "last escaped object", body: `{"client_metadata":null,"client_metad\u0061ta":{}}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			body := []byte(tt.body)
			before := SnapshotOpenAIOutboundSessionIdentityRuntimeMetrics()
			capture := CaptureOpenAIOAuthIdentity(nil, body, "")
			after := SnapshotOpenAIOutboundSessionIdentityRuntimeMetrics()
			require.Equal(t, tt.invalid, capture.InvalidMetadataCount)
			require.Equal(t, int64(tt.invalid), after.MetadataInvalidByCarrier.ClientMetadataContainer-before.MetadataInvalidByCarrier.ClientMetadataContainer)
			require.Equal(t, int64(tt.invalid), after.InvalidMetadataTotal-before.InvalidMetadataTotal)
			require.Equal(t, tt.body, string(body))
		})
	}
}

func TestOpenAIIdentityCaptureViewStrictConsumersIgnoreLenientInstallationBody(t *testing.T) {
	valid := `{"input":"history","prompt_cache_key":"cache","client_metadata":{"x-codex-installation-id":"installation","x-codex-turn-metadata":"{\"turn_id\":\"01989f44-7c00-7000-8000-000000000011\"}"}}`
	for _, tt := range []struct {
		name string
		body []byte
	}{
		{name: "trailing JSON", body: []byte(valid + ` {}`)},
		{name: "trailing garbage", body: []byte(valid + ` trailing`)},
		{name: "invalid UTF8", body: []byte(strings.Replace(valid, "history", "\xff", 1))},
	} {
		t.Run(tt.name, func(t *testing.T) {
			original := bytes.Clone(tt.body)
			capture := CaptureOpenAIOAuthIdentity(nil, tt.body, "")
			require.Equal(t, "installation", capture.ClientInstallationID)
			require.Empty(t, capture.WireProfile.InstallationID)
			require.False(t, capture.PromptCacheKey.Present)
			require.True(t, capture.RequestTurn.Generated)
			require.False(t, capture.RequestTurn.Explicit)
			require.NotEqual(t, "01989f44-7c00-7000-8000-000000000011", capture.RequestTurn.ID)
			require.Equal(t, original, tt.body)
		})
	}
}

func TestOpenAIIdentityCaptureViewPreservesInputAndOwnsSnapshot(t *testing.T) {
	body := []byte(`{"input":[{"role":"user","content":[{"type":"input_text","text":"<opaque>\u2028"}],"unknown":9007199254740993}],"unknown":{"keep":[null,false,18446744073709551615]},"client_metadata":{"x-codex-installation-id":"installation","x-codex-turn-metadata":"{\"request_kind\":\"turn\",\"turn_id\":\"01989f44-7c00-7000-8000-000000000011\",\"session_id\":\"session\",\"thread_id\":\"thread\",\"window_number\":18446744073709551615,\"workspaces\":{\"sequence\":9007199254740993},\"tool_namespaces_info\":{\"unknown\":[true,null]},\"custom\":\"keep\"}"}}`)
	original := bytes.Clone(body)
	capture := CaptureOpenAIOAuthIdentity(nil, body, "")
	require.Equal(t, original, body)
	require.Equal(t, "installation", capture.ClientInstallationID)
	require.Equal(t, uint64Pointer(^uint64(0)), capture.WireProfile.WindowNumber)
	require.JSONEq(t, `{"sequence":9007199254740993}`, string(capture.WireProfile.Workspaces))
	require.Contains(t, string(capture.WireProfile.Workspaces), "9007199254740993")
	require.JSONEq(t, `{"unknown":[true,null]}`, string(capture.WireProfile.ToolNamespacesInfo))
	require.Equal(t, "keep", capture.WireProfile.ExtraMetadata["custom"])
	frozen := cloneOpenAIOAuthIdentityCapture(capture)
	for i := range body {
		body[i] = '?'
	}
	require.Equal(t, frozen, capture, "capture must own its retained fields after ingress bytes are reused")
}

func BenchmarkOpenAIOAuthIdentityCaptureLargeHistory(b *testing.B) {
	for _, fixture := range []struct {
		name string
		size int
	}{
		{name: "4MiB", size: 4 << 20},
		{name: "64MiB", size: 64 << 20},
	} {
		b.Run(fixture.name, func(b *testing.B) {
			body := openAIIdentityCaptureHistoryFixture(fixture.size)
			original := bytes.Clone(body)
			b.ReportAllocs()
			b.SetBytes(int64(len(body)))
			for b.Loop() {
				capture := CaptureOpenAIOAuthIdentity(nil, body, "")
				if capture.ClientInstallationID != "installation" || capture.RequestTurn.ID != "01989f44-7c00-7000-8000-000000000011" {
					b.Fatal("large history fixture lost identity metadata")
				}
				runtime.KeepAlive(capture)
			}
			if !bytes.Equal(original, body) {
				b.Fatal("capture modified raw history")
			}
		})
	}
}

func BenchmarkOpenAIOAuthIdentityCaptureHTTPLargeHistory(b *testing.B) {
	previousMode := gin.Mode()
	gin.SetMode(gin.TestMode)
	defer gin.SetMode(previousMode)
	for _, fixture := range []struct {
		name string
		size int
	}{
		{name: "4MiB", size: 4 << 20},
		{name: "64MiB", size: 64 << 20},
	} {
		b.Run(fixture.name, func(b *testing.B) {
			body := openAIIdentityCaptureHistoryFixture(fixture.size)
			// Add the HTTP streaming flag without decoding the raw history fixture.
			body = append(body[:len(body)-1], []byte(`,"stream":true}`)...)
			original := bytes.Clone(body)
			b.ReportAllocs()
			b.SetBytes(int64(len(body)))
			for b.Loop() {
				// Each iteration represents a fresh ingress request, with no cached
				// identity capture or request context carried over from another turn.
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
				c.Request.Header.Set("Content-Type", "application/json")
				c.Request.Header.Set("User-Agent", "codex_cli_rs/0.200.1 (Windows 11.0.26100; x86_64) WindowsTerminal")
				capture := CaptureOpenAIOAuthIdentity(c, body, "")
				if capture.ClientInstallationID != "installation" || capture.RequestTurn.ID != "01989f44-7c00-7000-8000-000000000011" {
					b.Fatal("HTTP large history fixture lost identity metadata")
				}
				runtime.KeepAlive(capture)
			}
			if !bytes.Equal(original, body) {
				b.Fatal("HTTP capture modified raw history")
			}
		})
	}
}

func openAIIdentityCaptureHistoryFixture(historyBytes int) []byte {
	entry := []byte(`{"role":"user","content":[{"type":"input_text","text":"` + strings.Repeat("history ", 128) + `"}],"sequence":9007199254740993}`)
	var body bytes.Buffer
	body.Grow(historyBytes + 1024)
	body.WriteString(`{"model":"gpt-6.1-sol","input":[`)
	for remaining := historyBytes; remaining > len(entry); remaining -= len(entry) + 1 {
		body.Write(entry)
		body.WriteByte(',')
	}
	body.Write(entry)
	body.WriteString(`],"unknown":{"raw":[null,true,18446744073709551615]},"client_metadata":{"x-codex-installation-id":"installation","x-codex-turn-metadata":"{\"session_id\":\"session\",\"thread_id\":\"thread\",\"turn_id\":\"01989f44-7c00-7000-8000-000000000011\"}"}}`)
	return body.Bytes()
}
