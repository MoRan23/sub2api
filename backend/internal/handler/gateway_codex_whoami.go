package handler

import (
	"fmt"
	"net/http"
	"strings"

	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// codexPATWhoamiResponse is the small identity document consumed by Codex when
// personal_access_token auth is configured. Values are derived from the
// authenticated Sub2API user; internal user and API-key identifiers are never
// returned directly.
type codexPATWhoamiResponse struct {
	Email                   *string `json:"email"`
	ChatGPTUserID           string  `json:"chatgpt_user_id"`
	ChatGPTAccountID        string  `json:"chatgpt_account_id"`
	ChatGPTPlanType         string  `json:"chatgpt_plan_type"`
	ChatGPTAccountIsFedRAMP bool    `json:"chatgpt_account_is_fedramp"`
}

// Codex's eligibility check accepts plus/pro/pro_lite. Sub2API users do not
// currently have a persisted ChatGPT plan field, so plus is the stable default
// advertised by this compatibility endpoint until an explicit per-user plan
// setting is introduced.
const codexPATWhoamiDefaultPlanType = "plus"

// CodexPATWhoami handles GET /v1/user-auth-credential/whoami. Codex uses this
// endpoint to validate a PAT and determine whether context-management features
// are available. It intentionally requires the regular gateway API-key
// middleware, so disabled keys and inactive users cannot probe account data.
func (h *GatewayHandler) CodexPATWhoami(c *gin.Context) {
	const path = "/v1/user-auth-credential/whoami"
	service.BeginCodexContextManagementObservation(c, "whoami", path)
	apiKey, ok := middleware2.GetAPIKeyFromContext(c)
	if !ok || apiKey == nil || apiKey.User == nil {
		h.errorResponse(c, http.StatusUnauthorized, "authentication_error", "Invalid API key")
		service.RecordCodexContextManagementResult(c, "whoami", path, "rejected", http.StatusUnauthorized, 0, "authentication_error")
		return
	}

	user := apiKey.User
	email := strings.TrimSpace(user.Email)
	var emailPtr *string
	if email != "" {
		emailPtr = &email
	}

	// Codex only needs stable opaque identifiers. UUIDv5 keeps the value stable
	// across requests without exposing the database user ID or API key.
	userID := codexPATIdentityUUID("user", user.ID)
	accountID := codexPATIdentityUUID("account", user.ID)

	c.Header("Cache-Control", "no-store")
	previousErrors := len(c.Errors)
	c.JSON(http.StatusOK, codexPATWhoamiResponse{
		Email:                   emailPtr,
		ChatGPTUserID:           userID,
		ChatGPTAccountID:        accountID,
		ChatGPTPlanType:         codexPATWhoamiDefaultPlanType,
		ChatGPTAccountIsFedRAMP: false,
	})
	status, errorKind := "delivered", ""
	if len(c.Errors) > previousErrors {
		status, errorKind = "failed", "delivery_error"
	}
	service.RecordCodexContextManagementResult(c, "whoami", path, status, http.StatusOK, int64(c.Writer.Size()), errorKind)
}

func codexPATIdentityUUID(kind string, userID int64) string {
	// A private namespace prevents collisions with IDs from other integrations.
	ns := uuid.MustParse("4f8f4f9a-37e5-5a6e-8f28-28c8c9f6d1ad")
	name := fmt.Sprintf("%s:%d", kind, userID)
	return uuid.NewSHA1(ns, []byte(name)).String()
}
