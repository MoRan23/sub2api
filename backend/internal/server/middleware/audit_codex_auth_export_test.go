package middleware

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestCodexAuthExportSensitiveReadAuditsWithoutResponseCredentials(t *testing.T) {
	gin.SetMode(gin.TestMode)
	require.Equal(t, "admin.accounts.codex_auth.export", auditSensitiveReads["GET /api/v1/admin/accounts/:id/codex-auth"])
	repo := &auditCaptureRepository{}
	audit := service.NewAuditLogService(repo, nil)
	audit.Start()
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(string(ContextKeyUser), AuthSubject{UserID: 7})
		c.Set(string(ContextKeyUserRole), "admin")
		c.Next()
	})
	router.Use(gin.HandlerFunc(NewAuditLogMiddleware(audit)))
	router.GET("/api/v1/admin/accounts/:id/codex-auth", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"data": gin.H{"auth": gin.H{"tokens": gin.H{"access_token": "export-response-secret"}}}})
	})
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/admin/accounts/42/codex-auth", nil))
	audit.Stop()
	require.Equal(t, http.StatusOK, rec.Code)
	repo.mu.Lock()
	defer repo.mu.Unlock()
	require.Len(t, repo.logs, 1)
	require.Equal(t, "admin.accounts.codex_auth.export", repo.logs[0].Action)
	require.Equal(t, int64(7), *repo.logs[0].ActorUserID)
	require.Empty(t, repo.logs[0].RequestBody)
	serialized, err := json.Marshal(repo.logs[0])
	require.NoError(t, err)
	require.NotContains(t, string(serialized), "export-response-secret")
}
