package service

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
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

func recordOpenAIAlphaSearchResponsesTimezoneMapping(c *gin.Context, alphaBody, responsesBody []byte) {
	if !observeOpenAIRequestTimezoneAdapter(c) {
		return
	}
	paths := make(map[string]string)
	for _, item := range ScanOpenAIRequestTimezones(alphaBody).Items {
		// Other structured sources become quoted JSON in the adapter's prompt,
		// rather than independent environment or search-location fields.
		paths[item.Path] = ""
		if item.Path == "settings.user_location.timezone" && gjson.GetBytes(responsesBody, "tools.0.user_location").IsObject() {
			paths[item.Path] = "tools.0.user_location.timezone"
		}
	}
	recordOpenAIRequestTimezoneAdapterMapping(c, paths)
	captureOpenAIRequestTimezoneCheckpoint(c, responsesBody)
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
	if !observeOpenAIRequestTimezoneAdapter(c) && !hasDeferredOpenAIRequestTimezone(c) {
		return apicompat.ChatCompletionsToResponses(req)
	}
	out, paths, err := apicompat.ChatCompletionsToResponsesWithPathMapping(req)
	if err == nil {
		if hasDeferredOpenAIRequestTimezone(c) {
			paths = normalizeDeferredOpenAIChatInput(out, paths)
			freezeDeferredOpenAIRequestTimezoneBaseline(c, out, paths)
		} else {
			recordOpenAIRequestTimezoneAdapterMapping(c, paths)
		}
		captureOpenAIRequestTimezoneObjectCheckpoint(c, out)
	}
	return out, err
}

// The shared Chat converter preserves string content. OAuth eventually requires
// input_text parts, so express that protocol-equivalent shape before freezing
// the neutral baseline instead of discovering new sources after Codex adapts it.
// This helper is intentionally exclusive to the OAuth deferred conversion path.
func normalizeDeferredOpenAIChatInput(out *apicompat.ResponsesRequest, paths map[string]string) map[string]string {
	if out == nil || !gjson.ValidBytes(out.Input) {
		return paths
	}
	input := out.Input
	remapped := make(map[string]string, len(paths))
	for from, to := range paths {
		remapped[from] = to
	}
	valid := true
	gjson.ParseBytes(out.Input).ForEach(func(index, item gjson.Result) bool {
		content := item.Get("content")
		if item.Get("role").String() != "user" || content.Type != gjson.String {
			return true
		}
		path := strconv.FormatInt(index.Int(), 10) + ".content"
		var err error
		input, err = sjson.SetBytes(input, path, []map[string]string{{"type": "input_text", "text": content.String()}})
		if err != nil {
			valid = false
			return false
		}
		for from, to := range remapped {
			if to == "input."+path {
				remapped[from] = to + ".0.text"
			}
		}
		return true
	})
	if !valid {
		return paths
	}
	out.Input = input
	return remapped
}

func anthropicToResponsesWithTimezoneObservation(c *gin.Context, req *apicompat.AnthropicRequest) (*apicompat.ResponsesRequest, error) {
	if !observeOpenAIRequestTimezoneAdapter(c) && !hasDeferredOpenAIRequestTimezone(c) {
		return apicompat.AnthropicToResponses(req)
	}
	out, paths, err := apicompat.AnthropicToResponsesWithPathMapping(req)
	if err == nil {
		if hasDeferredOpenAIRequestTimezone(c) {
			freezeDeferredOpenAIRequestTimezoneBaseline(c, out, paths)
		} else {
			recordOpenAIRequestTimezoneAdapterMapping(c, paths)
		}
		captureOpenAIRequestTimezoneObjectCheckpoint(c, out)
	}
	return out, err
}

func hasDeferredOpenAIRequestTimezone(c *gin.Context) bool {
	if c == nil {
		return false
	}
	value, _ := c.Get(openAIRequestTimezoneDeferredKey)
	return value == true
}

func freezeDeferredOpenAIRequestTimezoneBaseline(c *gin.Context, converted any, paths map[string]string) {
	if !hasDeferredOpenAIRequestTimezone(c) {
		return
	}
	state, ok := RequestTimezoneStateFromContext(c)
	if !ok {
		return
	}
	encoded, err := json.Marshal(converted)
	if err != nil {
		return
	}
	var capture *openAIRequestTimezoneCapture
	if raw, ok := c.Get(openAIRequestTimezoneCaptureKey); ok {
		capture, _ = raw.(*openAIRequestTimezoneCapture)
	}
	var neutral *RequestTimezoneState
	if capture != nil {
		neutral = capture.convertedStates[state.passthrough]
	}
	if neutral == nil {
		_, neutral = prepareOpenAIRequestTimezoneBody(encoded, state.Policy, state.AcceptedAt, state.passthrough, IsFingerprintObservationEnabled(), false)
		if capture != nil {
			constrainConvertedTimezoneSources(neutral, capture.body, paths)
		}
		if prepared, ok := neutral.ApplyToBody(encoded); ok {
			neutral.preparedBody = prepared
		}
		if capture != nil {
			if capture.convertedStates == nil {
				capture.convertedStates = make(map[bool]*RequestTimezoneState)
			}
			capture.convertedStates[state.passthrough] = neutral
		}
	}
	projected := neutral.WithTarget(state.Target)
	projected.EgressLocation = state.EgressLocation
	SetRequestTimezoneState(c, projected)
	// Both integrity and timezone now describe the exact converted Responses
	// baseline. No client protocol path is falsely presented as checked here.
	c.Set(fingerprintObservationTimezonePathMappingContextKey, (map[string]string)(nil))
}

// Conversion can remove internal metadata. Preserve explicit negative source
// declarations through the adapter's structural map; otherwise a wrong tag
// would appear absent and incorrectly qualify for the strict fallback.
func constrainConvertedTimezoneSources(state *RequestTimezoneState, original []byte, paths map[string]string) {
	root := gjson.ParseBytes(original)
	rootMetadataPresent := requestTimezoneEnvironmentMetadataBlocker(root) != ""
	for i := range state.projectionSources {
		source := &state.projectionSources[i]
		if !source.occurrence.environment {
			continue
		}
		origin, matches := "", 0
		for from, to := range paths {
			if to == source.occurrence.item.Path && to != "" {
				origin = from
				matches++
			}
		}
		allowed := matches == 1
		parts := strings.Split(origin, ".")
		if len(parts) < 3 || parts[0] != "messages" {
			allowed = false
		}
		if allowed {
			message := gjson.GetBytes(original, strings.Join(parts[:2], "."))
			part := gjson.Result{}
			if len(parts) >= 5 {
				part = gjson.GetBytes(original, strings.Join(parts[:4], "."))
			}
			if message.Get("role").String() != "user" {
				allowed = false
			}
			if hasRequestTimezoneEnvironmentMetadata(message, part) {
				kinds := message.Get("internal_chat_message_metadata_passthrough.content_item_kinds")
				allowed = allowed && len(parts) >= 5 && part.Get("type").String() == "input_text" && kinds.IsArray() && kinds.Get(parts[3]).String() == "environments.environment_context"
				if allowed {
					source.occurrence.item.EnvironmentSource = TimezoneEnvironmentSourceMetadata
				}
			}
			if rootMetadataPresent && source.occurrence.item.EnvironmentSource != TimezoneEnvironmentSourceMetadata {
				allowed = false
			}
		}
		if !allowed {
			source.occurrence.eligible, source.occurrence.item.Current = false, false
			source.occurrence.item.EnvironmentSource = TimezoneEnvironmentSourceReference
		}
		if state.Inbound != nil {
			for j := range state.Inbound.Items {
				if state.Inbound.Items[j].Path == source.occurrence.item.Path {
					state.Inbound.Items[j] = source.occurrence.item
				}
			}
		}
	}
	state.buildTargetProjection()
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
