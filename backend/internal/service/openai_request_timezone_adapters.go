package service

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/gin-gonic/gin"
)

const openAIRequestTimezoneProvenanceKey = "openai_request_timezone_provenance"

func captureOpenAIRequestTimezoneCheckpoint(c *gin.Context, body []byte) {
	if observeOpenAIRequestTimezoneAdapter(c) {
		c.Set(openAIRequestTimezoneProvenanceKey, CaptureOpenAIRequestTimezoneProvenance(body))
	}
}

func captureOpenAIRequestTimezoneObjectCheckpoint(c *gin.Context, body any) {
	if !observeOpenAIRequestTimezoneAdapter(c) {
		return
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		// A failed provenance snapshot must not leave an older adapter's paths
		// eligible for matching the next serialized request.
		c.Set(openAIRequestTimezoneProvenanceKey, &OpenAIRequestTimezoneProvenance{})
		return
	}
	captureOpenAIRequestTimezoneCheckpoint(c, encoded)
}

func openAIRequestTimezoneFinalObservationPaths(c *gin.Context, state *RequestTimezoneState, body []byte) map[string]string {
	var paths map[string]string
	if c != nil {
		if raw, ok := c.Get(fingerprintObservationTimezonePathMappingContextKey); ok {
			paths, _ = raw.(map[string]string)
		}
	}
	if !IsFingerprintObservationEnabled() || body == nil || state == nil {
		return paths
	}
	var checkpoint *OpenAIRequestTimezoneProvenance
	if c != nil {
		if raw, ok := c.Get(openAIRequestTimezoneProvenanceKey); ok {
			checkpoint, _ = raw.(*OpenAIRequestTimezoneProvenance)
		}
	}
	if checkpoint == nil {
		checkpoint = CaptureOpenAIRequestTimezoneProvenance(state.PreparedBody())
	}
	finalPaths := checkpoint.MapTo(body)
	if paths == nil {
		return finalPaths
	}
	return apicompat.ComposeRequestPathMappings(paths, finalPaths)
}

// Protocol provenance is optional diagnostics. The normal conversion path does
// not build or scan mappings while observation is disabled.
func observeOpenAIRequestTimezoneAdapter(c *gin.Context) bool {
	if !IsFingerprintObservationEnabled() {
		return false
	}
	_, ok := RequestTimezoneStateFromContext(c)
	return ok
}

func recordOpenAIRequestTimezoneAdapterMapping(c *gin.Context, next map[string]string) {
	if !observeOpenAIRequestTimezoneAdapter(c) {
		return
	}
	if value, ok := c.Get(fingerprintObservationTimezonePathMappingContextKey); ok {
		if previous, valid := value.(map[string]string); valid && previous != nil {
			next = apicompat.ComposeRequestPathMappings(previous, next)
		}
	}
	SetFingerprintObservationTimezonePathMapping(c, next)
}

// Prefix trimming is an explicit adapter operation. Keep removed sources in the
// trace so another item shifted into their index cannot masquerade as them.
func recordOpenAIRequestTimezonePrefixTrim(c *gin.Context, array string, removed int) {
	if removed <= 0 || !observeOpenAIRequestTimezoneAdapter(c) {
		return
	}
	paths := make(map[string]string)
	value, _ := c.Get(fingerprintObservationTimezonePathMappingContextKey)
	if previous, ok := value.(map[string]string); ok && previous != nil {
		for source, destination := range previous {
			paths[source] = destination
		}
	} else if state, ok := RequestTimezoneStateFromContext(c); ok {
		for _, report := range state.Conversions {
			paths[report.Path] = report.Path
		}
	}
	for source, destination := range paths {
		parts := strings.SplitN(destination, ".", 3)
		if len(parts) != 3 || parts[0] != array {
			continue
		}
		index, err := strconv.Atoi(parts[1])
		if err != nil {
			continue
		}
		if index < removed {
			paths[source] = ""
		} else {
			paths[source] = array + "." + strconv.Itoa(index-removed) + "." + parts[2]
		}
	}
	SetFingerprintObservationTimezonePathMapping(c, paths)
}

func chatCompletionsToResponsesWithTimezoneObservation(c *gin.Context, req *apicompat.ChatCompletionsRequest) (*apicompat.ResponsesRequest, error) {
	if !observeOpenAIRequestTimezoneAdapter(c) {
		return apicompat.ChatCompletionsToResponses(req)
	}
	out, paths, err := apicompat.ChatCompletionsToResponsesWithPathMapping(req)
	if err == nil {
		recordOpenAIRequestTimezoneAdapterMapping(c, paths)
		captureOpenAIRequestTimezoneObjectCheckpoint(c, out)
	}
	return out, err
}

func anthropicToResponsesWithTimezoneObservation(c *gin.Context, req *apicompat.AnthropicRequest) (*apicompat.ResponsesRequest, error) {
	if !observeOpenAIRequestTimezoneAdapter(c) {
		return apicompat.AnthropicToResponses(req)
	}
	out, paths, err := apicompat.AnthropicToResponsesWithPathMapping(req)
	if err == nil {
		recordOpenAIRequestTimezoneAdapterMapping(c, paths)
		captureOpenAIRequestTimezoneObjectCheckpoint(c, out)
	}
	return out, err
}

func responsesToChatCompletionsWithTimezoneObservation(c *gin.Context, req *apicompat.ResponsesRequest, opts *apicompat.ResponsesToChatOptions) (*apicompat.ChatCompletionsRequest, error) {
	if !observeOpenAIRequestTimezoneAdapter(c) {
		return apicompat.ResponsesToChatCompletionsRequestWithOptions(req, opts)
	}
	out, paths, err := apicompat.ResponsesToChatCompletionsRequestWithPathMapping(req, opts)
	if err == nil {
		recordOpenAIRequestTimezoneAdapterMapping(c, paths)
		captureOpenAIRequestTimezoneObjectCheckpoint(c, out)
	}
	return out, err
}

func responsesToAnthropicWithTimezoneObservation(c *gin.Context, req *apicompat.ResponsesRequest) (*apicompat.AnthropicRequest, error) {
	if !observeOpenAIRequestTimezoneAdapter(c) {
		return apicompat.ResponsesToAnthropicRequest(req)
	}
	out, paths, err := apicompat.ResponsesToAnthropicRequestWithPathMapping(req)
	if err == nil {
		recordOpenAIRequestTimezoneAdapterMapping(c, paths)
		captureOpenAIRequestTimezoneObjectCheckpoint(c, out)
	}
	return out, err
}

func anthropicToChatCompletionsWithTimezoneObservation(c *gin.Context, req *apicompat.AnthropicRequest) (*apicompat.ChatCompletionsRequest, error) {
	if !observeOpenAIRequestTimezoneAdapter(c) {
		return apicompat.AnthropicToChatCompletionsRequest(req)
	}
	out, paths, err := apicompat.AnthropicToChatCompletionsRequestWithPathMapping(req)
	if err == nil {
		recordOpenAIRequestTimezoneAdapterMapping(c, paths)
		captureOpenAIRequestTimezoneObjectCheckpoint(c, out)
	}
	return out, err
}
