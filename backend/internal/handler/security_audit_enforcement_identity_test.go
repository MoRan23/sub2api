package handler

import (
	"net/http/httptest"
	"testing"

	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestSecurityAuditEnforcementIdentityIsServerOwnedAndStable(t *testing.T) {
	gin.SetMode(gin.TestMode)
	makeContext := func() *gin.Context {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
		c.Request.Header.Set("X-Request-ID", "same-client-id")
		return c
	}
	c := makeContext()
	first := buildSecurityAuditRequest(c, nil, middleware2.AuthSubject{UserID: 5}, "responses", "model", nil, "http")
	retry := buildSecurityAuditRequest(c, nil, middleware2.AuthSubject{UserID: 5}, "responses", "other", []byte(`{}`), "http")
	require.NotEmpty(t, first.EnforcementID)
	require.NotEqual(t, "same-client-id", first.EnforcementID)
	require.Equal(t, first.EnforcementID, retry.EnforcementID)
	require.Equal(t, first.EnforcementStartedAt, retry.EnforcementStartedAt)
	require.False(t, first.EnforcementStartedAt.IsZero())
	second := buildSecurityAuditRequest(makeContext(), nil, middleware2.AuthSubject{UserID: 5}, "responses", "model", nil, "http")
	require.NotEqual(t, first.EnforcementID, second.EnforcementID)
}

func TestSecurityAuditEnforcementIdentityChangesOnlyOnWSTurn(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("GET", "/v1/responses", nil)
	first := securityAuditEnforcementRequest(c, "first_turn")
	c.Set(securityAuditWSTurnContextKey, 1)
	require.Equal(t, first, securityAuditEnforcementRequest(c, "first_turn"))
	c.Set(securityAuditWSTurnContextKey, 2)
	second := securityAuditEnforcementRequest(c, "subsequent_turn")
	require.NotEqual(t, first.id, second.id)
	require.Equal(t, second, securityAuditEnforcementRequest(c, "subsequent_turn"))
}
