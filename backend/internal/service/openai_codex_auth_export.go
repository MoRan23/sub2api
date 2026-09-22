package service

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// OpenAICodexAuthExport separates the downloadable auth.json from UI warnings.
type OpenAICodexAuthExport struct {
	OS       string              `json:"os,omitempty"`
	Auth     OpenAICodexAuthFile `json:"auth"`
	Warnings []string            `json:"warnings"`
}

// BuildOpenAICodexAuthExportForOS uses only a privately read selected slot. It
// never falls back to the compatibility mirror or provisions installation data.
func BuildOpenAICodexAuthExportForOS(account *Account, slot *OpenAIOAuthOSCredential, now time.Time) (*OpenAICodexAuthExport, error) {
	if !IsOpenAIOAuthOSProfileOwner(account) {
		return nil, infraerrors.New(http.StatusBadRequest, "OPENAI_CODEX_AUTH_EXPORT_UNSUPPORTED", "platform, type, parent_account_id, auth_mode")
	}
	if slot == nil || slot.OwnerAccountID != account.ID || NormalizeOpenAIOSFamily(slot.OSFamily) == "" || len(slot.Credentials) == 0 {
		return nil, infraerrors.New(http.StatusBadRequest, "OPENAI_CODEX_AUTH_EXPORT_UNAUTHORIZED", "selected OS has no saved OAuth authorization")
	}
	selected := *account
	selected.Credentials = slot.Credentials
	result, err := BuildOpenAICodexAuthExport(&selected, now)
	if err != nil {
		return nil, err
	}
	result.OS = slot.OSFamily
	return result, nil
}

type OpenAICodexAuthFile struct {
	AuthMode     string                `json:"auth_mode"`
	OpenAIAPIKey *string               `json:"OPENAI_API_KEY"`
	Tokens       OpenAICodexAuthTokens `json:"tokens"`
}

type OpenAICodexAuthTokens struct {
	IDToken      string  `json:"id_token"`
	AccessToken  string  `json:"access_token"`
	RefreshToken string  `json:"refresh_token"`
	AccountID    *string `json:"account_id"`
}

// BuildOpenAICodexAuthExport serializes one freshly loaded database snapshot.
// It must not refresh credentials, resolve a shadow parent, or update the account.
func BuildOpenAICodexAuthExport(account *Account, now time.Time) (*OpenAICodexAuthExport, error) {
	if !IsOpenAIOAuthOSProfileOwner(account) {
		return nil, infraerrors.New(http.StatusBadRequest, "OPENAI_CODEX_AUTH_EXPORT_UNSUPPORTED", "platform, type, parent_account_id, auth_mode")
	}
	accessToken, _ := account.Credentials["access_token"].(string)
	idToken, _ := account.Credentials["id_token"].(string)
	missing := make([]string, 0, 2)
	if strings.TrimSpace(accessToken) == "" {
		missing = append(missing, "access_token")
	}
	if !codexAuthIDTokenParseable(idToken) {
		missing = append(missing, "id_token")
	}
	if len(missing) > 0 {
		return nil, infraerrors.New(http.StatusBadRequest, "OPENAI_CODEX_AUTH_EXPORT_INCOMPLETE", strings.Join(missing, ", "))
	}
	refreshToken, _ := account.Credentials["refresh_token"].(string)
	result := &OpenAICodexAuthExport{
		Auth: OpenAICodexAuthFile{AuthMode: "chatgpt", Tokens: OpenAICodexAuthTokens{
			IDToken: idToken, AccessToken: accessToken, RefreshToken: refreshToken,
		}},
		Warnings: []string{},
	}
	if strings.TrimSpace(refreshToken) == "" {
		result.Auth.Tokens.RefreshToken = ""
		result.Warnings = append(result.Warnings, "missing_refresh_token")
	}
	if accountID, ok := account.Credentials["chatgpt_account_id"].(string); ok && strings.TrimSpace(accountID) != "" {
		result.Auth.Tokens.AccountID = &accountID
	}
	expiresAt := account.GetOpenAITokenExpiresAt()
	if expiresAt == nil {
		var claims struct {
			Exp *int64 `json:"exp"`
		}
		if payload, ok := codexAuthJWTPayload(accessToken); ok && json.Unmarshal(payload, &claims) == nil && claims.Exp != nil {
			expiry := time.Unix(*claims.Exp, 0)
			expiresAt = &expiry
		}
	}
	if expiresAt != nil && !expiresAt.After(now) {
		result.Warnings = append(result.Warnings, "access_token_expired")
	}
	return result, nil
}

func codexAuthJWTPayload(token string) ([]byte, bool) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return nil, false
	}
	payload, err := base64.RawURLEncoding.Strict().DecodeString(parts[1])
	return payload, err == nil
}

// Match the ID-token fields read by Codex's TokenData deserializer. Other JWT
// claims (including exp, aud and iss) do not determine export eligibility.
// This is structural validation only, not a signature or entitlement check.
func codexAuthIDTokenParseable(token string) bool {
	payload, ok := codexAuthJWTPayload(token)
	if !ok {
		return false
	}
	var claims *struct {
		Email   *string `json:"email"`
		Profile *struct {
			Email *string `json:"email"`
		} `json:"https://api.openai.com/profile"`
		Auth *struct {
			PlanType  *string         `json:"chatgpt_plan_type"`
			UserID    *string         `json:"chatgpt_user_id"`
			LegacyID  *string         `json:"user_id"`
			AccountID *string         `json:"chatgpt_account_id"`
			FedRAMP   json.RawMessage `json:"chatgpt_account_is_fedramp"`
		} `json:"https://api.openai.com/auth"`
	}
	if json.Unmarshal(payload, &claims) != nil || claims == nil {
		return false
	}
	if claims.Auth != nil && len(claims.Auth.FedRAMP) > 0 {
		var fedramp bool
		return string(claims.Auth.FedRAMP) != "null" && json.Unmarshal(claims.Auth.FedRAMP, &fedramp) == nil
	}
	return true
}
