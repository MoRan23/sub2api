package service

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type accountTestProxyGuardRepository struct {
	AccountRepository
	account *Account
}

func (r *accountTestProxyGuardRepository) GetByID(context.Context, int64) (*Account, error) {
	return r.account, nil
}

func TestAccountConnectionRejectsMissingBoundProxyBeforeDispatch(t *testing.T) {
	proxyID := int64(42)
	for _, platform := range []string{PlatformOpenAI, PlatformAnthropic, PlatformGemini, PlatformGrok} {
		t.Run(platform, func(t *testing.T) {
			account := &Account{ID: 1, Platform: platform, Type: AccountTypeAPIKey, ProxyID: &proxyID}
			svc := &AccountTestService{accountRepo: &accountTestProxyGuardRepository{account: account}}
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest("POST", "/test", nil)
			err := svc.TestAccountConnection(c, 1, "test-model", "", "")
			require.ErrorContains(t, err, "configured account proxy is unavailable")
			require.Contains(t, rec.Body.String(), `"type":"error"`)
		})
	}
}
