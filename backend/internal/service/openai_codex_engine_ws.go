package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/runtimeobs"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Engine's public contract is HTTP/SSE. Its native WebSocket transport belongs
// to the internal worker, so client WS turns use this dedicated HTTP bridge.
// The original previous_response_id, tools, metadata, and opaque input are never
// rebuilt from connection history or replayed after an ambiguous execution.
func (s *OpenAIGatewayService) proxyCodexEngineWebSocket(ctx context.Context, c *gin.Context, conn *coderws.Conn, account *Account, first []byte, hooks *OpenAIWSIngressHooks) error {
	bodyObservation := openAIWSBodyObservation(c)
	if bodyObservation == nil {
		bodyObservation = StartOpenAIWSBodyObservation(c, len(first))
		defer bodyObservation.Close()
	}
	if s.cfg != nil && s.cfg.Gateway.OpenAIWS.ModeRouterV2Enabled && account.ResolveOpenAIResponsesWebSocketV2Mode(s.cfg.Gateway.OpenAIWS.IngressModeDefault) == OpenAIWSIngressModeOff {
		return NewOpenAIWSClientCloseError(coderws.StatusPolicyViolation, "websocket mode is disabled for this account", nil)
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if hooks != nil && hooks.ClientLifecycleContext != nil {
		stop := context.AfterFunc(hooks.ClientLifecycleContext, cancel)
		defer stop()
	}
	SetOpenAIClientTransport(c, OpenAIClientTransportWS)
	conn.SetReadLimit(ResolveOpenAIWSClientReadLimitBytes(s.cfg))
	type observedEngineFrame struct {
		openAIWSClientReadResult
		body *runtimeobs.BodyLease
	}
	frames := make(chan observedEngineFrame, 1)
	readerDone := make(chan struct{})
	write := func(payload []byte) error {
		writeCtx, release := context.WithTimeout(ctx, s.openAIWSWriteTimeout())
		defer release()
		if err := conn.Write(writeCtx, coderws.MessageText, payload); err != nil {
			cancel()
			return err
		}
		return nil
	}
	// Keep exactly one reader active during inference, so a real disconnect
	// cancels the HTTP request immediately instead of waiting for output/usage.
	go func() {
		defer close(readerDone)
		for {
			kind, payload, err := conn.Read(ctx)
			if err != nil {
				cancel()
				return
			}
			if gjson.ValidBytes(payload) && gjson.GetBytes(payload, "type").String() == "response.interrupt" {
				// This is a control frame, not another billable turn. The public
				// HTTP contract cannot acknowledge a native response interrupt.
				failure, _ := json.Marshal(map[string]any{
					"type": "response.interrupt.failed", "response_id": gjson.GetBytes(payload, "response_id").String(),
					"error": map[string]string{"code": "unsupported_operation", "message": "Engine HTTP bridge does not support response.interrupt"},
				})
				if write(failure) != nil {
					return
				}
				continue
			}
			frame := observedEngineFrame{openAIWSClientReadResult: openAIWSClientReadResult{messageType: kind, payload: payload}, body: bodyObservation.Begin(int64(len(payload)))}
			select {
			case frames <- frame:
			case <-ctx.Done():
				frame.body.Release()
				return
			default:
				frame.body.Release()
				// One pending create is enough; never stop reading a disconnected
				// peer behind an unbounded request queue.
				_ = conn.Close(coderws.StatusPolicyViolation, "only one pending response.create is allowed")
				cancel()
				return
			}
		}
	}()
	defer func() {
		cancel()
		_ = conn.CloseNow()
		<-readerDone
		for {
			select {
			case frame := <-frames:
				frame.body.Release()
			default:
				return
			}
		}
	}()
	model := ""
	if hooks != nil {
		model = strings.TrimSpace(hooks.InitialRequestModel)
	}
	payload := first
	for turn := 1; ; turn++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		startedAt := time.Now()
		if turn == 1 && hooks != nil && !hooks.InitialTurnStartedAt.IsZero() {
			startedAt = hooks.InitialTurnStartedAt
		}
		body, currentModel, err := prepareCodexEngineWSBody(payload, model)
		if err != nil {
			return NewOpenAIWSClientCloseError(coderws.StatusPolicyViolation, err.Error(), nil)
		}
		if turn == 1 && hooks != nil && strings.TrimSpace(hooks.InitialRequestModel) != "" {
			currentModel = strings.TrimSpace(hooks.InitialRequestModel)
		}
		if hooks != nil {
			if hooks.TurnStarted != nil {
				hooks.TurnStarted(turn, startedAt)
			}
			if hooks.BeforeRequest != nil {
				if err := hooks.BeforeRequest(turn, payload, currentModel); err != nil {
					return err
				}
			}
			if body, err = applyOpenAIWSReasoningEffortPolicy(body, hooks); err != nil {
				return NewOpenAIWSClientCloseError(coderws.StatusPolicyViolation, err.Error(), nil)
			}
			if hooks.MapRequestModel != nil {
				mapped, mapErr := hooks.MapRequestModel(turn, currentModel)
				if mapErr != nil {
					return mapErr
				}
				body, err = sjson.SetBytes(body, "model", mapped)
				if err != nil {
					return err
				}
			}
			if hooks.BeforeTurn != nil {
				if err := hooks.BeforeTurn(turn); err != nil {
					return err
				}
			}
			if hooks.BeforeEngineDispatch != nil {
				if err := hooks.BeforeEngineDispatch(turn, payload, currentModel); err != nil {
					return err
				}
			}
		}
		model = currentModel
		writer := &codexEngineWSWriter{ResponseWriter: c.Writer, header: make(http.Header), status: http.StatusOK, write: write}
		forwardErr := func() error {
			originalWriter := c.Writer
			c.Writer = writer
			defer func() { c.Writer = originalWriter }()
			c.Set("codex_engine_response_written", false)
			result, err := s.forwardCodexEngine(ctx, c, account, body, "/v1/responses", "")
			if result != nil {
				result.OpenAIWSMode = true
				result.Model = currentModel
			}
			// Audit callbacks must read this physical turn's response headers,
			// including an absent request ID, rather than the handshake's headers.
			if hooks != nil && hooks.AfterTurn != nil {
				hooks.AfterTurn(turn, result, err)
			}
			return err
		}()
		bodyObservation.CompleteCurrent()
		if forwardErr != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if writer.sent == 0 {
				_ = write(buildOpenAIWSHTTPBridgeErrorEvent(http.StatusBadGateway, "Engine request could not be completed"))
			}
			// Never expose an UpstreamFailoverError to the connection's account
			// retry loop: accepted/uncertain Engine turns are reconciled by receipt.
			return NewOpenAIWSClientCloseError(coderws.StatusInternalError, "Engine request failed", nil)
		}
		var idle <-chan time.Time
		var timer *time.Timer
		if timeout := s.openAIWSIngressInterTurnIdleTimeout(); timeout > 0 {
			timer = time.NewTimer(timeout)
			idle = timer.C
		}
		select {
		case next := <-frames:
			if timer != nil {
				timer.Stop()
			}
			payload = next.payload
			bodyObservation.Activate(next.body)
		case <-ctx.Done():
			if timer != nil {
				timer.Stop()
			}
			return ctx.Err()
		case <-idle:
			return NewOpenAIWSClientCloseError(coderws.StatusNormalClosure, "websocket idle timeout", nil)
		}
	}
}

func prepareCodexEngineWSBody(payload []byte, fallbackModel string) ([]byte, string, error) {
	if !gjson.ValidBytes(payload) || len(bytes.TrimSpace(payload)) == 0 || bytes.TrimSpace(payload)[0] != '{' {
		return nil, "", errors.New("response.create payload must be a JSON object")
	}
	if kind := gjson.GetBytes(payload, "type").String(); kind != "" && kind != "response.create" {
		return nil, "", fmt.Errorf("unsupported Engine websocket request type: %s", kind)
	}
	model := strings.TrimSpace(gjson.GetBytes(payload, "model").String())
	if model == "" {
		model = fallbackModel
	}
	if model == "" {
		return nil, "", errors.New("model is required in response.create payload")
	}
	body, err := sjson.DeleteBytes(payload, "type")
	if err == nil {
		body, err = sjson.SetBytes(body, "stream", true)
	}
	if err == nil && gjson.GetBytes(body, "model").String() != model {
		body, err = sjson.SetBytes(body, "model", model)
	}
	return body, model, err
}

// Adapts the raw HTTP relay at the wire boundary only. Complete SSE data fields
// become WS events; JSON HTTP errors retain their structured error and status.
type codexEngineWSWriter struct {
	gin.ResponseWriter
	header  http.Header
	status  int
	size    int
	sent    int
	written bool
	write   func([]byte) error
	err     error
}

func (w *codexEngineWSWriter) Header() http.Header { return w.header }
func (w *codexEngineWSWriter) WriteHeader(status int) {
	if !w.written {
		w.status = status
	}
}
func (w *codexEngineWSWriter) WriteHeaderNow()                       { w.written = true }
func (w *codexEngineWSWriter) Status() int                           { return w.status }
func (w *codexEngineWSWriter) Size() int                             { return w.size }
func (w *codexEngineWSWriter) Written() bool                         { return w.written }
func (w *codexEngineWSWriter) Flush()                                { _ = w.FlushError() }
func (w *codexEngineWSWriter) FlushError() error                     { return w.err }
func (w *codexEngineWSWriter) WriteString(value string) (int, error) { return w.Write([]byte(value)) }

func (w *codexEngineWSWriter) Write(data []byte) (int, error) {
	if w.err != nil {
		return 0, w.err
	}
	w.written = true
	payload := bytes.TrimSpace(data)
	event := ""
	if strings.Contains(strings.ToLower(w.header.Get("Content-Type")), "text/event-stream") {
		payload = nil
		for _, line := range bytes.Split(data, []byte{'\n'}) {
			line = bytes.TrimSuffix(line, []byte{'\r'})
			if bytes.HasPrefix(line, []byte("event:")) {
				event = strings.TrimSpace(string(line[6:]))
			}
			if bytes.HasPrefix(line, []byte("data:")) {
				if payload != nil {
					payload = append(payload, '\n')
				}
				payload = append(payload, bytes.TrimPrefix(line[5:], []byte{' '})...)
			}
		}
		payload = bytes.TrimSpace(payload)
		if len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) {
			w.size += len(data)
			return len(data), nil
		}
	}
	return w.writePayload(data, payload, event)
}

// The relay has already parsed this frame. Its data view is borrowed until the
// synchronous websocket write returns; only missing protocol fields allocate.
func (w *codexEngineWSWriter) writeSSEFrame(data, payload []byte, event string) (int, error) {
	if w.err != nil {
		return 0, w.err
	}
	w.written = true
	payload = bytes.TrimSpace(payload)
	if len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) {
		w.size += len(data)
		return len(data), nil
	}
	return w.writePayload(data, payload, event)
}

func (w *codexEngineWSWriter) writePayload(data, payload []byte, event string) (int, error) {
	if len(payload) == 0 || payload[0] != '{' || !gjson.ValidBytes(payload) {
		w.err = errors.New("invalid Engine response event")
		return 0, w.err
	}
	if event == "" && w.status >= 400 {
		event = "error"
	}
	if event != "" && !gjson.GetBytes(payload, "type").Exists() {
		payload, w.err = sjson.SetBytes(payload, "type", event)
	}
	if w.err == nil && w.status >= 400 && !gjson.GetBytes(payload, "status").Exists() {
		payload, w.err = sjson.SetBytes(payload, "status", w.status)
	}
	if w.err == nil {
		w.err = w.write(payload)
	}
	if w.err != nil {
		return 0, w.err
	}
	w.size += len(data)
	w.sent++
	return len(data), nil
}

var _ gin.ResponseWriter = (*codexEngineWSWriter)(nil)
var _ io.StringWriter = (*codexEngineWSWriter)(nil)
