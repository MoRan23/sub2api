package handler

import (
	"net/http"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestClassifySelectionFailureOSAuthorization(t *testing.T) {
	fallback := noAccountErrorClassification{Status: http.StatusServiceUnavailable, ErrType: "api_error", Message: "Service temporarily unavailable"}
	got := classifySelectionFailureError(service.ErrNoAvailableOpenAIOAuthOSAccounts, fallback)
	require.Equal(t, http.StatusServiceUnavailable, got.Status)
	require.Equal(t, "No available accounts have usable OpenAI OAuth authorization.", got.Message)
	require.NotContains(t, got.Message, "operating system")
	fallback = noAccountErrorClassification{Status: http.StatusNotFound, ErrType: "model_not_found", Message: "unknown model", ModelNotFound: true}
	require.Equal(t, fallback, classifySelectionFailureError(service.ErrNoAvailableOpenAIOAuthOSAccounts, fallback))
}
