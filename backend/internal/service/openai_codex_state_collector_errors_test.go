package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type codexStateDiagnosticTimeout struct{}

func (codexStateDiagnosticTimeout) Error() string   { return "private-network-timeout" }
func (codexStateDiagnosticTimeout) Timeout() bool   { return true }
func (codexStateDiagnosticTimeout) Temporary() bool { return true }

type codexStateDiagnosticReadError struct{ err error }

func (r codexStateDiagnosticReadError) Read([]byte) (int, error) { return 0, r.err }

func TestCodexTurnStateCollectorSafeFailureCategoriesReachStatus(t *testing.T) {
	const privateDetail = "private-proxy-password-and-response-body"
	completed := "data: {\"type\":\"response.completed\"}\n\n"
	for _, tc := range []struct {
		name, reason string
		status       int
		stream       string
		transportErr error
		readErr      error
		nilResponse  bool
		nilBody      bool
	}{
		{name: "transport", reason: "collector_transport_failed", transportErr: errors.New(privateDetail)},
		{name: "transport_timeout", reason: "collection_timeout", transportErr: &url.Error{Op: "POST", URL: "https://" + privateDetail, Err: codexStateDiagnosticTimeout{}}},
		{name: "transport_deadline", reason: "collection_timeout", transportErr: fmt.Errorf("%s: %w", privateDetail, context.DeadlineExceeded)},
		{name: "proxy_unavailable", reason: "collector_proxy_unavailable", transportErr: fmt.Errorf("%s: %w", privateDetail, ErrCodexTurnStateCollectorProxyUnavailable)},
		{name: "nil_response", reason: "collector_empty_response", nilResponse: true},
		{name: "nil_body", reason: "collector_empty_response", status: 200, nilBody: true},
		{name: "stream_read_failure", reason: "collector_stream_failed", status: 200, readErr: errors.New(privateDetail)},
		{name: "stream_timeout", reason: "collection_timeout", status: 200, readErr: codexStateDiagnosticTimeout{}},
		{name: "stream_eof", reason: "collector_response_incomplete", status: 200},
		{name: "malformed_stream", reason: "collector_response_incomplete", status: 200, stream: "data: " + privateDetail + "\n\n"},
		{name: "upstream_failed", reason: "collector_response_failed", status: 200, stream: "data: {\"type\":\"response.failed\",\"error\":{\"message\":\"" + privateDetail + "\"}}\n\n" + completed},
		{name: "upstream_incomplete", reason: "collector_response_incomplete", status: 200, stream: "data: {\"type\":\"response.incomplete\",\"error\":{\"message\":\"" + privateDetail + "\"}}\n\n" + completed},
		{name: "sse_limit", reason: "collector_rate_limited", status: 200, stream: "data: {\"type\":\"error\",\"code\":\"rate_limit_exceeded\",\"message\":\"" + privateDetail + "\"}\n\n" + completed},
		{name: "oversize_line", reason: "collector_event_too_large", status: 200, stream: "data: " + strings.Repeat("x", (256<<10)+1) + "\n\n" + completed},
		{name: "oversize_multiline_event", reason: "collector_event_too_large", status: 200, stream: strings.Repeat("data: "+strings.Repeat("x", 100<<10)+"\n", 3) + "\n" + completed},
		{name: "http_server_failure", reason: "collector_upstream_unavailable", status: 503, stream: privateDetail},
		{name: "http_server_failure_without_body", reason: "collector_upstream_unavailable", status: 502, nilBody: true},
		{name: "http_bad_request", reason: "collector_http_rejected", status: 400, stream: privateDetail},
		{name: "http_redirect", reason: "collector_http_rejected", status: 302, stream: privateDetail},
		{name: "http_proxy_auth", reason: "collector_proxy_auth_required", status: 407, stream: privateDetail},
		{name: "http_unauthorized", reason: "collector_auth_rejected", status: 401, stream: privateDetail},
		{name: "http_forbidden", reason: "collector_auth_rejected", status: 403, stream: privateDetail},
		{name: "http_rate_limit", reason: "collector_rate_limited", status: 429, stream: privateDetail},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolateCodexTurnStateSummaryStore(t)
			s, repo, account := newCodexStateTestService(t)
			now := time.Now().UTC().Truncate(time.Second)
			s.now = func() time.Time { return now }
			attempt := seedCodexStateTestDemand(t, s, account, "gpt-5")
			before, err := repo.Get(context.Background(), attempt.key)
			require.NoError(t, err)
			target := codexStateTestToken(10, now)
			collector := NewCodexTurnStateHTTPCollector(func(_ context.Context, request CodexTurnStateCollectRequest, _ *http.Request) (*http.Response, error) {
				require.EqualValues(t, 2, request.ProxyID)
				if tc.transportErr != nil {
					return nil, tc.transportErr
				}
				if tc.nilResponse {
					return nil, nil
				}
				response := &http.Response{StatusCode: tc.status, Header: http.Header{"X-Codex-Turn-State": {target}}}
				if !tc.nilBody {
					var body io.Reader = strings.NewReader(tc.stream)
					if tc.readErr != nil {
						body = io.MultiReader(body, codexStateDiagnosticReadError{tc.readErr})
					}
					response.Body = io.NopCloser(body)
				}
				return response, nil
			})
			var result CodexTurnStateCollectResult
			var collectErr error
			s.collector = codexStateTestCollector(func(ctx context.Context, request CodexTurnStateCollectRequest) (CodexTurnStateCollectResult, error) {
				result, collectErr = collector.Collect(ctx, request)
				return result, collectErr
			})
			s.collect(context.Background(), attempt.key)
			require.Equal(t, tc.status, result.StatusCode, "failure classification retains the actual HTTP status")
			require.Empty(t, result.Tokens, "failure cannot publish a valid header candidate")
			if collectErr != nil {
				require.NotContains(t, collectErr.Error(), privateDetail)
				require.NotContains(t, collectErr.Error(), "private-network-timeout")
			}
			after, err := repo.Get(context.Background(), attempt.key)
			require.NoError(t, err)
			require.Empty(t, after.EncryptedToken)
			require.Equal(t, tc.reason, after.LastError)
			require.Equal(t, tc.reason, after.CollectionReason)
			require.Equal(t, before.DemandReason, after.DemandReason)
			require.Equal(t, before.LastBusinessAt, after.LastBusinessAt)
			require.Equal(t, now.Add(30*time.Second), after.NextCollectAt)
			require.Equal(t, tc.reason == "collector_auth_rejected", after.CollectorPaused)
			status, err := s.GetStatus(context.Background(), account.ID)
			require.NoError(t, err)
			require.Len(t, status.Models, 1)
			require.Equal(t, tc.reason, status.Models[0].LastError)
			require.Equal(t, tc.reason, status.Models[0].CollectionReason)
			payload, err := json.Marshal(status)
			require.NoError(t, err)
			for _, private := range []string{privateDetail, "private-network-timeout", target, account.GetCredential("access_token")} {
				require.NotContains(t, string(payload), private)
			}
		})
	}
}

func TestCodexTurnStateCollectorFailureClassificationTrustsIdentityNotText(t *testing.T) {
	for _, category := range []struct {
		err  error
		code string
	}{
		{errCodexTurnStateCollectorTransportFailed, "collector_transport_failed"},
		{errCodexTurnStateCollectorEmptyResponse, "collector_empty_response"},
		{errCodexTurnStateCollectorStreamFailed, "collector_stream_failed"},
		{errCodexTurnStateCollectorResponseFailed, "collector_response_failed"},
		{errCodexTurnStateCollectorResponseIncomplete, "collector_response_incomplete"},
		{errCodexTurnStateCollectorEventTooLarge, "collector_event_too_large"},
		{errCodexTurnStateCollectorRateLimited, "collector_rate_limited"},
		{ErrCodexTurnStateCollectorProxyUnavailable, "collector_proxy_unavailable"},
	} {
		t.Run(category.code, func(t *testing.T) {
			for _, trusted := range []bool{false, true} {
				s, repo, account := newCodexStateTestService(t)
				attempt := seedCodexStateTestDemand(t, s, account, "gpt-5")
				failure, want := errors.New(category.code), "collection_failed"
				if trusted {
					failure, want = fmt.Errorf("private-wrapper: %w", category.err), category.code
				}
				s.collector = codexStateTestCollector(func(context.Context, CodexTurnStateCollectRequest) (CodexTurnStateCollectResult, error) {
					return CodexTurnStateCollectResult{StatusCode: http.StatusOK}, failure
				})
				s.collect(context.Background(), attempt.key)
				record, err := repo.Get(context.Background(), attempt.key)
				require.NoError(t, err)
				require.Equal(t, want, record.LastError)
				require.Equal(t, want, record.CollectionReason)
			}
		})
	}
}

func TestCodexTurnStateStatusExplainsUnavailableAccount(t *testing.T) {
	for _, tc := range []struct {
		name, reason string
		mutate       func(*Account, time.Time)
	}{
		{"inactive", "account_inactive", func(account *Account, _ time.Time) { account.Status = "error" }},
		{"scheduling_disabled", "account_scheduling_disabled", func(account *Account, _ time.Time) { account.Schedulable = false }},
		{"expired", "account_expired", func(account *Account, now time.Time) { account.ExpiresAt = &now }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _, account := newCodexStateTestService(t)
			now := s.now()
			tc.mutate(account, now)
			record := CodexTurnStateRecord{OwnerAccountID: account.ID, Model: "gpt-5", Generation: "gen1", LastBusinessAt: now, DemandReason: "extended_shape"}
			status := projectCodexTurnStateStatus(account.ID, account, []CodexTurnStateRecord{record}, []string{"gpt-5"}, nil, now)
			require.Equal(t, "blocked", status.Models[0].CollectionStatus)
			require.Equal(t, tc.reason, status.Models[0].CollectionReason)
		})
	}
}
