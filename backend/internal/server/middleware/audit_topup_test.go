package middleware

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestSubscriptionTopupImportOmitsSecretsWithoutConsumingBody(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repository := &auditCaptureRepository{}
	auditService := service.NewAuditLogService(repository, nil)
	auditService.Start()
	router := gin.New()
	router.Use(gin.HandlerFunc(NewAuditLogMiddleware(auditService)))
	router.POST("/api/v1/admin/payment/topup-skus/:id/cards/import", func(c *gin.Context) {
		var input struct {
			Codes []string `json:"codes"`
		}
		require.NoError(t, c.ShouldBindJSON(&input))
		require.Equal(t, []string{"topup-secret-canary"}, input.Codes)
		c.JSON(http.StatusOK, gin.H{"imported": 1})
	})
	request := httptest.NewRequest(http.MethodPost, "/api/v1/admin/payment/topup-skus/4/cards/import", bytes.NewBufferString(`{"codes":["topup-secret-canary"]}`))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	auditService.Stop()
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Len(t, repository.logs, 1)
	require.Equal(t, "<credential-bearing body omitted>", repository.logs[0].RequestBody)
}
