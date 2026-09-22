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
	require.Contains(t, got.Message, "authorized for the requested operating system")
	fallback = noAccountErrorClassification{Status: http.StatusNotFound, ErrType: "model_not_found", Message: "unknown model", ModelNotFound: true}
	require.Equal(t, fallback, classifySelectionFailureError(service.ErrNoAvailableOpenAIOAuthOSAccounts, fallback))
}
