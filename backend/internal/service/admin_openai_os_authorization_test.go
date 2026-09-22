package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

type completedOAuthBindRepository struct {
	AccountRepository
	OpenAIOAuthOSCredentialsRepository
	account     *Account
	reads       int
	binds       int
	os          string
	credentials map[string]any
}

func (r *completedOAuthBindRepository) GetByID(context.Context, int64) (*Account, error) {
	r.reads++
	copy := *r.account
	return &copy, nil
}

func (r *completedOAuthBindRepository) BindOpenAIOAuthOSCredentials(_ context.Context, _ int64, os string, credentials map[string]any, _ string) (*OpenAIOAuthOSCredential, error) {
	r.binds++
	r.os, r.credentials = os, credentials
	r.account.Credentials = PreserveOpenAIOAuthProviderCredentials(credentials, r.account.Credentials)
	return &OpenAIOAuthOSCredential{Credentials: r.account.Credentials}, nil
}

func TestAdminBindOpenAIOAuthCredentialsAcceptsCompletedAndRefreshOnlyTuples(t *testing.T) {
	for _, tokenKey := range []string{"access_token", "refresh_token"} {
		t.Run(tokenKey, func(t *testing.T) {
			repo := &completedOAuthBindRepository{account: &Account{ID: 42, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusDisabled,
				Credentials: map[string]any{"model_mapping": map[string]any{"a": "b"}},
			}}
			s := &adminServiceImpl{accountRepo: repo}
			account, err := s.BindOpenAIOAuthCredentials(context.Background(), 42, "", map[string]any{tokenKey: "completed-token", "cookie": "discard"})
			require.NoError(t, err)
			require.Equal(t, 1, repo.binds)
			require.Equal(t, 2, repo.reads, "respond with the account after the bind")
			require.Equal(t, OpenAIOSWindows, repo.os)
			require.Equal(t, "completed-token", account.Credentials[tokenKey])
			require.NotContains(t, repo.credentials, "cookie")
			require.Equal(t, map[string]any{"a": "b"}, account.Credentials["model_mapping"])
			require.Equal(t, StatusDisabled, account.Status, "only the repository may restore an auth-owned pause")
		})
	}
}

func TestAdminBindOpenAIOAuthCredentialsRejectsEmptyGrantBeforeMutation(t *testing.T) {
	for _, credentials := range []map[string]any{nil, {"model_mapping": map[string]any{"a": "b"}}, {"access_token": " ", "refresh_token": "\t"}} {
		repo := &completedOAuthBindRepository{account: &Account{ID: 42, Platform: PlatformOpenAI, Type: AccountTypeOAuth}}
		s := &adminServiceImpl{accountRepo: repo}
		_, err := s.BindOpenAIOAuthCredentials(context.Background(), 42, OpenAIOSLinux, credentials)
		require.Error(t, err)
		require.Zero(t, repo.binds)
	}
}

func TestOpenAIOAuthAccountStateIntentScopesAndDoesNotMutateParentContext(t *testing.T) {
	base := context.Background()
	first := WithOpenAIOAuthAccountStateIntent(base, 42, 0, -1)
	second := WithOpenAIOAuthAccountStateIntent(first, 43)
	require.False(t, OpenAIOAuthAccountStateIntentAllowed(base, 42))
	require.True(t, OpenAIOAuthAccountStateIntentAllowed(first, 42))
	require.False(t, OpenAIOAuthAccountStateIntentAllowed(first, 43))
	require.True(t, OpenAIOAuthAccountStateIntentAllowed(second, 42))
	require.True(t, OpenAIOAuthAccountStateIntentAllowed(second, 43))
	require.False(t, OpenAIOAuthAccountStateIntentAllowed(second, 0))
	require.False(t, OpenAIOAuthAccountStateIntentAllowed(WithOpenAIOAuthAccountStateIntent(base), 42))
}
