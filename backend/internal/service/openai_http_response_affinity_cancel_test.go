package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestOpenAIGatewayService_BindHTTPResponseAccount_CanceledRequestRestoresSharedAffinity(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, expired := range []bool{false, true} {
		name := "canceled"
		if expired {
			name = "deadline exceeded"
		}
		t.Run(name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			groupID := int64(4201)
			c.Set("api_key", &APIKey{ID: 501, GroupID: &groupID})
			SetOpenAIHTTPResponseOwner(c, 601, 501)

			requestCtx, cancel := context.WithCancel(context.Background())
			if expired {
				cancel()
				requestCtx, cancel = context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
			}
			cancel()
			require.Error(t, requestCtx.Err())

			cache := &responseBindContextProbeCache{}
			writer := &OpenAIGatewayService{cache: cache}
			account := &Account{ID: 37001, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
			const responseID = "resp_http_shared_after_cancel"
			writer.bindHTTPResponseAccount(requestCtx, c, account, responseID)

			// A fresh instance has no in-memory bindings: continuation routing and
			// authorization must both recover from the shared cache after cancellation.
			reader := &OpenAIGatewayService{cache: cache}
			got, err := reader.getOpenAIWSStateStore().GetResponseAccount(context.Background(), groupID, responseID)
			require.NoError(t, err)
			require.Equal(t, account.ID, got)
			owned, err := reader.ValidateOpenAIHTTPResponseOwner(context.Background(), groupID, responseID, 601, 502)
			require.NoError(t, err)
			require.True(t, owned, "another key of the same user can continue the response")
			owned, err = reader.ValidateOpenAIHTTPResponseOwner(context.Background(), groupID, responseID, 602, 501)
			require.NoError(t, err)
			require.False(t, owned, "cancellation must not weaken the response owner check")
		})
	}
}
