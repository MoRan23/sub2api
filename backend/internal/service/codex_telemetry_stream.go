package service

import (
	"bytes"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/tidwall/gjson"
)

const codexTelemetryMaxEventKinds = 128

type codexTelemetryEventMetricKey struct {
	kind    string
	success bool
}

// codexTelemetryStream observes only scalar protocol metadata. In particular it
// never retains text, tool arguments, raw frames, or upstream error messages.
// The caller serializes access at the HTTP/WS attempt boundary.
type codexTelemetryStream struct {
	result            CodexTelemetryResult
	usage             OpenAIUsage
	failed            bool
	seen              map[string]struct{}
	terminalSeen      map[string]struct{}
	eventMetrics      map[codexTelemetryEventMetricKey]int
	namedEventMetrics int
}

func (s *codexTelemetryStream) observe(raw []byte, fallback string, at time.Time, wait time.Duration) {
	s.observeEvent(raw, fallback, at, wait, true)
}

func (s *codexTelemetryStream) observeMetadata(raw []byte, at time.Time) {
	s.observeEvent(raw, "", at, -1, false)
}

func (s *codexTelemetryStream) observeEvent(raw []byte, fallback string, at time.Time, wait time.Duration, measured bool) {
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
	success := true
	if !valid {
		typ = "parse_error"
	}
	if measured {
		defer func() { s.recordEventMetric(typ, success, wait) }()
	}
	if s.result.FirstEventAt.IsZero() {
		s.result.FirstEventAt = at
	}
	if !valid {
		success = false
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
			success = false
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
	s.recordEventMetric("unknown", false, wait)
	// A cancelled read can be retried with a detached context on the same
	// physical connection. It is a failed receive measurement, not an upstream
	// terminal. The attempt's finish path supplies incomplete if no terminal
	// subsequently arrives.
}

func (s *codexTelemetryStream) recordEventMetric(kind string, success bool, wait time.Duration) {
	s.recordKnownEventMetric(codexTelemetryEventKind(kind), success, wait)
}

// recordKnownEventMetric accepts a kind already checked against the fixed wire
// vocabulary. Keep aggregation bounded independently of that vocabulary so a
// later protocol update cannot silently remove the memory/cardinality limit.
func (s *codexTelemetryStream) recordKnownEventMetric(kind string, success bool, wait time.Duration) {
	s.result.EventCount++
	if !success {
		s.result.FailedEventCount++
	}
	key := codexTelemetryEventMetricKey{kind: kind, success: success}
	index, exists := s.eventMetrics[key]
	if !exists && kind != "unknown" && s.namedEventMetrics >= codexTelemetryMaxEventKinds-2 {
		key.kind = "unknown"
		index, exists = s.eventMetrics[key]
	}
	if !exists {
		if s.eventMetrics == nil {
			s.eventMetrics = make(map[codexTelemetryEventMetricKey]int)
		}
		index = len(s.result.EventMetrics)
		key.kind = strings.Clone(key.kind)
		s.eventMetrics[key] = index
		if key.kind != "unknown" {
			s.namedEventMetrics++
		}
		s.result.EventMetrics = append(s.result.EventMetrics, CodexTelemetryEventMetric{Kind: key.kind, Success: success})
	}
	metric := &s.result.EventMetrics[index]
	metric.Count++
	if wait < 0 {
		return
	}
	ms := float64(wait) / float64(time.Millisecond)
	if metric.WaitCount == 0 {
		metric.WaitMinMS, metric.WaitMaxMS = ms, ms
		metric.WaitBuckets = make([]uint64, len(codexHistogramBounds)+1)
	} else {
		metric.WaitMinMS = math.Min(metric.WaitMinMS, ms)
		metric.WaitMaxMS = math.Max(metric.WaitMaxMS, ms)
	}
	metric.WaitCount++
	metric.WaitSumMS += ms
	metric.WaitBuckets[sort.SearchFloat64s(codexHistogramBounds, ms)]++
}

func codexTelemetryEventKind(kind string) string {
	// Never use an arbitrary upstream type as a persistent metric attribute.
	// The vocabulary includes Codex's Responses SSE/WS parser, the gateway's
	// compatibility terminals, and the standard typed Responses tool/audio
	// events. New upstream types remain observable as unknown until reviewed.
	switch kind {
	case "unknown", "parse_error", "error",
		"response.created", "response.queued", "response.in_progress",
		"response.completed", "response.failed", "response.incomplete",
		"response.done", "response.cancelled", "response.canceled",
		"response.output_item.added", "response.output_item.done",
		"response.content_part.added", "response.content_part.done",
		"response.output_text.delta", "response.output_text.done", "response.output_text.annotation.added",
		"response.refusal.delta", "response.refusal.done",
		"response.function_call_arguments.delta", "response.function_call_arguments.done",
		"response.custom_tool_call_input.delta", "response.custom_tool_call_input.done",
		"response.reasoning_summary_part.added", "response.reasoning_summary_part.done",
		"response.reasoning_summary_text.delta", "response.reasoning_summary_text.done",
		"response.reasoning_text.delta", "response.reasoning_text.done", "response.reasoning.delta",
		"response.metadata", "codex.response.metadata", "responsesapi.websocket_timing",
		"response.audio.delta", "response.audio.done", "response.audio_transcript.delta", "response.audio_transcript.done",
		"response.output_audio.delta", "response.output_audio.done",
		"response.output_audio_transcript.delta", "response.output_audio_transcript.done",
		"response.file_search_call.in_progress", "response.file_search_call.searching", "response.file_search_call.completed",
		"response.web_search_call.in_progress", "response.web_search_call.searching", "response.web_search_call.completed",
		"response.image_generation_call.in_progress", "response.image_generation_call.generating",
		"response.image_generation_call.partial_image", "response.image_generation_call.completed",
		"response.code_interpreter_call.in_progress", "response.code_interpreter_call.interpreting", "response.code_interpreter_call.completed",
		"response.code_interpreter_call_code.delta", "response.code_interpreter_call_code.done",
		"response.mcp_call_arguments.delta", "response.mcp_call_arguments.done",
		"response.mcp_call.in_progress", "response.mcp_call.completed", "response.mcp_call.failed",
		"response.mcp_list_tools.in_progress", "response.mcp_list_tools.completed", "response.mcp_list_tools.failed":
		return kind
	default:
		return "unknown"
	}
}
