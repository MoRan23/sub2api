package service

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

const codex0172Traceparent = "00-0123456789abcdef0123456789abcdef-0123456789abcdef-01"

func TestCodex0172HTTPTraceFinalWireAndObservation(t *testing.T) {
	for _, accountType := range []string{AccountTypeOAuth, AccountTypeSetupToken} {
		for _, tc := range []struct {
			name, path, parentStatus, stateStatus string
			headers                               http.Header
		}{
			{"valid", "/responses", fingerprintMetadataValid, fingerprintMetadataValid, http.Header{"Traceparent": {codex0172Traceparent}, "Tracestate": {"vendor=private-value"}}},
			{"compact", "/responses/compact", fingerprintMetadataValid, fingerprintMetadataMissing, http.Header{"traceparent": {codex0172Traceparent}}},
			{"missing", "/responses", fingerprintMetadataMissing, fingerprintMetadataMissing, nil},
			{"invalid", "/responses", fingerprintMetadataInvalid, fingerprintMetadataInvalid, http.Header{"Traceparent": {"invalid-private-parent"}, "Tracestate": {"vendor=private-value"}}},
			{"duplicate", "/responses", fingerprintMetadataInvalid, fingerprintMetadataMissing, http.Header{"Traceparent": {codex0172Traceparent}, "traceparent": {codex0172Traceparent}}},
		} {
			t.Run(accountType+"/"+tc.name, func(t *testing.T) {
				SetFingerprintObservationEnabled(false)
				SetFingerprintObservationEnabled(true)
				t.Cleanup(func() { SetFingerprintObservationEnabled(false) })
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = httptest.NewRequest(http.MethodPost, "/v1"+tc.path, nil)
				c.Request.Header = tc.headers.Clone()
				account := &Account{ID: 172, Platform: PlatformOpenAI, Type: accountType}
				req := httptest.NewRequest(http.MethodPost, "https://chatgpt.com/backend-api/codex"+tc.path, nil)
				req.Header = http.Header{
					"Traceparent": {"override-parent"}, "traceparent": {"override-parent-lowercase"},
					"TRACEPARENT": {"override-parent-uppercase"}, "tRaCeStAtE": {"override=mixed-case"},
					"Tracestate": {"override=private"}, fingerprintTraceparentStatusHeader: {"valid"},
					fingerprintTracestateStatusHeader: {"valid"}, fingerprintMetadataHeaderBytes: {"42"},
				}
				body := []byte(`{"model":"gpt-6-astra","instructions":"test","input":[]}`)
				svc := &OpenAIGatewayService{}
				finalBody, err := svc.FinalizeOpenAIOAuthResponsesRequest(c, account, req, body, OpenAIOAuthResponsesFinalizeOptions{FinalModel: "gpt-6-astra"})
				require.NoError(t, err)
				require.Equal(t, "42", req.Header.Get(fingerprintMetadataHeaderBytes), "trace projection must preserve unrelated metadata observations")
				require.Empty(t, req.Header.Get(fingerprintTraceparentStatusHeader))
				require.Empty(t, req.Header.Get(fingerprintTracestateStatusHeader))
				if tc.parentStatus == fingerprintMetadataValid {
					require.Equal(t, []string{codex0172Traceparent}, headerValuesCaseInsensitive(req.Header, "traceparent"))
				}
				if tc.parentStatus != fingerprintMetadataValid {
					require.Empty(t, headerValuesCaseInsensitive(req.Header, "traceparent"))
				}
				if tc.stateStatus == fingerprintMetadataValid {
					require.Equal(t, "vendor=private-value", req.Header.Get("tracestate"))
				}
				if tc.stateStatus != fingerprintMetadataValid {
					require.Empty(t, headerValuesCaseInsensitive(req.Header, "tracestate"))
				}
				svc.recordFingerprintObservationWithBody(c, account, installationIDResolution{}, req.Header, finalBody)
				entries := SnapshotFingerprintObservations(1)
				require.Len(t, entries, 1)
				require.Equal(t, &FingerprintTraceContext{TraceparentStatus: tc.parentStatus, TracestateStatus: tc.stateStatus, Source: FingerprintObservationEventHTTP}, entries[0].TraceContext)
				encoded, err := json.Marshal(entries[0])
				require.NoError(t, err)
				for _, private := range []string{codex0172Traceparent, "private-value", "invalid-private-parent", "override-parent"} {
					require.NotContains(t, string(encoded), private)
				}
			})
		}
	}
}

func TestCodex0172HTTPTraceRetryAndAPIKeyBoundary(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
	c.Request.Header.Set("traceparent", codex0172Traceparent)
	c.Request.Header.Set("tracestate", "vendor=original")
	wsHeaders := make(http.Header)
	copyOpenAITraceContext(wsHeaders, c)
	c.Request.Header.Set("traceparent", "changed-after-capture")
	svc := &OpenAIGatewayService{}
	body := []byte(`{"model":"gpt-6-astra","instructions":"test","input":[]}`)
	for range 2 {
		req := httptest.NewRequest(http.MethodPost, chatgptCodexURL, nil)
		_, err := svc.FinalizeOpenAIOAuthResponsesRequest(c, &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth}, req, body, OpenAIOAuthResponsesFinalizeOptions{FinalModel: "gpt-6-astra"})
		require.NoError(t, err)
		require.Equal(t, codex0172Traceparent, req.Header.Get("traceparent"))
		require.Equal(t, "vendor=original", req.Header.Get("tracestate"))
	}
	require.Equal(t, FingerprintObservationEventHTTP, fingerprintOpenAITraceContext(c, wsHeaders, FingerprintObservationEventHTTP).Source)
	require.Equal(t, FingerprintObservationEventWSHandshake, fingerprintOpenAITraceContext(c, wsHeaders, FingerprintObservationEventWSHandshake).Source)

	for _, account := range []*Account{
		{Platform: PlatformOpenAI, Type: AccountTypeAPIKey},
		{Platform: PlatformAnthropic, Type: AccountTypeAPIKey},
	} {
		req := httptest.NewRequest(http.MethodPost, openaiPlatformAPIURL, nil)
		req.Header.Set("traceparent", "existing-provider-header")
		_, err := svc.FinalizeOpenAIOAuthResponsesRequest(c, account, req, body, OpenAIOAuthResponsesFinalizeOptions{FinalModel: "gpt-6-astra"})
		require.NoError(t, err)
		require.Equal(t, "existing-provider-header", req.Header.Get("traceparent"))
		require.Empty(t, req.Header.Get("tracestate"))
	}

	req := httptest.NewRequest(http.MethodPost, chatgptCodexURL, nil)
	req.Header.Set("traceparent", "synthetic-without-inbound")
	_, err := svc.FinalizeOpenAIOAuthResponsesRequest(nil, &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth}, req, body, OpenAIOAuthResponsesFinalizeOptions{FinalModel: "gpt-6-astra"})
	require.NoError(t, err)
	require.Empty(t, req.Header.Get("traceparent"))
}

func TestCodex0172HTTPTraceObservationRequiresFinalHeaders(t *testing.T) {
	SetFingerprintObservationEnabled(false)
	SetFingerprintObservationEnabled(true)
	t.Cleanup(func() { SetFingerprintObservationEnabled(false) })
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/alpha/search", nil)
	c.Request.Header.Set("traceparent", codex0172Traceparent)
	c.Request.Header.Set("tracestate", "vendor=private-state")
	account := &Account{ID: 172, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	svc := &OpenAIGatewayService{}
	body := []byte(`{"model":"gpt-6-astra","instructions":"test","input":[]}`)
	svc.recordFingerprintObservationWithBody(c, account, installationIDResolution{}, nil, body)
	entries := SnapshotFingerprintObservations(1)
	require.Len(t, entries, 1)
	require.Nil(t, entries[0].TraceContext, "native alpha/search and other unprojected HTTP paths retain their observation behavior")
	req := httptest.NewRequest(http.MethodPost, chatgptCodexURL, nil)
	_, err := svc.FinalizeOpenAIOAuthResponsesRequest(c, account, req, body, OpenAIOAuthResponsesFinalizeOptions{FinalModel: "gpt-6-astra"})
	require.NoError(t, err)
	for _, headers := range []http.Header{
		nil,
		{"Traceparent": {"other-final-parent"}, "Tracestate": {"vendor=private-state"}},
		{"Traceparent": {codex0172Traceparent, codex0172Traceparent}},
	} {
		svc.recordFingerprintObservationWithBody(c, account, installationIDResolution{}, headers, body)
		entries := SnapshotFingerprintObservations(1)
		require.Len(t, entries, 1)
		require.Equal(t, &FingerprintTraceContext{TraceparentStatus: fingerprintMetadataMissing, TracestateStatus: fingerprintMetadataMissing, Source: FingerprintObservationEventHTTP}, entries[0].TraceContext)
	}
}

func BenchmarkCodex0172TraceProjection(b *testing.B) {
	for _, parent := range []string{"", codex0172Traceparent} {
		name := "absent"
		if parent != "" {
			name = "valid"
		}
		b.Run(name, func(b *testing.B) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			if parent != "" {
				c.Request.Header.Set("traceparent", parent)
			}
			captureOpenAITraceContext(c)
			headers := make(http.Header)
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				copyOpenAITraceContext(headers, c)
			}
		})
	}
}
