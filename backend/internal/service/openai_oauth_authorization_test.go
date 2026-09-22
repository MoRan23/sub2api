package service

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/codexnative"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/imroc/req/v3"
	"github.com/stretchr/testify/require"
)

type authorizationTestClient struct {
	calls           atomic.Int32
	response        *openai.TokenResponse
	err             error
	mu              sync.Mutex
	userAgent       string
	nativeUserAgent string
}

func (c *authorizationTestClient) ExchangeCode(ctx context.Context, _, _, _, _, _ string) (*openai.TokenResponse, error) {
	c.calls.Add(1)
	c.mu.Lock()
	c.userAgent, _ = OpenAIOAuthAuthIdentity(ctx)
	if scope, ok := codexnative.ScopeFromContext(ctx); ok {
		c.nativeUserAgent = scope.AccountUserAgent
	}
	c.mu.Unlock()
	return c.response, c.err
}
func (c *authorizationTestClient) RefreshToken(ctx context.Context, _, _ string) (*openai.TokenResponse, error) {
	return c.ExchangeCode(ctx, "", "", "", "", "")
}
func (c *authorizationTestClient) RefreshTokenWithClientID(ctx context.Context, _, _, _ string) (*openai.TokenResponse, error) {
	return c.ExchangeCode(ctx, "", "", "", "", "")
}

type authorizationTestRepository struct {
	AccountRepository
	OpenAIOAuthOSCredentialsRepository
	mu      sync.Mutex
	account *Account
	slots   map[string]*OpenAIOAuthOSCredential
}

func (r *authorizationTestRepository) GetByID(_ context.Context, id int64) (*Account, error) {
	if id != r.account.ID {
		return nil, ErrAccountNotFound
	}
	copy := *r.account
	copy.Credentials = maps.Clone(r.account.Credentials)
	return &copy, nil
}
func (r *authorizationTestRepository) GetOpenAIOAuthOSCredential(_ context.Context, id int64, os string) (*OpenAIOAuthOSCredential, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	grant := r.slots[r.account.OpenAIOAuthOSProfiles.DefaultOS]
	if grant == nil {
		return nil, nil
	}
	copy := *grant
	copy.OSFamily = os
	return &copy, nil
}
func (r *authorizationTestRepository) ListOpenAIOAuthOSCredentials(_ context.Context, _ int64) ([]*OpenAIOAuthOSCredential, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var slots []*OpenAIOAuthOSCredential
	for _, slot := range r.slots {
		slots = append(slots, slot)
	}
	return slots, nil
}
func (r *authorizationTestRepository) BindOpenAIOAuthOSCredentialsIfGeneration(_ context.Context, id int64, os, generation string, credentials map[string]any, _ string) (*OpenAIOAuthOSCredential, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	current := r.slots[r.account.OpenAIOAuthOSProfiles.DefaultOS]
	if (current == nil && generation != "") || (current != nil && current.AuthorizationGeneration != generation) {
		return nil, ErrOpenAIOAuthOSAuthorizationChanged
	}
	if credentialString(credentials, "chatgpt_account_id") != "workspace" || credentialString(credentials, "chatgpt_user_id") != "user" {
		return nil, ErrOpenAIOAuthOSSubjectMismatch
	}
	bound := &OpenAIOAuthOSCredential{OwnerAccountID: id, OSFamily: os, Credentials: maps.Clone(credentials), AuthorizationGeneration: "new-generation", Status: OpenAIOAuthAuthorizationAuthorized}
	r.slots[r.account.OpenAIOAuthOSProfiles.DefaultOS] = bound
	r.account.Credentials = PreserveOpenAIOAuthProviderCredentials(credentials, r.account.Credentials)
	return bound, nil
}

func authorizationTestSetup(t *testing.T) (*OpenAIOAuthService, *authorizationTestClient, *authorizationTestRepository) {
	t.Helper()
	account := &Account{ID: 42, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"access_token": "old-at", "refresh_token": "windows-rt"}}
	profiles, err := BuildOpenAIOAuthOSProfiles(account, nil)
	require.NoError(t, err)
	ApplyOpenAIOAuthOSProfiles(account, profiles)
	repo := &authorizationTestRepository{account: account, slots: map[string]*OpenAIOAuthOSCredential{OpenAIOSWindows: {OwnerAccountID: 42, OSFamily: OpenAIOSWindows, Credentials: maps.Clone(account.Credentials), AuthorizationGeneration: "windows-generation", Status: OpenAIOAuthAuthorizationAuthorized}}}
	client := &authorizationTestClient{response: &openai.TokenResponse{AccessToken: "new-at", RefreshToken: "linux-rt", ExpiresIn: 3600, IDToken: authorizationTestIDToken("workspace", "user")}}
	svc := NewOpenAIOAuthService(nil, client)
	svc.SetAccountRepository(repo)
	t.Cleanup(svc.Stop)
	return svc, client, repo
}

func authorizationTestIDToken(workspace, user string) string {
	claims := fmt.Sprintf(`{"exp":4102444800,"https://api.openai.com/auth":{"chatgpt_account_id":%q,"chatgpt_user_id":%q}}`, workspace, user)
	return "header." + base64.RawURLEncoding.EncodeToString([]byte(claims)) + ".signature"
}

func authorizationTestInput(t *testing.T, svc *OpenAIOAuthService, accountID int64, os string) *OpenAIExchangeCodeInput {
	t.Helper()
	result, err := svc.GenerateAuthURLForOS(context.Background(), nil, "", PlatformOpenAI, OpenAIOAuthAuthorizationTarget{AccountID: accountID, OS: os})
	require.NoError(t, err)
	parsed, err := url.Parse(result.AuthURL)
	require.NoError(t, err)
	return &OpenAIExchangeCodeInput{SessionID: result.SessionID, State: parsed.Query().Get("state"), Code: "test-code"}
}

func TestOpenAIOAuthAuthorizationRejectsCallbackRetargeting(t *testing.T) {
	svc, client, repo := authorizationTestSetup(t)
	input := authorizationTestInput(t, svc, 42, OpenAIOSLinux)
	for _, changed := range []OpenAIExchangeCodeInput{
		{AccountID: 43}, {OS: OpenAIOSMacOS}, {OS: "invalid"}, {Purpose: "create"}, {RedirectURI: "http://other.invalid/callback"},
	} {
		changed.SessionID, changed.State, changed.Code = input.SessionID, input.State, input.Code
		_, err := svc.ExchangeCode(context.Background(), &changed)
		require.Error(t, err)
	}
	require.Zero(t, client.calls.Load())
	info, err := svc.ExchangeCode(context.Background(), input)
	require.NoError(t, err)
	require.Equal(t, int64(42), info.Account.ID)
	require.Equal(t, OpenAIOSLinux, info.OS)
	require.Empty(t, info.AccessToken)
	require.Empty(t, info.RefreshToken)
	require.Equal(t, "linux-rt", repo.slots[OpenAIOSWindows].Credentials["refresh_token"])
	require.Len(t, repo.slots, 1)
	require.Equal(t, repo.account.OpenAIOAuthOSProfiles.Profiles[OpenAIOSLinux].UserAgent, client.userAgent)
}

func TestOpenAIOAuthAuthorizationSessionConsumedOnceAcrossConcurrentCallbacks(t *testing.T) {
	svc, client, _ := authorizationTestSetup(t)
	input := authorizationTestInput(t, svc, 0, OpenAIOSMacOS)
	var successful atomic.Int32
	var wg sync.WaitGroup
	for range 24 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := svc.ExchangeCode(context.Background(), input); err == nil {
				successful.Add(1)
			}
		}()
	}
	wg.Wait()
	require.Equal(t, int32(1), successful.Load())
	require.Equal(t, int32(1), client.calls.Load())
	require.Equal(t, OpenAIOSMacOS, openai.DetectOSFamilyFromUserAgent(client.userAgent))
}

func TestOpenAIOAuthAuthorizationFailedExchangeStillConsumesSession(t *testing.T) {
	svc, client, _ := authorizationTestSetup(t)
	client.err = errors.New("simulated exchange failure")
	input := authorizationTestInput(t, svc, 0, OpenAIOSLinux)
	_, err := svc.ExchangeCode(context.Background(), input)
	require.Error(t, err)
	_, err = svc.ExchangeCode(context.Background(), input)
	require.Error(t, err)
	require.Equal(t, int32(1), client.calls.Load())
}

func TestOpenAIOAuthAuthorizationDifferentUpstreamAccountRejected(t *testing.T) {
	for _, subject := range [][2]string{{"different-workspace", "user"}, {"workspace", "different-user"}} {
		t.Run(subject[0]+subject[1], func(t *testing.T) {
			svc, client, repo := authorizationTestSetup(t)
			client.response.IDToken = authorizationTestIDToken(subject[0], subject[1])
			input := authorizationTestInput(t, svc, 42, OpenAIOSLinux)
			_, err := svc.ExchangeCode(context.Background(), input)
			require.ErrorIs(t, err, ErrOpenAIOAuthOSSubjectMismatch)
			require.NotContains(t, repo.slots, OpenAIOSLinux)
			require.Equal(t, "windows-rt", repo.slots[OpenAIOSWindows].Credentials["refresh_token"])
		})
	}
}

func TestOpenAIOAuthAuthorizationStaleSessionCannotUndoRevoke(t *testing.T) {
	svc, _, repo := authorizationTestSetup(t)
	input := authorizationTestInput(t, svc, 42, OpenAIOSLinux)
	repo.slots[OpenAIOSWindows] = &OpenAIOAuthOSCredential{OSFamily: OpenAIOSWindows, AuthorizationGeneration: "revoked-generation", Status: OpenAIOAuthAuthorizationUnauthorized}
	_, err := svc.ExchangeCode(context.Background(), input)
	require.ErrorIs(t, err, ErrOpenAIOAuthOSAuthorizationChanged)
	require.Equal(t, OpenAIOAuthAuthorizationUnauthorized, repo.slots[OpenAIOSWindows].Status)
}

func TestOpenAIOAuthAuthorizationSameRefreshTokenCanUseAnotherIdentity(t *testing.T) {
	svc, client, repo := authorizationTestSetup(t)
	_, err := svc.AuthorizeAccountWithRefreshToken(context.Background(), 42, OpenAIOSLinux, "windows-rt", "")
	require.NoError(t, err)
	require.Equal(t, int32(1), client.calls.Load())
	require.Len(t, repo.slots, 1)
	require.Equal(t, "linux-rt", repo.slots[OpenAIOSWindows].Credentials["refresh_token"])
}

func TestOpenAIOAuthAuthorizationMissingSharedGrantCannotRefresh(t *testing.T) {
	svc, client, repo := authorizationTestSetup(t)
	delete(repo.slots, OpenAIOSWindows)
	account := *repo.account
	account.OpenAIOAuthCredentialOS = OpenAIOSLinux
	_, err := svc.RefreshAccountToken(context.Background(), &account)
	require.ErrorIs(t, err, ErrOpenAIOAuthOSUnauthorized)
	require.Zero(t, client.calls.Load())
}

func TestOpenAIOAuthAuthorizationManualImportUsesServerIdentity(t *testing.T) {
	svc, client, repo := authorizationTestSetup(t)
	info, err := svc.AuthorizeAccountWithRefreshToken(context.Background(), 42, OpenAIOSMacOS, "independent-rt", "")
	require.NoError(t, err)
	require.NotNil(t, info.Account)
	require.Empty(t, info.AccessToken)
	require.Equal(t, "workspace", repo.slots[OpenAIOSWindows].Credentials["chatgpt_account_id"])
	require.Equal(t, OpenAIOSMacOS, openai.DetectOSFamilyFromUserAgent(client.userAgent))
}

func TestOpenAIOAuthAuthorizationReauthorizationChangesSharedGrantPreservesIdentities(t *testing.T) {
	svc, client, repo := authorizationTestSetup(t)
	client.response.RefreshToken = "windows-new-rt"
	profiles := CloneOpenAIOAuthOSProfiles(repo.account.OpenAIOAuthOSProfiles)
	input := authorizationTestInput(t, svc, 42, OpenAIOSWindows)
	_, err := svc.ExchangeCode(context.Background(), input)
	require.NoError(t, err)
	require.Equal(t, "windows-new-rt", repo.slots[OpenAIOSWindows].Credentials["refresh_token"])
	require.Equal(t, profiles, repo.account.OpenAIOAuthOSProfiles)
	for _, os := range OpenAIOAuthOSFamilies() {
		grant, err := repo.GetOpenAIOAuthOSCredential(context.Background(), 42, os)
		require.NoError(t, err)
		require.Equal(t, os, grant.OSFamily)
		require.Equal(t, "windows-new-rt", grant.Credentials["refresh_token"])
	}
}

func TestOpenAIOAuthAuthorizationOmittedOSUsesAccountDefaultIdentity(t *testing.T) {
	svc, client, repo := authorizationTestSetup(t)
	input := authorizationTestInput(t, svc, 42, "")
	info, err := svc.ExchangeCode(context.Background(), input)
	require.NoError(t, err)
	require.Equal(t, repo.account.OpenAIOAuthOSProfiles.DefaultOS, info.OS)
	require.Equal(t, OpenAIOSWindows, openai.DetectOSFamilyFromUserAgent(client.userAgent))
	require.Len(t, repo.slots, 1)
}

func TestOpenAIOAuthAuthorizationMissingProviderIdentityCannotBind(t *testing.T) {
	svc, client, repo := authorizationTestSetup(t)
	client.response.IDToken = ""
	input := authorizationTestInput(t, svc, 42, OpenAIOSLinux)
	_, err := svc.ExchangeCode(context.Background(), input)
	require.ErrorContains(t, err, "did not identify")
	require.NotContains(t, repo.slots, OpenAIOSLinux)
}

func TestOpenAIOAuthAuthorizationRefreshRejectsChangedAttemptRevisionBeforeExchange(t *testing.T) {
	svc, client, repo := authorizationTestSetup(t)
	slot := repo.slots[OpenAIOSWindows]
	slot.Revision = 2
	account := *repo.account
	account.OpenAIOAuthCredentialOS, account.OpenAIOAuthCredentialOwnerID = OpenAIOSWindows, 42
	account.OpenAIOAuthAuthorizationGeneration, account.OpenAIOAuthCredentialRevision = slot.AuthorizationGeneration, 1
	_, err := svc.RefreshAccountToken(context.Background(), &account)
	require.ErrorIs(t, err, ErrOpenAIOAuthOSAuthorizationChanged)
	require.Zero(t, client.calls.Load())
}

func TestOpenAIOAuthAuthorizationImportValidationDoesNotMutateAccountPreferences(t *testing.T) {
	svc, client, _ := authorizationTestSetup(t)
	var enrichCalls atomic.Int32
	svc.SetPrivacyClientFactory(func(string) (*req.Client, error) {
		enrichCalls.Add(1)
		return nil, errors.New("validation must not enrich")
	})
	_, err := svc.RefreshTokenForOS(context.Background(), "independent-rt", "", "", OpenAIOSLinux)
	require.NoError(t, err)
	client.response.IDToken = authorizationTestIDToken("other-workspace", "other-user")
	_, err = svc.AuthorizeAccountWithRefreshToken(context.Background(), 42, OpenAIOSLinux, "independent-rt", "")
	require.ErrorIs(t, err, ErrOpenAIOAuthOSSubjectMismatch)
	require.Zero(t, enrichCalls.Load())
}

func TestOpenAIOAuthAuthorizationExplicitOSReplacesInheritedTransportHint(t *testing.T) {
	svc, client, repo := authorizationTestSetup(t)
	ctx := WithOpenAINativeHTTPScope(context.Background(), repo.account, "")
	_, err := svc.RefreshTokenForOS(ctx, "independent-rt", "", "", OpenAIOSLinux)
	require.NoError(t, err)
	require.Equal(t, OpenAIOSLinux, openai.DetectOSFamilyFromUserAgent(client.userAgent))
	require.Equal(t, client.userAgent, client.nativeUserAgent)
}
