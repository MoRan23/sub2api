package service

import (
	"errors"
	"strings"
	"time"
)

var ErrDiagnosticRateLimited = candyTestError("account_rate_limited")

// Diagnostic probes use the actual upstream model, bypassing model mappings.
// Expired limits and limits for other models must not prevent a probe.
func AccountDiagnosticRateLimited(a *Account, model string, now time.Time) bool {
	if a == nil {
		return false
	}
	if a.RateLimitResetAt != nil && now.Before(*a.RateLimitResetAt) {
		return true
	}
	reset := a.modelRateLimitResetAt(strings.TrimSpace(model))
	return reset != nil && now.Before(*reset)
}

func diagnosticRateLimitError(err error) bool {
	if errors.Is(err, ErrDiagnosticRateLimited) {
		return true
	}
	var failure CandyTestFailure
	return errors.As(err, &failure) && failure.CandyTestFailureCode() == "upstream_http_429"
}
