package service

import (
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

func codexAuthExportTestJWT(payload string) string {
	return "header." + base64.RawURLEncoding.EncodeToString([]byte(payload)) + ".signature"
}

func codexAuthExportTestAccount() *Account {
	return &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{
		"access_token": "access-test", "refresh_token": "refresh-test",
		"id_token":           codexAuthExportTestJWT(`{"email":"user@example.test","exp":1,"aud":"legacy-client","https://api.openai.com/auth":{"chatgpt_account_id":"jwt-workspace"}}`),
		"chatgpt_account_id": "selected-workspace",
	}}
}

func TestBuildOpenAICodexAuthExportOfficialShapeAndSnapshot(t *testing.T) {
	account := codexAuthExportTestAccount()
	account.Credentials["user_agent"] = "private-user-agent"
	account.Extra = map[string]any{"installation_id": "private-installation"}
	before, err := json.Marshal(account)
	require.NoError(t, err)
	result, err := BuildOpenAICodexAuthExport(account, time.Now())
	require.NoError(t, err, "expired ID tokens remain parseable and exportable")
	require.Empty(t, result.Warnings)
	require.Equal(t, "selected-workspace", *result.Auth.Tokens.AccountID)
	authJSON, err := json.Marshal(result.Auth)
	require.NoError(t, err)
	var auth map[string]any
	require.NoError(t, json.Unmarshal(authJSON, &auth))
	require.Len(t, auth, 3)
	require.Equal(t, "chatgpt", auth["auth_mode"])
	require.Contains(t, auth, "OPENAI_API_KEY")
	require.Nil(t, auth["OPENAI_API_KEY"])
	require.Len(t, auth["tokens"], 4)
	require.NotContains(t, string(authJSON), "private-")
	require.NotContains(t, string(authJSON), "last_refresh")
	after, err := json.Marshal(account)
	require.NoError(t, err)
	require.JSONEq(t, string(before), string(after))
}

func TestBuildOpenAICodexAuthExportWarningsAndNullAccount(t *testing.T) {
	for _, expirySource := range []string{"credentials", "access_token"} {
		t.Run(expirySource, func(t *testing.T) {
			account := codexAuthExportTestAccount()
			delete(account.Credentials, "refresh_token")
			delete(account.Credentials, "chatgpt_account_id")
			now := time.Unix(200, 0)
			if expirySource == "credentials" {
				account.Credentials["expires_at"] = int64(100)
			} else {
				account.Credentials["access_token"] = codexAuthExportTestJWT(`{"exp":100}`)
			}
			result, err := BuildOpenAICodexAuthExport(account, now)
			require.NoError(t, err)
			require.Equal(t, []string{"missing_refresh_token", "access_token_expired"}, result.Warnings)
			require.Empty(t, result.Auth.Tokens.RefreshToken)
			require.Nil(t, result.Auth.Tokens.AccountID)
		})
	}
}

func TestBuildOpenAICodexAuthExportRejectsUnsupportedAndIncomplete(t *testing.T) {
	result, err := BuildOpenAICodexAuthExport(nil, time.Now())
	require.Nil(t, result)
	require.Equal(t, "OPENAI_CODEX_AUTH_EXPORT_UNSUPPORTED", infraerrors.Reason(err))
	for name, modify := range map[string]func(*Account){
		"shadow":   func(a *Account) { id := int64(9); a.ParentAccountID = &id },
		"apikey":   func(a *Account) { a.Type = AccountTypeAPIKey },
		"setup":    func(a *Account) { a.Type = AccountTypeSetupToken },
		"platform": func(a *Account) { a.Platform = PlatformAnthropic },
		"pat": func(a *Account) {
			a.Credentials["auth_mode"] = "chatgptAuthTokens"
			a.Credentials["openai_auth_mode"] = "personal_access_token"
		},
		"agent": func(a *Account) { a.Credentials[openAIAuthModeCredentialKey] = OpenAIAuthModeAgentIdentity },
		"legacy_agent": func(a *Account) {
			a.Credentials[openAIAuthModeLegacyCredentialKey] = "  AgentIdentity  "
		},
		"legacy_agent_without_tokens": func(a *Account) {
			a.Credentials = map[string]any{openAIAuthModeLegacyCredentialKey: OpenAIAuthModeAgentIdentity}
		},
	} {
		t.Run(name, func(t *testing.T) {
			account := codexAuthExportTestAccount()
			modify(account)
			result, err := BuildOpenAICodexAuthExport(account, time.Now())
			require.Nil(t, result)
			require.Equal(t, "OPENAI_CODEX_AUTH_EXPORT_UNSUPPORTED", infraerrors.Reason(err))
		})
	}
	for _, badIDToken := range []string{"", "secret.invalid.signature", "header..signature", codexAuthExportTestJWT(`null`), codexAuthExportTestJWT(`[]`), codexAuthExportTestJWT(`{"email":42}`), codexAuthExportTestJWT(`{"https://api.openai.com/auth":{"chatgpt_account_is_fedramp":null}}`)} {
		account := codexAuthExportTestAccount()
		account.Credentials["id_token"] = badIDToken
		account.Credentials["access_token"] = ""
		result, err := BuildOpenAICodexAuthExport(account, time.Now())
		require.Nil(t, result)
		require.Equal(t, "OPENAI_CODEX_AUTH_EXPORT_INCOMPLETE", infraerrors.Reason(err))
		_, status := infraerrors.ToHTTP(err)
		require.Equal(t, "access_token, id_token", status.Message)
		require.NotContains(t, err.Error(), "secret")
	}
}
