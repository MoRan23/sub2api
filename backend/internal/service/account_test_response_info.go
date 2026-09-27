package service

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

const accountTestResponseInfoContextKey = "account_test_response_info"

// Manual connection tests expose upstream declarations, never opaque tokens.
type accountTestResponseInfo struct {
	UpstreamModel    string              `json:"upstream_model,omitempty"`
	ResponseEvidence *CodexModelEvidence `json:"response_evidence,omitempty"`
	observer         codexModelEvidenceObserver
	sent             bool
}

func beginAccountTestResponseInfo(c *gin.Context, _ *Account, headers http.Header) {
	if c == nil {
		return
	}
	info := &accountTestResponseInfo{}
	info.observer.observeHeaders(headers, "response")
	c.Set(accountTestResponseInfoContextKey, info)
}

func getAccountTestResponseInfo(c *gin.Context) *accountTestResponseInfo {
	if c == nil {
		return nil
	}
	value, _ := c.Get(accountTestResponseInfoContextKey)
	info, _ := value.(*accountTestResponseInfo)
	return info
}

func observeAccountTestResponseInfo(c *gin.Context, payload []byte) {
	info := getAccountTestResponseInfo(c)
	if info == nil || info.sent {
		return
	}
	info.observer.observePayload(payload)
	info.UpstreamModel = info.observer.model.Model()
}

func (s *AccountTestService) sendAccountTestResponseInfo(c *gin.Context) {
	if info := getAccountTestResponseInfo(c); info != nil && !info.sent {
		info.sent = true
		// This observer does not receive the final outbound model. Preserve the
		// declaration without inventing a comparison to the requested model.
		evidence := info.observer.snapshot("")
		if evidence.UpstreamResponseModel != "" {
			evidence.ModelRelation = "unknown"
		}
		info.ResponseEvidence = &evidence
		s.sendEvent(c, TestEvent{Type: "response_info", Data: info})
	}
}
