package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// Match the passthrough relay's bounded usage drain. This is a gateway cleanup
// budget, not the client's Cancel deadline (which is not sent on the wire).
const openAIWSNativeControlDrainTimeout = 1200 * time.Millisecond
const openAIWSNativeControlContextKey = "openai_ws_native_control"

var errOpenAIWSInterruptCleanup = errors.New("interrupted websocket connection retired")

// IsOpenAIWSInterruptCleanupError identifies client control cleanup, which
// requires a reconnect without recording an upstream account health failure.
func IsOpenAIWSInterruptCleanupError(err error) bool {
	return errors.Is(err, errOpenAIWSInterruptCleanup)
}

func NewOpenAIWSInterruptCleanupError() error {
	return NewOpenAIWSClientCloseError(coderws.StatusTryAgainLater, "websocket interruption cleanup requires reconnect", errOpenAIWSInterruptCleanup)
}

// A reader belongs to the downstream connection, not an account attempt. An
// attempt may fail over on that same connection; canceling coder/websocket's
// Read would close it. detach synchronizes with all control writes before the
// attempt releases its upstream lease. The handler's final close joins Read.
type openAIWSNativeControl struct {
	mu             sync.Mutex
	writerMu       sync.Mutex
	writer         func([]byte) error
	conn           *coderws.Conn
	readCtx        context.Context
	readIOCtx      context.Context
	cancelRead     context.CancelFunc
	idleTimeout    time.Duration
	writeTimeout   time.Duration
	startOnce      sync.Once
	started        atomic.Bool
	creates        chan []byte
	done           chan struct{}
	readErr        error
	lease          *openAIWSConnLease
	responseID     string
	pendingAck     bool
	interrupted    bool
	terminalSeen   bool
	retire         bool
	turnCtx        context.Context
	turnCancel     context.CancelFunc
	drainTimer     *time.Timer
	idleTimer      *time.Timer
	idleGeneration uint64
}

func getOpenAIWSNativeControl(c *gin.Context, ctx context.Context, conn *coderws.Conn, idleTimeout, writeTimeout time.Duration, writer func([]byte) error) *openAIWSNativeControl {
	var control *openAIWSNativeControl
	if value, ok := c.Get(openAIWSNativeControlContextKey); ok {
		control, _ = value.(*openAIWSNativeControl)
	}
	if control == nil || control.conn != conn {
		control = &openAIWSNativeControl{
			conn: conn, readCtx: ctx, idleTimeout: idleTimeout,
			creates: make(chan []byte, 1), done: make(chan struct{}),
		}
		control.readIOCtx, control.cancelRead = context.WithCancel(context.Background())
		c.Set(openAIWSNativeControlContextKey, control)
	}
	control.writerMu.Lock()
	control.writer = writer
	control.writerMu.Unlock()
	control.mu.Lock()
	control.writeTimeout = writeTimeout
	control.mu.Unlock()
	return control
}

func (c *openAIWSNativeControl) write(payload []byte) error {
	c.writerMu.Lock()
	defer c.writerMu.Unlock()
	return c.writer(payload)
}

func (c *openAIWSNativeControl) start() {
	c.startOnce.Do(func() { c.started.Store(true); go c.readLoop() })
}

func (c *openAIWSNativeControl) finishAttempt(err error) {
	c.detach()
	var failover *UpstreamFailoverError
	if errors.As(err, &failover) || !c.started.Load() {
		return
	}
	select {
	case <-c.done:
		return
	default:
	}
	status, reason := coderws.StatusNormalClosure, ""
	var closeErr *OpenAIWSClientCloseError
	if errors.As(err, &closeErr) {
		status, reason = closeErr.StatusCode(), closeErr.Reason()
	}
	c.closeReader(status, reason)
	<-c.done
}

func (c *openAIWSNativeControl) closeReader(status coderws.StatusCode, reason string) {
	// CloseNow cannot interrupt an already-running coder/websocket Close: it
	// waits for that same handshake. Cancel the armed Read instead, which closes
	// the transport internally, then join both the close and the reader.
	forceClose := time.AfterFunc(200*time.Millisecond, c.cancelRead)
	_ = c.conn.Close(status, truncateString(reason, 120))
	forceClose.Stop()
	c.cancelRead()
	_ = c.conn.CloseNow()
}

func (c *openAIWSNativeControl) readFrame() (coderws.MessageType, []byte, error) {
	readDone := make(chan openAIWSClientReadResult, 1)
	go func() {
		kind, payload, err := c.conn.Read(c.readIOCtx)
		readDone <- openAIWSClientReadResult{messageType: kind, payload: payload, err: err}
	}()
	select {
	case result := <-readDone:
		return result.messageType, result.payload, result.err
	case <-c.readCtx.Done():
		cause := context.Cause(c.readCtx)
		status, reason := coderws.StatusGoingAway, "websocket request canceled"
		if errors.Is(cause, ErrOpenAIWSIngressLeaseLost) {
			status, reason = coderws.StatusTryAgainLater, "websocket ingress capacity lease lost; please reconnect"
		}
		c.closeReader(status, reason)
		<-readDone
		return 0, nil, NewOpenAIWSClientCloseError(status, reason, cause)
	}
}

func (c *openAIWSNativeControl) readLoop() {
	defer close(c.done)
	for {
		kind, payload, err := c.readFrame()
		if err != nil {
			c.readerStopped(err)
			return
		}
		if kind != coderws.MessageText && kind != coderws.MessageBinary {
			c.readerStopped(NewOpenAIWSClientCloseError(coderws.StatusPolicyViolation, "unsupported websocket client message type", nil))
			return
		}
		if gjson.ValidBytes(payload) && gjson.GetBytes(payload, "type").String() == "response.interrupt" {
			if err := c.interrupt(payload); err != nil {
				c.readerStopped(err)
				return
			}
			continue
		}
		// Keep at most one next request without blocking control frames behind
		// it. Admission, payload adaptation and hooks stay on the turn owner.
		select {
		case c.creates <- payload:
			c.mu.Lock()
			c.stopIdleLocked()
			c.mu.Unlock()
		default:
			c.readerStopped(NewOpenAIWSClientCloseError(coderws.StatusPolicyViolation, "too many pending websocket requests", nil))
			return
		}
	}
}

func (c *openAIWSNativeControl) readerStopped(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.readErr == nil {
		c.readErr = err
	}
	c.stopIdleLocked()
	if c.lease != nil && (!c.terminalSeen || c.pendingAck) {
		c.retire = true
		c.armDrainLocked()
	}
}

func (c *openAIWSNativeControl) next(ctx context.Context) ([]byte, error) {
	c.start()
	select {
	case <-c.done:
		c.mu.Lock()
		defer c.mu.Unlock()
		return nil, c.readErr
	default:
	}
	select {
	case payload := <-c.creates:
		return payload, nil
	case <-c.done:
		c.mu.Lock()
		defer c.mu.Unlock()
		return nil, c.readErr
	case <-ctx.Done():
		cause := context.Cause(ctx)
		if errors.Is(cause, ErrOpenAIWSIngressLeaseLost) {
			return nil, NewOpenAIWSClientCloseError(coderws.StatusTryAgainLater, "websocket ingress capacity lease lost; please reconnect", cause)
		}
		return nil, NewOpenAIWSClientCloseError(coderws.StatusGoingAway, "websocket request canceled", cause)
	}
}

func (c *openAIWSNativeControl) begin(ctx context.Context, lease *openAIWSConnLease) context.Context {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.detachLocked()
	c.stopIdleLocked()
	c.lease = lease
	c.responseID = ""
	c.pendingAck, c.interrupted, c.terminalSeen = false, false, false
	c.retire = c.readErr != nil
	c.turnCtx, c.turnCancel = context.WithCancel(ctx)
	if c.retire {
		// This attempt has not dispatched a create yet. Do not start fresh
		// upstream work after the downstream reader has already stopped.
		c.turnCancel()
	}
	return c.turnCtx
}

func (c *openAIWSNativeControl) detachLocked() {
	c.lease = nil
	if c.drainTimer != nil {
		c.drainTimer.Stop()
		c.drainTimer = nil
	}
	if c.turnCancel != nil {
		c.turnCancel()
		c.turnCancel = nil
	}
}

func (c *openAIWSNativeControl) detach() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.detachLocked()
}

func (c *openAIWSNativeControl) retireLease(lease *openAIWSConnLease) {
	// MarkBroken evicts immediately, so finish any control write before closing
	// its transport. Keep the interrupted/ack flags for the turn error classifier.
	c.detach()
	lease.MarkBroken()
}

func (c *openAIWSNativeControl) armDrainLocked() {
	if c.drainTimer == nil && c.turnCancel != nil {
		cancel := c.turnCancel
		c.drainTimer = time.AfterFunc(openAIWSNativeControlDrainTimeout, cancel)
	}
}

func (c *openAIWSNativeControl) stopIdleLocked() {
	c.idleGeneration++
	if c.idleTimer != nil {
		c.idleTimer.Stop()
		c.idleTimer = nil
	}
}

func (c *openAIWSNativeControl) startIdleLocked() {
	c.stopIdleLocked()
	if len(c.creates) != 0 || c.readErr != nil || c.idleTimeout <= 0 {
		return
	}
	generation := c.idleGeneration
	c.idleTimer = time.AfterFunc(c.idleTimeout, func() {
		c.mu.Lock()
		if generation != c.idleGeneration {
			c.mu.Unlock()
			return
		}
		c.readErr = NewOpenAIWSClientCloseError(coderws.StatusNormalClosure, "websocket idle timeout", context.DeadlineExceeded)
		c.mu.Unlock()
		c.closeReader(coderws.StatusNormalClosure, "websocket idle timeout")
	})
}

func (c *openAIWSNativeControl) interrupt(payload []byte) error {
	id := gjson.GetBytes(payload, "response_id")
	if id.Type != gjson.String || strings.TrimSpace(id.String()) == "" {
		return c.write([]byte(`{"type":"error","status":400,"error":{"type":"invalid_request_error","code":"invalid_response_id","message":"response.interrupt requires a nonempty response_id"}}`))
	}
	c.mu.Lock()
	if c.lease == nil || c.responseID != id.String() || c.terminalSeen || c.retire || c.pendingAck {
		c.mu.Unlock()
		ack, _ := json.Marshal(map[string]any{"type": "response.interrupt.failed", "response_id": id.String()})
		return c.write(ack)
	}
	// Holding mu gives detach/release a write barrier. The transport serializes
	// this write with response.create and permits the independent upstream read.
	c.interrupted, c.pendingAck = true, true
	err := c.lease.WriteJSONWithContextTimeout(c.turnCtx, json.RawMessage(payload), c.writeTimeout)
	if err != nil {
		c.retire = true
		c.armDrainLocked()
	}
	c.mu.Unlock()
	// Keep draining usage if the write failed; it must not trigger replay.
	return nil
}

func (c *openAIWSNativeControl) observeCreated(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.responseID == "" {
		c.responseID = id
	}
}

func (c *openAIWSNativeControl) observeAck(payload []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	id := gjson.GetBytes(payload, "response_id")
	if !c.pendingAck || id.Type != gjson.String || id.String() != c.responseID || gjson.GetBytes(payload, "type").String() == "response.interrupt.failed" {
		c.retire = true
		c.armDrainLocked()
	}
	c.pendingAck = false
	if c.terminalSeen {
		if c.drainTimer != nil && !c.retire {
			c.drainTimer.Stop()
			c.drainTimer = nil
		}
		c.startIdleLocked()
	}
}

func (c *openAIWSNativeControl) terminal() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.terminalSeen = true
	if c.pendingAck {
		c.armDrainLocked()
		return false
	}
	c.startIdleLocked()
	return true
}

func (c *openAIWSNativeControl) receivedTerminal() {
	c.mu.Lock()
	c.terminalSeen = true
	c.mu.Unlock()
}

func (c *openAIWSNativeControl) finishReady() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.terminalSeen && !c.pendingAck
}

func (c *openAIWSNativeControl) interruptedTurn() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.interrupted || c.readErr != nil
}

func (c *openAIWSNativeControl) mustRetire() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.retire || c.pendingAck
}

func (c *openAIWSNativeControl) clientGone() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return isOpenAIWSClientDisconnectError(c.readErr)
}

func (c *openAIWSNativeControl) cleanupError() error {
	return NewOpenAIWSInterruptCleanupError()
}
