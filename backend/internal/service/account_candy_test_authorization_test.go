package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type candyAuthorizationCheckRepo struct {
	*stubOpenAIAccountRepo
	check func(context.Context) error
}

func (r *candyAuthorizationCheckRepo) GetByID(ctx context.Context, id int64) (*Account, error) {
	if err := r.check(ctx); err != nil {
		return nil, err
	}
	return r.stubOpenAIAccountRepo.GetByID(ctx, id)
}

type candyAuthorizationResponseReader struct {
	io.Reader
	once       sync.Once
	beforeRead func()
}

func (r *candyAuthorizationResponseReader) Read(p []byte) (int, error) {
	r.once.Do(r.beforeRead)
	return r.Reader.Read(p)
}

const candyAuthorizationCompletedResponse = "data: {\"type\": \"response.completed\", \"response\": {\"id\": \"response_synthetic\", \"model\": \"gpt-5.5\", \"status\": \"completed\", \"output\": [{\"type\": \"message\", \"role\": \"assistant\", \"content\": [{\"type\": \"output_text\", \"text\": \"<html><body><svg></svg></body></html>\"}]}]}}\n\n"

func TestCandyCompletedResponseSurvivesAuthorizationMonitorCancellation(t *testing.T) {
	account := newOpenAIRejectedFieldTestAccount()
	var armed, checking atomic.Bool
	checkStarted := make(chan struct{})
	repo := &candyAuthorizationCheckRepo{
		stubOpenAIAccountRepo: &stubOpenAIAccountRepo{accounts: []Account{*account}},
		check: func(ctx context.Context) error {
			if armed.Load() && checking.CompareAndSwap(false, true) {
				close(checkStarted)
				<-ctx.Done()
				return ctx.Err()
			}
			return ctx.Err()
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*CandyTestHeartbeat)
	defer cancel()
	body := &candyAuthorizationResponseReader{
		Reader: strings.NewReader(candyAuthorizationCompletedResponse),
		beforeRead: func() {
			armed.Store(true)
			select {
			case <-checkStarted:
			case <-ctx.Done():
			}
		},
	}
	upstream := &httpUpstreamRecorder{responses: []*http.Response{{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(body)}}}
	gateway := newOpenAIRejectedFieldTestService(upstream)
	gateway.accountRepo = repo
	result, err := newCandySyntheticCatalogTransport(repo, gateway).Execute(ctx, &CandyTestItem{AccountID: account.ID, Model: "gpt-5.5", PromptVersion: CandyTestPromptVersion})
	require.True(t, checking.Load(), "the authorization query must overlap successful completion")
	require.NoError(t, ctx.Err(), "the parent test was not cancelled or timed out")
	require.NotNil(t, result)
	require.True(t, result.Completed)
	require.NoError(t, err, "stopping the monitor after completion is not an authorization change")
	document, extractErr := ExtractPelicanHTML(result.ResponseText)
	require.NoError(t, extractErr)
	require.Equal(t, "<html><body><svg></svg></body></html>", document)
	require.Len(t, upstream.bodies, 1)
}

func TestCandyAuthorizationReadFailureIsNotCredentialChange(t *testing.T) {
	account := newOpenAIRejectedFieldTestAccount()
	var completed atomic.Bool
	repo := &candyAuthorizationCheckRepo{
		stubOpenAIAccountRepo: &stubOpenAIAccountRepo{accounts: []Account{*account}},
		check: func(context.Context) error {
			if completed.Load() {
				return errors.New("synthetic database connection failure")
			}
			return nil
		},
	}
	body := &candyAuthorizationResponseReader{Reader: strings.NewReader(candyAuthorizationCompletedResponse), beforeRead: func() { completed.Store(true) }}
	upstream := &httpUpstreamRecorder{responses: []*http.Response{{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(body)}}}
	gateway := newOpenAIRejectedFieldTestService(upstream)
	gateway.accountRepo = repo
	result, err := newCandySyntheticCatalogTransport(repo, gateway).Execute(context.Background(), &CandyTestItem{AccountID: account.ID, Model: "gpt-5.5", PromptVersion: CandyTestPromptVersion})
	require.NotNil(t, result)
	require.True(t, result.Completed)
	require.EqualError(t, err, "authorization_check_failed")
	require.Len(t, upstream.bodies, 1)
}

func TestCandyAuthorizationAllowsTokenRefreshButRejectsReplacement(t *testing.T) {
	before := &Account{ID: 12, Platform: PlatformOpenAI, Type: AccountTypeOAuth,
		OpenAIOAuthCredentialOwnerID: 12, OpenAIOAuthAuthorizationGeneration: "original-grant",
		Credentials: map[string]any{"access_token": "old", "refresh_token": "old-refresh", "chatgpt_account_id": "same-account"}}
	after := snapshotOAuthRefreshAccount(before)
	after.Credentials["access_token"] = "new"
	after.Credentials["refresh_token"] = "new-refresh"
	after.Credentials["expires_at"] = time.Now().Add(time.Hour).Format(time.RFC3339)
	after.OpenAIOAuthCredentialRevision++
	require.True(t, sameCandyAuthorization(before, after))
	after.OpenAIOAuthAuthorizationGeneration = "replacement-grant"
	require.False(t, sameCandyAuthorization(before, after))
}

func TestCandyAuthorizationCheckErrorPreservesCause(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want error
	}{
		{"cancelled read", context.Canceled, context.Canceled},
		{"read deadline", context.DeadlineExceeded, context.DeadlineExceeded},
		{"deleted account", ErrAccountNotFound, candyTestError("authorization_changed")},
		{"replacement grant", ErrOpenAIOAuthOSAuthorizationChanged, candyTestError("authorization_changed")},
		{"database unavailable", errors.New("synthetic database failure"), candyTestError("authorization_check_failed")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.ErrorIs(t, candyAuthorizationCheckError(context.Background(), tc.err), tc.want)
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.ErrorIs(t, candyAuthorizationCheckError(ctx, errors.New("driver cancelled query")), context.Canceled)
}
