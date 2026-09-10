package service

import (
	"bytes"
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

const openAIRequestTimezoneCaptureKey = "openai_request_timezone_capture"
const openAIRequestTimezoneStateKey = "openai_request_timezone_state"

type openAIRequestTimezoneCapture struct {
	acceptedAt               time.Time
	body                     []byte
	inbound                  *TimezoneScanResult
	states                   map[bool]*RequestTimezoneState
	compactionInputReordered bool
}

// CaptureOpenAIRequestTimezone freezes the ingress clock and optional observation
// before queuing, channel mapping, or account-specific request adaptation.
func (s *OpenAIGatewayService) CaptureOpenAIRequestTimezone(c *gin.Context, body []byte) {
	if c == nil {
		return
	}
	if _, exists := c.Get(openAIRequestTimezoneCaptureKey); exists {
		return
	}
	acceptedAt := time.Now()
	ctx := context.Background()
	if c.Request != nil {
		ctx = c.Request.Context()
	}
	s.freezeOpenAIRequestPolicy(ctx, c)
	capture := &openAIRequestTimezoneCapture{acceptedAt: acceptedAt, body: bytes.Clone(body), states: make(map[bool]*RequestTimezoneState)}
	if globalFingerprintObserver.enabled.Load() {
		capture.inbound = ScanOpenAIRequestTimezones(body)
	}
	c.Set(openAIRequestTimezoneCaptureKey, capture)
}

func (s *OpenAIGatewayService) freezeOpenAIRequestPolicy(ctx context.Context, c *gin.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if c != nil && c.Request != nil {
		if policy, ok := openai.RequestPolicyFromContext(c.Request.Context()); ok {
			return openai.WithRequestPolicy(ctx, policy)
		}
	}
	var settings *SettingService
	if s != nil {
		settings = s.settingService
	}
	ctx = FreezeOpenAIRequestPolicy(ctx, settings)
	if c != nil && c.Request != nil {
		policy, _ := openai.RequestPolicyFromContext(ctx)
		c.Request = c.Request.WithContext(openai.WithRequestPolicy(c.Request.Context(), policy))
	}
	return ctx
}

func RequestTimezoneStateFromContext(c *gin.Context) (*RequestTimezoneState, bool) {
	if c == nil {
		return nil, false
	}
	value, ok := c.Get(openAIRequestTimezoneStateKey)
	state, valid := value.(*RequestTimezoneState)
	return state, ok && valid && state != nil
}

func SetRequestTimezoneState(c *gin.Context, state *RequestTimezoneState) {
	if c != nil {
		c.Set(openAIRequestTimezoneStateKey, state)
	}
}

// MarkOpenAIRequestTimezoneCompactionReorder records a known, stable adapter
// operation. It never reclassifies which original environment belongs to now.
func MarkOpenAIRequestTimezoneCompactionReorder(c *gin.Context) {
	if c != nil {
		if value, ok := c.Get(openAIRequestTimezoneCaptureKey); ok {
			value.(*openAIRequestTimezoneCapture).compactionInputReordered = true
		}
	}
}

func applyCapturedOpenAIRequestTimezone(c *gin.Context, capture *openAIRequestTimezoneCapture, state *RequestTimezoneState, body []byte) []byte {
	// Each account attempt starts from the same ingress body. A previous
	// attempt's protocol adapter must not define this attempt's provenance.
	if c != nil {
		c.Set(fingerprintObservationTimezonePathMappingContextKey, (map[string]string)(nil))
	}
	active := state
	if capture.compactionInputReordered {
		active = CloneRequestTimezoneState(state)
		paths := make(map[string]string)
		for _, report := range state.Conversions {
			paths[report.Path] = report.Path
			parts := strings.SplitN(report.Path, ".", 3)
			if len(parts) != 3 || parts[0] != "input" {
				continue
			}
			index, err := strconv.Atoi(parts[1])
			if err != nil {
				continue
			}
			removed, position := 0, 0
			gjson.GetBytes(capture.body, "input").ForEach(func(_, item gjson.Result) bool {
				if position >= index {
					return false
				}
				if item.Get("type").String() == "compaction_trigger" {
					removed++
				}
				position++
				return true
			})
			paths[report.Path] = "input." + strconv.Itoa(index-removed) + "." + parts[2]
		}
		for i := range active.patches {
			if path, ok := paths[active.patches[i].path]; ok {
				active.patches[i].path = path
			}
		}
		SetFingerprintObservationTimezonePathMapping(c, paths)
	}
	prepared, applied := active.ApplyToBody(body)
	if !applied {
		active = CloneRequestTimezoneState(active)
		for i := range active.Conversions {
			if active.Conversions[i].Status == "converted" {
				active.Conversions[i].Status = "skipped"
				active.Conversions[i].Reason = "source_changed_before_apply"
				active.Conversions[i].Output = active.Conversions[i].Original
				active.Conversions[i].DateAfter = active.Conversions[i].DateBefore
			}
		}
	}
	SetRequestTimezoneState(c, active)
	captureOpenAIRequestTimezoneCheckpoint(c, prepared)
	return prepared
}

func (s *OpenAIGatewayService) prepareOpenAIRequestTimezone(ctx context.Context, c *gin.Context, account *Account, body []byte, passthrough bool) []byte {
	if account == nil || account.Platform != PlatformOpenAI {
		return body
	}
	ctx = s.freezeOpenAIRequestPolicy(ctx, c)
	policy, _ := openai.RequestPolicyFromContext(ctx)
	// WS bridges carry an already frozen frame state. Its patches may apply to
	// the expanded replay; never replace that replay with the original frame.
	if state, ok := RequestTimezoneStateFromContext(c); ok && GetOpenAIClientTransport(c) == OpenAIClientTransportWS {
		if prepared, applied := state.ApplyToBody(body); applied {
			return prepared
		}
		return body
	}
	s.CaptureOpenAIRequestTimezone(c, body)
	capture := &openAIRequestTimezoneCapture{acceptedAt: time.Now(), body: body, states: make(map[bool]*RequestTimezoneState)}
	if c != nil {
		if value, ok := c.Get(openAIRequestTimezoneCaptureKey); ok {
			capture = value.(*openAIRequestTimezoneCapture)
		}
	}
	if state, ok := capture.states[passthrough]; ok {
		return applyCapturedOpenAIRequestTimezone(c, capture, state, body)
	}
	_, state := PrepareOpenAIRequestTimezone(capture.body, policy, capture.acceptedAt, passthrough, globalFingerprintObserver.enabled.Load())
	state.Inbound = capture.inbound
	capture.states[passthrough] = state
	return applyCapturedOpenAIRequestTimezone(c, capture, state, body)
}
