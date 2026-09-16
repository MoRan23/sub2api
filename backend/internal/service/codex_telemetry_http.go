package service

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/tidwall/gjson"
)

type codexTelemetryHTTPContextKey struct{}
type codexTelemetryHTTPResponseKey struct{}

// Only ordinary inference entry points opt into telemetry. Keep the caller's
// lifetime separate from the detached context used by the inference transport.
func markCodexTelemetryHTTPRequest(request *http.Request, caller context.Context) *http.Request {
	if request == nil || caller == nil {
		return request
	}
	return request.WithContext(context.WithValue(request.Context(), codexTelemetryHTTPContextKey{}, caller))
}

func (s *OpenAIGatewayService) beginCodexTelemetryHTTPRequest(request *http.Request, proxyURL string, account *Account) *CodexTelemetryAttempt {
	if s == nil || !s.codexTelemetry.Enabled() || account == nil || !account.IsOpenAIOAuth() || request == nil || request.GetBody == nil || request.URL == nil || !strings.HasSuffix(strings.TrimRight(request.URL.Path, "/"), "/responses") {
		return nil
	}
	caller, ok := request.Context().Value(codexTelemetryHTTPContextKey{}).(context.Context)
	if !ok {
		return nil
	}
	// Read a separate final request snapshot. The inference body is not consumed
	// or replaced, and Begin retains only the extracted scalar identity metadata.
	reader, err := request.GetBody()
	if err != nil {
		return nil
	}
	body, err := io.ReadAll(reader)
	_ = reader.Close()
	if err != nil {
		return nil
	}
	return s.beginCodexTelemetryFromWire(caller, account, request.Header, body, proxyURL, false)
}

type codexTelemetryHTTPAttempt interface {
	Finish(CodexTelemetryResult)
	Retry(CodexTelemetryResult)
}

func observeCodexTelemetryHTTPResponse(attempt codexTelemetryHTTPAttempt, response *http.Response, sendErr error) {
	if attempt == nil {
		return
	}
	status := 0
	if response != nil {
		status = response.StatusCode
	}
	if sendErr != nil || response == nil || status < 200 || status >= 300 {
		attempt.Retry(CodexTelemetryResult{Status: "failed", HTTPStatus: status, FinishedAt: time.Now()})
		return
	}
	if response.Body == nil {
		attempt.Retry(CodexTelemetryResult{Status: "interrupted", HTTPStatus: status, FinishedAt: time.Now()})
		return
	}
	collector := &codexTelemetryHTTPCollector{attempt: attempt, status: status}
	request := response.Request
	if request == nil {
		request = &http.Request{}
	}
	response.Request = request.WithContext(context.WithValue(request.Context(), codexTelemetryHTTPResponseKey{}, collector))
	response.Body = &codexTelemetryHTTPBody{ReadCloser: response.Body, collector: collector}
}

type codexTelemetryHTTPBody struct {
	io.ReadCloser
	collector *codexTelemetryHTTPCollector
}

func (b *codexTelemetryHTTPBody) Close() error {
	err := b.ReadCloser.Close()
	b.collector.close()
	return err
}

// The existing gateway SSE/JSON parsers call this before any model/tool rewrite.
// No second parser, reader goroutine, body buffering, or response limit is added.
func observeCodexTelemetryHTTPPayload(response *http.Response, payload []byte, eventType string) {
	if response == nil || response.Request == nil {
		return
	}
	collector, _ := response.Request.Context().Value(codexTelemetryHTTPResponseKey{}).(*codexTelemetryHTTPCollector)
	if collector != nil {
		collector.observe(payload, eventType)
	}
}

// The gateway may reject an otherwise completed response (for example an empty
// answer eligible for failover). Commit only after its existing parser returns.
func completeCodexTelemetryHTTPResponse(response *http.Response, parseErr error) {
	if response == nil || response.Request == nil {
		return
	}
	collector, _ := response.Request.Context().Value(codexTelemetryHTTPResponseKey{}).(*codexTelemetryHTTPCollector)
	if collector != nil {
		collector.complete(parseErr)
	}
}

func beginCodexTelemetryHTTPParsing(response *http.Response) {
	if response == nil || response.Request == nil {
		return
	}
	collector, _ := response.Request.Context().Value(codexTelemetryHTTPResponseKey{}).(*codexTelemetryHTTPCollector)
	if collector != nil {
		collector.mu.Lock()
		collector.parsing = true
		collector.mu.Unlock()
	}
}

func observeCodexTelemetryHTTPBody(response *http.Response, body []byte) {
	if response == nil || response.Request == nil || response.Request.Context().Value(codexTelemetryHTTPResponseKey{}) == nil {
		return
	}
	if bodyHasSSEFraming(body) {
		forEachOpenAISSEFrame(string(body), func(eventType string, payload []byte) {
			observeCodexTelemetryHTTPPayload(response, payload, eventType)
		})
		return
	}
	observeCodexTelemetryHTTPPayload(response, body, "")
}

type codexTelemetryHTTPCollector struct {
	mu                     sync.Mutex
	attempt                codexTelemetryHTTPAttempt
	status                 int
	done                   bool
	parsing                bool
	firstEvent, firstToken time.Time
	latest                 CodexTelemetryResult
	usage                  OpenAIUsage
	reasoningTokens        int64
}

func (c *codexTelemetryHTTPCollector) observe(payload []byte, eventType string) {
	if !gjson.ValidBytes(payload) {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.done {
		return
	}
	now := time.Now()
	if c.firstEvent.IsZero() {
		c.firstEvent = now
	}
	eventType = strings.TrimSpace(eventType)
	if eventType == "" {
		eventType = strings.TrimSpace(gjson.GetBytes(payload, "type").String())
	}
	if c.firstToken.IsZero() && openAIStreamDataStartsVisibleOutput(string(payload), eventType) {
		c.firstToken = now
	}
	status := ""
	switch eventType {
	case "response.completed":
		status = "completed"
	case "response.failed", "response.incomplete", "error":
		status = "failed"
	case "response.cancelled", "response.canceled":
		status = "cancelled"
	case "response.done", "":
		status = firstNonEmpty(gjson.GetBytes(payload, "response.status").String(), gjson.GetBytes(payload, "status").String())
	}
	result := codexTelemetryResultFromResponse(payload, status, c.status, c.firstEvent, c.firstToken)
	parseOpenAIResponseUsageInto(payload, eventType, &c.usage)
	if result.ReasoningOutputTokens > 0 {
		c.reasoningTokens = result.ReasoningOutputTokens
	}
	result.InputTokens, result.CachedInputTokens, result.OutputTokens = int64(c.usage.InputTokens), int64(c.usage.CacheReadInputTokens), int64(c.usage.OutputTokens)
	result.ReasoningOutputTokens = c.reasoningTokens
	if result.ResponseID == "" {
		result.ResponseID = c.latest.ResponseID
	}
	// Progress frames may report a response ID, but never establish a terminal
	// status. A bare error may be followed by a successful authoritative terminal.
	if status == "" {
		c.latest.ResponseID = result.ResponseID
		c.latest.InputTokens, c.latest.CachedInputTokens, c.latest.OutputTokens = result.InputTokens, result.CachedInputTokens, result.OutputTokens
		c.latest.ReasoningOutputTokens = result.ReasoningOutputTokens
		return
	}
	result.FinishedAt = now
	c.latest = result
	if eventType == "error" {
		return
	}
}

func (c *codexTelemetryHTTPCollector) complete(parseErr error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.done {
		return
	}
	c.done = true
	result := c.latest
	result.HTTPStatus, result.FirstEventAt, result.FirstTokenAt = c.status, c.firstEvent, c.firstToken
	if result.FinishedAt.IsZero() {
		result.FinishedAt = time.Now()
	}
	if parseErr == nil && result.Status == "completed" {
		c.attempt.Finish(result)
		return
	}
	if parseErr != nil && result.Status == "completed" {
		result.Status = "failed"
	}
	if result.Status == "" {
		result.Status = "interrupted"
	}
	c.attempt.Retry(result)
}

func (c *codexTelemetryHTTPCollector) close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.done {
		return
	}
	if c.parsing || c.latest.Status != "" {
		// A parser can close the body before returning its acceptance decision.
		return
	}
	c.done = true
	result := c.latest
	if result.Status == "" {
		result.Status = "interrupted"
	}
	result.HTTPStatus, result.FirstEventAt, result.FirstTokenAt, result.FinishedAt = c.status, c.firstEvent, c.firstToken, time.Now()
	c.attempt.Retry(result)
}
