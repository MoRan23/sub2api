package service

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOpenAIWSSessionHeaderValueForLogPrefersCanonicalWireHeader(t *testing.T) {
	headers := http.Header{
		"Session-Id": []string{"canonical-session"},
		"Session_id": []string{"legacy-session"},
	}

	require.Equal(t, "canonical-session", openAIWSSessionHeaderValueForLog(headers, ""))
	require.True(t, hasOpenAIWSSessionHeader(headers))
}

func TestOpenAIWSSessionHeaderValueForLogFallsBackToLegacyWireHeader(t *testing.T) {
	headers := http.Header{"Session_id": []string{"legacy-session"}}

	require.Equal(t, "legacy-session", openAIWSSessionHeaderValueForLog(headers, ""))
	require.True(t, hasOpenAIWSSessionHeader(headers))
}
