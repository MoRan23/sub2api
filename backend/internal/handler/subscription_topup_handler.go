package handler

import (
	"strconv"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/gin-gonic/gin"
)

func (h *PaymentHandler) ListTopupProducts(c *gin.Context) {
	if _, ok := requireAuth(c); !ok {
		return
	}
	cfg, err := h.configService.GetPaymentConfig(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	if !cfg.Enabled {
		response.ErrorFrom(c, infraerrors.Forbidden("PAYMENT_DISABLED", "payment system is disabled"))
		return
	}
	products, err := h.paymentService.SubscriptionTopupService().ListProducts(c.Request.Context(), false)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, products)
}

func (h *PaymentHandler) GetTopupOrderCards(c *gin.Context) {
	subject, ok := requireAuth(c)
	if !ok {
		return
	}
	orderID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || orderID <= 0 {
		response.BadRequest(c, "Invalid order ID")
		return
	}
	delivery, err := h.paymentService.SubscriptionTopupService().GetOrderCards(c.Request.Context(), orderID, subject.UserID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	response.Success(c, delivery)
}
