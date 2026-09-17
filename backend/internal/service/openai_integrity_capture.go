package service

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

const openAIIntegrityCaptureKey = "openai_request_integrity_capture"

// The switch is frozen before adaptation. Compatible entrances install their
// baseline only when the first Responses adapter output becomes available.
// Neither a failover nor a retry may replace it with a later, adapted body.
type openAIIntegrityCapture struct {
	mu        sync.Mutex
	enabled   bool
	state     *OpenAIRequestIntegrityState
	model     string
	recovery  string
	todoGuard bool
}

func openAIIntegrityCaptureFromContext(c *gin.Context) *openAIIntegrityCapture {
	if c == nil {
		return nil
	}
	value, _ := c.Get(openAIIntegrityCaptureKey)
	capture, _ := value.(*openAIIntegrityCapture)
	return capture
}

func (s *OpenAIGatewayService) freezeOpenAIRequestIntegrity(ctx context.Context, c *gin.Context) *openAIIntegrityCapture {
	if c == nil {
		return nil
	}
	if capture := openAIIntegrityCaptureFromContext(c); capture != nil {
		return capture
	}
	var settings *SettingService
	if s != nil {
		settings = s.settingService
	}
	capture := &openAIIntegrityCapture{enabled: settings.IsOpenAIRequestIntegrityObserveEnabled(ctx)}
	c.Set(openAIIntegrityCaptureKey, capture)
	return capture
}

func (s *OpenAIGatewayService) captureOpenAIRequestIntegrity(ctx context.Context, c *gin.Context, protocol string, baseline []byte) {
	capture := s.freezeOpenAIRequestIntegrity(ctx, c)
	if capture == nil {
		return
	}
	capture.mu.Lock()
	defer capture.mu.Unlock()
	if capture.state == nil {
		capture.state = NewOpenAIRequestIntegrityState(capture.enabled, protocol, baseline)
	}
}

func (s *OpenAIGatewayService) captureOpenAIAdapterIntegrity(ctx context.Context, c *gin.Context, protocol string, response any) {
	capture := s.freezeOpenAIRequestIntegrity(ctx, c)
	if capture == nil {
		return
	}
	capture.mu.Lock()
	defer capture.mu.Unlock()
	if capture.state != nil {
		return
	}
	var body []byte
	if capture.enabled {
		// A failed diagnostic marshal is a missing baseline, never a gateway error.
		body, _ = json.Marshal(response)
	}
	capture.state = NewOpenAIRequestIntegrityState(capture.enabled, protocol, body)
}

func (s *OpenAIGatewayService) beginOpenAIWSRequestIntegrityTurn(ctx context.Context, c *gin.Context, raw []byte, newTurn bool) {
	if c == nil {
		return
	}
	if newTurn {
		// Each accepted client turn gets its own frozen policy and attempt counter.
		// Physical reconnects must pass false and retain the original baseline.
		c.Set(openAIIntegrityCaptureKey, (*openAIIntegrityCapture)(nil))
	}
	s.captureOpenAIRequestIntegrity(ctx, c, "responses", raw)
}

func setOpenAIRequestIntegrityExpectedModel(c *gin.Context, model string) {
	if capture := openAIIntegrityCaptureFromContext(c); capture != nil {
		capture.mu.Lock()
		capture.model = model
		capture.mu.Unlock()
	}
}

func setOpenAIRequestIntegrityRecovery(c *gin.Context, reason string) {
	if capture := openAIIntegrityCaptureFromContext(c); capture != nil {
		capture.mu.Lock()
		capture.recovery = reason
		capture.mu.Unlock()
	}
}

func setOpenAIRequestIntegrityTodoGuard(c *gin.Context) {
	if capture := openAIIntegrityCaptureFromContext(c); capture != nil {
		capture.mu.Lock()
		capture.todoGuard = true
		capture.mu.Unlock()
	}
}

func resetOpenAIRequestIntegrityAttemptRules(c *gin.Context) {
	if capture := openAIIntegrityCaptureFromContext(c); capture != nil {
		capture.mu.Lock()
		capture.model, capture.recovery = "", ""
		capture.todoGuard = false
		capture.mu.Unlock()
	}
}

// Called for the actual HTTP body / response.create frame before a physical
// send, even when fingerprint collection is off. Only summaries enter its ring.
func (s *OpenAIGatewayService) observeOpenAIRequestIntegrity(c *gin.Context, account *Account, headers http.Header, body []byte, transport string, timezone *RequestTimezoneState) *RequestIntegrityObservation {
	if c == nil || c.Request == nil || account == nil || !account.IsOpenAIOAuth() || len(body) == 0 {
		return nil
	}
	ctx := c.Request.Context()
	capture := s.freezeOpenAIRequestIntegrity(ctx, c)
	if !capture.enabled {
		return nil
	}
	if s != nil && s.isAgentIdentityAccount(ctx, account) {
		return nil
	}
	path := ""
	if c.Request.URL != nil {
		path = strings.TrimRight(strings.ToLower(c.Request.URL.Path), "/")
	}
	if !strings.HasSuffix(path, "/responses") && !strings.HasSuffix(path, "/messages") && !strings.HasSuffix(path, "/chat/completions") {
		return nil
	}
	// Oversize data is passed directly to the bounded checker, without parsing
	// it again for optional request-kind classification.
	if len(body) <= requestIntegrityMaxBytes {
		profile := finalFingerprintCodexWireProfile(headers, body)
		flatKind, _ := ParseCodexWireRequestKind(gjson.GetBytes(body, "request_kind").String())
		if profile.RequestKind.internal() || flatKind.internal() || gjson.GetBytes(body, "generate").Type == gjson.False || IsExplicitImageGenerationIntent("/responses", "", body) {
			return nil
		}
	}
	capture.mu.Lock()
	if capture.state == nil {
		// A missed entrance is visible as skipped; the final wire is never used
		// to manufacture a matching baseline.
		capture.state = NewOpenAIRequestIntegrityState(capture.enabled, "responses", nil)
	}
	state := capture.state
	switch GetOpenAIClientTransport(c) {
	case OpenAIClientTransportWS:
		if transport == "http" {
			transport = "http_bridge"
		}
	case OpenAIClientTransportHTTP:
		if transport == "ws" {
			transport = "http_to_ws"
		}
	}
	opts := RequestIntegrityCheckOptions{Transport: transport, ExpectedModel: capture.model, KnownRecovery: capture.recovery, TimezoneState: timezone, CompatTodoGuard: capture.todoGuard}
	capture.mu.Unlock()
	return state.Check(account, body, opts)
}
