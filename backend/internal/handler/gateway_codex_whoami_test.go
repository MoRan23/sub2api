package handler

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestCodexPATIdentityUUIDIsStableAndScoped(t *testing.T) {
	require.Equal(t, codexPATIdentityUUID("user", 1), codexPATIdentityUUID("user", 1))
	require.NotEqual(t, codexPATIdentityUUID("user", 1), codexPATIdentityUUID("user", 2))
	require.NotEqual(t, codexPATIdentityUUID("user", 1), codexPATIdentityUUID("account", 1))
}

func TestGatewayHandlerCodexPATWhoamiReturnsStableUserIdentity(t *testing.T) {
	gin.SetMode(gin.TestMode)
	user := &service.User{ID: 42, Email: "pat-user@example.com"}
	apiKey := &service.APIKey{User: user}

	newContext := func() (*gin.Context, *httptest.ResponseRecorder) {
		recorder := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(recorder)
		ctx.Set(string(middleware2.ContextKeyAPIKey), apiKey)
		return ctx, recorder
	}

	h := &GatewayHandler{}
	ctx, first := newContext()
	h.CodexPATWhoami(ctx)
	require.Equal(t, 200, first.Code)
	var body codexPATWhoamiResponse
	require.NoError(t, json.Unmarshal(first.Body.Bytes(), &body))
	require.Equal(t, "pat-user@example.com", *body.Email)
	require.Equal(t, codexPATIdentityUUID("user", 42), body.ChatGPTUserID)
	require.Equal(t, codexPATIdentityUUID("account", 42), body.ChatGPTAccountID)
	require.Equal(t, codexPATWhoamiDefaultPlanType, body.ChatGPTPlanType)
	require.False(t, body.ChatGPTAccountIsFedRAMP)

	ctx, second := newContext()
	h.CodexPATWhoami(ctx)
	require.Equal(t, first.Body.String(), second.Body.String())
}

func TestGatewayHandlerCodexPATWhoamiAllowsMissingEmail(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{User: &service.User{ID: 7}})

	(&GatewayHandler{}).CodexPATWhoami(ctx)
	require.Equal(t, 200, recorder.Code)
	require.Contains(t, recorder.Body.String(), `"email":null`)
}
