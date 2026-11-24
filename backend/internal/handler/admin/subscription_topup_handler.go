package admin

import (
	"net/http"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

func (h *PaymentHandler) ListTopupProducts(c *gin.Context) {
	products, err := h.paymentService.SubscriptionTopupService().ListProducts(c.Request.Context(), true)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, products)
}

func (h *PaymentHandler) CreateTopupProduct(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 256<<10)
	var input service.TopupProductInput
	if err := c.ShouldBindJSON(&input); err != nil {
		response.BadRequest(c, "Invalid product")
		return
	}
	product, err := h.paymentService.SubscriptionTopupService().CreateProduct(c.Request.Context(), input)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, product)
}

func (h *PaymentHandler) UpdateTopupProduct(c *gin.Context) {
	id, ok := parseIDParam(c, "id")
	if !ok {
		return
	}
	var input service.TopupProductInput
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 256<<10)
	if err := c.ShouldBindJSON(&input); err != nil {
		response.BadRequest(c, "Invalid product")
		return
	}
	product, err := h.paymentService.SubscriptionTopupService().UpdateProduct(c.Request.Context(), id, input)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, product)
}

func (h *PaymentHandler) ArchiveTopupProduct(c *gin.Context) {
	id, ok := parseIDParam(c, "id")
	if !ok {
		return
	}
	if err := h.paymentService.SubscriptionTopupService().ArchiveProduct(c.Request.Context(), id); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"archived": true})
}

func (h *PaymentHandler) CreateTopupSKU(c *gin.Context) {
	id, ok := parseIDParam(c, "id")
	if !ok {
		return
	}
	var input service.TopupSKUInput
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 16<<10)
	if err := c.ShouldBindJSON(&input); err != nil {
		response.BadRequest(c, "Invalid specification")
		return
	}
	sku, err := h.paymentService.SubscriptionTopupService().CreateSKU(c.Request.Context(), id, input)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, sku)
}

func (h *PaymentHandler) UpdateTopupSKU(c *gin.Context) {
	id, ok := parseIDParam(c, "id")
	if !ok {
		return
	}
	var input service.TopupSKUInput
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 16<<10)
	if err := c.ShouldBindJSON(&input); err != nil {
		response.BadRequest(c, "Invalid specification")
		return
	}
	sku, err := h.paymentService.SubscriptionTopupService().UpdateSKU(c.Request.Context(), id, input)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, sku)
}

func (h *PaymentHandler) ArchiveTopupSKU(c *gin.Context) {
	id, ok := parseIDParam(c, "id")
	if !ok {
		return
	}
	if err := h.paymentService.SubscriptionTopupService().ArchiveSKU(c.Request.Context(), id); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"archived": true})
}

func (h *PaymentHandler) ListTopupCards(c *gin.Context) {
	id, ok := parseIDParam(c, "id")
	if !ok {
		return
	}
	page, pageSize := response.ParsePagination(c)
	cards, err := h.paymentService.SubscriptionTopupService().ListCards(c.Request.Context(), id, c.Query("status"), page, pageSize)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, cards)
}

func (h *PaymentHandler) ImportTopupCards(c *gin.Context) {
	id, ok := parseIDParam(c, "id")
	if !ok {
		return
	}
	var input struct {
		Codes []string `json:"codes" binding:"required,min=1,max=1000"`
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 4<<20)
	if err := c.ShouldBindJSON(&input); err != nil {
		response.BadRequest(c, "Provide 1 to 1000 CDKs")
		return
	}
	count, err := h.paymentService.SubscriptionTopupService().ImportCards(c.Request.Context(), id, input.Codes)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"imported": count})
}

func (h *PaymentHandler) RevealTopupCard(c *gin.Context) {
	id, ok := parseIDParam(c, "id")
	if !ok {
		return
	}
	code, err := h.paymentService.SubscriptionTopupService().RevealCard(c.Request.Context(), id)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	response.Success(c, gin.H{"code": code})
}

func (h *PaymentHandler) SetTopupCardEnabled(c *gin.Context) {
	id, ok := parseIDParam(c, "id")
	if !ok {
		return
	}
	var input struct {
		Enabled *bool `json:"enabled" binding:"required"`
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 1<<10)
	if err := c.ShouldBindJSON(&input); err != nil {
		response.BadRequest(c, "enabled is required")
		return
	}
	if err := h.paymentService.SubscriptionTopupService().SetCardEnabled(c.Request.Context(), id, *input.Enabled); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"updated": true})
}

func (h *PaymentHandler) ResendTopupCards(c *gin.Context) {
	id, ok := parseIDParam(c, "id")
	if !ok {
		return
	}
	if err := h.paymentService.SubscriptionTopupService().QueueMailResend(c.Request.Context(), id); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"queued": true})
}
