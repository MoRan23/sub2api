package service

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestCodexEngineTempUnschedBodyIsolatesErrorFields(t *testing.T) {
	for _, tc := range []struct {
		name, payload, event string
	}{
		{"http error", `{"output":"generated-secret","history":"history-secret","error":{"code":"capacity_unavailable","message":"maintenance","output":"nested-output-secret","metadata":"metadata-secret"}}`, ""},
		{"terminal error", `{"response":{"output":"generated-secret","history":"history-secret","error":{"code":"capacity_unavailable","message":"maintenance","metadata":"metadata-secret"}}}`, "response.failed"},
		{"bare error", `{"type":"error","code":"capacity_unavailable","message":"maintenance","output":"generated-secret","history":"history-secret"}`, "error"},
		{"top-level http fields", `{"code":"capacity_unavailable","message":"maintenance","output":"generated-secret","history":"history-secret"}`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			failure := newCodexEngineResponseError(http.StatusServiceUnavailable, []byte(tc.payload), tc.event)
			require.True(t, gjson.ValidBytes(failure.tempUnschedBody))
			require.Equal(t, "capacity_unavailable", gjson.GetBytes(failure.tempUnschedBody, "error.code").String())
			require.Equal(t, "maintenance", gjson.GetBytes(failure.tempUnschedBody, "error.message").String())
			require.NotContains(t, string(failure.tempUnschedBody), "secret")
		})
	}
}

func TestCodexEngineTempUnschedBodyBoundsWithoutDiagnosticTruncation(t *testing.T) {
	message := strings.Repeat("x", 4096) + " maintenance " + strings.Repeat("x", 2*tempUnschedBodyMaxBytes)
	payload, err := json.Marshal(map[string]any{"response": map[string]any{
		"output": strings.Repeat("generated-secret", tempUnschedBodyMaxBytes),
		"error":  map[string]string{"code": "capacity_unavailable", "message": message},
	}})
	require.NoError(t, err)
	failure := newCodexEngineResponseError(http.StatusOK, payload, "response.failed")
	require.LessOrEqual(t, len(failure.tempUnschedBody), tempUnschedBodyMaxBytes)
	require.True(t, gjson.ValidBytes(failure.tempUnschedBody))
	require.Contains(t, string(failure.tempUnschedBody), "maintenance")
	require.NotContains(t, string(failure.tempUnschedBody), "generated-secret")
	for _, message := range []string{strings.Repeat("\x00", tempUnschedBodyMaxBytes), strings.Repeat("<>&\"\\", tempUnschedBodyMaxBytes), strings.Repeat("维修中", tempUnschedBodyMaxBytes)} {
		message = "maintenance " + message
		payload, err := json.Marshal(map[string]any{"error": map[string]string{"message": message}})
		require.NoError(t, err)
		failure := newCodexEngineResponseError(http.StatusServiceUnavailable, payload, "")
		require.LessOrEqual(t, len(failure.tempUnschedBody), tempUnschedBodyMaxBytes)
		require.True(t, gjson.ValidBytes(failure.tempUnschedBody))
		require.True(t, strings.HasPrefix(gjson.GetBytes(failure.tempUnschedBody, "error.message").String(), "maintenance "))
	}
}

func TestCodexEngineTempUnschedBodyAcceptsOnlyScalarErrorText(t *testing.T) {
	failure := newCodexEngineResponseError(http.StatusServiceUnavailable, []byte(`"maintenance"`), "")
	require.Equal(t, "maintenance", gjson.GetBytes(failure.tempUnschedBody, "error.message").String())
	for _, payload := range []string{`["maintenance"]`, `{"output":"maintenance"}`, `{"metadata":"maintenance"}`} {
		failure := newCodexEngineResponseError(http.StatusServiceUnavailable, []byte(payload), "")
		require.Empty(t, failure.tempUnschedBody, payload)
	}
}

func TestCodexEngineTempUnschedBodyMergeRetainsEarlierError(t *testing.T) {
	previous := newCodexEngineResponseError(http.StatusOK, []byte(`{"type":"error","code":"capacity_unavailable","message":"maintenance"}`), "error")
	terminal := newCodexEngineResponseError(http.StatusOK, []byte(`{"response":{"status":"failed","error":{"type":"server_error","status_code":503},"output":"generated-secret"}}`), "response.failed")
	body := mergeCodexEngineTempUnschedBodies(terminal.tempUnschedBody, previous.tempUnschedBody)
	require.Equal(t, "capacity_unavailable", gjson.GetBytes(body, "error.code").String())
	require.Equal(t, "maintenance", gjson.GetBytes(body, "error.message").String())
	require.Equal(t, "server_error", gjson.GetBytes(body, "error.type").String())
	require.NotContains(t, string(body), "generated-secret")
	require.Equal(t, previous.tempUnschedBody, mergeCodexEngineTempUnschedBodies(nil, previous.tempUnschedBody))
	require.Equal(t, terminal.tempUnschedBody, mergeCodexEngineTempUnschedBodies(terminal.tempUnschedBody, nil))
}

func TestCodexEngineTempUnschedBodyPreservesRootUnauthorizedOnly(t *testing.T) {
	for _, tc := range []struct {
		payload           string
		credentialFailure bool
	}{
		{`{"detail":"Unauthorized"}`, true},
		{`{"detail":"Unauthorized","error":{"message":"maintenance"}}`, true},
		{`{"error":{"detail":"Unauthorized"}}`, false},
	} {
		failure := newCodexEngineResponseError(http.StatusUnauthorized, []byte(tc.payload), "")
		require.Equal(t, tc.credentialFailure, unauthorizedRequiresCredentialHandling(engineAccount(), failure.tempUnschedBody))
	}
}

func TestCodexEngineTempUnschedBodyOmitsSynthesizedFailureValues(t *testing.T) {
	empty := newCodexEngineResponseError(http.StatusServiceUnavailable, nil, "")
	require.Equal(t, "Service Unavailable", empty.message)
	require.Empty(t, empty.tempUnschedBody)
	terminal := newCodexEngineResponseError(http.StatusOK, []byte(`{"type":"response.failed","response":{"output":"maintenance"}}`), "response.failed")
	require.Equal(t, http.StatusBadGateway, terminal.status)
	require.Zero(t, terminal.reportedStatus)
	require.Empty(t, terminal.tempUnschedBody)
	bare := newCodexEngineResponseError(http.StatusOK, []byte(`{"type":"error","message":"maintenance"}`), "error")
	require.Equal(t, http.StatusBadGateway, bare.status)
	require.Zero(t, bare.reportedStatus)
	require.NotContains(t, string(bare.tempUnschedBody), "502")
	require.NotContains(t, string(bare.tempUnschedBody), "Bad Gateway")
	plain := newCodexEngineResponseError(http.StatusServiceUnavailable, []byte(strings.Repeat("maintenance ", tempUnschedBodyMaxBytes)), "")
	require.Len(t, plain.tempUnschedBody, tempUnschedBodyMaxBytes)
	require.Contains(t, string(plain.tempUnschedBody), "maintenance")
}
