package service

import (
	"context"
	"errors"
	"time"
)

type diagnosticRetryValidationKey struct{}

// Retries stay within the original task deadline. Compatibility retries inside
// Forward remain disabled: each attempt permits exactly one upstream send.
func retryDiagnostic(ctx context.Context, delay time.Duration, run func(context.Context) (*CandyTestExecution, error)) (*CandyTestExecution, error) {
	started := time.Now()
	var usage *OpenAIUsage
	var result *CandyTestExecution
	var err error
	retries := 0
	defer func() {
		if result != nil {
			result.Usage = usage
			result.Retries = retries
			result.DurationMs = time.Since(started).Milliseconds()
		}
	}()
	for attempt := 0; attempt < 3; attempt++ {
		if ctx.Err() != nil {
			return result, safeCandyTestError(ctx, ctx.Err())
		}
		if attempt > 0 {
			timer := time.NewTimer(time.Duration(attempt) * delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return result, safeCandyTestError(ctx, ctx.Err())
			case <-timer.C:
			}
		}
		if validate, ok := ctx.Value(diagnosticRetryValidationKey{}).(func(context.Context) error); ok {
			if err := validate(ctx); err != nil {
				return result, err
			}
		}
		if attempt > 0 {
			retries++
		}
		result, err = run(ctx)
		if result == nil {
			result = &CandyTestExecution{}
		}
		if u := result.Usage; u != nil {
			if usage == nil {
				usage = &OpenAIUsage{}
			}
			usage.InputTokens += u.InputTokens
			usage.OutputTokens += u.OutputTokens
			usage.ImageInputTokens += u.ImageInputTokens
			usage.ImageOutputTokens += u.ImageOutputTokens
			usage.ImageCacheReadTokens += u.ImageCacheReadTokens
			usage.CacheCreationInputTokens += u.CacheCreationInputTokens
			usage.CacheReadInputTokens += u.CacheReadInputTokens
		}
		if !retryableDiagnosticError(err) {
			break
		}
	}
	return result, err
}

func retryableDiagnosticError(err error) bool {
	var failure CandyTestFailure
	if !errors.As(err, &failure) {
		return false
	}
	switch failure.CandyTestFailureCode() {
	case "upstream_failed", "upstream_timeout", "upstream_connection_failed", "upstream_stream_interrupted", "upstream_first_output_timeout",
		"missing_terminal", "incomplete_response", "upstream_stream_failed", "upstream_incomplete", "upstream_http_408",
		"upstream_http_500", "upstream_http_502", "upstream_http_503", "upstream_http_504":
		return true
	default:
		return false
	}
}
