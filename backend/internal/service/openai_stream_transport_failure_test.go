//go:build unit

package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestOpenAIStreamReadTransportClassification(t *testing.T) {
	for _, tc := range []struct {
		name  string
		cause error
		want  bool
	}{
		{name: "TCP EOF", cause: io.ErrUnexpectedEOF, want: true},
		{name: "HTTP2 reset", cause: errors.New("stream error: stream ID 11; INTERNAL_ERROR; received from peer"), want: true},
		{name: "timeout", cause: context.DeadlineExceeded, want: true},
		{name: "clean missing terminal", cause: ErrOpenAIUpstreamStreamTruncated},
		{name: "unknown failure", cause: errors.New("malformed stream")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			readErr := newOpenAIUpstreamStreamReadError(tc.cause)
			require.ErrorIs(t, readErr, tc.cause)
			require.Equal(t, tc.want, IsUpstreamTransportFailure(readErr))
			failover := newOpenAIRawStreamTruncatedFailoverError(nil, nil, "request-id", tc.cause)
			require.Equal(t, tc.want, failover.TransportFailure)
			require.Equal(t, !tc.want, failover.ShouldReportAccountScheduleFailure())
		})
	}
}

func TestOpenAIImagesReadFailureDoesNotApplySynthetic502Cooldown(t *testing.T) {
	for _, tc := range []struct {
		name       string
		cause      error
		wantWrites int
	}{
		{name: "network reset", cause: io.ErrUnexpectedEOF},
		{name: "HTTP2 reset", cause: errors.New("stream error: stream ID 11; INTERNAL_ERROR; received from peer")},
		{name: "integrity failure still applies rule", cause: ErrOpenAIUpstreamStreamTruncated, wantWrites: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := newImagesCooldownContext(t)
			account := imagesCooldownAccount()
			require.NoError(t, json.Unmarshal([]byte(`{"temp_unschedulable_enabled":true,"temp_unschedulable_rules":[{"error_code":502,"scope":"account","duration_minutes":30,"keywords":["stream"]}]}`), &account.Credentials))
			repo := &engineTempRuleRepo{account: account}
			rates := NewRateLimitService(repo, nil, &config.Config{}, nil, &openAIAPIKeyHealthCacheStub{})
			svc := &OpenAIGatewayService{rateLimitService: rates}
			rates.SetAccountRuntimeBlocker(svc)
			err := svc.handleOpenAIImagesOAuthResponseError(context.Background(), c, account, "gpt-image-2", "https://upstream.example/v1/responses",
				&http.Response{StatusCode: http.StatusOK, Header: http.Header{}}, OpenAIImagesJSONKeepaliveAdjustedWrittenSize(c), newOpenAIUpstreamStreamReadError(tc.cause))
			var failover *UpstreamFailoverError
			require.ErrorAs(t, err, &failover)
			require.Equal(t, tc.wantWrites == 0, failover.TransportFailure)
			require.Equal(t, tc.wantWrites, repo.accountCalls)
			require.Equal(t, tc.wantWrites != 0, svc.isOpenAIAccountRuntimeBlocked(account))
		})
	}
}

func TestOpenAIStreamTransportFailoverSkipsAccountSideEffects(t *testing.T) {
	c, _ := newImagesCooldownContext(t)
	account := imagesCooldownAccount()
	require.NoError(t, json.Unmarshal([]byte(`{"temp_unschedulable_enabled":true,"temp_unschedulable_rules":[{"error_code":502,"scope":"account","duration_minutes":30,"keywords":["stream"]}]}`), &account.Credentials))
	repo := &engineTempRuleRepo{account: account}
	rates := NewRateLimitService(repo, nil, &config.Config{}, nil, &openAIAPIKeyHealthCacheStub{})
	svc := &OpenAIGatewayService{rateLimitService: rates}
	rates.SetAccountRuntimeBlocker(svc)
	headers := http.Header{"X-Request-Id": {"request-id"}}
	err := svc.newOpenAIStreamTransportFailoverError(c, account, true, "request-id", "stream read failed", headers)
	require.True(t, err.TransportFailure)
	require.False(t, err.ShouldReportAccountScheduleFailure())
	require.Equal(t, http.StatusBadGateway, err.StatusCode)
	require.Zero(t, repo.accountCalls)
	require.False(t, svc.isOpenAIAccountRuntimeBlocked(account))
	headers.Set("X-Request-Id", "changed")
	require.Equal(t, "request-id", err.ResponseHeaders.Get("X-Request-Id"))
}

func TestOpenAIWSTransportFailurePreservesHTTPHandshakeEvidence(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		cause  error
		want   bool
	}{
		{name: "dial timeout", cause: context.DeadlineExceeded, want: true},
		{name: "dial EOF", cause: io.ErrUnexpectedEOF, want: true},
		{name: "real HTTP 502", status: http.StatusBadGateway, cause: io.ErrUnexpectedEOF},
		{name: "real HTTP 429", status: http.StatusTooManyRequests, cause: context.DeadlineExceeded},
		{name: "unknown dial error", cause: errors.New("handshake rejected")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dialErr := &openAIWSDialError{StatusCode: tc.status, Err: tc.cause}
			mapped := (&OpenAIGatewayService{}).mapOpenAIWSPassthroughDialError(dialErr, tc.status, nil)
			require.Equal(t, tc.want, IsOpenAIWSTransportFailure(mapped))
			require.Equal(t, tc.want, IsUpstreamTransportFailure(mapped))
			fallback := wrapOpenAIWSFallback("dial_failed", dialErr)
			require.Equal(t, tc.want, IsUpstreamTransportFailure(fallback))
			require.ErrorIs(t, fallback, tc.cause)
		})
	}
	turnErr := wrapOpenAIWSIngressTurnError("read_upstream", io.ErrUnexpectedEOF, true)
	require.True(t, IsUpstreamTransportFailure(turnErr))
	require.False(t, isOpenAIWSIngressTurnRetryable(turnErr), "downstream output still prevents replay")
	require.True(t, IsOpenAIWSCyberPolicyError(fmt.Errorf("wrapped: %w", errOpenAIWSCyberPolicyStop)))
	require.False(t, IsOpenAIWSTransportFailure(errOpenAIWSCyberPolicyStop))
}
