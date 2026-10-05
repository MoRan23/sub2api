package service

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestDiagnosticRateLimitUsesDirectModelAndExpiry(t *testing.T) {
	now := time.Now()
	future, past := now.Add(time.Hour), now.Add(-time.Second)
	a := &Account{Platform: PlatformOpenAI, Credentials: map[string]any{"model_mapping": map[string]any{"probe": "alias"}}}
	require.False(t, AccountDiagnosticRateLimited(nil, "probe", now))
	a.RateLimitResetAt = &future
	require.True(t, AccountDiagnosticRateLimited(a, "probe", now))
	a.RateLimitResetAt = &past
	require.False(t, AccountDiagnosticRateLimited(a, "probe", now))
	a.Extra = map[string]any{modelRateLimitsKey: map[string]any{"alias": map[string]any{"rate_limit_reset_at": future.Format(time.RFC3339)}}}
	require.False(t, AccountDiagnosticRateLimited(a, "probe", now), "tests bypass aliases")
	a.Extra[modelRateLimitsKey].(map[string]any)["probe"] = map[string]any{"rate_limit_reset_at": future.Format(time.RFC3339)}
	require.True(t, AccountDiagnosticRateLimited(a, "probe", now))
	require.False(t, AccountDiagnosticRateLimited(a, "other", now))
	require.False(t, AccountDiagnosticRateLimited(a, "probe", future))
}

func TestCandyRateLimitedOptionsAvoidCatalogRequests(t *testing.T) {
	a := newOpenAIRejectedFieldTestAccount()
	future := time.Now().Add(time.Hour)
	a.RateLimitResetAt = &future
	r := NewAccountCandyTestTransport(&stubOpenAIAccountRepo{accounts: []Account{*a}}, nil)
	r.fetchModels = func(context.Context, *Account) (*OpenAIModelsResponse, error) {
		t.Fatal("rate-limited account fetched upstream models")
		return nil, nil
	}
	options, err := r.Options(context.Background(), []int64{a.ID})
	require.NoError(t, err)
	require.Equal(t, "account_rate_limited", options.Accounts[0].SkipReason)
	require.Empty(t, options.Models)
}

func TestDiagnosticTransportRateLimitFences(t *testing.T) {
	for _, scenario := range []string{"before", "model", "before_send", "before_retry", "stream", "expired"} {
		for _, kind := range []string{"pelican", "attribution"} {
			t.Run(scenario+"/"+kind, func(t *testing.T) {
				a := newOpenAIRejectedFieldTestAccount()
				repo := &stubOpenAIAccountRepo{accounts: []Account{*a}}
				future := time.Now().Add(time.Hour)
				limited := func() { repo.accounts[0].RateLimitResetAt = &future }
				if scenario == "before" {
					limited()
				}
				if scenario == "model" {
					repo.accounts[0].Extra[modelRateLimitsKey] = map[string]any{"gpt-5.5": map[string]any{"rate_limit_reset_at": future.Format(time.RFC3339)}}
				}
				if scenario == "expired" {
					past := time.Now().Add(-time.Hour)
					repo.accounts[0].RateLimitResetAt = &past
				}
				response := &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(candyAuthorizationCompletedResponse))}
				if scenario == "before_retry" {
					response = newOpenAIRejectedFieldTestResponse(503, `{"error":{"message":"temporary"}}`)
					response.Body = &diagnosticCloseHook{ReadCloser: response.Body, close: limited}
				}
				if scenario == "stream" {
					response.Body = io.NopCloser(strings.NewReader("data: {\"type\":\"response.failed\",\"response\":{\"error\":{\"code\":\"rate_limit_exceeded\"}}}\n\n"))
				}
				upstream := &httpUpstreamRecorder{responses: []*http.Response{response}}
				gateway := newOpenAIRejectedFieldTestService(upstream)
				gateway.accountRepo = repo
				runner := newCandySyntheticCatalogTransport(repo, gateway)
				if scenario == "before_send" {
					checks := 0
					runner.accounts = &candyAuthorizationCheckRepo{stubOpenAIAccountRepo: repo, check: func(context.Context) error {
						checks++
						if checks > 1 {
							limited()
						}
						return nil
					}}
				}
				var err error
				if kind == "pelican" {
					_, err = runner.Execute(context.Background(), &CandyTestItem{AccountID: a.ID, Model: "gpt-5.5", PromptVersion: CandyTestPromptVersion})
				} else {
					_, err = runner.Probe(context.Background(), a.ID, "gpt-5.5", "synthetic prompt")
				}
				if scenario == "expired" {
					require.NoError(t, err)
					require.Len(t, upstream.requests, 1)
				} else {
					require.True(t, diagnosticRateLimitError(err), "expected a rate-limit error, got %v", err)
					if scenario == "before_retry" || scenario == "stream" {
						require.Len(t, upstream.requests, 1)
					} else {
						require.Empty(t, upstream.requests)
					}
				}
			})
		}
	}
}

func TestDiagnosticStreamRateLimitStopsRetries(t *testing.T) {
	for _, event := range []string{
		`{"type":"response.failed","response":{"error":{"code":"rate_limit_exceeded"}}}`,
		`{"type":"error","error":{"type":"rate_limit_error"}}`,
	} {
		writer := newCandyResponseWriter(nil)
		_, err := writer.Write([]byte("data: " + event + "\n\n"))
		require.NoError(t, err)
		require.Equal(t, "account_rate_limited", writer.failure)
		require.False(t, retryableDiagnosticError(candyTestError(writer.failure)))
	}
}

func TestManualAttributionBypassesLocalRateLimitsWithoutChangingAccount(t *testing.T) {
	a := newOpenAIRejectedFieldTestAccount()
	future, past := time.Now().Add(time.Hour), time.Now().Add(-time.Hour)
	a.Status, a.Schedulable, a.ExpiresAt, a.RateLimitResetAt = "disabled", false, &past, &future
	a.Extra[modelRateLimitsKey] = map[string]any{"gpt-5.5": map[string]any{"rate_limit_reset_at": future.Format(time.RFC3339)}}
	repo := &stubOpenAIAccountRepo{accounts: []Account{*a}}
	response := &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(candyAuthorizationCompletedResponse))}
	upstream := &httpUpstreamRecorder{responses: []*http.Response{response}}
	gateway := newOpenAIRejectedFieldTestService(upstream)
	gateway.accountRepo = repo
	runner := newCandySyntheticCatalogTransport(repo, gateway)
	ctx := context.WithValue(context.Background(), manualAttributionContextKey{}, true)
	result, err := runner.Probe(ctx, a.ID, "gpt-5.5", "synthetic manual probe")
	require.NoError(t, err)
	require.True(t, result.Completed)
	require.Len(t, upstream.requests, 1)
	require.False(t, repo.accounts[0].Schedulable)
	require.Equal(t, "disabled", repo.accounts[0].Status)
	require.Equal(t, &future, repo.accounts[0].RateLimitResetAt)
	_, err = runner.Probe(context.Background(), a.ID, "gpt-5.5", "scheduled probe")
	require.ErrorIs(t, err, ErrDiagnosticRateLimited)
	require.Len(t, upstream.requests, 1, "automatic detection still skips the account")
}
