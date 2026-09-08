//go:build unit

package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/stretchr/testify/require"
)

type crsOpenAIRefreshRepo struct {
	AccountRepository
	stored       *Account
	shadow       *Account
	patchErr     error
	patchCalls   int
	genericCalls int
}

func (r *crsOpenAIRefreshRepo) Create(_ context.Context, account *Account) error {
	account.ID = 7
	r.stored = snapshotOAuthRefreshAccount(account)
	return nil
}

func (r *crsOpenAIRefreshRepo) Update(_ context.Context, account *Account) error {
	if account.IsCredentialShadow() {
		r.shadow = snapshotOAuthRefreshAccount(account)
	} else {
		r.stored = snapshotOAuthRefreshAccount(account)
	}
	return nil
}

func (r *crsOpenAIRefreshRepo) GetByCRSAccountID(context.Context, string) (*Account, error) {
	return snapshotOAuthRefreshAccount(r.stored), nil
}

func (r *crsOpenAIRefreshRepo) GetByID(context.Context, int64) (*Account, error) {
	return snapshotOAuthRefreshAccount(r.stored), nil
}

func (r *crsOpenAIRefreshRepo) ListShadowsByParent(context.Context, int64) ([]*Account, error) {
	if r.shadow == nil {
		return nil, nil
	}
	return []*Account{snapshotOAuthRefreshAccount(r.shadow)}, nil
}

func (r *crsOpenAIRefreshRepo) UpdateCredentials(context.Context, int64, map[string]any) error {
	r.genericCalls++
	return errors.New("CRS OpenAI refresh must not replace the full credential document")
}

func (r *crsOpenAIRefreshRepo) PatchOpenAIOAuthCredentialsIfUnchanged(_ context.Context, id int64, expected map[string]any, proxyID *int64, patch map[string]any, removed []string) (bool, error) {
	r.patchCalls++
	if r.patchErr != nil {
		return false, r.patchErr
	}
	return applyOpenAIRefreshTestPatch(r.stored, id, expected, proxyID, patch, removed), nil
}

type crsOpenAIRefreshClient struct {
	OpenAIOAuthClient
	onRefresh func()
	err       error
}

func (c *crsOpenAIRefreshClient) RefreshTokenWithClientID(context.Context, string, string, string) (*openai.TokenResponse, error) {
	if c.onRefresh != nil {
		c.onRefresh()
	}
	if c.err != nil {
		return nil, c.err
	}
	return &openai.TokenResponse{AccessToken: "refreshed-access", RefreshToken: "refreshed-token", ExpiresIn: 3600}, nil
}

func TestCRSSyncOpenAIRefreshPreservesConcurrentModelRestrictions(t *testing.T) {
	for _, update := range []bool{false, true} {
		name := "created"
		if update {
			name = "updated"
		}
		t.Run(name, func(t *testing.T) {
			repo := &crsOpenAIRefreshRepo{}
			if update {
				repo.stored = openAIRefreshMappingAccount()
				repo.stored.Extra = map[string]any{"crs_account_id": "crs-openai-1"}
			}
			client := &crsOpenAIRefreshClient{onRefresh: func() { changeOpenAIRefreshMapping(repo.stored) }}
			result := runCRSOpenAIRefreshSync(t, repo, client)
			require.Len(t, result.Items, 1)
			require.Equal(t, name, result.Items[0].Action)
			require.Equal(t, 1, repo.patchCalls)
			require.Zero(t, repo.genericCalls)
			require.Equal(t, "refreshed-access", repo.stored.GetCredential("access_token"))
			require.NotZero(t, repo.stored.Credentials["_token_version"])
			require.False(t, repo.stored.IsModelSupported("gpt-6-astra"))
			require.True(t, repo.stored.IsModelSupported("gpt-5.6-sol"))
			require.Equal(t, float64(20), repo.stored.Credentials["quota_limit"])
			require.False(t, repo.stored.Schedulable)
		})
	}
}

func TestCRSSyncOpenAIRefreshUsesDurableProxyAndKeepsImportSuccess(t *testing.T) {
	for _, failure := range []string{"reauthorized", "refresh", "persist"} {
		t.Run(failure, func(t *testing.T) {
			repo := &crsOpenAIRefreshRepo{stored: openAIRefreshMappingAccount()}
			repo.stored.Extra = map[string]any{"crs_account_id": "crs-openai-1"}
			repo.shadow = &Account{ID: 70, Platform: PlatformOpenAI, Type: AccountTypeOAuth, ParentAccountID: new(int64(7))}
			client := &crsOpenAIRefreshClient{onRefresh: func() {
				repo.stored.ProxyID = new(int64(99))
				changeOpenAIRefreshMapping(repo.stored)
				if failure == "reauthorized" {
					repo.stored.Credentials["access_token"] = "reauthorized-access"
				}
			}}
			if failure == "refresh" {
				client.err = errors.New("refresh unavailable")
			}
			if failure == "persist" {
				repo.patchErr = errors.New("database unavailable")
			}
			result := runCRSOpenAIRefreshSync(t, repo, client)
			require.Equal(t, "updated", result.Items[0].Action)
			require.Equal(t, 1, result.Updated)
			require.Zero(t, result.Failed)
			require.Zero(t, repo.genericCalls)
			require.False(t, repo.stored.IsModelSupported("gpt-6-astra"))
			if failure == "persist" {
				require.Nil(t, repo.shadow.ProxyID, "unavailable durable state must not propagate the old proxy")
			} else {
				require.Equal(t, new(int64(99)), repo.shadow.ProxyID)
			}
			if failure == "reauthorized" {
				require.Equal(t, "reauthorized-access", repo.stored.GetCredential("access_token"))
			}
		})
	}
}

func runCRSOpenAIRefreshSync(t *testing.T, repo AccountRepository, oauthClient OpenAIOAuthClient) *SyncFromCRSResult {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/web/auth/login" {
			_, _ = w.Write([]byte(`{"success":true,"token":"sync-token"}`))
			return
		}
		require.Equal(t, "/admin/sync/export-accounts", r.URL.Path)
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
			"success": true,
			"data": map[string]any{"openaiOAuthAccounts": []any{map[string]any{
				"id": "crs-openai-1", "kind": "openai", "name": "OpenAI sync",
				"isActive": true, "schedulable": true, "credentials": openAIRefreshMappingAccount().Credentials,
			}}},
		}))
	}))
	t.Cleanup(server.Close)
	oauthService := NewOpenAIOAuthService(nil, oauthClient)
	t.Cleanup(oauthService.Stop)
	cfg := &config.Config{}
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	svc := NewCRSSyncService(repo, nil, nil, oauthService, nil, cfg)
	result, err := svc.SyncFromCRS(context.Background(), SyncFromCRSInput{BaseURL: server.URL, Username: "admin", Password: "password"})
	require.NoError(t, err)
	return result
}
