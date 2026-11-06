package service

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
)

var ErrCodexEngineBillingProtocol = errors.New("invalid Engine billing contract")

const CodexEngineBillingContract = "confirmed-events"

const (
	CodexEngineBillingContractHeader      = "X-Codex-Engine-Billing-Contract"
	CodexEngineBillingInstanceHeader      = "X-Codex-Engine-Instance-Id"
	CodexEngineBillingClientRequestHeader = "X-Codex-Engine-Client-Request-Id"
	codexEngineBillingMaxBody             = 1 << 20
)

// CodexEngineBillingUsageEvent contains consumption facts, never the public
// context-window usage counters. Every event is settled independently; totals
// in a receipt are only integrity checks.
type CodexEngineBillingUsageEvent struct {
	EventID          string
	Sequence         int64
	Kind             string
	Purpose          string
	OccurredAt       time.Time
	Model            string
	ServiceTier      string
	ReasoningEffort  string
	Usage            OpenAIUsage
	ImageCount       int
	ImageSize        string
	SearchCalls      int
	Fingerprint      string
	Raw              json.RawMessage
	UsagePresent     map[string]bool
	ApplicableUsage  map[string]bool
	UsageValues      map[string]int64
	UsageComplete    bool
	SearchOperations int
	ImageActual      *CodexEngineBillingImageMetadata
	ImageRequested   *CodexEngineBillingImageMetadata
}

type CodexEngineBillingImageMetadata struct {
	Count           *int   `json:"count,omitempty"`
	N               *int   `json:"n,omitempty"`
	Size            string `json:"size,omitempty"`
	Quality         string `json:"quality,omitempty"`
	OutputFormat    string `json:"output_format,omitempty"`
	FrameCount      *int   `json:"frame_count,omitempty"`
	FrameDurationMS *int   `json:"frame_duration_ms,omitempty"`
}

type CodexEngineBillingSummaryGroup struct {
	Kind        string           `json:"kind"`
	Purpose     string           `json:"purpose"`
	Model       string           `json:"model"`
	ServiceTier *string          `json:"service_tier"`
	Usage       map[string]int64 `json:"usage"`
}

type CodexEngineBillingPage struct {
	InstanceID          string
	ReceiptID           string
	ClientRequestID     string
	Revision            int64
	ExecutionStatus     string
	Finalized           bool
	UsageComplete       bool
	LastSequence        int64
	PageThroughSequence int64
	Events              []CodexEngineBillingUsageEvent
	SummarySequence     int64
	Summary             []CodexEngineBillingSummaryGroup
	Seal                json.RawMessage
	HasMore             bool
	NextCursor          string
	PollAfterSeconds    int
}

func validCodexEngineInstanceID(id string) bool {
	if len(id) != 36 || !strings.HasPrefix(id, "cei_") {
		return false
	}
	for _, r := range id[4:] {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}

func validCodexEngineOpaqueID(id string, max int) bool {
	if id == "" || len(id) > max {
		return false
	}
	for _, r := range id {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func validCodexEngineClientRequestID(id string) bool {
	if len(id) < 1 || len(id) > 128 {
		return false
	}
	for _, ch := range []byte(id) {
		if ch < 33 || ch > 126 {
			return false
		}
	}
	return true
}

func codexEngineBillingHeader(headers http.Header, name string) string {
	var values []string
	for k, v := range headers {
		if strings.EqualFold(k, name) {
			values = append(values, v...)
		}
	}
	if len(values) != 1 {
		return ""
	}
	return values[0]
}

// ValidateCodexEngineBillingHeaders accepts evidence only from the selected
// physical Engine response; callers must not pass inbound client headers.
func ValidateCodexEngineBillingHeaders(headers http.Header, instanceID string) error {
	if codexEngineBillingHeader(headers, CodexEngineBillingContractHeader) != CodexEngineBillingContract ||
		!validCodexEngineInstanceID(instanceID) || codexEngineBillingHeader(headers, CodexEngineBillingInstanceHeader) != instanceID {
		return ErrCodexEngineBillingProtocol
	}
	return nil
}

// validateCodexEngineBillingJSON rejects ambiguous duplicate keys before normal
// decoding. Meter documents are small and bounded independently of image data.
func validateCodexEngineBillingJSON(data []byte) error {
	if len(data) == 0 || len(data) > codexEngineBillingMaxBody {
		return ErrCodexEngineBillingProtocol
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	nodes := 0
	var read func(int) error
	read = func(depth int) error {
		nodes++
		if depth > 32 || nodes > 30000 {
			return ErrCodexEngineBillingProtocol
		}
		t, err := d.Token()
		if err != nil {
			return err
		}
		if delim, ok := t.(json.Delim); ok {
			switch delim {
			case '{':
				seen := map[string]bool{}
				for d.More() {
					k, err := d.Token()
					if err != nil {
						return err
					}
					key, ok := k.(string)
					if !ok || seen[key] {
						return ErrCodexEngineBillingProtocol
					}
					seen[key] = true
					if err := read(depth + 1); err != nil {
						return err
					}
				}
				close, err := d.Token()
				if err != nil || close != json.Delim('}') {
					return ErrCodexEngineBillingProtocol
				}
			case '[':
				for d.More() {
					if err := read(depth + 1); err != nil {
						return err
					}
				}
				close, err := d.Token()
				if err != nil || close != json.Delim(']') {
					return ErrCodexEngineBillingProtocol
				}
			default:
				return ErrCodexEngineBillingProtocol
			}
		}
		return nil
	}
	if err := read(0); err != nil {
		return ErrCodexEngineBillingProtocol
	}
	if _, err := d.Token(); err != io.EOF {
		return ErrCodexEngineBillingProtocol
	}
	return nil
}

func parseCodexEngineBillingUsage(raw json.RawMessage) (map[string]int64, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return nil, ErrCodexEngineBillingProtocol
	}
	allowed := map[string]bool{"input_tokens": true, "output_tokens": true, "cached_input_tokens": true, "cache_write_input_tokens": true, "image_input_tokens": true, "cached_image_input_tokens": true, "image_output_tokens": true, "search_calls": true, "search_operations": true, "images": true}
	values := make(map[string]int64, len(fields))
	for k, v := range fields {
		if !allowed[k] {
			return nil, ErrCodexEngineBillingProtocol
		}
		n, err := strconv.ParseInt(string(v), 10, 64)
		if err != nil || n < 0 || int64(int(n)) != n {
			return nil, ErrCodexEngineBillingProtocol
		}
		values[k] = n
	}
	for _, pair := range [][2]string{{"cached_input_tokens", "input_tokens"}, {"cache_write_input_tokens", "input_tokens"}, {"image_input_tokens", "input_tokens"}, {"cached_image_input_tokens", "image_input_tokens"}, {"image_output_tokens", "output_tokens"}} {
		sub, subOK := values[pair[0]]
		total, totalOK := values[pair[1]]
		if subOK && totalOK && sub > total {
			return nil, ErrCodexEngineBillingProtocol
		}
	}
	return values, nil
}

func validCodexEngineBillingTuple(kind, purpose, model string) bool {
	if strings.TrimSpace(model) == "" || len(model) > 256 || strings.IndexFunc(model, unicode.IsControl) >= 0 {
		return false
	}
	switch kind {
	case "model", "search", "image":
		switch purpose {
		case "response", "compact", "search", "image":
			return true
		}
	}
	return false
}

func parseCodexEngineBillingEvent(raw json.RawMessage) (CodexEngineBillingUsageEvent, error) {
	var w struct {
		EventID         string          `json:"event_id"`
		Sequence        *int64          `json:"sequence"`
		Kind            string          `json:"kind"`
		Purpose         string          `json:"purpose"`
		Model           string          `json:"model"`
		ServiceTier     *string         `json:"service_tier"`
		OccurredAt      string          `json:"occurred_at"`
		UsageComplete   *bool           `json:"usage_complete"`
		ApplicableUsage []string        `json:"applicable_usage"`
		Usage           json.RawMessage `json:"usage"`
		Image           *struct {
			Actual    *CodexEngineBillingImageMetadata `json:"actual"`
			Requested *CodexEngineBillingImageMetadata `json:"requested"`
		} `json:"image"`
	}
	if err := json.Unmarshal(raw, &w); err != nil || w.Sequence == nil || *w.Sequence < 1 || w.UsageComplete == nil || !validCodexEngineOpaqueID(w.EventID, 256) || !validCodexEngineBillingTuple(w.Kind, w.Purpose, w.Model) {
		return CodexEngineBillingUsageEvent{}, ErrCodexEngineBillingProtocol
	}
	occurredAt, err := time.Parse(time.RFC3339Nano, w.OccurredAt)
	if err != nil {
		return CodexEngineBillingUsageEvent{}, ErrCodexEngineBillingProtocol
	}
	if w.ServiceTier != nil && (!validCodexEngineOpaqueID(*w.ServiceTier, 128)) {
		return CodexEngineBillingUsageEvent{}, ErrCodexEngineBillingProtocol
	}
	values, err := parseCodexEngineBillingUsage(w.Usage)
	if err != nil {
		return CodexEngineBillingUsageEvent{}, ErrCodexEngineBillingProtocol
	}
	if len(w.ApplicableUsage) == 0 || len(w.ApplicableUsage) > 10 {
		return CodexEngineBillingUsageEvent{}, ErrCodexEngineBillingProtocol
	}
	applicable := make(map[string]bool, len(w.ApplicableUsage))
	for _, key := range w.ApplicableUsage {
		if applicable[key] {
			return CodexEngineBillingUsageEvent{}, ErrCodexEngineBillingProtocol
		}
		probe, _ := json.Marshal(map[string]int{key: 0})
		if _, err := parseCodexEngineBillingUsage(probe); err != nil {
			return CodexEngineBillingUsageEvent{}, err
		}
		applicable[key] = true
		if _, ok := values[key]; !ok && *w.UsageComplete {
			return CodexEngineBillingUsageEvent{}, ErrCodexEngineBillingProtocol
		}
	}
	for key := range values {
		if !applicable[key] {
			return CodexEngineBillingUsageEvent{}, ErrCodexEngineBillingProtocol
		}
	}
	e := CodexEngineBillingUsageEvent{EventID: w.EventID, Sequence: *w.Sequence, Kind: w.Kind, Purpose: w.Purpose, Model: w.Model, UsageComplete: *w.UsageComplete, UsageValues: values, UsagePresent: map[string]bool{}, ImageCount: int(values["images"]), SearchCalls: int(values["search_calls"]), SearchOperations: int(values["search_operations"])}
	e.ApplicableUsage = applicable
	e.OccurredAt = occurredAt
	if w.ServiceTier != nil {
		e.ServiceTier = *w.ServiceTier
	}
	for key := range values {
		e.UsagePresent[key] = true
	}
	e.Usage = OpenAIUsage{InputTokens: int(values["input_tokens"]), OutputTokens: int(values["output_tokens"]), CacheReadInputTokens: int(values["cached_input_tokens"]), CacheCreationInputTokens: int(values["cache_write_input_tokens"]), ImageInputTokens: int(values["image_input_tokens"]), ImageCacheReadTokens: int(values["cached_image_input_tokens"]), ImageOutputTokens: int(values["image_output_tokens"])}
	if w.Image != nil {
		if e.Kind != "image" {
			return e, ErrCodexEngineBillingProtocol
		}
		e.ImageActual, e.ImageRequested = w.Image.Actual, w.Image.Requested
		for _, meta := range []*CodexEngineBillingImageMetadata{e.ImageActual, e.ImageRequested} {
			if meta == nil {
				continue
			}
			for _, n := range []*int{meta.Count, meta.N, meta.FrameCount, meta.FrameDurationMS} {
				if n != nil && *n < 0 {
					return e, ErrCodexEngineBillingProtocol
				}
			}
			if len(meta.Size) > 128 || len(meta.Quality) > 128 || len(meta.OutputFormat) > 32 {
				return e, ErrCodexEngineBillingProtocol
			}
		}
		if e.ImageActual != nil {
			e.ImageSize = e.ImageActual.Size
			if e.ImageActual.Count != nil {
				if e.UsagePresent["images"] && e.ImageCount != *e.ImageActual.Count {
					return e, ErrCodexEngineBillingProtocol
				}
				e.ImageCount = *e.ImageActual.Count
			}
		}
	}
	// Only immutable billing fields belong to the fingerprint. A later harmless
	// diagnostic field, object order, or applicability-array order is not a new
	// charge and must not conflict with a previously observed event.
	sort.Strings(w.ApplicableUsage)
	facts := map[string]any{"event_id": w.EventID, "sequence": *w.Sequence, "kind": w.Kind, "purpose": w.Purpose, "model": w.Model, "service_tier": w.ServiceTier, "occurred_at": w.OccurredAt, "usage_complete": *w.UsageComplete, "applicable_usage": w.ApplicableUsage, "usage": values}
	if w.Image != nil {
		facts["image"] = w.Image
	}
	e.Raw, _ = json.Marshal(facts)
	digest := sha256.Sum256(e.Raw)
	e.Fingerprint = hex.EncodeToString(digest[:])
	return e, nil
}

// ParseCodexEngineBillingUsageEvent restores only the canonical, bounded facts
// previously received from the original authenticated Engine.
func ParseCodexEngineBillingUsageEvent(raw []byte) (CodexEngineBillingUsageEvent, error) {
	if err := validateCodexEngineBillingJSON(raw); err != nil {
		return CodexEngineBillingUsageEvent{}, err
	}
	return parseCodexEngineBillingEvent(raw)
}

func ParseCodexEngineBillingPage(data []byte) (*CodexEngineBillingPage, error) {
	if err := validateCodexEngineBillingJSON(data); err != nil {
		return nil, err
	}
	var w struct {
		Object          string            `json:"object"`
		Contract        string            `json:"contract"`
		InstanceID      string            `json:"instance_id"`
		ReceiptID       string            `json:"receipt_id"`
		ClientRequestID string            `json:"client_request_id"`
		Revision        *int64            `json:"revision"`
		ExecutionStatus string            `json:"execution_status"`
		Finalized       *bool             `json:"finalized"`
		UsageComplete   *bool             `json:"usage_complete"`
		LastSequence    *int64            `json:"last_sequence"`
		PageThrough     *int64            `json:"page_through_sequence"`
		Events          []json.RawMessage `json:"events"`
		Summary         *struct {
			Sequence *int64            `json:"as_of_sequence"`
			Groups   []json.RawMessage `json:"groups"`
		} `json:"summary"`
		Pending *struct {
			Open *int64 `json:"open_sources"`
			Gaps *int64 `json:"gaps"`
		} `json:"pending_usage"`
		Seal       json.RawMessage `json:"seal"`
		HasMore    *bool           `json:"has_more"`
		NextCursor string          `json:"next_cursor"`
		PollAfter  *int            `json:"poll_after_seconds"`
	}
	if err := json.Unmarshal(data, &w); err != nil || w.Object != "codex_engine.billing" || w.Contract != CodexEngineBillingContract || !validCodexEngineInstanceID(w.InstanceID) || codexEngineUsageIDFromHeaders(http.Header{codexEngineUsageIDHeader: []string{w.ReceiptID}}) == "" || w.Revision == nil || *w.Revision < 1 || w.Finalized == nil || w.UsageComplete == nil || w.LastSequence == nil || *w.LastSequence < 0 || w.PageThrough == nil || *w.PageThrough < 0 || *w.PageThrough > *w.LastSequence || w.HasMore == nil || w.NextCursor == "" || len(w.NextCursor) > 8192 || w.PollAfter == nil || *w.PollAfter < 0 || w.Events == nil || len(w.Events) > 200 || w.Summary == nil || w.Summary.Sequence == nil || *w.Summary.Sequence < *w.PageThrough || *w.Summary.Sequence > *w.LastSequence || w.Pending == nil || w.Pending.Open == nil || *w.Pending.Open < 0 || w.Pending.Gaps == nil || *w.Pending.Gaps < 0 {
		return nil, ErrCodexEngineBillingProtocol
	}
	switch w.ExecutionStatus {
	case "pending", "running", "succeeded", "failed", "canceled", "unknown":
	default:
		return nil, ErrCodexEngineBillingProtocol
	}
	p := &CodexEngineBillingPage{InstanceID: w.InstanceID, ReceiptID: w.ReceiptID, ClientRequestID: w.ClientRequestID, Revision: *w.Revision, ExecutionStatus: w.ExecutionStatus, Finalized: *w.Finalized, UsageComplete: *w.UsageComplete, LastSequence: *w.LastSequence, PageThroughSequence: *w.PageThrough, SummarySequence: *w.Summary.Sequence, HasMore: *w.HasMore, NextCursor: w.NextCursor, PollAfterSeconds: *w.PollAfter}
	if p.HasMore && len(w.Events) == 0 {
		return nil, ErrCodexEngineBillingProtocol
	}
	seen := map[string]bool{}
	for _, raw := range w.Events {
		e, err := parseCodexEngineBillingEvent(raw)
		if err != nil || seen[e.EventID] || e.Sequence > p.PageThroughSequence || len(p.Events) > 0 && e.Sequence <= p.Events[len(p.Events)-1].Sequence {
			return nil, ErrCodexEngineBillingProtocol
		}
		seen[e.EventID] = true
		p.Events = append(p.Events, e)
	}
	if len(p.Events) > 0 && p.Events[len(p.Events)-1].Sequence != p.PageThroughSequence {
		return nil, ErrCodexEngineBillingProtocol
	}
	summaryKeys := map[string]bool{}
	for _, rawGroup := range w.Summary.Groups {
		var group struct {
			Kind        string          `json:"kind"`
			Purpose     string          `json:"purpose"`
			Model       string          `json:"model"`
			ServiceTier *string         `json:"service_tier"`
			Usage       json.RawMessage `json:"usage"`
		}
		if err := json.Unmarshal(rawGroup, &group); err != nil {
			return nil, ErrCodexEngineBillingProtocol
		}
		values, err := parseCodexEngineBillingUsage(group.Usage)
		if err != nil {
			return nil, err
		}
		g := CodexEngineBillingSummaryGroup{Kind: group.Kind, Purpose: group.Purpose, Model: group.Model, ServiceTier: group.ServiceTier, Usage: values}
		if g.ServiceTier != nil && !validCodexEngineOpaqueID(*g.ServiceTier, 128) {
			return nil, ErrCodexEngineBillingProtocol
		}
		if !validCodexEngineBillingTuple(g.Kind, g.Purpose, g.Model) {
			return nil, ErrCodexEngineBillingProtocol
		}
		key, _ := json.Marshal([]any{g.Kind, g.Purpose, g.Model, g.ServiceTier})
		if summaryKeys[string(key)] {
			return nil, ErrCodexEngineBillingProtocol
		}
		summaryKeys[string(key)] = true
		p.Summary = append(p.Summary, g)
	}
	if p.Finalized {
		var seal struct {
			LastSequence *int64 `json:"last_sequence"`
			SealedAt     string `json:"sealed_at"`
			Reason       string `json:"reason"`
		}
		if err := json.Unmarshal(w.Seal, &seal); err != nil || seal.LastSequence == nil || *seal.LastSequence != p.LastSequence || *w.Pending.Open != 0 {
			return nil, ErrCodexEngineBillingProtocol
		}
		if _, err := time.Parse(time.RFC3339Nano, seal.SealedAt); err != nil {
			return nil, ErrCodexEngineBillingProtocol
		}
		if p.UsageComplete {
			if seal.Reason != "complete" || *w.Pending.Gaps != 0 {
				return nil, ErrCodexEngineBillingProtocol
			}
		} else if seal.Reason != "source_closed_with_gaps" || *w.Pending.Gaps == 0 {
			return nil, ErrCodexEngineBillingProtocol
		}
		p.Seal = append(json.RawMessage(nil), w.Seal...)
	} else if len(w.Seal) > 0 && !bytes.Equal(bytes.TrimSpace(w.Seal), []byte("null")) {
		return nil, ErrCodexEngineBillingProtocol
	}
	return p, nil
}

// CodexEngineBillingIdentity binds reconciliation to the original Engine
// credential owner. The digest contains no recoverable credential. Operational
// settings (proxy, scheduling, concurrency) do not change billing ownership.
func CodexEngineBillingIdentity(account *Account) string {
	if account == nil || account.ID <= 0 || !account.IsCodexEngine() || strings.TrimSpace(account.GetOpenAIApiKey()) == "" {
		return ""
	}
	endpoint, err := codexEngineURL(account.GetOpenAIBaseURL(), "/v1/usage/receipts")
	if err != nil {
		return ""
	}
	value, _ := json.Marshal([]any{"engine-billing-v1", account.ID, account.Platform, account.Type,
		account.Extra[OpenAIAPIKeyModeExtraKey], endpoint, account.GetOpenAIApiKey()})
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}
