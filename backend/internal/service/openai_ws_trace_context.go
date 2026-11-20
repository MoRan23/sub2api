package service

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

const (
	openAIWSTraceContextKey            = "openai_ws_trace_context"
	fingerprintTraceparentStatusHeader = "X-Sub2-Observation-Traceparent-Status"
	fingerprintTracestateStatusHeader  = "X-Sub2-Observation-Tracestate-Status"
)

type FingerprintTraceContext struct {
	TraceparentStatus string `json:"traceparent_status"`
	TracestateStatus  string `json:"tracestate_status"`
	Source            string `json:"source"`
}

type openAIWSTraceContext struct {
	parent      string
	state       string
	observation FingerprintTraceContext
}

func captureOpenAIWSTraceContext(c *gin.Context) openAIWSTraceContext {
	if c == nil || c.Request == nil {
		return parseOpenAIWSTraceContext(nil)
	}
	if cached, exists := c.Get(openAIWSTraceContextKey); exists {
		return cached.(openAIWSTraceContext)
	}
	value := parseOpenAIWSTraceContext(c.Request.Header)
	c.Set(openAIWSTraceContextKey, value)
	return value
}

func parseOpenAIWSTraceContext(headers http.Header) openAIWSTraceContext {
	value := openAIWSTraceContext{observation: FingerprintTraceContext{
		TraceparentStatus: fingerprintMetadataMissing, TracestateStatus: fingerprintMetadataMissing,
		Source: FingerprintObservationEventWSHandshake,
	}}
	var parent, state string
	parentCount, stateCount := 0, 0
	for name, entries := range headers {
		if len(entries) == 0 {
			continue
		}
		if strings.EqualFold(name, "traceparent") {
			parentCount += len(entries)
			parent = entries[0]
		}
		if strings.EqualFold(name, "tracestate") {
			stateCount += len(entries)
			state = entries[0]
		}
	}
	if parentCount > 0 {
		value.observation.TraceparentStatus = fingerprintMetadataInvalid
		if parentCount == 1 && validOpenAITraceparent(parent) {
			value.parent = parent
			value.observation.TraceparentStatus = fingerprintMetadataValid
		}
	}
	if stateCount > 0 {
		value.observation.TracestateStatus = fingerprintMetadataInvalid
		if stateCount == 1 && value.parent != "" && validOpenAITracestate(state) {
			value.state = state
			value.observation.TracestateStatus = fingerprintMetadataValid
		}
	}
	return value
}

func copyOpenAIWSTraceContext(headers http.Header, c *gin.Context) {
	// Administrative overrides cannot create tracing context on the user's behalf.
	deleteHeaderAllForms(headers, "traceparent")
	deleteHeaderAllForms(headers, "tracestate")
	deleteHeaderAllForms(headers, strings.ToLower(fingerprintTraceparentStatusHeader))
	deleteHeaderAllForms(headers, strings.ToLower(fingerprintTracestateStatusHeader))
	deleteHeaderAllForms(headers, strings.ToLower(fingerprintMetadataHeaderBytes))
	value := captureOpenAIWSTraceContext(c)
	if value.parent != "" {
		headers.Set("traceparent", value.parent)
	}
	if value.state != "" {
		headers.Set("tracestate", value.state)
	}
}

func validOpenAITraceparent(value string) bool {
	if len(value) < 55 || len(value) > 512 || value[2] != '-' || value[35] != '-' || value[52] != '-' {
		return false
	}
	for i := 0; i < 55; i++ {
		if i == 2 || i == 35 || i == 52 {
			continue
		}
		if !(value[i] >= '0' && value[i] <= '9' || value[i] >= 'a' && value[i] <= 'f') {
			return false
		}
	}
	if value[:2] == "ff" || value[3:35] == "00000000000000000000000000000000" || value[36:52] == "0000000000000000" {
		return false
	}
	if value[:2] == "00" {
		return len(value) == 55
	}
	if len(value) == 55 {
		return true
	}
	if len(value) < 57 || value[55] != '-' {
		return false
	}
	for i := 56; i < len(value); i++ {
		if value[i] < 0x21 || value[i] > 0x7e || value[i] == ',' {
			return false
		}
	}
	return true
}

func validOpenAITracestate(value string) bool {
	if len(value) == 0 || len(value) > 512 {
		return false
	}
	seen := make(map[string]struct{}, 4)
	for member := range strings.SplitSeq(value, ",") {
		member = strings.Trim(member, " \t")
		key, data, ok := strings.Cut(member, "=")
		if !ok || len(seen) == 32 || !validOpenAITracestateKey(key) || len(data) == 0 || len(data) > 256 || data[len(data)-1] == ' ' {
			return false
		}
		if _, duplicate := seen[key]; duplicate {
			return false
		}
		seen[key] = struct{}{}
		for i := range len(data) {
			if data[i] < 0x20 || data[i] > 0x7e || data[i] == '=' || data[i] == ',' {
				return false
			}
		}
	}
	return true
}

func validOpenAITracestateKey(key string) bool {
	if len(key) == 0 || len(key) > 256 {
		return false
	}
	tenant, system, multi := strings.Cut(key, "@")
	if multi && (len(tenant) > 241 || len(system) == 0 || len(system) > 14 || system[0] < 'a' || system[0] > 'z') {
		return false
	}
	if key[0] < 'a' || key[0] > 'z' {
		if !multi || key[0] < '0' || key[0] > '9' {
			return false
		}
	}
	for i := range len(key) {
		ch := key[i]
		if multi && i == len(tenant) {
			continue
		}
		if !(ch >= 'a' && ch <= 'z' || ch >= '0' && ch <= '9' || ch == '_' || ch == '-' || ch == '*' || ch == '/') {
			return false
		}
	}
	return true
}

func fingerprintWSHandshakeTraceContext(headers http.Header) *FingerprintTraceContext {
	result := &FingerprintTraceContext{TraceparentStatus: fingerprintMetadataMissing,
		TracestateStatus: fingerprintMetadataMissing, Source: FingerprintObservationEventWSHandshake}
	if headers.Get("traceparent") != "" {
		result.TraceparentStatus = fingerprintMetadataValid
	}
	if headers.Get("tracestate") != "" {
		result.TracestateStatus = fingerprintMetadataValid
	}
	if status := headers.Get(fingerprintTraceparentStatusHeader); status != "" {
		result.TraceparentStatus = status
	}
	if status := headers.Get(fingerprintTracestateStatusHeader); status != "" {
		result.TracestateStatus = status
	}
	return result
}
