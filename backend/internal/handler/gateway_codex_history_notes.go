package handler

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
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
	statusCode, headers := resp.StatusCode, resp.Header
	var body io.Reader = resp.Body
	var readErr error
	if path == "/alpha/notes/v2/thread_hint" && resp.StatusCode == http.StatusNotFound && service.IsNewCodexThreadHintRequest(c) &&
		(resp.Header.Get("Content-Encoding") == "" || resp.Header.Get("Content-Encoding") == "identity") {
		// A freshly allocated upstream session has no Notes resource yet. Match
		// only its bounded, empty Not found response; established sessions and
		// other errors must retain the upstream response and error classification.
		const maxEmptyHintBody = 1024
		var prefix []byte
		prefix, readErr = io.ReadAll(io.LimitReader(resp.Body, maxEmptyHintBody+1))
		body = io.MultiReader(bytes.NewReader(prefix), resp.Body)
		if readErr == nil && len(prefix) <= maxEmptyHintBody {
			if resp.ContentLength > 0 && int64(len(prefix)) != resp.ContentLength {
				readErr = io.ErrUnexpectedEOF
			} else if isCodexEmptyThreadHintNotFound(prefix) {
				const emptyHint = `{"text":""}`
				statusCode = http.StatusOK
				body = strings.NewReader(emptyHint)
				headers = resp.Header.Clone()
				if headers == nil {
					headers = make(http.Header)
				}
				for _, name := range []string{"Content-Encoding", "Transfer-Encoding", "ETag", "Content-MD5", "Digest"} {
					headers.Del(name)
				}
				headers.Set("Content-Type", "application/json")
				headers.Set("Content-Length", strconv.Itoa(len(emptyHint)))
			}
		}
	}
	for name, values := range headers {
		for _, value := range values {
			c.Header(name, value)
		}
	}
	c.Status(statusCode)
	n, copyErr := io.Copy(c.Writer, body)
	status, errorKind := "delivered", ""
	if statusCode < 200 || statusCode >= 300 {
		status, errorKind = "rejected", "upstream_rejected"
	}
	if copyErr != nil || readErr != nil {
		status, errorKind = "failed", "delivery_error"
	}
	service.RecordCodexContextManagementResult(c, kind, path, status, statusCode, n, errorKind)
}

func isCodexEmptyThreadHintNotFound(body []byte) bool {
	if len(bytes.TrimSpace(body)) == 0 {
		return true
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(body, &fields) != nil || len(fields) != 1 {
		return false
	}
	var detail string
	return json.Unmarshal(fields["detail"], &detail) == nil && strings.EqualFold(strings.TrimSpace(detail), "not found")
}
