package handler

import (
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestClassifySelectionFailureDoesNotInventAuthorizationGate(t *testing.T) {
	fallback := noAccountErrorClassification{Status: http.StatusServiceUnavailable, ErrType: "api_error", Message: "Service temporarily unavailable"}
	legacyError := errors.New("no available accounts for requested OS authorization")
	got := classifySelectionFailureError(legacyError, fallback)
	require.Equal(t, fallback, got)
	fallback = noAccountErrorClassification{Status: http.StatusNotFound, ErrType: "model_not_found", Message: "unknown model", ModelNotFound: true}
	require.Equal(t, fallback, classifySelectionFailureError(legacyError, fallback))
}
