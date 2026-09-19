package service

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type codexTurnStateHTTPRequestKey struct{}
type codexTurnStateHTTPResponseKey struct{}

// Preparation follows the client provenance guard and precedes integrity and
// wire observation. The transport consumes this frozen request without choosing
// another token. Every physical retry constructs a fresh request and attempt.
func (s *OpenAIGatewayService) prepareOpenAICodexStateHTTPRequest(c *gin.Context, account *Account, request *http.Request) *http.Request {
	noteOpenAICodexStatePatch(c, nil, nil, nil)
	if s == nil || s.codexTurnStateService == nil || request == nil || request.URL == nil || request.GetBody == nil || !strings.HasSuffix(strings.TrimRight(request.URL.Path, "/"), "/responses") {
		return request
	}
	reader, err := request.GetBody()
	if err != nil {
		return request
	}
	body, err := io.ReadAll(reader)
	_ = reader.Close()
	if err != nil || !gjson.ValidBytes(body) || gjson.GetBytes(body, "generate").Type == gjson.False {
		return request
	}
	model := strings.TrimSpace(gjson.GetBytes(body, "model").String())
	attempt, err := s.codexTurnStateService.Prepare(request.Context(), account, model)
	if err != nil || attempt == nil {
		return request
	}
	if attempt.Enabled && !s.codexTurnStateService.ValidateCredentialHeaders(request.Context(), attempt, request.Header) {
		finishCodexTurnStateHTTPAttempt(s.codexTurnStateService, attempt, false)
		return request
	}
	finalBody := body
	if token := attempt.Snapshot.Token; token != "" {
		// Only an existing HTTP body carrier is synchronized; WS creates its own
		// per-turn carrier. No metadata from the client grants trust to this path.
		if gjson.GetBytes(body, "client_metadata.x-codex-turn-state").Exists() {
			finalBody, err = sjson.SetBytes(body, "client_metadata.x-codex-turn-state", token)
			if err != nil {
				finishCodexTurnStateHTTPAttempt(s.codexTurnStateService, attempt, false)
				return request
			}
		}
		request.Header = request.Header.Clone()
		for key := range request.Header {
			if strings.EqualFold(key, openAICodexTurnStateHeader) {
				delete(request.Header, key)
			}
		}
		request.Header.Set(openAICodexTurnStateHeader, token)
		if !bytes.Equal(body, finalBody) {
			// Freeze a private copy shared by Body, GetBody and the integrity view.
			frozen := append([]byte(nil), finalBody...)
			if request.Body != nil {
				_ = request.Body.Close()
			}
			request.Body = io.NopCloser(bytes.NewReader(frozen))
			request.ContentLength = int64(len(frozen))
			for key := range request.Header {
				if strings.EqualFold(key, "Content-Length") {
					delete(request.Header, key)
				}
			}
			request.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(frozen)), nil }
		}
	}
	noteOpenAICodexStatePatch(c, attempt, body, finalBody)
	collector := &codexTurnStateHTTPCollector{service: s.codexTurnStateService, attempt: attempt}
	return request.WithContext(context.WithValue(request.Context(), codexTurnStateHTTPRequestKey{}, collector))
}

func finishCodexTurnStateHTTPAttempt(service *CodexTurnStateService, attempt *CodexTurnStateAttempt, delivered bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = service.Finish(ctx, attempt, delivered)
	finishOpenAICodexStateObservation(attempt)
}

type codexTurnStateHTTPCollector struct {
	mu                       sync.Mutex
	service                  *CodexTurnStateService
	attempt                  *CodexTurnStateAttempt
	parsing, delivered, done bool
}

func observeCodexTurnStateHTTPResponse(request *http.Request, response *http.Response, sendErr error) {
	if request == nil {
		return
	}
	collector, _ := request.Context().Value(codexTurnStateHTTPRequestKey{}).(*codexTurnStateHTTPCollector)
	if collector == nil {
		return
	}
	if sendErr == nil && response != nil {
		// Error response headers are still real upstream observations, but the
		// failed status below must never reach cache publication.
		collector.service.ObserveHeaders(collector.attempt, response.Header)
	}
	if sendErr != nil || response == nil || response.StatusCode < 200 || response.StatusCode >= 300 || response.Body == nil {
		collector.finish(false)
		return
	}
	responseRequest := response.Request
	if responseRequest == nil {
		responseRequest = request
	}
	response.Request = responseRequest.WithContext(context.WithValue(responseRequest.Context(), codexTurnStateHTTPResponseKey{}, collector))
	response.Body = &codexTurnStateHTTPBody{ReadCloser: response.Body, collector: collector}
}

func codexTurnStateHTTPCollectorFromResponse(response *http.Response) *codexTurnStateHTTPCollector {
	if response == nil || response.Request == nil {
		return nil
	}
	collector, _ := response.Request.Context().Value(codexTurnStateHTTPResponseKey{}).(*codexTurnStateHTTPCollector)
	return collector
}

func observeCodexTurnStateHTTPPayload(response *http.Response, payload []byte) {
	if collector := codexTurnStateHTTPCollectorFromResponse(response); collector != nil {
		collector.service.ObserveEvent(collector.attempt, payload)
	}
}

func beginCodexTurnStateHTTPParsing(response *http.Response) {
	if collector := codexTurnStateHTTPCollectorFromResponse(response); collector != nil {
		collector.mu.Lock()
		collector.parsing = true
		collector.mu.Unlock()
	}
}

// Called only after a successful downstream semantic write/flush. Metadata and
// staged headers are not delivery. Publication waits for the parser's decision.
func markCodexTurnStateHTTPDelivered(response *http.Response) {
	if collector := codexTurnStateHTTPCollectorFromResponse(response); collector != nil {
		collector.mu.Lock()
		collector.delivered = true
		collector.mu.Unlock()
	}
}

func completeCodexTurnStateHTTPResponse(response *http.Response, parseErr error) {
	if collector := codexTurnStateHTTPCollectorFromResponse(response); collector != nil {
		collector.mu.Lock()
		delivered := collector.delivered
		collector.mu.Unlock()
		// A post-delivery disconnect cannot initiate failover. A rejected or
		// abandoned response never sets delivered and therefore cannot publish.
		collector.finish(delivered)
	}
}

func (c *codexTurnStateHTTPCollector) finish(delivered bool) {
	c.mu.Lock()
	if c.done {
		c.mu.Unlock()
		return
	}
	c.done = true
	c.mu.Unlock()
	finishCodexTurnStateHTTPAttempt(c.service, c.attempt, delivered)
}

type codexTurnStateHTTPBody struct {
	io.ReadCloser
	collector *codexTurnStateHTTPCollector
}

func (b *codexTurnStateHTTPBody) Close() error {
	err := b.ReadCloser.Close()
	b.collector.mu.Lock()
	parsing := b.collector.parsing
	b.collector.mu.Unlock()
	if !parsing {
		b.collector.finish(false)
	}
	return err
}
