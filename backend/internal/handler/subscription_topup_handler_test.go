package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestSubscriptionTopupEndpointsRequireAuthenticatedOwner(t *testing.T) {
	handler := &PaymentHandler{}
	for _, method := range []func(*gin.Context){handler.ListTopupProducts, handler.GetTopupOrderCards} {
		writer := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(writer)
		ctx.Request = httptest.NewRequest(http.MethodGet, "/", nil)
		method(ctx)
		require.Equal(t, http.StatusUnauthorized, writer.Code)
	}
}

func TestSubscriptionTopupOrderCardsRejectsInvalidIDBeforeLoading(t *testing.T) {
	handler := &PaymentHandler{}
	for _, id := range []string{"0", "-1", "not-a-number", "9223372036854775808"} {
		writer := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(writer)
		ctx.Request = httptest.NewRequest(http.MethodGet, "/", nil)
		ctx.Params = gin.Params{{Key: "id", Value: id}}
		ctx.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 1})
		handler.GetTopupOrderCards(ctx)
		require.Equal(t, http.StatusBadRequest, writer.Code)
	}
}
