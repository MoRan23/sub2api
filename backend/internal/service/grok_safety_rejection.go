package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"strings"
	"sync"

	"github.com/Wei-Shaw/sub2api/internal/pkg/groksafetyobserver"
	"github.com/Wei-Shaw/sub2api/internal/util/responseheaders"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/tidwall/gjson"
)

const GrokSafetyRejectionCode = "safety_review_rejected"
const grokSafetyObservationLimit = 1 << 20

// GrokSafetyRejection contains only bounded, request-local billing evidence.
// Fee is the request-start group policy, never a value supplied by the upstream.
type GrokSafetyRejection struct {
	CallID                  string  `json:"call_id"`
	Code                    string  `json:"code"`
	Fee                     float64 `json:"fee"`
	HTTPStatus              int     `json:"http_status"`
	UpstreamRequestID       string  `json:"upstream_request_id,omitempty"`
	UpstreamClientRequestID string  `json:"upstream_client_request_id,omitempty"`
	TokenPricingUnavailable bool    `json:"token_pricing_unavailable,omitempty"`
}

var ErrGrokSafetyRejected = errors.New("Grok upstream safety review rejected")

type grokSafetyContextKey struct{}
type grokSafetyHolder struct {
	mu       sync.Mutex
	fee      float64
	selected *grokSafetyAttempt
}
type grokSafetyAttempt struct {
	mu         sync.Mutex
	holder     *grokSafetyHolder
	evidence   GrokSafetyRejection
	rejected   bool
	usage      OpenAIUsage
	usageFinal bool
	body       *grokSafetyBody
}

// WithGrokSafetyFee freezes the policy once. A zero fee still observes refusals
// and final usage, so disabling the fixed fee does not hide genuine usage.
func WithGrokSafetyFee(ctx context.Context, fee float64) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if grokSafetyHolderFromContext(ctx) != nil {
		return ctx
	}
	if math.IsNaN(fee) || math.IsInf(fee, 0) || fee < 0 {
		fee = 0
	}
	h := &grokSafetyHolder{fee: fee}
	ctx = context.WithValue(ctx, grokSafetyContextKey{}, h)
	return groksafetyobserver.WithFactory(ctx, func(*http.Request) groksafetyobserver.Observer { return h.begin() })
}

func grokSafetyHolderFromContext(ctx context.Context) *grokSafetyHolder {
	if ctx == nil {
		return nil
	}
	h, _ := ctx.Value(grokSafetyContextKey{}).(*grokSafetyHolder)
	return h
}

func WithGrokSafetyGroup(ctx context.Context, group *Group) context.Context {
	if group == nil || group.Platform != PlatformGrok {
		return ctx
	}
	fee := 0.0
	if group.GrokSafetyFeeEnabled && group.GrokSafetyFee != nil {
		fee = *group.GrokSafetyFee
	}
	return WithGrokSafetyFee(ctx, fee)
}

func freshGrokSafetyGroup(ctx context.Context, group *Group) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx = context.WithValue(ctx, grokSafetyContextKey{}, (*grokSafetyHolder)(nil))
	ctx = groksafetyobserver.WithFactory(ctx, nil)
	return WithGrokSafetyGroup(ctx, group)
}
func (h *grokSafetyHolder) begin() *grokSafetyAttempt {
	a := &grokSafetyAttempt{holder: h, evidence: GrokSafetyRejection{CallID: uuid.NewString(), Code: GrokSafetyRejectionCode, Fee: h.fee}}
	a.Selected()
	return a
}
func (a *grokSafetyAttempt) Selected() {
	a.holder.mu.Lock()
	a.holder.selected = a
	a.holder.mu.Unlock()
}
func (a *grokSafetyAttempt) Rejected() bool { a.mu.Lock(); defer a.mu.Unlock(); return a.rejected }
func (h *grokSafetyHolder) current() *grokSafetyAttempt {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.selected
}

func (a *grokSafetyAttempt) ObserveResponse(resp *http.Response) {
	a.mu.Lock()
	a.evidence.HTTPStatus = resp.StatusCode
	a.evidence.UpstreamRequestID = grokSafetySmallID(firstNonEmpty(resp.Header.Get("x-request-id"), resp.Header.Get("xai-request-id")))
	a.evidence.UpstreamClientRequestID = grokSafetySmallID(resp.Header.Get("x-client-request-id"))
	a.mu.Unlock()
	if resp.Body != nil {
		b := &grokSafetyBody{body: resp.Body, attempt: a, sse: strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "text/event-stream")}
		a.mu.Lock()
		a.body = b
		a.mu.Unlock()
		resp.Body = b
	}
}

func PrepareGrokSafetyRequest(req *http.Request) *http.Request {
	if req == nil || grokSafetyHolderFromContext(req.Context()) == nil {
		return req
	}
	return groksafetyobserver.Enable(req)
}

func (a *grokSafetyAttempt) takeOver(resp *http.Response) {
	a.mu.Lock()
	b := a.body
	a.mu.Unlock()
	if b == nil {
		return
	}
	b.mu.Lock()
	b.done = true
	b.buf = nil
	b.line = nil
	b.mu.Unlock()
	if resp.Body == b {
		resp.Body = b.body
	}
}
func grokSafetySmallID(s string) string {
	if len(s) > 256 {
		s = s[:256]
	}
	return strings.Clone(s)
}

// ObserveGrokSafetyHTTPResponse covers fake transports and direct HTTP clients.
// Responses already observed by the repository are selected, never wrapped twice.
func ObserveGrokSafetyHTTPResponse(ctx context.Context, req *http.Request, resp *http.Response) {
	if resp == nil {
		return
	}
	if groksafetyobserver.FromResponse(resp) == nil {
		if h := grokSafetyHolderFromContext(ctx); h != nil {
			groksafetyobserver.Attach(req, resp, h.begin())
		}
	}
	groksafetyobserver.Select(resp)
	if a, ok := groksafetyobserver.FromResponse(resp).(*grokSafetyAttempt); ok {
		a.takeOver(resp)
	}
}

// StartGrokSafetyAttempt starts one Realtime session or handshake observation.
func StartGrokSafetyAttempt(ctx context.Context, status int, headers http.Header) {
	if h := grokSafetyHolderFromContext(ctx); h != nil {
		h.begin().ObserveResponse(&http.Response{StatusCode: status, Header: headers})
	}
}

// ObserveGrokSafetyMessage observes a complete upstream WS message, not an
// outbound request or arbitrary nested model text.
func ObserveGrokSafetyMessage(ctx context.Context, payload []byte) {
	ObserveGrokSafetyPayload(ctx, payload, false)
}

// ObserveGrokSafetyPayload reuses a complete payload already admitted by the
// caller's existing size limits; it never copies the complete response.
func ObserveGrokSafetyPayload(ctx context.Context, payload []byte, final bool) {
	if h := grokSafetyHolderFromContext(ctx); h != nil {
		a := h.current()
		if a == nil {
			a = h.begin()
		}
		a.observeJSON(payload, "", final)
	}
}

func IsGrokSafetyRejectionJSON(data []byte) bool {
	if (!bytes.Contains(data, []byte(GrokSafetyRejectionCode)) && !bytes.ContainsRune(data, '\\')) || !gjson.ValidBytes(data) {
		return false
	}
	for _, path := range []string{"error.code", "response.error.code"} {
		v := gjson.GetBytes(data, path)
		if v.Type == gjson.String && v.Str == GrokSafetyRejectionCode {
			return true
		}
	}
	return false
}

func GrokSafetyRejected(ctx context.Context) bool { return GrokSafetyRejectionFromContext(ctx) != nil }
func GrokSafetyRejectionFromContext(ctx context.Context) *GrokSafetyRejection {
	h := grokSafetyHolderFromContext(ctx)
	if h == nil {
		return nil
	}
	a := h.current()
	if a == nil {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.rejected {
		return nil
	}
	evidence := a.evidence
	return &evidence
}

// FinalizeGrokSafetyRejection leaves ordinary outcomes untouched. Rejection
// wins over later success frames; only explicitly final observed token usage is
// billable, never estimated image/video/audio/search success units.
func FinalizeGrokSafetyRejection(ctx context.Context, result *OpenAIForwardResult, err error) (*OpenAIForwardResult, error) {
	h := grokSafetyHolderFromContext(ctx)
	if h == nil {
		return result, err
	}
	a := h.current()
	if a == nil {
		return result, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.rejected {
		return result, err
	}
	if result == nil {
		result = &OpenAIForwardResult{}
	}
	evidence := a.evidence
	result.GrokSafetyRejection = &evidence
	result.RequestID = evidence.CallID
	result.Usage = OpenAIUsage{}
	if a.usageFinal {
		result.Usage = a.usage
	}
	result.ImageCount = 0
	result.ImageOutputSizes = nil
	result.ImageSizeBreakdown = nil
	result.VideoCount = 0
	result.VideoDurationSeconds = 0
	result.SearchCount = 0
	result.WebSearchCalls = 0
	result.AudioUsage = nil
	result.UpstreamTerminalEvent = "response.failed"
	return result, ErrGrokSafetyRejected
}

func finalizeGrokSafetyForward(ctx context.Context, account *Account, body []byte, result *OpenAIForwardResult, err error) (*OpenAIForwardResult, error) {
	if account == nil || !account.IsGrok() {
		return result, err
	}
	result, err = FinalizeGrokSafetyRejection(ctx, result, err)
	if result != nil && result.GrokSafetyRejection != nil {
		if result.Model == "" {
			result.Model = gjson.GetBytes(body, "model").String()
		}
		if result.UpstreamModel == "" {
			result.UpstreamModel = account.GetMappedModel(result.Model)
		}
	}
	return result, err
}

func (s *OpenAIGatewayService) writeGrokSafetyErrorResponse(ctx context.Context, c *gin.Context, resp *http.Response, body []byte) (*OpenAIForwardResult, error) {
	ObserveGrokSafetyPayload(ctx, body, true)
	detail := body
	if len(detail) > 4096 {
		detail = detail[:4096]
	}
	SetOpsUpstreamError(c, resp.StatusCode, extractUpstreamErrorMessage(body), string(detail))
	responseheaders.WriteFilteredHeaders(c.Writer.Header(), resp.Header, s.responseHeaderFilter)
	writeGrokSafetyRejectionResponse(c, resp.StatusCode, body)
	return FinalizeGrokSafetyRejection(ctx, nil, ErrGrokSafetyRejected)
}

// Messages requires its error envelope even when the upstream uses Responses.
// Only the original error object is moved; no refusal fields are synthesized.
func grokSafetyMessagesErrorPayload(body []byte) []byte {
	e := gjson.GetBytes(body, "error")
	if !e.IsObject() {
		e = gjson.GetBytes(body, "response.error")
	}
	payload, err := json.Marshal(struct {
		Type  string          `json:"type"`
		Error json.RawMessage `json:"error"`
	}{Type: "error", Error: json.RawMessage(e.Raw)})
	if err != nil {
		return body
	}
	return payload
}

func (a *grokSafetyAttempt) observeJSON(data []byte, event string, plainJSON bool) {
	if len(data) == 0 {
		return
	}
	rejected := IsGrokSafetyRejectionJSON(data)
	// Most SSE deltas contain neither an error nor usage. Skip full validation
	// and field scans for those frames. A backslash retains the general path so
	// escaped JSON keys and values have exactly the same decoded semantics.
	if !plainJSON && !rejected && !bytes.Contains(data, []byte(`"usage"`)) && !bytes.ContainsRune(data, '\\') {
		return
	}
	if !gjson.ValidBytes(data) {
		return
	}
	typ := gjson.GetBytes(data, "type").String()
	if typ == "" {
		typ = event
	}
	terminal := plainJSON || rejected || openAIStreamEventTypeIsTerminal(typ)
	// Chat Completions' final include_usage frame has an explicitly empty
	// choices array. Do not charge progressive usage from ordinary deltas.
	usageOnly := false
	// Use a borrowed view: GetBytes on choices would copy an entire image/text
	// delta. The result is inspected synchronously and never retained.
	if choices := gjson.Get(openAIWSPayloadStringView(data), "choices"); choices.IsArray() && strings.TrimSpace(choices.Raw[1:len(choices.Raw)-1]) == "" {
		terminal = true
		usageOnly = true
	}
	var usage OpenAIUsage
	final := false
	if terminal {
		usage, final = grokSafetyFinalUsage(data)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.rejected {
		if usageOnly && final {
			a.usage = usage
			a.usageFinal = true
		}
		return
	}
	if rejected {
		a.rejected = true
	}
	if final {
		a.usage = usage
		a.usageFinal = true
	}
}

func grokSafetyFinalUsage(data []byte) (OpenAIUsage, bool) {
	for _, path := range []string{"usage", "response.usage", "data.usage", "data.response.usage"} {
		v := gjson.GetBytes(data, path)
		if !v.IsObject() {
			continue
		}
		if !grokSafetyValidTokenFields(v, 0) {
			return OpenAIUsage{}, false
		}
		return openAIUsageFromGJSON(v)
	}
	return OpenAIUsage{}, false
}

func grokSafetyValidTokenFields(value gjson.Result, depth int) bool {
	valid := true
	value.ForEach(func(key, v gjson.Result) bool {
		if v.IsObject() && depth < 3 {
			valid = grokSafetyValidTokenFields(v, depth+1)
		} else if strings.HasSuffix(key.Str, "tokens") {
			valid = v.Type == gjson.Number && v.Num >= 0 && v.Num < float64(math.MaxInt64) && math.Trunc(v.Num) == v.Num
		}
		return valid
	})
	return valid
}

// grokSafetyBody observes bytes the existing consumer actually reads. It never
// drains, rewrites or changes read boundaries and retains at most one small
// JSON/frame observation. Oversized frames are skipped, not partially parsed.
type grokSafetyBody struct {
	mu           sync.Mutex
	body         io.ReadCloser
	attempt      *grokSafetyAttempt
	sse          bool
	decided      bool
	buf          []byte
	line         []byte
	event        string
	overflow     bool
	lineOverflow bool
	done         bool
}

func (b *grokSafetyBody) Read(p []byte) (int, error) {
	n, err := b.body.Read(p)
	b.mu.Lock()
	defer b.mu.Unlock()
	if n > 0 {
		b.feed(p[:n])
	}
	if err == io.EOF {
		b.finish()
	}
	return n, err
}
func (b *grokSafetyBody) Close() error {
	err := b.body.Close()
	b.mu.Lock()
	defer b.mu.Unlock()
	b.finish()
	return err
}
func (b *grokSafetyBody) feed(p []byte) {
	if b.done {
		return
	}
	if !b.decided {
		trimmed := bytes.TrimLeft(p, " \t\r\n")
		if len(trimmed) == 0 {
			return
		}
		b.decided = true
		if !b.sse {
			b.sse = trimmed[0] != '{' && trimmed[0] != '['
		}
	}
	if !b.sse {
		if len(b.buf)+len(p) > grokSafetyObservationLimit {
			b.overflow = true
			b.buf = nil
			return
		}
		if !b.overflow {
			b.buf = append(b.buf, p...)
			if bytes.HasSuffix(bytes.TrimSpace(p), []byte("}")) && bytes.Contains(b.buf, []byte(GrokSafetyRejectionCode)) {
				b.attempt.observeJSON(b.buf, "", true)
			}
		}
		return
	}
	for len(p) > 0 {
		i := bytes.IndexByte(p, '\n')
		part := p
		if i >= 0 {
			part = p[:i]
		}
		if !b.lineOverflow {
			if len(b.line)+len(part) > grokSafetyObservationLimit {
				b.lineOverflow = true
				b.line = nil
				b.overflow = true
			} else {
				b.line = append(b.line, part...)
			}
		}
		if i < 0 {
			break
		}
		b.processLine()
		p = p[i+1:]
	}
}
func (b *grokSafetyBody) processLine() {
	line := bytes.TrimSuffix(b.line, []byte{'\r'})
	if !b.lineOverflow {
		if len(line) == 0 {
			b.frame()
		} else if bytes.HasPrefix(line, []byte("data:")) {
			d := line[5:]
			if len(d) > 0 && d[0] == ' ' {
				d = d[1:]
			}
			if !b.overflow {
				if len(b.buf)+len(d)+1 > grokSafetyObservationLimit {
					b.overflow = true
					b.buf = nil
				} else {
					if len(b.buf) > 0 {
						b.buf = append(b.buf, '\n')
					}
					b.buf = append(b.buf, d...)
				}
			}
		} else if bytes.HasPrefix(line, []byte("event:")) {
			e := bytes.TrimSpace(line[6:])
			if len(e) < 128 {
				b.event = string(e)
			}
		}
	}
	b.line = b.line[:0]
	b.lineOverflow = false
}
func (b *grokSafetyBody) frame() {
	if !b.overflow {
		b.attempt.observeJSON(b.buf, b.event, false)
	}
	b.buf = b.buf[:0]
	b.event = ""
	b.overflow = false
}
func (b *grokSafetyBody) finish() {
	if b.done {
		return
	}
	if b.sse {
		if len(b.line) > 0 || b.lineOverflow {
			b.processLine()
		}
		b.frame()
	} else if !b.overflow {
		b.attempt.observeJSON(b.buf, "", true)
	}
	b.done = true
	b.buf = nil
	b.line = nil
}
