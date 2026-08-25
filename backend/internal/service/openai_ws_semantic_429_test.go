package service

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type openAIWSSemantic429Repo struct {
	AccountRepository
	resetTimes []time.Time
}

func (r *openAIWSSemantic429Repo) SetRateLimited(_ context.Context, _ int64, resetAt time.Time) error {
	r.resetTimes = append(r.resetTimes, resetAt)
	return nil
}

func (r *openAIWSSemantic429Repo) UpdateExtra(context.Context, int64, map[string]any) error {
	return nil
}

func newOpenAIWSSemantic429Service(repo AccountRepository) *OpenAIGatewayService {
	rateLimits := NewRateLimitService(repo, nil, nil, nil, nil)
	svc := &OpenAIGatewayService{rateLimitService: rateLimits}
	rateLimits.SetAccountRuntimeBlocker(svc)
	return svc
}

func successfulOpenAIWSQuotaHeaders() http.Header {
	return http.Header{
		"X-Codex-Primary-Used-Percent":        []string{"37"},
		"X-Codex-Primary-Reset-After-Seconds": []string{"604800"},
		"X-Codex-Primary-Window-Minutes":      []string{"10080"},
	}
}

func TestOpenAIWSSemantic429IgnoresSuccessfulHandshakeQuotaHeaders(t *testing.T) {
	repo := &openAIWSSemantic429Repo{}
	svc := newOpenAIWSSemantic429Service(repo)
	account := &Account{ID: 611, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	headers := successfulOpenAIWSQuotaHeaders()
	body := []byte(`{"type":"error","error":{"type":"rate_limit_error","code":"rate_limit_exceeded","message":"try again"}}`)

	svc.persistOpenAIWSSemanticRateLimitSignal(context.Background(), account, body, "rate_limit_exceeded", "rate_limit_error", "try again")
	failoverErr := svc.newOpenAIWSSemanticRateLimitFailoverError(account, headers, body, "try again")

	require.Empty(t, repo.resetTimes)
	require.False(t, svc.isOpenAIAccountRuntimeBlocked(account))
	require.True(t, failoverErr.RetryableOnSameAccount)
	require.Equal(t, "604800", failoverErr.ResponseHeaders.Get("X-Codex-Primary-Reset-After-Seconds"), "handshake headers remain available for diagnostics and response propagation")
}

func TestOpenAIWSDial429StillClassifiesErrorResponseHeaders(t *testing.T) {
	repo := &openAIWSSemantic429Repo{}
	svc := newOpenAIWSSemantic429Service(repo)
	account := &Account{ID: 612, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	headers := successfulOpenAIWSQuotaHeaders()
	body := []byte(`{"error":{"type":"rate_limit_error","code":"rate_limit_exceeded"}}`)

	svc.persistOpenAIWSRateLimitSignal(context.Background(), account, headers, body, "rate_limit_exceeded", "rate_limit_error", "rate limited")
	failoverErr := svc.newOpenAIWSRateLimitFailoverError(account, headers, body, "rate limited")

	require.Len(t, repo.resetTimes, 1)
	require.Greater(t, time.Until(repo.resetTimes[0]), 6*24*time.Hour)
	require.True(t, svc.isOpenAIAccountRuntimeBlocked(account))
	require.False(t, failoverErr.RetryableOnSameAccount)
	require.Equal(t, "604800", failoverErr.ResponseHeaders.Get("X-Codex-Primary-Reset-After-Seconds"))
}

func TestOpenAIWSSemantic429UsesNestedQuotaBody(t *testing.T) {
	repo := &openAIWSSemantic429Repo{}
	svc := newOpenAIWSSemantic429Service(repo)
	account := &Account{ID: 613, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	resetAt := time.Now().Add(2 * time.Hour).UTC().Truncate(time.Second)
	body := []byte(fmt.Sprintf(`{"type":"response.failed","response":{"error":{"type":"usage_limit_reached","code":"usage_limit_reached","resets_at":%d}}}`, resetAt.Unix()))

	svc.persistOpenAIWSSemanticRateLimitSignal(context.Background(), account, body, "usage_limit_reached", "usage_limit_reached", "quota exhausted")
	failoverErr := svc.newOpenAIWSSemanticRateLimitFailoverError(account, successfulOpenAIWSQuotaHeaders(), body, "quota exhausted")

	require.Len(t, repo.resetTimes, 1)
	require.WithinDuration(t, resetAt, repo.resetTimes[0], time.Second)
	require.True(t, svc.isOpenAIAccountRuntimeBlocked(account))
	require.False(t, failoverErr.RetryableOnSameAccount)
	require.Equal(t, "604800", failoverErr.ResponseHeaders.Get("X-Codex-Primary-Reset-After-Seconds"))
}

func TestOpenAIWSTerminalResponseFailedQuotaWithoutResetIs429(t *testing.T) {
	tests := []struct {
		name   string
		field  string
		signal string
	}{
		{name: "usage limit type", field: "type", signal: "usage_limit_reached"},
		{name: "insufficient quota code", field: "code", signal: "insufficient_quota"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := &openAIWSSemantic429Repo{}
			svc := newOpenAIWSSemantic429Service(repo)
			account := &Account{ID: 620, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
			body := []byte(fmt.Sprintf(`{"type":"response.failed","response":{"error":{%q:%q,"message":"quota exhausted"}}}`, tt.field, tt.signal))

			terminal := svc.handleOpenAIWSTerminalTransientFailure(
				context.Background(),
				account,
				"gpt-5.6-sol",
				successfulOpenAIWSQuotaHeaders(),
				body,
			)

			require.Equal(t, "response.failed", terminal)
			require.True(t, svc.isOpenAIAccountRuntimeBlocked(account))
			require.Len(t, repo.resetTimes, 1)
			require.Less(t, time.Until(repo.resetTimes[0]), time.Hour,
				"the HTTP 101 handshake quota snapshot must not classify a WS terminal event")
		})
	}
}
