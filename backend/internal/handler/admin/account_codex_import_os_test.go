package admin

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestImportCodexExistingDifferentOSReusesSharedAuthorization(t *testing.T) {
	access := buildCodexAccessToken(t, "workspace", "user", time.Now().Add(time.Hour))
	admin := newCodexImportMemoryAdminService([]service.Account{{ID: 10, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Credentials: map[string]any{"access_token": access, "chatgpt_account_id": "workspace", "chatgpt_user_id": "user"}}})
	h := &AccountHandler{adminService: admin}
	result, err := h.importCodexSessions(context.Background(), CodexSessionImportRequest{OS: service.OpenAIOSMacOS}, []codexImportEntry{{Index: 1, Value: map[string]any{"access_token": access}}})
	require.NoError(t, err)
	require.Zero(t, result.Failed)
	require.Equal(t, 1, result.Updated)
	require.Empty(t, admin.createdAccounts)
	require.Equal(t, access, admin.accounts[0].Credentials["access_token"])
}

func TestImportCodexNewAccountUsesOneGrantAndSelectedInitialIdentity(t *testing.T) {
	admin := newCodexImportMemoryAdminService(nil)
	h := &AccountHandler{adminService: admin}
	access := buildCodexAccessToken(t, "workspace", "user", time.Now().Add(time.Hour))
	result, err := h.importCodexSessions(context.Background(), CodexSessionImportRequest{OS: service.OpenAIOSMacOS}, []codexImportEntry{{Index: 1, Value: access}})
	require.NoError(t, err)
	require.Equal(t, 1, result.Created)
	require.Len(t, admin.createdAccounts, 1)
	require.Equal(t, service.OpenAIOSMacOS, admin.createdAccounts[0].OpenAIOAuthInitialOS)
	require.Empty(t, admin.createdAccounts[0].OpenAIOAuthInitialCredentials)
}

func TestImportCodexRejectsInvalidOSBeforeWrites(t *testing.T) {
	admin := newCodexImportMemoryAdminService(nil)
	h := &AccountHandler{adminService: admin}
	_, err := h.importCodexSessions(context.Background(), CodexSessionImportRequest{OS: "other"}, []codexImportEntry{{Index: 1, Value: "new-access"}})
	require.Error(t, err)
	require.Empty(t, admin.createdAccounts)
}

func TestImportCodexCompletedTupleBindsWithoutProfileOrRefreshExchange(t *testing.T) {
	admin := newCodexImportMemoryAdminService([]service.Account{{ID: 10, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth,
		Credentials: map[string]any{"access_token": "old-access", "refresh_token": "old-refresh", "model_mapping": map[string]any{"a": "b"}},
	}})
	admin.accounts[0].OpenAIOAuthOSProfiles = nil
	client := &codexImportOAuthClientStub{}
	oauth := service.NewOpenAIOAuthService(nil, client)
	t.Cleanup(oauth.Stop)
	h := &AccountHandler{adminService: admin, openaiOAuthService: oauth}
	account, err := h.authorizeCodexImportedOS(context.Background(), &admin.accounts[0], service.OpenAIOSLinux,
		&codexImportAccount{RefreshToken: "imported-refresh", IDToken: "imported-id"},
		map[string]any{"access_token": "imported-access", "refresh_token": "imported-refresh", "id_token": "imported-id"})
	require.NoError(t, err)
	require.Equal(t, "imported-access", account.Credentials["access_token"])
	require.Equal(t, "imported-refresh", account.Credentials["refresh_token"])
	require.Equal(t, "imported-id", account.Credentials["id_token"])
	require.Equal(t, map[string]any{"a": "b"}, account.Credentials["model_mapping"])
	require.Empty(t, client.calls)
}
