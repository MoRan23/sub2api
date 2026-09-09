//go:build unit

package middleware

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestAPIKeyAuthCodexHistoryNotesRequiresUnexpiredCredential(t *testing.T) {
	gin.SetMode(gin.TestMode)
	past, future := time.Now().Add(-time.Hour), time.Now().Add(time.Hour)
	for _, mode := range []string{config.RunModeStandard, config.RunModeSimple} {
		for _, path := range []string{
			"/v1/alpha/history/v2/list_windows",
			"/v1/alpha/notes/v2/write_file",
			"/backend-api/codex/alpha/history/v2/read_item",
			"/backend-api/codex/alpha/notes/v2/append_to_file",
		} {
			for _, tc := range []struct {
				name      string
				status    string
				expiresAt *time.Time
				wantCode  int
			}{
				{name: "active_without_balance", status: service.StatusActive, wantCode: http.StatusOK},
				{name: "active_with_exhausted_quota", status: service.StatusActive, expiresAt: &future, wantCode: http.StatusOK},
				{name: "quota_exhausted", status: service.StatusAPIKeyQuotaExhausted, wantCode: http.StatusOK},
				{name: "expired_status", status: service.StatusAPIKeyExpired, wantCode: http.StatusForbidden},
				{name: "expired_time", status: service.StatusActive, expiresAt: &past, wantCode: http.StatusForbidden},
				{name: "expired_and_quota_exhausted", status: service.StatusAPIKeyQuotaExhausted, expiresAt: &past, wantCode: http.StatusForbidden},
			} {
				t.Run(mode+path+"/"+tc.name, func(t *testing.T) {
					user := &service.User{ID: 7, Role: service.RoleUser, Status: service.StatusActive}
					group := &service.Group{ID: 42, Platform: service.PlatformOpenAI, Status: service.StatusActive, Hydrated: true}
					key := &service.APIKey{
						ID: 100, UserID: user.ID, Key: "codex-aux-expiry", Status: tc.status,
						User: user, Group: group, GroupID: &group.ID, ExpiresAt: tc.expiresAt,
						Quota: 1, QuotaUsed: 1,
					}
					repo := &stubApiKeyRepo{getByKey: func(context.Context, string) (*service.APIKey, error) {
						clone := *key
						return &clone, nil
					}}
					cfg := &config.Config{RunMode: mode}
					svc := service.NewAPIKeyService(repo, nil, nil, nil, nil, nil, cfg)
					router := gin.New()
					router.Use(gin.HandlerFunc(NewAPIKeyAuthMiddleware(svc, nil, cfg)))
					handled := false
					router.POST(path, func(c *gin.Context) {
						handled = true
						c.Status(http.StatusOK)
					})
					req := httptest.NewRequest(http.MethodPost, path, nil)
					req.Header.Set("Authorization", "Bearer "+key.Key)
					w := httptest.NewRecorder()
					router.ServeHTTP(w, req)
					require.Equal(t, tc.wantCode, w.Code, w.Body.String())
					require.Equal(t, tc.wantCode == http.StatusOK, handled)
					if tc.wantCode == http.StatusForbidden {
						requireAPIKeyAuthError(t, w, "API_KEY_EXPIRED", "API key 已过期")
					}
				})
			}
		}
	}
}

func TestAPIKeyAuthCodexHistoryNotesRequiresSubscriptionForSubscriptionGroup(t *testing.T) {
	gin.SetMode(gin.TestMode)
	limit := 1.0
	group := &service.Group{
		ID: 42, Platform: service.PlatformOpenAI, Status: service.StatusActive, Hydrated: true,
		SubscriptionType: service.SubscriptionTypeSubscription, DailyLimitUSD: &limit,
	}
	user := &service.User{ID: 7, Role: service.RoleUser, Status: service.StatusActive, Balance: 0}
	key := &service.APIKey{ID: 100, UserID: user.ID, Key: "codex-sub-required", Status: service.StatusActive, User: user, Group: group, GroupID: &group.ID}
	apiKeyRepo := &stubApiKeyRepo{getByKey: func(context.Context, string) (*service.APIKey, error) {
		clone := *key
		return &clone, nil
	}}
	cfg := &config.Config{RunMode: config.RunModeStandard}
	apiKeyService := service.NewAPIKeyService(apiKeyRepo, nil, nil, nil, nil, nil, cfg)
	subscriptionService := service.NewSubscriptionService(nil, &stubUserSubscriptionRepo{
		getActive: func(context.Context, int64, int64) (*service.UserSubscription, error) {
			return nil, errors.New("subscription not found")
		},
	}, nil, nil, cfg)
	t.Cleanup(subscriptionService.Stop)
	router := gin.New()
	router.Use(gin.HandlerFunc(NewAPIKeyAuthMiddleware(apiKeyService, subscriptionService, cfg)))
	router.POST("/v1/alpha/history/v2/list_windows", func(c *gin.Context) { c.Status(http.StatusOK) })

	req := httptest.NewRequest(http.MethodPost, "/v1/alpha/history/v2/list_windows", nil)
	req.Header.Set("Authorization", "Bearer "+key.Key)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	require.Equal(t, http.StatusForbidden, w.Code)
	requireAPIKeyAuthError(t, w, "SUBSCRIPTION_NOT_FOUND", "No active subscription found for this group")
}

func TestAPIKeyAuthCodexHistoryNotesExpiryPreservesOtherEndpointRules(t *testing.T) {
	gin.SetMode(gin.TestMode)
	past := time.Now().Add(-time.Hour)
	for _, mode := range []string{config.RunModeStandard, config.RunModeSimple} {
		for _, status := range []string{service.StatusActive, service.StatusAPIKeyExpired} {
			for _, endpoint := range []struct {
				method string
				path   string
			}{
				{method: http.MethodGet, path: "/v1/user-auth-credential/whoami"},
				{method: http.MethodGet, path: "/v1/usage"},
				{method: http.MethodGet, path: "/v1/sub2api/billing"},
				{method: http.MethodPost, path: "/v1/responses"},
			} {
				t.Run(mode+"/"+status+endpoint.path, func(t *testing.T) {
					key := &service.APIKey{
						ID: 101, UserID: 7, Key: "codex-other-expiry", Status: status, ExpiresAt: &past,
						User: &service.User{ID: 7, Role: service.RoleUser, Status: service.StatusActive, Balance: 10},
					}
					repo := &stubApiKeyRepo{getByKey: func(context.Context, string) (*service.APIKey, error) {
						clone := *key
						return &clone, nil
					}}
					cfg := &config.Config{RunMode: mode}
					svc := service.NewAPIKeyService(repo, nil, nil, nil, nil, nil, cfg)
					router := gin.New()
					router.Use(gin.HandlerFunc(NewAPIKeyAuthMiddleware(svc, nil, cfg)))
					router.Handle(endpoint.method, endpoint.path, func(c *gin.Context) { c.Status(http.StatusOK) })
					req := httptest.NewRequest(endpoint.method, endpoint.path, nil)
					req.Header.Set("Authorization", "Bearer "+key.Key)
					w := httptest.NewRecorder()
					router.ServeHTTP(w, req)
					wantCode := http.StatusOK
					if mode == config.RunModeStandard && endpoint.path == "/v1/responses" {
						wantCode = http.StatusForbidden
					}
					require.Equal(t, wantCode, w.Code, w.Body.String())
					if wantCode == http.StatusForbidden {
						requireAPIKeyAuthError(t, w, "API_KEY_EXPIRED", "API key 已过期")
					}
				})
			}
		}
	}
}
