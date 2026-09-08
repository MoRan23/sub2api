package handler

import (
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// CodexHistoryNotes proxies Codex's auxiliary history/notes calls. These
// requests are intentionally outside model billing and account concurrency;
// the gateway service still performs normal PAT/API-key authentication and
// selects an eligible OpenAI account.
func (h *GatewayHandler) CodexHistoryNotes(c *gin.Context) {
	path := c.Request.URL.Path
	kind := "history"
	if strings.Contains(path, "/alpha/notes/") {
		kind = "notes"
	}
	service.BeginCodexContextManagementObservation(c, kind, path)
	if h == nil || h.settingService == nil || !h.settingService.IsOpenAICodexPATContextManagementEnabled(c.Request.Context()) {
		c.Status(http.StatusNotFound)
		service.RecordCodexContextManagementResult(c, kind, path, "disabled", http.StatusNotFound, 0, "feature_disabled")
		return
	}
	apiKey, ok := middleware.GetAPIKeyFromContext(c)
	if !ok || apiKey == nil || h.openAIGatewayService == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": gin.H{"type": "authentication_error", "message": "Invalid API key"}})
		service.RecordCodexContextManagementResult(c, kind, path, "rejected", http.StatusUnauthorized, 0, "authentication_error")
		return
	}
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"type": "invalid_request_error", "message": "Failed to read request body"}})
		service.RecordCodexContextManagementResult(c, kind, path, "failed", http.StatusBadRequest, 0, "request_body_error")
		return
	}
	// Use the physical URL to preserve the fixed /alpha/history/v2 or
	// /alpha/notes/v2 prefix. Gin's wildcard parameter contains only the
	// suffix (for example "/list_windows"), so rebuilding from c.Param would
	// incorrectly produce "/alpha/list_windows".
	path = ""
	if c.Request != nil && c.Request.URL != nil {
		physical := c.Request.URL.Path
		if idx := strings.Index(physical, "/alpha/"); idx >= 0 {
			path = physical[idx:]
		}
	}
	if path == "" {
		path = c.Param("path")
		if path == "" || path[0] != '/' {
			path = "/" + path
		}
	}
	resp, err := h.openAIGatewayService.ForwardCodexHistoryNotes(c.Request.Context(), c, apiKey, path, body)
	if err != nil {
		if errors.Is(err, service.ErrCodexHistoryNotesInvalidContext) {
			c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"type": "invalid_request_error", "message": "Invalid History/Notes context"}})
			service.RecordCodexContextManagementResult(c, kind, path, "rejected", http.StatusBadRequest, 0, "invalid_context")
			return
		}
		c.JSON(http.StatusBadGateway, gin.H{"error": gin.H{"type": "upstream_error", "message": err.Error()}})
		service.RecordCodexContextManagementResult(c, kind, path, "failed", http.StatusBadGateway, 0, "upstream_error")
		return
	}
	if resp == nil {
		c.Status(http.StatusBadGateway)
		service.RecordCodexContextManagementResult(c, kind, path, "failed", http.StatusBadGateway, 0, "empty_response")
		return
	}
	writeCodexHistoryNotesResponse(c, resp, kind, path)
}

func writeCodexHistoryNotesResponse(c *gin.Context, resp *http.Response, kind, path string) {
	defer resp.Body.Close()
	for name, values := range resp.Header {
		for _, value := range values {
			c.Header(name, value)
		}
	}
	c.Status(resp.StatusCode)
	n, copyErr := io.Copy(c.Writer, resp.Body)
	status, errorKind := "delivered", ""
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		status, errorKind = "rejected", "upstream_rejected"
	}
	if copyErr != nil {
		status, errorKind = "failed", "delivery_error"
	}
	service.RecordCodexContextManagementResult(c, kind, path, status, resp.StatusCode, n, errorKind)
}
