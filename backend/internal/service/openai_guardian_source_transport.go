package service

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"
)

type openAIGuardianSourceHTTPContextKey struct{}

type openAIGuardianSourceHTTPSnapshot struct {
	accountID int64
	plan      OpenAIOAuthIdentityPlan
}

// Freeze only the source identity needed at the physical send boundary. The
// request may receive additional header policy after construction, so neither
// a builder nor a fingerprint observation is evidence of its final identity.
func markOpenAIGuardianSourceHTTPRequest(request *http.Request, c *gin.Context, account *Account) *http.Request {
	if request == nil || account == nil || !account.IsOpenAIOAuth() {
		return request
	}
	plan, ok := OpenAIOAuthIdentityPlanFromContext(c)
	if !ok || !plan.TurnIdentityEnabled || plan.Capture.Logical.SessionKey == "" ||
		plan.Capture.Logical.SessionKey != plan.Capture.Logical.ThreadKey ||
		plan.TurnIdentity.SessionID == plan.TurnIdentity.ThreadID {
		return request
	}
	snapshot := openAIGuardianSourceHTTPSnapshot{accountID: account.ID, plan: plan}
	return request.WithContext(context.WithValue(request.Context(), openAIGuardianSourceHTTPContextKey{}, snapshot))
}

func recordOpenAIGuardianSourceHTTPRequest(request *http.Request, account *Account) {
	if request == nil || account == nil || !account.IsOpenAIOAuth() {
		return
	}
	snapshot, ok := request.Context().Value(openAIGuardianSourceHTTPContextKey{}).(openAIGuardianSourceHTTPSnapshot)
	if !ok || snapshot.accountID != account.ID {
		return
	}
	body := openAIUpstreamRequestBodySnapshot(request, nil)
	if len(body) == 0 {
		return
	}
	recordOpenAICodexGuardianSourceThread(snapshot.plan, request.Header, body)
}
