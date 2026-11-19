package service

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// Continuation policy failures are local request errors, never account-health
// failures or a reason to retry another credential.
func writeOpenAIHTTPReasoningContinuationError(c *gin.Context, err error) error {
	if c != nil {
		MarkOpsClientBusinessLimited(c, OpsClientBusinessLimitedReasonLocalPolicyDenied)
		c.JSON(http.StatusForbidden, gin.H{"error": gin.H{
			"type": "permission_error", "code": "reasoning_effort_continuation_unavailable", "message": err.Error(),
		}})
	}
	return err
}
