package service

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func TestOpenAIWSHTTPBridgeRetryAdvice(t *testing.T) {
	for _, tc := range []struct{ name, nested, outer, header, path, want string }{
		{"http", "", "", "12", "headers.retry-after", "12"},
		{"nested_priority", "5", "10", "12", "error.headers.retry-after", "5"},
		{"outer_priority", "invalid", "10", "12", "headers.retry-after", "10"},
		{"invalid_control", "\n5\n", "", "12", "headers.retry-after", "12"},
		{"tab_valid", "\t5\t", "", "12", "error.headers.retry-after", "\t5\t"},
		{"invalid_http", "", "", "\n12", "headers.retry-after", ""},
		{"http_date", "", "", "Wed, 28 Oct 2026 12:00:00 GMT", "headers.retry-after", "Wed, 28 Oct 2026 12:00:00 GMT"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := []byte(`{"error":{"headers":{}},"headers":{}}`)
			if tc.nested != "" {
				source, _ = sjson.SetBytes(source, "error.headers.Retry-After", tc.nested)
			}
			if tc.outer != "" {
				source, _ = sjson.SetBytes(source, "headers.Retry-After", tc.outer)
			}
			headers := http.Header{"Retry-After": {tc.header}}
			payload := openAIWSBridgeRetryAdvice(buildOpenAIWSHTTPBridgeErrorEvent(429, "busy", headers), source, headers)
			require.Equal(t, tc.want, gjson.GetBytes(payload, tc.path).String())
			require.Equal(t, int64(429), gjson.GetBytes(payload, "status").Int())
		})
	}
}

func TestOpenAIWSHTTPBridgeRetryAdviceJSONHeaderValues(t *testing.T) {
	for _, tc := range []struct{ source, path, want string }{
		{`{"error":{"headers":{"retry-after":5}}}`, "error.headers.retry-after", "5"},
		{`{"error":{"headers":{"retry-after":"30","Retry-After":"5"}}}`, "error.headers.retry-after", "30"},
		{`{"error":{"headers":{"retry-after":"invalid","Retry-After":"5"}},"headers":{"retry-after":"12"}}`, "headers.retry-after", "12"},
		{`{"error":{"headers":"invalid"},"headers":{"retry-after":"12"}}`, "headers.retry-after", "12"},
	} {
		payload := openAIWSBridgeRetryAdvice(buildOpenAIWSHTTPBridgeErrorEvent(429, "busy"), []byte(tc.source), nil)
		require.Equal(t, tc.want, gjson.GetBytes(payload, tc.path).String())
	}
}
