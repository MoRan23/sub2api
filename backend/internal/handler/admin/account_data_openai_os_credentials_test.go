package admin

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

type codexImportOAuthClientStub struct {
	service.OpenAIOAuthClient
	responses map[string]*openai.TokenResponse
	calls     []string
}

func (s *codexImportOAuthClientStub) RefreshTokenWithClientID(_ context.Context, rt, _, _ string) (*openai.TokenResponse, error) {
	s.calls = append(s.calls, rt)
	if value := s.responses[rt]; value != nil {
		return value, nil
	}
	return nil, errors.New("mock token rejected")
}

func backupOAuthResponse(t *testing.T, accountID, userID, access, refresh string) *openai.TokenResponse {
	t.Helper()
	return &openai.TokenResponse{AccessToken: access, RefreshToken: refresh, ExpiresIn: 3600,
		IDToken: buildCodexImportTestJWT(t, time.Now().Add(time.Hour), map[string]any{"sub": userID, "https://api.openai.com/auth": map[string]any{"chatgpt_account_id": accountID, "chatgpt_user_id": userID}})}
}

func TestOpenAIOAuthBackupExportUsesAccountCredentialsAndOmitsRuntimeIdentity(t *testing.T) {
	account := &service.Account{ID: 4, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth,
		Credentials:           map[string]any{"access_token": "current-account-token", "refresh_token": "current-refresh", "user_agent": "private-device", "_token_version": "private-version", "model_mapping": map[string]any{"a": "b"}},
		Extra:                 map[string]any{"openai_pinned_installation_id": "private-installation", "codex_turn_state_generation": "private-generation", "note": "keep"},
		OpenAIOAuthOSProfiles: &service.OpenAIOAuthOSProfiles{DefaultOS: service.OpenAIOSWindows}}
	stub := &dataOSCredentialAdminStub{stubAdminService: newStubAdminService(), slots: []*service.OpenAIOAuthOSCredential{
		{OwnerAccountID: 4, OSFamily: service.OpenAIOSWindows, Credentials: map[string]any{"access_token": "windows-token", "_token_version": "secret-version"}},
		{OwnerAccountID: 4, OSFamily: service.OpenAIOSMacOS, Credentials: map[string]any{"access_token": "mac-token", "refresh_token": "mac-refresh", "sync_session_id": "private-root"}},
		{OwnerAccountID: 4, OSFamily: service.OpenAIOSLinux},
	}}
	h := &AccountHandler{adminService: stub}
	os, slots, err := h.exportOpenAIOAuthAuthorizations(context.Background(), account)
	require.NoError(t, err)
	require.Equal(t, service.OpenAIOSWindows, os)
	require.Nil(t, slots, "new backups export one shared credential tuple, not OS authorizations")
	require.Equal(t, "current-account-token", account.Credentials["access_token"])
	require.Equal(t, "current-refresh", account.Credentials["refresh_token"])
	require.Equal(t, map[string]any{"a": "b"}, account.Credentials["model_mapping"])
	require.NotContains(t, account.Credentials, "_token_version")
	require.NotContains(t, portableOpenAIOAuthCredentials(account, account.Credentials), "user_agent")
	require.Equal(t, map[string]any{"note": "keep"}, portableOpenAIOAuthExtra(account))
}

func TestOpenAIOAuthBackupImportLegacyMissingRefreshBecomesShared(t *testing.T) {
	h := &AccountHandler{}
	item := &DataAccount{Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, OpenAIOAuthDefaultOS: service.OpenAIOSMacOS,
		Credentials:               map[string]any{"user_agent": "ignored-device", "installation_id": "ignored-id"},
		OpenAIOAuthAuthorizations: map[string]DataOpenAIOAuthAuthorization{service.OpenAIOSMacOS: {Credentials: map[string]any{"access_token": "saved-access", "_token_version": "ignored-generation"}}}}
	os, slots, err := h.prepareOpenAIOAuthBackupImport(context.Background(), item, nil)
	require.NoError(t, err)
	require.Equal(t, service.OpenAIOSMacOS, os)
	require.Nil(t, slots)
	require.Equal(t, map[string]any{"access_token": "saved-access"}, item.Credentials)
}

func TestOpenAIOAuthBackupVersionGatePreservesLegacyImports(t *testing.T) {
	for _, version := range []int{0, dataVersion, dataOSAuthorizationVersion} {
		require.NoError(t, validateDataHeader(DataPayload{Version: version, Proxies: []DataProxy{}, Accounts: []DataAccount{}}))
	}
	account := DataAccount{Name: "unauthorized", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, OpenAIOAuthDefaultOS: service.OpenAIOSWindows}
	require.NoError(t, validateDataAccount(account))
	payload := DataPayload{Version: dataVersion, Proxies: []DataProxy{}, Accounts: []DataAccount{account}}
	require.ErrorContains(t, validateDataHeader(payload), "version 2")
	payload.Version = dataOSAuthorizationVersion
	require.NoError(t, validateDataHeader(payload))
}

func TestOpenAIOAuthBackupImportSelectsDefaultWholeTupleWithoutExchanges(t *testing.T) {
	client := &codexImportOAuthClientStub{responses: map[string]*openai.TokenResponse{
		"windows-rt": backupOAuthResponse(t, "workspace", "user", "verified-windows", "rotated-windows"),
		"mac-rt":     backupOAuthResponse(t, "workspace", "user", "verified-mac", "rotated-mac"),
	}}
	oauth := service.NewOpenAIOAuthService(nil, client)
	t.Cleanup(oauth.Stop)
	h := &AccountHandler{openaiOAuthService: oauth}
	item := &DataAccount{Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, OpenAIOAuthDefaultOS: service.OpenAIOSWindows,
		Credentials: map[string]any{"access_token": "untrusted-mirror"},
		OpenAIOAuthAuthorizations: map[string]DataOpenAIOAuthAuthorization{
			service.OpenAIOSWindows: {Credentials: map[string]any{"refresh_token": "windows-rt", "chatgpt_account_id": "forged"}},
			service.OpenAIOSMacOS:   {Credentials: map[string]any{"refresh_token": "mac-rt", "chatgpt_account_id": "different-forged"}},
		}}
	os, slots, err := h.prepareOpenAIOAuthBackupImport(context.Background(), item, nil)
	require.NoError(t, err)
	require.Equal(t, service.OpenAIOSWindows, os)
	require.Nil(t, slots)
	require.Equal(t, "windows-rt", item.Credentials["refresh_token"])
	require.Equal(t, "forged", item.Credentials["chatgpt_account_id"], "backup restore follows the single-credential import contract")
	require.NotContains(t, item.Credentials, "access_token", "must not combine the selected grant with the compatibility mirror")
	require.Empty(t, client.calls)
}

func TestOpenAIOAuthBackupImportIgnoresUnusedDuplicateAndMissingRefresh(t *testing.T) {
	for _, rt := range []string{"same-rt", ""} {
		t.Run(rt, func(t *testing.T) {
			client := &codexImportOAuthClientStub{}
			oauth := service.NewOpenAIOAuthService(nil, client)
			t.Cleanup(oauth.Stop)
			h := &AccountHandler{openaiOAuthService: oauth}
			item := &DataAccount{Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, OpenAIOAuthDefaultOS: service.OpenAIOSWindows,
				OpenAIOAuthAuthorizations: map[string]DataOpenAIOAuthAuthorization{
					service.OpenAIOSWindows: {Credentials: map[string]any{"refresh_token": "same-rt"}},
					service.OpenAIOSMacOS:   {Credentials: map[string]any{"refresh_token": rt}},
				}}
			_, slots, err := h.prepareOpenAIOAuthBackupImport(context.Background(), item, nil)
			require.NoError(t, err)
			require.Nil(t, slots)
			require.Equal(t, "same-rt", item.Credentials["refresh_token"])
			require.Empty(t, client.calls)
		})
	}
}

func TestOpenAIOAuthBackupImportDoesNotFallBackFromMissingDefault(t *testing.T) {
	client := &codexImportOAuthClientStub{responses: map[string]*openai.TokenResponse{
		"windows-rt": backupOAuthResponse(t, "workspace-a", "user", "windows-access", "rotated-windows"),
		"mac-rt":     backupOAuthResponse(t, "workspace-b", "user", "mac-access", "rotated-mac"),
	}}
	oauth := service.NewOpenAIOAuthService(nil, client)
	t.Cleanup(oauth.Stop)
	h := &AccountHandler{openaiOAuthService: oauth}
	item := &DataAccount{Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, OpenAIOAuthDefaultOS: service.OpenAIOSLinux,
		OpenAIOAuthAuthorizations: map[string]DataOpenAIOAuthAuthorization{
			service.OpenAIOSWindows: {Credentials: map[string]any{"refresh_token": "windows-rt", "chatgpt_account_id": "workspace-a"}},
			service.OpenAIOSMacOS:   {Credentials: map[string]any{"refresh_token": "mac-rt", "access_token": "mac-only", "chatgpt_account_id": "workspace-b"}},
		}}
	_, slots, err := h.prepareOpenAIOAuthBackupImport(context.Background(), item, nil)
	require.NoError(t, err)
	require.Nil(t, slots)
	require.NotContains(t, item.Credentials, "refresh_token")
	require.NotContains(t, item.Credentials, "chatgpt_account_id")
	require.NotContains(t, item.Credentials, "access_token")
	require.Empty(t, client.calls)
}

type codexImportAuthorizationRepository struct {
	service.AccountRepository
	service.OpenAIOAuthOSCredentialsRepository
	admin *codexImportMemoryAdminService
}

func (r *codexImportAuthorizationRepository) GetByID(ctx context.Context, id int64) (*service.Account, error) {
	return r.admin.GetAccount(ctx, id)
}
func (r *codexImportAuthorizationRepository) GetOpenAIOAuthOSCredential(ctx context.Context, id int64, os string) (*service.OpenAIOAuthOSCredential, error) {
	return r.admin.GetOpenAIOAuthOSCredential(ctx, id, os)
}
func (r *codexImportAuthorizationRepository) ListOpenAIOAuthOSCredentials(ctx context.Context, id int64) ([]*service.OpenAIOAuthOSCredential, error) {
	a, err := r.admin.GetAccount(ctx, id)
	if err != nil {
		return nil, err
	}
	slot, err := r.admin.GetOpenAIOAuthOSCredential(ctx, id, a.OpenAIOAuthOSProfiles.DefaultOS)
	return []*service.OpenAIOAuthOSCredential{slot}, err
}
func (r *codexImportAuthorizationRepository) BindOpenAIOAuthOSCredentialsIfGeneration(ctx context.Context, id int64, os, gen string, credentials map[string]any, _ string) (*service.OpenAIOAuthOSCredential, error) {
	a, err := r.admin.GetAccount(ctx, id)
	if err != nil {
		return nil, err
	}
	if os != a.OpenAIOAuthOSProfiles.DefaultOS || gen != "fixture-generation" || credentials["chatgpt_account_id"] != a.Credentials["chatgpt_account_id"] || credentials["chatgpt_user_id"] != a.Credentials["chatgpt_user_id"] {
		return nil, service.ErrOpenAIOAuthOSSubjectMismatch
	}
	a.Credentials = service.PreserveOpenAIOAuthProviderCredentials(credentials, a.Credentials)
	return r.admin.GetOpenAIOAuthOSCredential(ctx, id, os)
}
func enableCodexImportVerification(t *testing.T, h *AccountHandler, admin *codexImportMemoryAdminService, access, refresh string) {
	t.Helper()
	client := &codexImportOAuthClientStub{responses: map[string]*openai.TokenResponse{refresh: backupOAuthResponse(t, "workspace-1", "user-1", access, refresh)}}
	oauth := service.NewOpenAIOAuthService(nil, client)
	oauth.SetAccountRepository(&codexImportAuthorizationRepository{admin: admin})
	t.Cleanup(oauth.Stop)
	h.openaiOAuthService = oauth
}
