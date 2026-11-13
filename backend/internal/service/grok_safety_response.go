package service

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// Preserve the upstream refusal identity, without adding billing details to the
// public error or treating it as an account-health failure.
func writeGrokSafetyRejectionResponse(c *gin.Context, status int, body []byte) {
	WriteGrokSafetyRejectionResponse(c, status, body)
}

func WriteGrokSafetyRejectionResponse(c *gin.Context, status int, body []byte) {
	if c == nil || IsResponseCommitted(c) {
		return
	}
	upstreamStatus := status
	if status < 400 {
		status = http.StatusForbidden
	}
	message := extractUpstreamErrorMessage(body)
	upstreamError := gjson.GetBytes(body, "error")
	if !upstreamError.IsObject() {
		upstreamError = gjson.GetBytes(body, "response.error")
	}
	if message == "" {
		message = upstreamError.Get("message").String()
	}
	if message == "" {
		message = "Request rejected by the upstream safety review"
	}
	setOpsUpstreamError(c, upstreamStatus, message, "")
	MarkResponseCommitted(c)
	isMessages := c.Request != nil && c.Request.URL != nil && strings.HasSuffix(strings.TrimRight(c.Request.URL.Path, "/"), "/messages")
	if !isMessages && gjson.GetBytes(body, "error").IsObject() && gjson.ValidBytes(body) {
		// Native HTTP errors retain all upstream fields and original bytes.
		c.Data(status, "application/json", body)
		return
	}
	var errorValue any = gin.H{"code": GrokSafetyRejectionCode, "type": "invalid_request_error", "message": message}
	if upstreamError.IsObject() && gjson.ValidBytes(body) {
		// Responses events carry the same error object under response.error.
		errorValue = json.RawMessage(upstreamError.Raw)
	}
	payload := gin.H{"error": errorValue}
	if isMessages {
		payload["type"] = "error"
	}
	c.JSON(status, payload)
}
