package service

import (
	"bytes"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/tidwall/gjson"
)

const codexTelemetryMaxEventSamples = 256

// codexTelemetryStream observes only scalar protocol metadata. In particular it
// never retains text, tool arguments, raw frames, or upstream error messages.
// The caller serializes access at the HTTP/WS attempt boundary.
type codexTelemetryStream struct {
	result       CodexTelemetryResult
	usage        OpenAIUsage
	failed       bool
	seen         map[string]struct{}
	terminalSeen map[string]struct{}
}

func (s *codexTelemetryStream) observe(raw []byte, fallback string, at time.Time, wait time.Duration) {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("[DONE]")) {
		return
	}
	root := gjson.ParseBytes(raw)
	typ := strings.TrimSpace(root.Get("type").String())
	if typ == "" {
		typ = strings.TrimSpace(fallback)
	}
	if typ == "ping" || typ == "pong" {
		return
	}
	valid := gjson.ValidBytes(raw) && root.IsObject()
	if valid && s.duplicate(root, typ) {
		return
	}
	s.result.EventCount++
	waitIndex := -1
	if wait >= 0 && len(s.result.EventWaitDurationsMS) < codexTelemetryMaxEventSamples {
		waitIndex = len(s.result.EventWaitDurationsMS)
		s.result.EventWaitDurationsMS = append(s.result.EventWaitDurationsMS, float64(wait)/float64(time.Millisecond))
		s.result.EventWaitFailed = append(s.result.EventWaitFailed, false)
	}
	if s.result.FirstEventAt.IsZero() {
		s.result.FirstEventAt = at
	}
	if !valid {
		s.result.FailedEventCount++
		if waitIndex >= 0 {
			s.result.EventWaitFailed[waitIndex] = true
		}
		s.failed, s.result.Status, s.result.FinishedAt = true, "failed", at
		return
	}
	response := root.Get("response")
	if !response.IsObject() {
		response = root
	}
	if id := response.Get("id").String(); id != "" && len(id) <= 256 {
		s.result.ResponseID = strings.Clone(id)
	}
	if tier := response.Get("service_tier").String(); tier != "" {
		s.result.ServiceTier = strings.Clone(normalizeObservedOpenAIServiceTier(tier))
	}
	parseOpenAIResponseUsageInto(raw, typ, &s.usage)
	s.result.InputTokens, s.result.CachedInputTokens, s.result.OutputTokens = int64(s.usage.InputTokens), int64(s.usage.CacheReadInputTokens), int64(s.usage.OutputTokens)
	if value := response.Get("usage.output_tokens_details.reasoning_tokens").Int(); value > s.result.ReasoningOutputTokens {
		s.result.ReasoningOutputTokens = value
	}

	output, agentMessage := codexTelemetryOutputClass(root, typ)
	if output && s.result.FirstTokenAt.IsZero() {
		s.result.FirstTokenAt = at
	}
	if agentMessage && s.result.FirstAgentMessageAt.IsZero() {
		s.result.FirstAgentMessageAt = at
	}
	s.observeEndTurn(response.Get("end_turn"))
	if item := root.Get("item"); item.IsObject() {
		s.observeItem(item)
	}
	for _, item := range response.Get("output").Array() {
		s.observeItem(item)
	}
	if typ == "responsesapi.websocket_timing" {
		s.observeServerTiming(root.Get("timing_metrics"))
	}

	status := ""
	switch typ {
	case "response.completed":
		status = "completed"
	case "response.failed", "error":
		status = "failed"
	case "response.incomplete":
		status = "incomplete"
	case "response.cancelled", "response.canceled":
		status = "cancelled"
	case "response.done", "":
		status = codexTelemetryUpstreamStatus(response.Get("status").String())
	}
	if status != "" {
		if actual := codexTelemetryUpstreamStatus(response.Get("status").String()); actual != "" {
			status = actual
		}
		if typ == "error" || typ == "response.failed" || root.Get("error").IsObject() || response.Get("error").IsObject() {
			status = "failed"
		} else if typ == "response.incomplete" {
			status = "incomplete"
		} else if typ == "response.cancelled" || typ == "response.canceled" {
			status = "cancelled"
		}
		if status != "completed" {
			s.result.FailedEventCount++
			if waitIndex >= 0 {
				s.result.EventWaitFailed[waitIndex] = true
			}
			s.failed = true
		}
		// A subsequent completion cannot turn this physical attempt's failure
		// into success. A genuine retry owns a new stream observer.
		if !s.failed || s.result.Status == "" || status != "completed" {
			s.result.Status, s.result.FinishedAt = status, at
		}
	}
}

func codexTelemetryUpstreamStatus(status string) string {
	switch status {
	case "completed":
		return "completed"
	case "failed":
		return "failed"
	case "incomplete":
		return "incomplete"
	case "cancelled", "canceled":
		return "cancelled"
	default:
		return ""
	}
}

func (s *codexTelemetryStream) duplicate(root gjson.Result, typ string) bool {
	terminalType := ""
	switch typ {
	case "response.completed", "response.failed", "response.incomplete", "response.done", "response.cancelled", "response.canceled":
		terminalType = typ
	case "":
		terminalType = codexTelemetryUpstreamStatus(root.Get("status").String())
	}
	if terminalType != "" {
		id := firstNonEmpty(root.Get("response.id").String(), root.Get("id").String())
		if len(id) <= 256 {
			key := terminalType + ":" + id
			if _, exists := s.terminalSeen[key]; exists {
				return true
			}
			if s.terminalSeen == nil {
				s.terminalSeen = make(map[string]struct{})
			}
			if len(s.terminalSeen) < 16 {
				s.terminalSeen[key] = struct{}{}
			}
		}
	}
	key := ""
	if seq := root.Get("sequence_number"); seq.Type == gjson.Number {
		key = "sequence:" + strconv.FormatInt(seq.Int(), 10)
	} else if id := root.Get("event_id").String(); id != "" && len(id) <= 256 {
		key = "event:" + id
	} else if typ == "response.output_item.added" || typ == "response.output_item.done" {
		if id := root.Get("item.id").String(); id != "" && len(id) <= 256 {
			key = typ + ":" + id
		}
	}
	if key == "" || len(key) > 512 {
		return false
	}
	if _, exists := s.seen[key]; exists {
		return true
	}
	if s.seen == nil {
		s.seen = make(map[string]struct{})
	}
	// Identity-only dedupe is bounded. Anonymous deltas cannot safely be
	// deduplicated: identical text can be two distinct protocol events.
	if len(s.seen) < 512 {
		s.seen[key] = struct{}{}
	}
	return false
}

func codexTelemetryOutputClass(root gjson.Result, typ string) (output, agentMessage bool) {
	switch typ {
	case "response.output_text.delta", "response.output_text.done":
		return true, true
	case "response.reasoning_summary_text.delta", "response.reasoning_summary_text.done", "response.reasoning_text.delta", "response.reasoning.delta":
		return true, false
	case "response.output_item.added", "response.output_item.done":
		item := root.Get("item")
		switch item.Get("type").String() {
		case "message":
			if item.Get("role").String() != "assistant" {
				return false, false
			}
			for _, content := range item.Get("content").Array() {
				if (content.Get("type").String() == "output_text" || content.Get("type").String() == "text") && content.Get("text").String() != "" {
					return true, true
				}
			}
			return false, true
		case "reasoning":
			for _, field := range []string{"summary", "content"} {
				for _, content := range item.Get(field).Array() {
					if content.Get("text").String() != "" {
						return true, false
					}
				}
			}
		case "local_shell_call", "function_call", "custom_tool_call", "tool_search_call", "web_search_call", "image_generation_call", "compaction", "context_compaction":
			return true, false
		}
	}
	return false, false
}

func (s *codexTelemetryStream) observeEndTurn(value gjson.Result) {
	if value.Type != gjson.True && value.Type != gjson.False {
		return
	}
	if s.result.EndTurn != nil && !*s.result.EndTurn {
		return
	}
	endTurn := value.Bool()
	s.result.EndTurn = &endTurn
}

func (s *codexTelemetryStream) observeItem(item gjson.Result) {
	s.observeEndTurn(item.Get("end_turn"))
	switch item.Get("type").String() {
	case "function_call", "custom_tool_call", "local_shell_call", "tool_search_call":
		id := firstNonEmpty(item.Get("call_id").String(), item.Get("id").String())
		if id == "" || len(id) > 256 || len(s.result.PendingToolCallIDs) >= 64 {
			return
		}
		for _, existing := range s.result.PendingToolCallIDs {
			if existing == id {
				return
			}
		}
		s.result.PendingToolCallIDs = append(s.result.PendingToolCallIDs, strings.Clone(id))
	}
}

func (s *codexTelemetryStream) observeServerTiming(timing gjson.Result) {
	for _, key := range []string{
		"responses_duration_excl_engine_and_client_tool_time_ms", "engine_service_total_ms",
		"engine_iapi_ttft_total_ms", "engine_service_ttft_total_ms",
		"engine_iapi_tbt_across_engine_calls_ms", "engine_service_tbt_across_engine_calls_ms",
	} {
		value := timing.Get(key)
		if value.Type != gjson.Number {
			continue
		}
		n := value.Float()
		if n < 0 || math.IsNaN(n) || math.IsInf(n, 0) {
			continue
		}
		if s.result.ServerTiming == nil {
			s.result.ServerTiming = make(map[string]float64)
		}
		s.result.ServerTiming[key] = n
	}
}

func (s *codexTelemetryStream) readFailed(at time.Time, wait time.Duration) {
	// A socket's close after a terminal is a connection lifecycle event, not
	// another model event and not an explicit client interruption.
	if s.result.Status != "" {
		return
	}
	s.result.EventCount++
	s.result.FailedEventCount++
	if wait >= 0 && len(s.result.EventWaitDurationsMS) < codexTelemetryMaxEventSamples {
		s.result.EventWaitDurationsMS = append(s.result.EventWaitDurationsMS, float64(wait)/float64(time.Millisecond))
		s.result.EventWaitFailed = append(s.result.EventWaitFailed, true)
	}
	// A cancelled read can be retried with a detached context on the same
	// physical connection. It is a failed receive measurement, not an upstream
	// terminal. The attempt's finish path supplies incomplete if no terminal
	// subsequently arrives.
}
