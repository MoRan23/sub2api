package service

import (
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
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/runtimeobs"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// A business error has already been relayed verbatim. Only explicit temporary
// scheduling rules may use it; it must never trigger default account health
// handling or a request replay on another account.
type codexEngineResponseError struct {
	status                          int
	reportedStatus                  int
	code, errorType, message, event string
	tempUnschedBody                 []byte
}

var errCodexEngineFirstOutputTimeout = errors.New("Codex-Engine first output timeout")

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
	code, errorType, message, event := ownCodexEngineErrorSummary(failure, message, event)
	return &codexEngineResponseError{status: status, reportedStatus: reportedStatus, code: code, errorType: errorType, message: message, event: event,
		tempUnschedBody: codexEngineTempUnschedBody(payload, failure, event)}
}

// Result.Get returns string views into the parsed error object (or the whole
// frame for bare errors). Only these fields may outlive the parsed payload.
func ownCodexEngineErrorSummary(failure gjson.Result, message, event string) (code, errorType, ownedMessage, ownedEvent string) {
	return strings.Clone(failure.Get("code").String()), strings.Clone(failure.Get("type").String()), strings.Clone(message), strings.Clone(event)
}

func recordCodexEngineError(c *gin.Context, account *Account, resp *http.Response, failure *codexEngineResponseError, usage *OpenAIUsage) {
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
	// Mark only after the stream has finished collecting authoritative usage.
	// A bare error may precede response.failed, and the first cyber mark wins.
	// The error-only envelope preserves the structured code without retaining
	// generated output or interpreting a refusal mentioned inside its message.
	markOpenAICyberPolicyEvent(c, detail, failure.status, usage)
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
	// Execution follows its caller, including synchronous images. Accepted
	// asynchronous image jobs supply their own task context here. Late metering
	// is recovered from the durable receipt, never by keeping execution alive.
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	effort := gjson.GetBytes(body, "reasoning.effort").String()
	if effort == "" {
		effort = gjson.GetBytes(body, "reasoning_effort").String()
	}
	var firstTimer *time.Timer
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
		// A canceled caller must never start a fresh upstream attempt.
		return nil, downstreamCtx.Err()
	}
	billingJob, err := s.prepareCodexEngineBillingDispatch(ctx, c, account, body, endpoint, model, billingModel, upstreamModel)
	if err != nil {
		return nil, err
	}
	if billingJob != nil {
		req.Header.Set(CodexEngineBillingClientRequestHeader, billingJob.ClientRequestID)
		defer s.codexEngineBilling.Wake()
	}
	if err := ctx.Err(); err != nil {
		s.markCodexEngineBillingNotDispatched(billingJob)
		return nil, err
	}
	if timeout := s.openAIFirstOutputTimeout(effort); timeout > 0 && streamRequest && !imageRequest {
		firstTimer = time.AfterFunc(timeout, func() { cancel(errCodexEngineFirstOutputTimeout) })
		defer firstTimer.Stop()
	}
	attempt := &codexEngineTransportAttempt{}
	req = attempt.traceRequest(req)
	resp, err := s.httpUpstream.Do(req, proxy, account.ID, account.Concurrency)
	SetOpsLatencyMs(c, OpsUpstreamLatencyMsKey, time.Since(start).Milliseconds())
	if err != nil {
		if attempt.definitelyUnsent(err) {
			s.markCodexEngineBillingNotDispatched(billingJob)
		}
		if errors.Is(context.Cause(ctx), errCodexEngineFirstOutputTimeout) {
			recordCodexEngineFirstOutputTimeout(c, account, nil, start, "response_headers")
			return nil, errCodexEngineFirstOutputTimeout
		}
		return nil, s.handleCodexEngineTransportError(ctx, c, account, err, attempt)
	}
	defer resp.Body.Close()
	if err := s.observeCodexEngineBillingResponse(c, account, billingJob, resp.Header); err != nil {
		cancel(err)
		return nil, err
	}
	stopBodyClose := context.AfterFunc(ctx, func() { _ = resp.Body.Close() })
	defer stopBodyClose()
	result := &OpenAIForwardResult{Model: model, BillingModel: billingModel, UpstreamModel: upstreamModel, UpstreamEndpoint: endpoint, ImageEndpoint: imageRequest,
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
		err = s.relayCodexEngineStream(ctx, downstreamCtx, c, resp, result, firstTimer, start, imageRequest, cancel)
	} else {
		data, readErr := ReadUpstreamResponseBody(resp.Body, s.cfg, c, openAITooLargeError)
		if readErr != nil {
			if downstreamCtx.Err() != nil {
				result.ClientDisconnect = true
				return result, downstreamCtx.Err()
			}
			return result, readErr
		}
		observeCodexEnginePayload(result, data, "")
		contentType := resp.Header.Get("Content-Type")
		if contentType == "" {
			contentType = "application/json"
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 || gjson.GetBytes(data, "error").IsObject() {
			err = newCodexEngineResponseError(resp.StatusCode, data, "")
		}
		c.Header("Content-Type", contentType)
		c.Status(resp.StatusCode)
		c.Set("codex_engine_response_written", true)
		if downstreamCtx.Err() != nil {
			result.ClientDisconnect = true
			cancel(downstreamCtx.Err())
			err = errors.Join(err, downstreamCtx.Err())
		} else if n, writeErr := c.Writer.Write(data); writeErr != nil || n != len(data) {
			if writeErr == nil {
				writeErr = io.ErrShortWrite
			}
			result.ClientDisconnect = true
			cancel(writeErr)
			err = errors.Join(err, writeErr)
		} else if downstreamCtx.Err() != nil {
			result.ClientDisconnect = true
			cancel(downstreamCtx.Err())
			err = errors.Join(err, downstreamCtx.Err())
		}
	}
	if err == nil && (resp.StatusCode >= 300 || resp.StatusCode < 200) {
		err = newCodexEngineResponseError(resp.StatusCode, nil, "")
	}
	var failure *codexEngineResponseError
	if errors.Is(err, errCodexEngineFirstOutputTimeout) {
		recordCodexEngineFirstOutputTimeout(c, account, resp.Header, start, "stream")
	}
	if errors.As(err, &failure) {
		s.observeCodexEngineDeploymentUnavailable(account, resp, failure, time.Now())
		// Match the scheduler's model dimension, including its ordinary mapping
		// for compact/default wire-model overrides. Resolve the original once.
		modelKey := modelRateLimitKeyForUpstreamModelNotFound(ctx, account, model)
		s.observeCodexEngineTempUnschedulable(ctx, account, resp, failure, modelKey)
		recordCodexEngineError(c, account, resp, failure, &result.Usage)
	}
	if err == nil && endpoint == "/v1/alpha/search" {
		result.WebSearchCalls = 1
	}
	if result.ResponseID != "" {
		s.bindHTTPResponseAccount(ctx, c, account, result.ResponseID)
	}
	if failure != nil && !openAIUsageHasTokens(&result.Usage) && result.ImageCount == 0 && codexEngineUsageIDFromHeaders(result.UpstreamHeaders) == "" && !isOpenAICandyTest(ctx) {
		// An unmetered rejection has no billable partial result. Diagnostics
		// still need model evidence; they never enter downstream billing.
		return nil, err
	}
	return result, err
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
		codexEngineUnavailableScopeHeader,
		"X-Codex-Engine-Compatibility", "X-Codex-Engine-Ignored-Fields", "X-Codex-Engine-Token-Estimate") {
		if values := src.Values(name); len(values) > 0 {
			dst[http.CanonicalHeaderKey(name)] = append([]string(nil), values...)
		}
	}
}

// Gin implements Flush without returning the socket error. Prefer an explicit
// FlushError, or unwrap its unbuffered wrappers to reach net/http's implementation.
func flushCodexEngineWriter(writer gin.ResponseWriter) error {
	writer.WriteHeaderNow()
	var current http.ResponseWriter = writer
	for range 32 {
		switch w := current.(type) {
		case interface{ FlushError() error }:
			return w.FlushError()
		case interface{ Unwrap() http.ResponseWriter }:
			current = w.Unwrap()
		case http.Flusher:
			w.Flush()
			return nil
		default:
			return http.ErrNotSupported
		}
	}
	return http.ErrNotSupported
}

func observeCodexEnginePayload(result *OpenAIForwardResult, data []byte, event string) {
	observeCodexEnginePayloadFields(result, data, event)
	root := observeCodexEngineImages(result, data, nil)
	observeCodexEngineUsageFinalityParsed(result, data, event, root)
}

func observeCodexEnginePayloadFields(result *OpenAIForwardResult, data []byte, event string) {
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
}

// Relay complete SSE frames without rewriting their bytes. A separate reader
// keeps the existing idle/first-output deadlines effective even on a stalled body.
func (s *OpenAIGatewayService) relayCodexEngineStream(ctx, downstreamCtx context.Context, c *gin.Context, resp *http.Response, result *OpenAIForwardResult, firstTimer *time.Timer, start time.Time, imageRequest bool, cancel context.CancelCauseFunc) error {
	defer runtimeobs.Default.BeginSSE()()
	maxLine := defaultMaxLineSize
	if s.cfg != nil && s.cfg.Gateway.MaxLineSize > 0 {
		maxLine = s.cfg.Gateway.MaxLineSize
	}
	idle := time.Duration(0)
	if s.cfg != nil {
		idle = time.Duration(s.cfg.Gateway.StreamDataIntervalTimeout) * time.Second
	}
	if imageRequest {
		idle = s.openAIImageStreamDataInterval()
	}
	reader := newCodexEngineSSEReader(resp.Body, maxLine, idle > 0)
	defer reader.stop()
	var timer *time.Timer
	var timeout <-chan time.Time
	if idle > 0 {
		timer = time.NewTimer(idle)
		defer timer.Stop()
		timeout = timer.C
	}
	c.Status(resp.StatusCode)
	terminal := false
	var streamFailure *codexEngineResponseError
	var clientDone <-chan struct{}
	if downstreamCtx != nil {
		clientDone = downstreamCtx.Done()
	}
	markClientDisconnected := func(err error) error {
		result.ClientDisconnect = true
		cancel(err)
		if streamFailure != nil {
			return errors.Join(streamFailure, err)
		}
		return err
	}
	imageCounter := newOpenAIImageOutputCounter()
	flush := func(frame *codexEngineSSEFrame) error {
		payload, event := frame.payload(), frame.eventName()
		kind := event
		if kind == "" {
			kind = gjson.GetBytes(payload, "type").String()
		}
		observeCodexEnginePayloadFields(result, payload, kind)
		root := observeCodexEngineImages(result, payload, imageCounter)
		observeCodexEngineUsageFinalityParsed(result, payload, kind, root)
		semantic := codexEngineStreamStartsSemanticTTFT(payload, kind)
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
				nextFailure.tempUnschedBody = mergeCodexEngineTempUnschedBodies(nextFailure.tempUnschedBody, streamFailure.tempUnschedBody)
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
		if downstreamCtx != nil && downstreamCtx.Err() != nil {
			return markClientDisconnected(downstreamCtx.Err())
		}
		c.Set("codex_engine_response_written", true)
		var n int
		var err error
		if writer, ok := c.Writer.(*codexEngineWSWriter); ok {
			n, err = writer.writeSSEFrame(frame.raw, payload, event)
		} else {
			n, err = c.Writer.Write(frame.raw)
		}
		if err != nil || n != len(frame.raw) {
			if err == nil {
				err = io.ErrShortWrite
			}
			return markClientDisconnected(err)
		}
		if downstreamCtx != nil && downstreamCtx.Err() != nil {
			return markClientDisconnected(downstreamCtx.Err())
		}
		if err := flushCodexEngineWriter(c.Writer); err != nil {
			return markClientDisconnected(err)
		}
		if downstreamCtx != nil && downstreamCtx.Err() != nil {
			return markClientDisconnected(downstreamCtx.Err())
		}
		return nil
	}
	for {
		select {
		case <-ctx.Done():
			if downstreamCtx != nil && downstreamCtx.Err() != nil {
				return markClientDisconnected(downstreamCtx.Err())
			}
			if streamFailure != nil {
				return errors.Join(streamFailure, context.Cause(ctx))
			}
			return context.Cause(ctx)
		case <-clientDone:
			return markClientDisconnected(downstreamCtx.Err())
		case <-timeout:
			if remaining := reader.idleRemaining(idle); remaining > 0 {
				timer.Reset(remaining)
				continue
			}
			if streamFailure != nil {
				return streamFailure
			}
			return fmt.Errorf("Codex-Engine stream idle timeout")
		case <-reader.progress:
			if timer != nil {
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				remaining := reader.idleRemaining(idle)
				if remaining <= 0 {
					remaining = time.Nanosecond
				}
				timer.Reset(remaining)
			}
		case frame, ok := <-reader.frames:
			if !ok {
				return ErrOpenAIUpstreamStreamTruncated
			}
			runtimeobs.Default.ObserveSSEBuffer(int64(cap(frame.raw) + cap(frame.scratch)))
			readErr := frame.err
			if frame.oversized {
				reader.release(frame)
				if streamFailure != nil {
					return errors.Join(streamFailure, readErr)
				}
				return readErr
			}
			err := flush(frame)
			reader.release(frame)
			if err != nil {
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
			if readErr != nil {
				if streamFailure != nil {
					return streamFailure
				}
				if readErr != io.EOF {
					return readErr
				}
				if !terminal {
					return ErrOpenAIUpstreamStreamTruncated
				}
				return nil
			}
		}
	}
}
