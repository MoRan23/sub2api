//go:build unit

package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/adminaccess"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type giftSubtractAdminStub struct {
	service.AdminService
	ordinary, gift float64
	writes         int
	err            error
}

func (s *giftSubtractAdminStub) UpdateUserBalance(_ context.Context, id int64, ordinary, gift float64, operation, notes string) (*service.User, error) {
	s.writes++
	s.ordinary, s.gift = ordinary, gift
	if s.err != nil {
		return nil, s.err
	}
	return &service.User{ID: id, Balance: 10 - ordinary, GiftBalance: 10 - gift}, nil
}

func giftSubtractHandler(t *testing.T, svc *giftSubtractAdminStub) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	previous := service.DefaultIdempotencyCoordinator()
	service.SetDefaultIdempotencyCoordinator(service.NewIdempotencyCoordinator(newMemoryIdempotencyRepoStub(), service.DefaultIdempotencyConfig()))
	t.Cleanup(func() { service.SetDefaultIdempotencyCoordinator(previous) })
	h := NewUserHandler(svc, nil, nil, nil, nil, nil, nil)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 9})
		if c.GetHeader("X-Test-Scoped") == "true" {
			c.Request = c.Request.WithContext(adminaccess.WithPrincipal(c.Request.Context(), &adminaccess.Principal{
				KeyID: "test-key", Policy: adminaccess.Policy{GlobalPermissions: []string{"users.balance.write"}},
			}))
		}
		c.Next()
	})
	router.POST("/users/:id/balance", h.UpdateBalance)
	return router
}

func TestUserBalanceGiftSubtractValidation(t *testing.T) {
	tests := []struct {
		name, body     string
		status         int
		ordinary, gift float64
	}{
		{"legacy ordinary", `{"operation":"subtract","balance":2}`, 200, 2, 0},
		{"gift only", `{"operation":"subtract","gift_balance":3}`, 200, 0, 3},
		{"both wallets", `{"operation":"subtract","balance":2,"gift_balance":3}`, 200, 2, 3},
		{"eight decimals", `{"operation":"subtract","balance":0.12345678,"gift_balance":0.00000001}`, 200, 0.12345678, 0.00000001},
		{"zero", `{"operation":"subtract","balance":0,"gift_balance":0}`, 400, 0, 0},
		{"negative ordinary", `{"operation":"subtract","balance":-1,"gift_balance":3}`, 400, 0, 0},
		{"negative gift", `{"operation":"subtract","balance":1,"gift_balance":-3}`, 400, 0, 0},
		{"invalid type", `{"operation":"subtract","gift_balance":"3"}`, 400, 0, 0},
		{"infinite", `{"operation":"subtract","gift_balance":1e999}`, 400, 0, 0},
		{"set still rejects gift", `{"operation":"set","balance":1,"gift_balance":3}`, 400, 0, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := &giftSubtractAdminStub{}
			router := giftSubtractHandler(t, svc)
			req := httptest.NewRequest(http.MethodPost, "/users/7/balance", strings.NewReader(tt.body))
			req.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			router.ServeHTTP(response, req)
			require.Equal(t, tt.status, response.Code, response.Body.String())
			if tt.status == http.StatusOK {
				require.Equal(t, 1, svc.writes)
				require.Equal(t, tt.ordinary, svc.ordinary)
				require.Equal(t, tt.gift, svc.gift)
			} else {
				require.Zero(t, svc.writes)
			}
		})
	}
}

func TestUserBalanceGiftSubtractIdempotencyAndScopedResponse(t *testing.T) {
	svc := &giftSubtractAdminStub{}
	router := giftSubtractHandler(t, svc)
	for i, body := range []string{
		`{"operation":"subtract","balance":1,"gift_balance":2}`,
		`{"operation":"subtract","balance":1,"gift_balance":2}`,
		`{"operation":"subtract","balance":1,"gift_balance":3}`,
	} {
		req := httptest.NewRequest(http.MethodPost, "/users/7/balance", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", "gift-subtract-once")
		if i > 0 {
			req.Header.Set("X-Test-Scoped", "true")
		}
		response := httptest.NewRecorder()
		router.ServeHTTP(response, req)
		if i == 2 {
			require.Equal(t, http.StatusConflict, response.Code, response.Body.String())
		} else {
			require.Equal(t, http.StatusOK, response.Code, response.Body.String())
			if i == 1 {
				require.Equal(t, "true", response.Header().Get("X-Idempotency-Replayed"))
				require.NotContains(t, response.Body.String(), "gift_balance")
				require.NotContains(t, response.Body.String(), "balance")
			}
		}
	}
	require.Equal(t, 1, svc.writes)
}

func TestUserBalanceGiftSubtractInsufficientWalletResponse(t *testing.T) {
	for _, tt := range []struct {
		err    error
		reason string
	}{
		{service.ErrOrdinaryBalanceInsufficient, "INSUFFICIENT_ORDINARY_BALANCE"},
		{service.ErrGiftBalanceInsufficient, "INSUFFICIENT_GIFT_BALANCE"},
	} {
		t.Run(tt.reason, func(t *testing.T) {
			svc := &giftSubtractAdminStub{err: tt.err}
			router := giftSubtractHandler(t, svc)
			req := httptest.NewRequest(http.MethodPost, "/users/7/balance", strings.NewReader(`{"operation":"subtract","balance":1,"gift_balance":2}`))
			req.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			router.ServeHTTP(response, req)
			require.Equal(t, http.StatusBadRequest, response.Code, response.Body.String())
			require.Contains(t, response.Body.String(), tt.reason)
		})
	}
}
