package service

import (
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

const codexContextObservationContextKey = "codex_context_observation"
const codexContextObservationGenerationContextKey = "codex_context_observation_generation"

// CodexContextManagementObservationEntry is a bounded diagnostic record for
// PAT whoami and History/Notes requests. It intentionally excludes request
// bodies, tokens, note contents, and upstream error text.
type CodexContextManagementObservationEntry struct {
	captureGeneration  uint64
	SequenceID         uint64                          `json:"sequence_id"`
	Timestamp          time.Time                       `json:"timestamp"`
	UserID             int64                           `json:"user_id"`
	Username           string                          `json:"username"`
	Email              string                          `json:"email"`
	APIKeyID           int64                           `json:"api_key_id"`
	APIKeyName         string                          `json:"api_key_name"`
	AccountID          int64                           `json:"account_id"`
	AccountName        string                          `json:"account_name"`
	Kind               string                          `json:"kind"`
	Path               string                          `json:"path"`
	Status             string                          `json:"status"`
	HTTPStatus         int                             `json:"http_status"`
	UpstreamHTTPStatus int                             `json:"upstream_http_status"`
	UpstreamSent       bool                            `json:"upstream_sent"`
	StickyHit          bool                            `json:"sticky_hit"`
	StickySource       string                          `json:"sticky_source"`
	Fallback           bool                            `json:"fallback"`
	Attempt            int                             `json:"attempt"`
	DurationMS         int64                           `json:"duration_ms"`
	DeliveredBytes     int64                           `json:"delivered_bytes"`
	RewriteFields      []string                        `json:"rewrite_fields,omitempty"`
	Rewrites           []CodexContextManagementRewrite `json:"rewrites,omitempty"`
	SessionID          string                          `json:"session_id,omitempty"`
	ThreadID           string                          `json:"thread_id,omitempty"`
	WindowID           string                          `json:"window_id,omitempty"`
	ContextWindowID    string                          `json:"context_window_id,omitempty"`
	ErrorKind          string                          `json:"error_kind,omitempty"`
}

type CodexContextManagementRewrite struct {
	Field  string `json:"field"`
	Before string `json:"before,omitempty"`
	After  string `json:"after,omitempty"`
}

type CodexContextManagementObservationSummary struct {
	Total     int `json:"total"`
	Successes int `json:"successes"`
	Failures  int `json:"failures"`
	Fallbacks int `json:"fallbacks"`
	Rewritten int `json:"rewritten"`
}

type CodexContextManagementObservationPage struct {
	Enabled  bool                                     `json:"enabled"`
	Items    []CodexContextManagementObservationEntry `json:"items"`
	Total    int                                      `json:"total"`
	Page     int                                      `json:"page"`
	PageSize int                                      `json:"page_size"`
	Pages    int                                      `json:"pages"`
	Summary  CodexContextManagementObservationSummary `json:"summary"`
}

type codexContextObservationStore struct {
	enabled    atomic.Bool
	generation uint64
	mu         sync.Mutex
	ring       []CodexContextManagementObservationEntry
	head, size int
	seq        uint64
}

var globalCodexContextObservation = &codexContextObservationStore{
	ring: make([]CodexContextManagementObservationEntry, fingerprintObservationCapacity),
}

func setCodexContextObservationEnabled(enabled bool) {
	o := globalCodexContextObservation
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.enabled.Load() != enabled {
		o.generation++
	}
	o.enabled.Store(enabled)
	if !enabled {
		var zero CodexContextManagementObservationEntry
		for i := range o.ring {
			o.ring[i] = zero
		}
		o.head, o.size, o.seq = 0, 0, 0
	}
}

func recordCodexContextManagementObservation(c *gin.Context, entry CodexContextManagementObservationEntry) {
	o := globalCodexContextObservation
	if o == nil || !IsFingerprintObservationEnabled() {
		return
	}
	if entry.Timestamp.IsZero() {
		entry.Timestamp = time.Now()
	}
	if c != nil {
		actor := fingerprintObservationActorFromContext(c)
		if entry.UserID == 0 {
			entry.UserID = actor.UserID
		}
		if entry.Username == "" {
			entry.Username = actor.Username
		}
		if entry.Email == "" {
			entry.Email = actor.Email
		}
		if entry.APIKeyID == 0 {
			entry.APIKeyID = actor.APIKeyID
		}
		if entry.APIKeyName == "" {
			entry.APIKeyName = actor.APIKeyName
		}
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if !o.enabled.Load() || len(o.ring) == 0 || entry.captureGeneration != o.generation {
		return
	}
	o.seq++
	entry.SequenceID = o.seq
	entry.RewriteFields = append([]string(nil), entry.RewriteFields...)
	entry.Rewrites = append([]CodexContextManagementRewrite(nil), entry.Rewrites...)
	o.ring[o.head] = entry
	o.head = (o.head + 1) % len(o.ring)
	if o.size < len(o.ring) {
		o.size++
	}
}

// SetFingerprintObservationEnabled also gates and scrubs PAT context records.
func syncCodexContextObservationEnabled(enabled bool) { setCodexContextObservationEnabled(enabled) }

func PageCodexContextManagementObservations(page, pageSize int) CodexContextManagementObservationPage {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 20
	}
	if pageSize > 100 {
		pageSize = 100
	}
	o := globalCodexContextObservation
	o.mu.Lock()
	defer o.mu.Unlock()
	entries := make([]CodexContextManagementObservationEntry, 0, o.size)
	enabled := o.enabled.Load() && IsFingerprintObservationEnabled()
	if enabled {
		for i := 0; i < o.size; i++ {
			idx := (o.head - 1 - i + len(o.ring)) % len(o.ring)
			entries = append(entries, o.ring[idx])
		}
	}
	summary := CodexContextManagementObservationSummary{}
	for _, e := range entries {
		summary.Total++
		if e.Status == "delivered" {
			summary.Successes++
		} else if e.Status != "" {
			summary.Failures++
		}
		if e.Fallback {
			summary.Fallbacks++
		}
		if len(e.RewriteFields) > 0 {
			summary.Rewritten++
		}
	}
	total := len(entries)
	pages := (total + pageSize - 1) / pageSize
	if pages < 1 {
		pages = 1
	}
	if page > pages {
		page = pages
	}
	start := (page - 1) * pageSize
	if start > total {
		start = total
	}
	end := start + pageSize
	if end > total {
		end = total
	}
	items := append([]CodexContextManagementObservationEntry(nil), entries[start:end]...)
	for i := range items {
		items[i].RewriteFields = append([]string(nil), items[i].RewriteFields...)
		items[i].Rewrites = append([]CodexContextManagementRewrite(nil), items[i].Rewrites...)
	}
	if items == nil {
		items = []CodexContextManagementObservationEntry{}
	}
	return CodexContextManagementObservationPage{Enabled: enabled, Items: items, Total: total, Page: page, PageSize: pageSize, Pages: pages, Summary: summary}
}

func BeginCodexContextManagementObservation(c *gin.Context, kind, path string) {
	if c == nil {
		return
	}
	o := globalCodexContextObservation
	o.mu.Lock()
	defer o.mu.Unlock()
	if captured, exists := c.Get(codexContextObservationGenerationContextKey); exists {
		if generation, ok := captured.(uint64); !ok || generation != o.generation {
			c.Set(codexContextObservationContextKey, nil)
			return
		}
	} else {
		c.Set(codexContextObservationGenerationContextKey, o.generation)
	}
	if !o.enabled.Load() || !IsFingerprintObservationEnabled() {
		c.Set(codexContextObservationContextKey, nil)
		return
	}
	c.Set(codexContextObservationContextKey, &CodexContextManagementObservationEntry{
		captureGeneration: o.generation, Timestamp: time.Now(), Kind: kind, Path: codexContextObservationPath(path), StickySource: "none",
	})
}

func codexContextObservationFromContext(c *gin.Context) *CodexContextManagementObservationEntry {
	if c == nil || !IsFingerprintObservationEnabled() {
		return nil
	}
	raw, ok := c.Get(codexContextObservationContextKey)
	if !ok {
		return nil
	}
	entry, _ := raw.(*CodexContextManagementObservationEntry)
	if entry != nil {
		o := globalCodexContextObservation
		o.mu.Lock()
		current := o.enabled.Load() && entry.captureGeneration == o.generation
		o.mu.Unlock()
		if !current {
			return nil
		}
	}
	return entry
}

// RecordCodexContextManagementResult records only a bounded error class, never
// an upstream error message. Success means the handler finished the body copy.
func RecordCodexContextManagementResult(c *gin.Context, kind, path, status string, httpStatus int, deliveredBytes int64, errorKind string) {
	if !IsFingerprintObservationEnabled() {
		return
	}
	entry := codexContextObservationFromContext(c)
	if entry == nil {
		return
	}
	entry.Status, entry.HTTPStatus = status, httpStatus
	entry.DeliveredBytes = deliveredBytes
	if errorKind != "upstream_error" || entry.ErrorKind == "" {
		entry.ErrorKind = errorKind
	}
	entry.DurationMS = time.Since(entry.Timestamp).Milliseconds()
	recordCodexContextManagementObservation(c, *entry)
	c.Set(codexContextObservationContextKey, nil)
}

func codexContextObservationPath(path string) string {
	if path == "/v1/user-auth-credential/whoami" {
		return path
	}
	for _, prefix := range []string{"/alpha/history/v2/", "/alpha/notes/v2/"} {
		if i := strings.Index(path, prefix); i >= 0 {
			op := strings.TrimPrefix(path[i:], prefix)
			switch op {
			case "list_windows", "list_items", "read_item", "search_contents", "list_files_by_prefix", "read_file", "append_to_file", "write_file":
				return prefix + op
			case "thread_hint":
				if prefix == "/alpha/notes/v2/" {
					return prefix + op
				}
			}
			return prefix + "[operation]"
		}
	}
	return "[unknown]"
}

func observeCodexContextIdentityRewrite(c *gin.Context, beforeBody, afterBody []byte, beforeHeaders, afterHeaders http.Header) {
	entry := codexContextObservationFromContext(c)
	if entry == nil {
		return
	}
	beforeRaw, afterRaw := codexContextIdentityRawFields(beforeBody, beforeHeaders), codexContextIdentityRawFields(afterBody, afterHeaders)
	before, after := safeCodexContextIdentityFields(beforeRaw), safeCodexContextIdentityFields(afterRaw)
	keys := make([]string, 0, len(after))
	for k := range after {
		keys = append(keys, k)
	}
	for k := range before {
		if _, exists := after[k]; !exists {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, key := range keys {
		beforeValue, beforeExists := beforeRaw[key]
		afterValue, afterExists := afterRaw[key]
		if beforeExists == afterExists && string(beforeValue) == string(afterValue) {
			continue
		}
		entry.RewriteFields = append(entry.RewriteFields, key)
		entry.Rewrites = append(entry.Rewrites, CodexContextManagementRewrite{Field: key, Before: before[key], After: after[key]})
	}
	for _, prefix := range []string{"body.", "body.context.", "body.metadata.", "body.client_metadata.x-codex-turn-metadata.", "header.", "header.x-codex-turn-metadata."} {
		if value := after[prefix+"session_id"]; value != "" && value != "[invalid]" {
			entry.SessionID = value
		}
		if value := after[prefix+"thread_id"]; value != "" && value != "[invalid]" {
			entry.ThreadID = value
		}
		if value := after[prefix+"window_id"]; value != "" && value != "[invalid]" {
			entry.WindowID = value
		}
		if value := after[prefix+"context_window_id"]; value != "" && value != "[invalid]" {
			entry.ContextWindowID = value
		}
	}
}

func codexContextIdentityRawFields(body []byte, headers http.Header) map[string]json.RawMessage {
	result := make(map[string]json.RawMessage)
	var read func(string, map[string]json.RawMessage)
	read = func(prefix string, root map[string]json.RawMessage) {
		for _, field := range []string{"session_id", "thread_id", "window_id", "window_number", "context_window_id", "first_window_id", "previous_window_id"} {
			if raw, exists := root[field]; exists {
				result[prefix+field] = raw
			}
		}
	}
	var root map[string]json.RawMessage
	if json.Unmarshal(body, &root) == nil {
		read("body.", root)
		for _, name := range []string{"context", "metadata", "client_metadata"} {
			var child map[string]json.RawMessage
			if json.Unmarshal(root[name], &child) == nil {
				read("body."+name+".", child)
				var encoded string
				var turn map[string]json.RawMessage
				if json.Unmarshal(child["x-codex-turn-metadata"], &encoded) == nil && json.Unmarshal([]byte(encoded), &turn) == nil {
					read("body."+name+".x-codex-turn-metadata.", turn)
				}
			}
		}
	}
	if headers != nil {
		for _, field := range []string{"session_id", "thread_id", "session-id", "thread-id", "conversation_id", "conversation-id", "x-client-request-id", "x-codex-installation-id"} {
			if value := headers.Get(field); value != "" {
				raw, _ := json.Marshal(value)
				result["header."+field] = raw
			}
		}
		var metadata map[string]json.RawMessage
		if json.Unmarshal([]byte(headers.Get("x-codex-turn-metadata")), &metadata) == nil {
			read("header.x-codex-turn-metadata.", metadata)
		}
		if value := headers.Get("x-codex-window-id"); value != "" {
			raw, _ := json.Marshal(value)
			result["header.x-codex-window-id"] = raw
		}
	}
	return result
}

func safeCodexContextIdentityFields(raw map[string]json.RawMessage) map[string]string {
	result := make(map[string]string, len(raw))
	for key, value := range raw {
		field := key[strings.LastIndex(key, ".")+1:]
		if field == "x-codex-window-id" {
			field = "window_id"
		}
		result[key] = safeCodexContextIdentityValue(field, value)
	}
	return result
}

func safeCodexContextIdentityValue(field string, raw json.RawMessage) string {
	if field == "window_number" {
		var number uint64
		if json.Unmarshal(raw, &number) == nil {
			return strconv.FormatUint(number, 10)
		}
		return "[invalid]"
	}
	var value string
	if json.Unmarshal(raw, &value) != nil {
		return "[invalid]"
	}
	if value == "" {
		return ""
	}
	if id, err := uuid.Parse(value); err == nil {
		return id.String()
	}
	if field == "window_id" {
		if i := strings.LastIndexByte(value, ':'); i > 0 {
			id, idErr := uuid.Parse(value[:i])
			number, numErr := strconv.ParseUint(value[i+1:], 10, 64)
			if idErr == nil && numErr == nil {
				return id.String() + ":" + strconv.FormatUint(number, 10)
			}
		}
	}
	return "[invalid]"
}
