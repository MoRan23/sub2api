package service

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// A business error has already been relayed verbatim. It must never become an
// account health signal or a request replay on another account.
type codexEngineResponseError struct {
	status                          int
	reportedStatus                  int
	code, errorType, message, event string
}

var errCodexEngineFirstOutputTimeout = errors.New("Codex-Engine first output timeout")
var errCodexEngineUsageDrainTimeout = errors.New("Codex-Engine usage drain window expired after client disconnect")

type codexEngineUsageDrainGuard struct {
	mu     sync.Mutex
	timer  *time.Timer
	closed bool
	window time.Duration
	cancel context.CancelCauseFunc
}

func (g *codexEngineUsageDrainGuard) start() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed || g.timer != nil || g.window <= 0 {
		return
	}
	g.timer = time.AfterFunc(g.window, func() { g.cancel(errCodexEngineUsageDrainTimeout) })
}

func (g *codexEngineUsageDrainGuard) close() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.closed = true
	if g.timer != nil {
		g.timer.Stop()
	}
}

func (e *codexEngineResponseError) Error() string {
	return fmt.Sprintf("Codex-Engine response error (%d) [%s]: %s", e.status, e.code, e.message)
}
func isCodexEngineResponseError(err error) bool {
	var target *codexEngineResponseError
	return errors.As(err, &target)
}

// Preserve the wire response while reporting the semantic failure of a 2xx SSE
// stream. Never retain the response output, history or arbitrary extra fields.
func newCodexEngineResponseError(status int, payload []byte, event string) *codexEngineResponseError {
	failure := gjson.GetBytes(payload, "response.error")
	if !failure.IsObject() {
		failure = gjson.GetBytes(payload, "error")
	}
	if !failure.IsObject() && (event == "error" || event == "response.failed") &&
		(gjson.GetBytes(payload, "message").Exists() || gjson.GetBytes(payload, "code").Exists()) {
		failure = gjson.ParseBytes(payload)
	}
	reportedStatus := 0
	for _, value := range []gjson.Result{failure.Get("status_code"), failure.Get("status"), gjson.GetBytes(payload, "status_code"), gjson.GetBytes(payload, "status")} {
		if reported := value.Int(); reported >= 400 && reported <= 599 {
			reportedStatus = int(reported)
			break
		}
	}
	if status >= 200 && status < 300 {
		status = http.StatusBadGateway
		if reportedStatus != 0 {
			status = reportedStatus
		}
	}
	message := failure.Get("message").String()
	if strings.TrimSpace(message) == "" {
		message = firstNonEmpty(gjson.GetBytes(payload, "response.incomplete_details.reason").String(), event, http.StatusText(status))
	}
	return &codexEngineResponseError{status: status, reportedStatus: reportedStatus, code: failure.Get("code").String(), errorType: failure.Get("type").String(), message: message, event: event}
}

func recordCodexEngineError(c *gin.Context, account *Account, resp *http.Response, failure *codexEngineResponseError) {
	clean := func(value string, limit int) string {
		if key := account.GetOpenAIApiKey(); key != "" {
			value = strings.ReplaceAll(value, key, "[redacted]")
		}
		return truncateString(sanitizeUpstreamErrorMessage(strings.TrimSpace(value)), limit)
	}
	failure.code = clean(failure.code, 128)
	failure.errorType = clean(failure.errorType, 128)
	failure.message = clean(failure.message, 2048)
	// A small error-only envelope keeps provider codes available to ops without
	// storing generated content or the rest of a response.failed event.
	errorFields := map[string]any{
		"code": failure.code, "type": failure.errorType, "message": failure.message,
	}
	if failure.reportedStatus != 0 {
		errorFields["status_code"] = failure.reportedStatus
	}
	detail, _ := json.Marshal(map[string]any{"error": errorFields})
	setOpsUpstreamError(c, failure.status, failure.message, string(detail))
	kind := "http_error"
	if failure.event != "" {
		kind = "stream_error"
	}
	appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
		Platform: account.Platform, AccountID: account.ID, AccountName: account.Name,
		ProxyID: opsUpstreamProxyID(account), ProxyName: opsUpstreamProxyName(account),
		UpstreamStatusCode: failure.status, UpstreamRequestID: firstNonEmpty(resp.Header.Get("X-Request-Id"), resp.Header.Get("Request-Id")),
		Kind: kind, Reason: failure.code, Message: failure.message, Detail: string(detail),
		UpstreamResponseBody: string(detail),
	})
	if failure.event == "response.failed" || failure.event == "error" {
		// The terminal response may include output before its error fields, beyond
		// the middleware's bounded capture. Preserve the already parsed, sanitized
		// failure independently, without retaining output or changing the wire.
		c.Set("codex_engine_terminal_error", []byte("event: "+failure.event+"\ndata: "+string(detail)+"\n\n"))
	}
}

// CodexEngineTerminalError returns an error-only diagnostic envelope parsed from
// the complete SSE frame. It is never sent to the client or used for billing.
func CodexEngineTerminalError(c *gin.Context) []byte {
	if c == nil {
		return nil
	}
	value, _ := c.Get("codex_engine_terminal_error")
	detail, _ := value.([]byte)
	return detail
}

// CodexEngineResponseWritten prevents generic handlers from appending a second
// error envelope after a raw Engine response or a partially delivered stream.
func CodexEngineResponseWritten(c *gin.Context) bool {
	return c != nil && c.GetBool("codex_engine_response_written")
}

var codexEnginePublicHeaders = []string{
	"Accept", "User-Agent", "OpenAI-Beta", "Anthropic-Version", "Anthropic-Beta",
	"X-Codex-Session-Id", "Session-Id", "Session_Id", "X-Session-Id", "X-Opencode-Session", "X-Conversation-Id",
	"X-Codex-Thread-Id", "Thread-Id", "Thread_Id", "X-Codex-Turn-Metadata",
	"X-Codex-Image-Turn-Id", "X-Codex-Tool-Call-Id",
}

func codexEngineURL(base, endpoint string) (string, error) {
	u, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	if u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("Codex-Engine base_url must be a platform API HTTP(S) address")
	}
	prefix := strings.TrimRight(u.Path, "/")
	escapedPrefix := strings.TrimRight(u.EscapedPath(), "/")
	if strings.HasSuffix(prefix, "/v1") {
		prefix = strings.TrimSuffix(prefix, "/v1")
	}
	if strings.HasSuffix(escapedPrefix, "/v1") {
		escapedPrefix = strings.TrimSuffix(escapedPrefix, "/v1")
	}
	u.Path, u.RawPath = prefix+endpoint, escapedPrefix+endpoint
	return u.String(), nil
}

// Multipart is the only protocol conversion performed here. Raw JSON is never
// decoded into a fixed schema, preserving unknown fields and integer precision.
func codexEngineImagesJSON(body []byte, contentType string) ([]byte, error) {
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		return nil, err
	}
	if mediaType != "multipart/form-data" {
		return body, nil
	}
	reader := multipart.NewReader(bytes.NewReader(body), params["boundary"])
	fields := map[string]any{}
	images := []map[string]string{}
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		const limit = 20 << 20
		data, err := io.ReadAll(io.LimitReader(part, limit+1))
		_ = part.Close()
		if err != nil {
			return nil, err
		}
		if len(data) > limit {
			return nil, fmt.Errorf("multipart image or field exceeds 20 MiB")
		}
		name := part.FormName()
		if part.FileName() != "" {
			contentType := part.Header.Get("Content-Type")
			if contentType == "" {
				contentType = http.DetectContentType(data)
			}
			image := map[string]string{"image_url": "data:" + contentType + ";base64," + base64.StdEncoding.EncodeToString(data)}
			if name == "image" || strings.HasPrefix(name, "image[") || name == "images" || strings.HasPrefix(name, "images[") {
				images = append(images, image)
			} else {
				fields[name] = image
			}
			continue
		}
		value := string(data)
		switch name {
		case "n", "output_compression", "partial_images":
			if !gjson.Valid(value) || gjson.Parse(value).Type != gjson.Number {
				return nil, fmt.Errorf("%s must be a number", name)
			}
			fields[name] = json.Number(value)
		case "stream":
			if value != "true" && value != "false" {
				return nil, fmt.Errorf("stream must be a boolean")
			}
			fields[name] = value == "true"
		default:
			fields[name] = value
		}
	}
	if len(images) > 0 {
		fields["images"] = images
	}
	return json.Marshal(fields)
}

func (s *OpenAIGatewayService) forwardCodexEngine(ctx context.Context, c *gin.Context, account *Account, body []byte, endpoint, defaultMappedModel string) (*OpenAIForwardResult, error) {
	start := time.Now()
	ctx = s.freezeOpenAIRequestPolicy(ctx, c)
	model := gjson.GetBytes(body, "model").String()
	compact := endpoint == "/v1/responses/compact" || (endpoint == "/v1/responses" && !gjson.GetBytes(body, "stream").Bool() && HasCompactionTriggerInInput(body))
	billingModel, upstreamModel := resolveOpenAIForwardMappedModels(account, model, compact)
	if defaultMappedModel != "" {
		upstreamModel = resolveOpenAIForwardModel(account, model, defaultMappedModel)
		billingModel = upstreamModel
	}
	var err error
	if upstreamModel != "" && upstreamModel != model {
		body, err = sjson.SetBytes(body, "model", upstreamModel)
	}
	if err != nil {
		return nil, err
	}
	body, err = s.applyOpenAIFastPolicyToBody(ctx, account, upstreamModel, body)
	if err != nil {
		var blocked *OpenAIFastBlockedError
		if errors.As(err, &blocked) {
			writeOpenAIFastPolicyBlockedResponse(c, blocked)
		}
		return nil, err
	}
	base, err := s.validateUpstreamBaseURL(account.GetOpenAIBaseURL())
	if err != nil {
		return nil, err
	}
	target, err := codexEngineURL(base, endpoint)
	if err != nil {
		return nil, err
	}
	imageRequest := strings.HasPrefix(endpoint, "/v1/images/")
	streamRequest := gjson.GetBytes(body, "stream").Bool()
	downstreamCtx := ctx
	if imageRequest {
		var release context.CancelFunc
		ctx, release = detachUpstreamContext(ctx)
		defer release()
	} else {
		// Keep a streaming attempt alive after a downstream disconnect so that
		// Engine can finish it and return authoritative usage. Synchronous JSON
		// requests retain their existing cancellation semantics.
		var release context.CancelFunc
		ctx, release = detachStreamUpstreamContext(ctx, streamRequest)
		defer release()
	}
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	drainWindow := time.Duration(0)
	if s.cfg != nil {
		drainWindow = time.Duration(s.cfg.Gateway.StreamDataIntervalTimeout) * time.Second
	}
	if imageRequest {
		drainWindow = s.openAIImageStreamDataInterval()
	}
	drainGuard := &codexEngineUsageDrainGuard{window: drainWindow, cancel: cancel}
	defer drainGuard.close()
	if streamRequest && downstreamCtx != nil && !isOpenAICandyTest(ctx) {
		// Observe the caller separately from the detached upstream context, also
		// covering a disconnect while Engine has not yet returned response headers.
		stopDisconnectWatch := context.AfterFunc(downstreamCtx, drainGuard.start)
		defer stopDisconnectWatch()
	}
	effort := gjson.GetBytes(body, "reasoning.effort").String()
	if effort == "" {
		effort = gjson.GetBytes(body, "reasoning_effort").String()
	}
	var firstTimer *time.Timer
	if timeout := s.openAIFirstOutputTimeout(effort); timeout > 0 && streamRequest && !imageRequest {
		firstTimer = time.AfterFunc(timeout, func() { cancel(errCodexEngineFirstOutputTimeout) })
		defer firstTimer.Stop()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req = req.WithContext(WithHTTPUpstreamProfile(req.Context(), HTTPUpstreamProfileOpenAI))
	for _, name := range codexEnginePublicHeaders {
		for _, value := range c.Request.Header.Values(name) {
			req.Header.Add(name, value)
		}
	}
	req.Header.Set("Authorization", "Bearer "+account.GetOpenAIApiKey())
	req.Header.Set("Content-Type", "application/json")
	SetActualOpenAIUpstreamEndpoint(c, endpoint)
	SetOpsUpstreamModel(c, upstreamModel)
	proxy := ""
	if account.ProxyID != nil && account.Proxy != nil {
		proxy = account.Proxy.URL()
	}
	// Reuse the transport profile/proxy/concurrency, without OAuth wire identity
	// rewriting or plugin/control headers.
	req, err = candyTestBeforeSend(req)
	if err != nil {
		return nil, err
	}
	if err := prepareOpenAIDaybreakHTTPRequest(req, account, s.settingService); err != nil {
		return nil, err
	}
	if downstreamCtx != nil && downstreamCtx.Err() != nil {
		// Detachment only protects an attempt already in flight. It must not
		// start a fresh upstream request for a caller that has already gone away.
		return nil, downstreamCtx.Err()
	}
	resp, err := s.httpUpstream.Do(req, proxy, account.ID, account.Concurrency)
	SetOpsLatencyMs(c, OpsUpstreamLatencyMsKey, time.Since(start).Milliseconds())
	if err != nil {
		if errors.Is(context.Cause(ctx), errCodexEngineFirstOutputTimeout) {
			recordCodexEngineFirstOutputTimeout(c, account, nil, start, "response_headers")
			return nil, errCodexEngineFirstOutputTimeout
		}
		if errors.Is(context.Cause(ctx), errCodexEngineUsageDrainTimeout) {
			recordCodexEngineUsageDrainTimeout(c, account, nil)
			return nil, errCodexEngineUsageDrainTimeout
		}
		return nil, s.handleOpenAIUpstreamTransportError(ctx, c, account, err, true)
	}
	defer resp.Body.Close()
	result := &OpenAIForwardResult{Model: model, BillingModel: billingModel, UpstreamModel: upstreamModel, UpstreamEndpoint: endpoint,
		RequestID: resp.Header.Get("X-Request-Id"), UpstreamHeaders: resp.Header.Clone(), ResponseHeaders: resp.Header.Clone()}
	if effort != "" {
		result.ReasoningEffort = &effort
	}
	if tier := gjson.GetBytes(body, "service_tier").String(); tier != "" {
		result.ServiceTier = &tier
	}
	defer func() { result.Duration = time.Since(start) }()
	writeCodexEngineResponseHeaders(c.Writer.Header(), resp.Header)
	if strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "text/event-stream") {
		result.Stream = true
		err = s.relayCodexEngineStream(ctx, downstreamCtx, c, resp, result, firstTimer, start, imageRequest, drainGuard.start)
	} else {
		data, readErr := ReadUpstreamResponseBody(resp.Body, s.cfg, c, openAITooLargeError)
		if readErr != nil {
			return result, readErr
		}
		observeCodexEnginePayload(result, data, "")
		contentType := resp.Header.Get("Content-Type")
		if contentType == "" {
			contentType = "application/json"
		}
		c.Data(resp.StatusCode, contentType, data)
		c.Set("codex_engine_response_written", true)
		if resp.StatusCode < 200 || resp.StatusCode >= 300 || gjson.GetBytes(data, "error").IsObject() {
			err = newCodexEngineResponseError(resp.StatusCode, data, "")
		}
	}
	if err == nil && (resp.StatusCode >= 300 || resp.StatusCode < 200) {
		err = newCodexEngineResponseError(resp.StatusCode, nil, "")
	}
	var failure *codexEngineResponseError
	if errors.Is(err, errCodexEngineFirstOutputTimeout) {
		recordCodexEngineFirstOutputTimeout(c, account, resp.Header, start, "stream")
	}
	if errors.Is(err, errCodexEngineUsageDrainTimeout) {
		recordCodexEngineUsageDrainTimeout(c, account, resp.Header)
	}
	if errors.As(err, &failure) {
		recordCodexEngineError(c, account, resp, failure)
	}
	if err == nil && endpoint == "/v1/alpha/search" {
		result.WebSearchCalls = 1
	}
	if result.ResponseID != "" {
		s.bindHTTPResponseAccount(ctx, c, account, result.ResponseID)
	}
	if failure != nil && !openAIUsageHasTokens(&result.Usage) && result.ImageCount == 0 && !isOpenAICandyTest(ctx) {
		// An unmetered rejection has no billable partial result. Diagnostics
		// still need model evidence; they never enter downstream billing.
		return nil, err
	}
	return result, err
}

func recordCodexEngineUsageDrainTimeout(c *gin.Context, account *Account, headers http.Header) {
	message := errCodexEngineUsageDrainTimeout.Error()
	setOpsUpstreamError(c, http.StatusGatewayTimeout, message, "")
	appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
		Platform: account.Platform, AccountID: account.ID, AccountName: account.Name,
		ProxyID: opsUpstreamProxyID(account), ProxyName: opsUpstreamProxyName(account),
		UpstreamStatusCode: http.StatusGatewayTimeout, UpstreamRequestID: firstNonEmpty(headers.Get("X-Request-Id"), headers.Get("Request-Id")),
		Kind: "usage_drain_timeout", Reason: "usage_drain_timeout", Message: message,
	})
}

func recordCodexEngineFirstOutputTimeout(c *gin.Context, account *Account, headers http.Header, start time.Time, phase string) {
	message := errCodexEngineFirstOutputTimeout.Error()
	setOpsUpstreamError(c, http.StatusGatewayTimeout, message, "")
	appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
		Platform: account.Platform, AccountID: account.ID, AccountName: account.Name,
		ProxyID: opsUpstreamProxyID(account), ProxyName: opsUpstreamProxyName(account),
		UpstreamStatusCode: http.StatusGatewayTimeout, UpstreamRequestID: firstNonEmpty(headers.Get("X-Request-Id"), headers.Get("Request-Id")),
		Kind: "first_output_timeout", Reason: "first_output_timeout", Message: message,
		Detail: fmt.Sprintf("phase=%s elapsed_ms=%d", phase, time.Since(start).Milliseconds()),
	})
}

func writeCodexEngineResponseHeaders(dst, src http.Header) {
	for _, name := range append(append([]string{}, codexEnginePublicHeaders...),
		"Content-Type", "Cache-Control", "X-Request-Id", "Request-Id", "Retry-After", "X-Should-Retry",
		"X-Codex-Engine-Compatibility", "X-Codex-Engine-Ignored-Fields", "X-Codex-Engine-Token-Estimate") {
		if values := src.Values(name); len(values) > 0 {
			dst[http.CanonicalHeaderKey(name)] = append([]string(nil), values...)
		}
	}
}

func observeCodexEnginePayload(result *OpenAIForwardResult, data []byte, event string) {
	parseOpenAIResponseUsageInto(data, event, &result.Usage)
	if usage, ok := openAIUsageFromGJSON(gjson.GetBytes(data, "message.usage")); ok {
		mergeOpenAIUsageNonZero(&result.Usage, usage)
	}
	if id := extractOpenAIResponseIDFromJSONBytes(data); id != "" {
		result.ResponseID = id
	}
	for _, path := range []string{"response.model", "message.model", "model"} {
		if model := gjson.GetBytes(data, path).String(); model != "" {
			if result.UpstreamResponseModel != "" && result.UpstreamResponseModel != model {
				result.UpstreamResponseModelConflict = true
			}
			result.UpstreamResponseModel = model
			break
		}
	}
	for _, path := range []string{"response.service_tier", "service_tier"} {
		if tier := gjson.GetBytes(data, path).String(); tier != "" {
			result.UpstreamResponseServiceTier = tier
			break
		}
	}
	if sizes := collectOpenAIResponseImageOutputSizesFromJSONBytes(data); len(sizes) > 0 {
		result.ImageOutputSizes = sizes
	}
	if count := extractOpenAIImagesBillableCountFromJSONBytes(data); count > result.ImageCount {
		result.ImageCount = count
	}
}

// Relay complete SSE frames without rewriting their bytes. A separate reader
// keeps the existing idle/first-output deadlines effective even on a stalled body.
func (s *OpenAIGatewayService) relayCodexEngineStream(ctx, downstreamCtx context.Context, c *gin.Context, resp *http.Response, result *OpenAIForwardResult, firstTimer *time.Timer, start time.Time, imageRequest bool, startUsageDrain func()) error {
	type readResult struct {
		line []byte
		err  error
	}
	lines := make(chan readResult, 1)
	done := make(chan struct{})
	defer close(done)
	maxLine := defaultMaxLineSize
	if s.cfg != nil && s.cfg.Gateway.MaxLineSize > 0 {
		maxLine = s.cfg.Gateway.MaxLineSize
	}
	go func() {
		reader := bufio.NewReader(resp.Body)
		for {
			var line []byte
			var err error
			for {
				var piece []byte
				piece, err = reader.ReadSlice('\n')
				line = append(line, piece...)
				if len(line) > maxLine {
					err = fmt.Errorf("upstream SSE line exceeds configured limit")
					break
				}
				if err != bufio.ErrBufferFull {
					break
				}
			}
			select {
			case lines <- readResult{line, err}:
			case <-done:
				return
			}
			if err != nil {
				return
			}
		}
	}()
	idle := time.Duration(0)
	if s.cfg != nil {
		idle = time.Duration(s.cfg.Gateway.StreamDataIntervalTimeout) * time.Second
	}
	if imageRequest {
		idle = s.openAIImageStreamDataInterval()
	}
	var timer *time.Timer
	var timeout <-chan time.Time
	if idle > 0 {
		timer = time.NewTimer(idle)
		defer timer.Stop()
		timeout = timer.C
	}
	c.Status(resp.StatusCode)
	var frame, payload []byte
	event, terminal := "", false
	var streamFailure *codexEngineResponseError
	var clientDone <-chan struct{}
	if downstreamCtx != nil {
		clientDone = downstreamCtx.Done()
	}
	markClientDisconnected := func() {
		if result.ClientDisconnect {
			return
		}
		result.ClientDisconnect = true
		clientDone = nil
		// Reuse the configured idle duration as the maximum disconnected usage
		// collection window. Heartbeats must not retain a departed client's slot
		// indefinitely. Zero retains the existing disabled-timeout semantics.
		if startUsageDrain != nil {
			startUsageDrain()
		}
	}
	observeClientDisconnect := func() {
		if downstreamCtx != nil && downstreamCtx.Err() != nil {
			markClientDisconnected()
		}
	}
	imageCounter := newOpenAIImageOutputCounter()
	flush := func() error {
		kind := event
		if kind == "" {
			kind = gjson.GetBytes(payload, "type").String()
		}
		observeCodexEnginePayload(result, payload, kind)
		imageCounter.AddSSEData(payload)
		result.ImageCount = max(result.ImageCount, imageCounter.Count())
		if sizes := imageCounter.Sizes(); len(sizes) > 0 {
			result.ImageOutputSizes = sizes
		}
		semantic := openAIStreamDataStartsSemanticTTFT(string(payload), kind)
		if kind == "message_start" || kind == "ping" {
			semantic = false
		}
		if semantic && result.FirstTokenMs == nil {
			elapsed := int(time.Since(start).Milliseconds())
			result.FirstTokenMs = &elapsed
			if firstTimer != nil {
				firstTimer.Stop()
			}
		}
		observeClientDisconnect()
		if !result.ClientDisconnect {
			c.Set("codex_engine_response_written", true)
			if _, err := c.Writer.Write(frame); err != nil {
				markClientDisconnected()
			} else {
				observeClientDisconnect()
				if !result.ClientDisconnect {
					c.Writer.Flush()
				}
			}
		}
		failed := kind == "error" || kind == "response.failed" || kind == "response.incomplete" || gjson.GetBytes(payload, "error").IsObject()
		if failed {
			if kind == "" {
				kind = "error"
			}
			nextFailure := newCodexEngineResponseError(resp.StatusCode, payload, kind)
			if streamFailure != nil {
				// A terminal can supply only status/usage after a detailed bare
				// error. Keep the earlier diagnosis while accepting its real usage.
				if nextFailure.code == "" {
					nextFailure.code = streamFailure.code
				}
				if nextFailure.errorType == "" {
					nextFailure.errorType = streamFailure.errorType
				}
				if nextFailure.message == kind || nextFailure.message == http.StatusText(nextFailure.status) {
					nextFailure.message = streamFailure.message
				}
				if nextFailure.reportedStatus == 0 && streamFailure.reportedStatus != 0 {
					nextFailure.reportedStatus, nextFailure.status = streamFailure.reportedStatus, streamFailure.status
				}
			}
			streamFailure = nextFailure
			// A bare error can precede response.failed with the authoritative
			// partial usage. Relay it unchanged and finish at the protocol terminal
			// event, EOF or the existing upstream deadlines; never replay it.
			if kind != "error" {
				terminal = true
			}
		}
		if bytes.Equal(bytes.TrimSpace(payload), []byte("[DONE]")) || kind == "response.completed" || kind == "response.done" || kind == "message_stop" || (imageRequest && strings.HasSuffix(kind, ".completed")) {
			terminal = true
		}
		frame, payload, event = nil, nil, ""
		return nil
	}
	for {
		select {
		case <-ctx.Done():
			if streamFailure != nil {
				return errors.Join(streamFailure, context.Cause(ctx))
			}
			return context.Cause(ctx)
		case <-clientDone:
			observeClientDisconnect()
		case <-timeout:
			if result.ClientDisconnect {
				if streamFailure != nil {
					return errors.Join(streamFailure, errCodexEngineUsageDrainTimeout)
				}
				return errCodexEngineUsageDrainTimeout
			}
			if streamFailure != nil {
				return streamFailure
			}
			return fmt.Errorf("Codex-Engine stream idle timeout")
		case item := <-lines:
			if timer != nil {
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				timer.Reset(idle)
			}
			frame = append(frame, item.line...)
			line := bytes.TrimRight(item.line, "\r\n")
			if bytes.HasPrefix(line, []byte("data:")) {
				if len(payload) > 0 {
					payload = append(payload, '\n')
				}
				payload = append(payload, bytes.TrimPrefix(line[5:], []byte(" "))...)
			} else if bytes.HasPrefix(line, []byte("event:")) {
				event = strings.TrimSpace(string(line[6:]))
			}
			if len(frame) > maxLine {
				limitErr := fmt.Errorf("upstream SSE event exceeds configured limit")
				if streamFailure != nil {
					return errors.Join(streamFailure, limitErr)
				}
				return limitErr
			}
			if len(line) == 0 || item.err != nil {
				if err := flush(); err != nil {
					return err
				}
				if terminal {
					if firstTimer != nil {
						firstTimer.Stop()
					}
					if streamFailure != nil {
						return streamFailure
					}
					return nil
				}
			}
			if item.err != nil {
				if streamFailure != nil {
					return streamFailure
				}
				if item.err != io.EOF {
					return item.err
				}
				if !terminal {
					return ErrOpenAIUpstreamStreamTruncated
				}
				return nil
			}
		}
	}
}
