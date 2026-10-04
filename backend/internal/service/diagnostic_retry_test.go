package service

import (
	"context"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestDiagnosticRetriesTransientErrorsOnly(t *testing.T) {
	for _, code := range []string{"upstream_failed", "upstream_http_503", "upstream_timeout", "missing_terminal"} {
		t.Run(code, func(t *testing.T) {
			calls := 0
			execution, err := retryDiagnostic(context.Background(), 0, func(context.Context) (*CandyTestExecution, error) {
				calls++
				e := &CandyTestExecution{Usage: &OpenAIUsage{InputTokens: 2, OutputTokens: 3, CacheReadInputTokens: 1}}
				if calls < 3 {
					return e, candyTestError(code)
				}
				e.Completed, e.ResponseText = true, "final answer"
				return e, nil
			})
			require.NoError(t, err)
			require.Equal(t, 3, calls)
			require.Equal(t, 2, execution.Retries)
			require.True(t, execution.Completed)
			require.Equal(t, "final answer", execution.ResponseText)
			require.Equal(t, &OpenAIUsage{InputTokens: 6, OutputTokens: 9, CacheReadInputTokens: 3}, execution.Usage)
		})
	}
	for _, code := range []string{"account_rate_limited", "upstream_http_429", "authorization_changed", "configuration_changed", "upstream_http_401", "upstream_http_403", "upstream_http_400", "response_too_large", "missing_html", "ambiguous_html", "cancelled", "timeout"} {
		t.Run(code, func(t *testing.T) {
			calls := 0
			e, err := retryDiagnostic(context.Background(), 0, func(context.Context) (*CandyTestExecution, error) {
				calls++
				return nil, candyTestError(code)
			})
			require.EqualError(t, err, code)
			require.Equal(t, 1, calls)
			require.Zero(t, e.Retries)
		})
	}
}

func TestDiagnosticRetryBoundCancellationAndConfigurationFence(t *testing.T) {
	for _, scenario := range []string{"exhausted", "cancelled", "changed", "deadline"} {
		t.Run(scenario, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			ctx = context.WithValue(ctx, diagnosticRetryValidationKey{}, func(context.Context) error {
				if scenario == "changed" && calls > 0 {
					return candyTestError("configuration_or_authorization_changed")
				}
				return nil
			})
			delay := time.Duration(0)
			if scenario == "deadline" {
				var release context.CancelFunc
				ctx, release = context.WithTimeout(ctx, 10*time.Millisecond)
				defer release()
				delay = time.Hour
			}
			e, err := retryDiagnostic(ctx, delay, func(context.Context) (*CandyTestExecution, error) {
				calls++
				if scenario == "cancelled" {
					cancel()
				}
				return nil, candyTestError("upstream_failed")
			})
			switch scenario {
			case "exhausted":
				require.Equal(t, 3, calls)
				require.Equal(t, 2, e.Retries)
				require.EqualError(t, err, "upstream_failed")
			case "changed":
				require.Equal(t, 1, calls)
				require.EqualError(t, err, "configuration_or_authorization_changed")
			case "cancelled":
				require.Equal(t, 1, calls)
				require.EqualError(t, err, "cancelled")
			case "deadline":
				require.Equal(t, 1, calls)
				require.EqualError(t, err, "timeout")
			}
		})
	}
}

func TestDiagnosticErrorClassificationIsSafe(t *testing.T) {
	for _, tc := range []struct {
		err  error
		code string
	}{
		{&net.OpError{Op: "dial", Err: errors.New("secret proxy credentials")}, "upstream_connection_failed"},
		{context.DeadlineExceeded, "upstream_timeout"},
		{io.ErrUnexpectedEOF, "upstream_stream_interrupted"},
		{&UpstreamFailoverError{StatusCode: 504, ResponseBody: []byte(`{"error":{"type":"first_output_timeout","message":"secret"}}`)}, "upstream_first_output_timeout"},
		{&UpstreamFailoverError{StatusCode: 503}, "upstream_http_503"},
		{errors.New("secret provider body"), "upstream_failed"},
	} {
		require.EqualError(t, safeCandyTestError(context.Background(), tc.err), tc.code)
	}
}
