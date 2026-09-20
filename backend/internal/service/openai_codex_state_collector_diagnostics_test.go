package service

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"strings"
	"syscall"
	"testing"
	"time"

	utls "github.com/refraction-networking/utls"
	"github.com/stretchr/testify/require"
)

func TestCodexTurnStateCollectorTransportDiagnostics(t *testing.T) {
	const private = "secret-user:secret-password@private-proxy.invalid"
	for _, tc := range []struct {
		name, stage, reason string
		err                 error
	}{
		{name: "dns", err: &net.DNSError{Name: private, Err: private}, reason: "collector_dns_failed"},
		{name: "refused", err: &net.OpError{Op: "dial", Err: syscall.ECONNREFUSED}, reason: "collector_connection_refused"},
		{name: "reset", err: syscall.ECONNRESET, reason: "collector_connection_closed"},
		{name: "broken_pipe", err: syscall.EPIPE, reason: "collector_connection_closed"},
		{name: "eof", err: io.EOF, reason: "collector_connection_closed"},
		{name: "unexpected_eof", err: io.ErrUnexpectedEOF, reason: "collector_connection_closed"},
		{name: "certificate", err: x509.UnknownAuthorityError{}, reason: "collector_tls_failed"},
		{name: "native_record", err: utls.RecordHeaderError{Msg: private}, reason: "collector_tls_failed"},
		{name: "standard_record", err: tls.RecordHeaderError{Msg: private}, reason: "collector_tls_failed"},
		{name: "native_certificate", err: &utls.CertificateVerificationError{Err: errors.New(private)}, reason: "collector_tls_failed"},
		{name: "tcp_tls_alert", err: &net.OpError{Op: "remote error", Err: errors.New("tls: handshake failure")}, reason: "collector_tls_failed"},
		{name: "actual_handshake", stage: "tls", err: errors.New(private), reason: "collector_tls_failed"},
		{name: "socks_password", err: &net.OpError{Op: "socks connect", Err: errors.New("username/password authentication failed")}, reason: "collector_proxy_auth_required"},
		{name: "socks_methods", err: &net.OpError{Op: "socks connect", Err: errors.New("no acceptable authentication methods")}, reason: "collector_proxy_auth_required"},
		{name: "socks_protocol", err: &net.OpError{Op: "socks connect", Err: errors.New("invalid username/password version")}, reason: "collector_proxy_auth_required"},
		{name: "socks_reject", err: &net.OpError{Op: "socks connect", Err: errors.New("unknown error connection not allowed by ruleset")}, reason: "collector_proxy_tunnel_failed"},
		{name: "connect_auth", err: errors.New("Proxy Authentication Required"), reason: "collector_proxy_auth_required"},
		{name: "connect_reject_with_stage", stage: "proxy_tunnel", err: errors.New("Forbidden"), reason: "collector_proxy_tunnel_failed"},
		{name: "ambiguous_forbidden", err: errors.New("Forbidden"), reason: "collector_transport_failed"},
		{name: "do_not_search_messages", err: errors.New("Proxy Authentication Required: " + private), reason: "collector_transport_failed"},
		{name: "do_not_search_wrappers", err: fmt.Errorf("Proxy Authentication Required: %w", errors.New(private)), reason: "collector_transport_failed"},
		{name: "generic_timeout", err: context.DeadlineExceeded, reason: "collection_timeout"},
		{name: "dial_timeout", err: &net.OpError{Op: "dial", Err: context.DeadlineExceeded}, reason: "collector_connect_timeout"},
		{name: "trace_dns_timeout", stage: "dns", err: context.DeadlineExceeded, reason: "collector_connect_timeout"},
		{name: "trace_tls_timeout", stage: "tls", err: context.DeadlineExceeded, reason: "collector_tls_timeout"},
		{name: "trace_header_timeout", stage: "response_headers", err: context.DeadlineExceeded, reason: "collector_response_header_timeout"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cause := &url.Error{Op: "POST", URL: "https://" + private, Err: tc.err}
			failure := newCodexTurnStateCollectorTransportError(cause, tc.stage)
			require.Equal(t, tc.reason, failure.Error())
			require.ErrorIs(t, failure, errCodexTurnStateCollectorTransportFailed)
			require.ErrorIs(t, failure, tc.err, "original identity remains available internally")
			require.Equal(t, tc.reason, codexTurnStateCollectorFailureReason(CodexTurnStateCollectResult{}, failure))
			for _, status := range []int{401, 403, 429} {
				want := "collector_auth_rejected"
				if status == 429 {
					want = "collector_rate_limited"
				}
				require.Equal(t, want, codexTurnStateCollectorFailureReason(CodexTurnStateCollectResult{StatusCode: status}, failure))
			}
		})
	}
}

func TestCodexTurnStateCollectorTraceDiagnosticsAndSafeLog(t *testing.T) {
	account, _ := codexCollectorTransportFixture()
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	const private = "private-proxy-password-and-response-body"
	for _, tc := range []struct {
		name, stage, reason string
		advance             func(*httptrace.ClientTrace)
	}{
		{"dial", "connect", "collector_connect_timeout", func(trace *httptrace.ClientTrace) { trace.ConnectStart("tcp", private) }},
		{"tls", "tls", "collector_tls_timeout", func(trace *httptrace.ClientTrace) { trace.TLSHandshakeStart() }},
		{"headers", "response_headers", "collector_response_header_timeout", func(trace *httptrace.ClientTrace) { trace.WroteRequest(httptrace.WroteRequestInfo{}) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			output.Reset()
			collector := NewCodexTurnStateHTTPCollector(func(ctx context.Context, input CodexTurnStateCollectRequest, _ *http.Request) (*http.Response, error) {
				input.onSend(time.Now())
				tc.advance(httptrace.ContextClientTrace(ctx))
				return nil, &url.Error{Op: "POST", URL: "https://" + private, Err: context.DeadlineExceeded}
			})
			result, err := collector.Collect(context.Background(), CodexTurnStateCollectRequest{Account: account, ProxyID: 2, Model: "gpt-5.6-sol"})
			require.ErrorIs(t, err, context.DeadlineExceeded)
			require.Equal(t, tc.reason, err.Error())
			require.Empty(t, result.Tokens)
			var entry map[string]any
			require.NoError(t, json.Unmarshal(output.Bytes(), &entry))
			require.Equal(t, tc.reason, entry["reason"])
			require.Equal(t, tc.stage, entry["stage"])
			require.Equal(t, float64(2), entry["proxy_id"])
			require.NotContains(t, output.String(), private)
			require.NotContains(t, output.String(), account.GetCredential("access_token"))
			require.NotContains(t, output.String(), "https://")
		})
	}
}

func TestCodexTurnStateCollectorSendBoundaryEvidence(t *testing.T) {
	account, _ := codexCollectorTransportFixture()
	for _, sent := range []bool{false, true} {
		collector := NewCodexTurnStateHTTPCollector(func(_ context.Context, input CodexTurnStateCollectRequest, _ *http.Request) (*http.Response, error) {
			if sent {
				input.onSend(time.Now())
			}
			return nil, errors.New("private-error")
		})
		result, err := collector.Collect(context.Background(), CodexTurnStateCollectRequest{Account: account, ProxyID: 2, Model: "gpt-5.6-sol"})
		require.Error(t, err)
		require.NotEmpty(t, result.observationID)
		require.Equal(t, sent, !result.requestSentAt.IsZero())
	}
	collector := NewCodexTurnStateHTTPCollector(func(context.Context, CodexTurnStateCollectRequest, *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("data: {\"type\":\"response.completed\"}\n\n"))}, nil
	})
	result, err := collector.Collect(context.Background(), CodexTurnStateCollectRequest{Account: account, ProxyID: 2, Model: "gpt-5.6-sol"})
	require.NoError(t, err)
	require.False(t, result.requestSentAt.IsZero())
}

func TestCodexTurnStateCollectorDeadlinePreservesPersistedPhase(t *testing.T) {
	for _, tc := range []struct{ stage, reason string }{
		{"connect", "collector_connect_timeout"},
		{"tls", "collector_tls_timeout"},
		{"response_headers", "collector_response_header_timeout"},
	} {
		t.Run(tc.stage, func(t *testing.T) {
			s, repo, account := newCodexStateTestService(t)
			now := time.Now()
			s.now = func() time.Time { return now }
			attempt := seedCodexStateTestDemand(t, s, account, "gpt-5")
			s.collector = codexStateTestCollector(func(ctx context.Context, _ CodexTurnStateCollectRequest) (CodexTurnStateCollectResult, error) {
				<-ctx.Done()
				return CodexTurnStateCollectResult{}, newCodexTurnStateCollectorTransportError(ctx.Err(), tc.stage)
			})
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			s.collect(ctx, attempt.key)
			record, err := repo.Get(context.Background(), attempt.key)
			require.NoError(t, err)
			require.Equal(t, tc.reason, record.LastError)
			require.Equal(t, tc.reason, record.CollectionReason)
			require.Equal(t, now.Add(30*time.Second), record.NextCollectAt)
			require.False(t, record.CollectorPaused)
		})
	}
	limited := codexTurnStateCollectorDeadlineError(errCodexTurnStateCollectorRateLimited)
	require.ErrorIs(t, limited, errCodexTurnStateCollectorRateLimited)
	require.Equal(t, "collector_rate_limited", codexTurnStateCollectorFailureReason(CodexTurnStateCollectResult{StatusCode: 200}, limited))
	require.ErrorIs(t, codexTurnStateCollectorDeadlineError(errors.New("private-error")), context.DeadlineExceeded)
}
