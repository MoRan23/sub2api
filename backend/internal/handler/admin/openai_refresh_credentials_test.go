package admin

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type adminOpenAIRefreshClient struct {
	service.OpenAIOAuthClient
	beforeReturn func()
	err          error
	calls        int
}

func (c *adminOpenAIRefreshClient) RefreshTokenWithClientID(context.Context, string, string, string) (*openai.TokenResponse, error) {
	c.calls++
	if c.beforeReturn != nil {
		c.beforeReturn()
	}
	return &openai.TokenResponse{AccessToken: "rotated-access", RefreshToken: "rotated-refresh", ExpiresIn: 3600}, c.err
}

type adminOpenAIRefreshPersistence struct {
	service.AdminService
	initial      *service.Account
	durable      *service.Account
	expected     *service.Account
	credentials  map[string]any
	applied      bool
	err          error
	persistCalls int
	fullUpdates  int
	onPersist    func()
}

func (s *adminOpenAIRefreshPersistence) GetAccount(context.Context, int64) (*service.Account, error) {
	return s.initial, nil
}

func (s *adminOpenAIRefreshPersistence) PersistOpenAIOAuthRefreshCredentials(_ context.Context, expected *service.Account, credentials map[string]any) (*service.Account, bool, error) {
	s.persistCalls++
	s.expected, s.credentials = expected, credentials
	if s.onPersist != nil {
		s.onPersist()
	}
	return s.durable, s.applied, s.err
}

func (s *adminOpenAIRefreshPersistence) UpdateAccount(context.Context, int64, *service.UpdateAccountInput) (*service.Account, error) {
	s.fullUpdates++
	return nil, errors.New("refresh must not replace administrator credentials")
}

func (*adminOpenAIRefreshPersistence) EnsureOpenAIPrivacy(context.Context, *service.Account) string {
	return ""
}
func (*adminOpenAIRefreshPersistence) EnsureAntigravityPrivacy(context.Context, *service.Account) string {
	return ""
}

type adminOpenAIRefreshInvalidator struct {
	calls int
	err   error
}

func (i *adminOpenAIRefreshInvalidator) InvalidateToken(ctx context.Context, _ *service.Account) error {
	i.calls++
	i.err = ctx.Err()
	return nil
}

func invokeAdminOpenAIRefresh(t *testing.T, ctx context.Context, dedicated bool, admin *adminOpenAIRefreshPersistence, client *adminOpenAIRefreshClient, invalidator *adminOpenAIRefreshInvalidator) error {
	t.Helper()
	oauth := service.NewOpenAIOAuthService(nil, client)
	t.Cleanup(oauth.Stop)
	if !dedicated {
		handler := &AccountHandler{adminService: admin, openaiOAuthService: oauth, tokenCacheInvalidator: invalidator}
		_, _, err := handler.refreshSingleAccount(ctx, admin.initial)
		return err
	}
	handler := NewOpenAIOAuthHandler(oauth, admin, nil, nil)
	handler.SetTokenCacheInvalidator(invalidator)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/admin/openai/accounts/6799/refresh", nil).WithContext(ctx)
	c.Params = gin.Params{{Key: "id", Value: "6799"}}
	handler.RefreshAccountToken(c)
	if w.Code != http.StatusOK {
		return errors.New(w.Body.String())
	}
	return nil
}

func TestAdminOpenAIRefreshUsesConditionalCredentialsPersistence(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, dedicated := range []bool{false, true} {
		name := "account_single_and_batch"
		if dedicated {
			name = "dedicated_oauth"
		}
		t.Run(name, func(t *testing.T) {
			for _, tc := range []struct {
				name       string
				applied    bool
				persistErr error
			}{
				{name: "mapping_updated_during_refresh", applied: true},
				{name: "reauthorization_winner", applied: false},
				{name: "committed_but_durable_read_failed", applied: true, persistErr: errors.New("durable state unavailable")},
			} {
				t.Run(tc.name, func(t *testing.T) {
					ctx, cancel := context.WithCancel(context.Background())
					defer cancel()
					initial := &service.Account{ID: 6799, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth,
						Credentials: map[string]any{"access_token": "old-access", "refresh_token": "old-refresh", "model_mapping": map[string]any{"gpt-5.6-sol": "gpt-5.6-sol"}}}
					durable := *initial
					durable.Credentials = map[string]any{"access_token": "durable-access", "refresh_token": "durable-refresh", "model_mapping": map[string]any{"gpt-5.6-terra": "gpt-5.6-terra"}}
					admin := &adminOpenAIRefreshPersistence{initial: initial, durable: &durable, applied: tc.applied, err: tc.persistErr}
					if tc.persistErr != nil {
						admin.onPersist = cancel
					}
					client := &adminOpenAIRefreshClient{beforeReturn: func() {
						// The provider call and concurrent administrator mutation must not
						// change the expected credential snapshot used for CAS.
						initial.Credentials["access_token"] = "modified-in-memory"
					}}
					invalidator := &adminOpenAIRefreshInvalidator{}
					err := invokeAdminOpenAIRefresh(t, ctx, dedicated, admin, client, invalidator)
					if tc.persistErr == nil {
						require.NoError(t, err)
					} else {
						require.Error(t, err)
					}
					require.Equal(t, 1, admin.persistCalls)
					require.Zero(t, admin.fullUpdates)
					require.Equal(t, "old-access", admin.expected.GetCredential("access_token"))
					require.Equal(t, "rotated-access", admin.credentials["access_token"])
					require.Equal(t, "rotated-refresh", admin.credentials["refresh_token"])
					require.Equal(t, map[string]any{"gpt-5.6-terra": "gpt-5.6-terra"}, durable.Credentials["model_mapping"])
					if tc.applied {
						require.Equal(t, 1, invalidator.calls)
						require.NoError(t, invalidator.err, "cache cleanup must survive cancellation after commit")
					} else {
						require.Zero(t, invalidator.calls)
					}
				})
			}
		})
	}
}

func TestAdminOpenAIRefreshDoesNotPersistFailedOrCancelledAttempt(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, dedicated := range []bool{false, true} {
		for _, cancelAttempt := range []bool{false, true} {
			ctx, cancel := context.WithCancel(context.Background())
			admin := &adminOpenAIRefreshPersistence{initial: &service.Account{ID: 6799, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth,
				Credentials: map[string]any{"refresh_token": "old-refresh"}}}
			client := &adminOpenAIRefreshClient{err: errors.New("upstream unavailable")}
			if cancelAttempt {
				client.err = nil
				client.beforeReturn = cancel
			}
			invalidator := &adminOpenAIRefreshInvalidator{}
			require.Error(t, invokeAdminOpenAIRefresh(t, ctx, dedicated, admin, client, invalidator))
			cancel()
			require.Zero(t, admin.persistCalls)
			require.Zero(t, admin.fullUpdates)
			require.Zero(t, invalidator.calls)
		}
	}
}
