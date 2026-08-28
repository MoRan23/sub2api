package service

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// OpenAIOAuthResponsesFinalizeOptions contains values that are only known
// after model mapping and request-policy rewrites have completed. Identity is
// deliberately supplied as a frozen plan so finalization never consults a
// store, account setting, or mutable request body to resolve it again.
type OpenAIOAuthResponsesFinalizeOptions struct {
	Plan             OpenAIOAuthIdentityPlan
	FinalModel       string
	FinalServiceTier string
	// RequestKind is the final wire request kind (normally "turn" or
	// "compaction"). The immutable plan remains authoritative; this explicit
	// value lets the wire-profile layer validate/finalize the copied plan once
	// request-kind support is attached to it.
	RequestKind string
	Transport   string
	// DisableResponsesLite is reserved for Responses protocol adapters whose
	// required top-level tools are not valid in the Lite schema (for example
	// alpha/search's web_search fallback). It overrides both manifest and
	// client marker signals for this physical request.
	DisableResponsesLite bool
}

// FinalizeOpenAIOAuthResponsesRequest is the single final wire projection for
// non-WebSocket ChatGPT Codex Responses requests. Callers must invoke it after
// account header overrides, beta-feature injection, model mapping, service-tier
// policy, and request-kind selection are complete.
//
// API-key accounts are an intentional no-op: their headers and body retain the
// existing byte-for-byte behavior. Native alpha/search does not use this
// finalizer; its Responses fallback does.
func (s *OpenAIGatewayService) FinalizeOpenAIOAuthResponsesRequest(
	c *gin.Context,
	account *Account,
	req *http.Request,
	body []byte,
	options OpenAIOAuthResponsesFinalizeOptions,
) ([]byte, error) {
	if account == nil || !account.UsesOpenAICodexProtocol() {
		return body, nil
	}
	if req == nil {
		return body, errors.New("finalize openai OAuth Responses request: request is nil")
	}
	requestKind := strings.TrimSpace(options.RequestKind)
	if requestKind == "" {
		requestKind = string(CodexWireRequestTurn)
	}
	if isOpenAINativeCompactionV2(c) {
		requestKind = string(CodexWireRequestCompaction)
	}
	compactionMode := CodexCompactionModeNone
	if requestKind == string(CodexWireRequestCompaction) {
		switch {
		case isOpenAINativeCompactionV2(c):
			compactionMode = CodexCompactionModeRemoteV2
		case isOpenAIResponsesCompactPath(c) || options.Plan.ProjectionMode == OpenAIOAuthIdentityProjectionCompact:
			compactionMode = CodexCompactionModeLegacy
		default:
			if isBareOpenAICodexResponsesPath(c) && openAICodexHTTPLocalResponsesShape([][]byte{body}) {
				_, local := options.Plan.WireProfile.localResponsesCompactionCandidate()
				if local {
					compactionMode = CodexCompactionModeLocalResponses
				}
			}
		}
	}
	observedCapabilities := s.openAICodexModelCapabilities(
		options.Plan.CredentialOwnerNamespace,
		strings.TrimSpace(options.FinalModel),
	)
	modelCapabilities := effectiveCodexModelCapabilities(
		observedCapabilities,
		explicitOpenAIResponsesLiteHTTP(c, req.Header),
	)
	if options.DisableResponsesLite {
		modelCapabilities = CodexModelCapabilities{Known: true}
	}
	finalPlan, err := FinalizeOpenAICodexWirePlanWithOptions(options.Plan, FinalizeOpenAICodexWirePlanOptions{
		RequestKind:       requestKind,
		ModelCapabilities: modelCapabilities,
		MetadataProfile:   s.codexMetadataProfileSnapshot(),
		CompactionMode:    compactionMode,
		FinalModel:        strings.TrimSpace(options.FinalModel),
		FinalServiceTier:  strings.TrimSpace(options.FinalServiceTier),
	})
	if err != nil {
		return body, err
	}
	SetOpenAIOAuthIdentityPlan(c, finalPlan)

	if finalPlan.ClientIdentityEnabled {
		// Some compat builders intentionally clear originator while reshaping the
		// request. Seed all three headers from the frozen plan here so the pure
		// projector can apply Normalize or SafePair semantics uniformly. Do not
		// use the legacy compatibility helper here: it also overwrites OpenAI-Beta
		// and would discard independent feature tokens before cleanup.
		ensureOpenAIOAuthResponsesClientIdentity(req.Header, finalPlan.ClientIdentity)
	}
	finalInputBody := body
	if modelCapabilities.UseResponsesLite {
		finalInputBody, _, err = normalizeOpenAIResponsesLiteToolsPayload(body)
		if err != nil {
			writeOpenAIResponsesLiteValidationError(c, err)
			return body, err
		}
	}
	applyOpenAIResponsesLiteHTTPHeader(req.Header, modelCapabilities.UseResponsesLite)
	projectedBody, err := ApplyOpenAIOAuthIdentityPlan(req.Header, finalInputBody, finalPlan)
	if err != nil {
		return body, err
	}
	stripOpenAILegacyResponsesBeta(req.Header)
	applyOpenAICodexRoutingHintFromPlan(req.Header, finalPlan)
	finalBody := s.guardOpenAICodexTurnStateEchoForPlan(c, account, finalPlan, req.Header, projectedBody)
	setOpenAIRequestBodySnapshot(req, finalBody)

	if finalPlan.TurnIdentityEnabled {
		setFingerprintObservationOutboundIdentity(c, finalPlan.TurnIdentity)
	}
	logOpenAIRoutingDiagnostics(
		req.Context(),
		account,
		strings.TrimSpace(options.Transport),
		options.FinalModel,
		options.FinalServiceTier,
		strings.TrimSpace(req.Header.Get(openAICodexRoutingHintHeader)) != "",
		"not_applicable",
	)
	return finalBody, nil
}

func writeOpenAIResponsesLiteValidationError(c *gin.Context, err error) {
	if c == nil || err == nil {
		return
	}
	param := "tools"
	var validationErr *openAIResponsesLiteValidationError
	if errors.As(err, &validationErr) {
		param = validationErr.param
	}
	setOpsUpstreamError(c, http.StatusBadRequest, err.Error(), "")
	c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{
		"type": "invalid_request_error", "message": err.Error(), "param": param,
	}})
}

func explicitOpenAIResponsesLiteHTTP(c *gin.Context, headers http.Header) bool {
	if hasOpenAIResponsesLiteHeader(headers) {
		return true
	}
	return c != nil && c.Request != nil && hasOpenAIResponsesLiteHeader(c.Request.Header)
}

func hasOpenAIResponsesLiteHeader(headers http.Header) bool {
	for _, value := range headerValuesCaseInsensitive(headers, responsesLiteHeader) {
		if isOpenAIResponsesLiteHeader(value) {
			return true
		}
	}
	return false
}

func applyOpenAIResponsesLiteHTTPHeader(headers http.Header, enabled bool) {
	deleteOpenAIHeaderEqualFold(headers, responsesLiteHeader)
	if enabled {
		headers.Set(responsesLiteHeader, "true")
	}
}

func applyOpenAICodexRoutingHintFromPlan(headers http.Header, plan OpenAIOAuthIdentityPlan) {
	if headers == nil {
		return
	}
	deleteOpenAIHeaderEqualFold(headers, openAICodexRoutingHintHeader)
	if hint := strings.TrimSpace(plan.WireProfile.RoutingHint); hint != "" {
		headers.Set(openAICodexRoutingHintHeader, hint)
	}
}

func ensureOpenAIOAuthResponsesClientIdentity(headers http.Header, plan CodexClientIdentityPlan) {
	if headers == nil {
		return
	}
	if strings.TrimSpace(headers.Get("User-Agent")) == "" {
		headers.Set("User-Agent", plan.UserAgent)
	}
	if strings.TrimSpace(headers.Get("Originator")) == "" {
		headers.Set("Originator", plan.Originator)
	}
	if strings.TrimSpace(headers.Get("Version")) == "" {
		headers.Set("Version", plan.Version)
	}
}

func setOpenAIRequestBodySnapshot(req *http.Request, body []byte) {
	if req == nil {
		return
	}
	snapshot := bytes.Clone(body)
	req.Body = io.NopCloser(bytes.NewReader(snapshot))
	req.ContentLength = int64(len(snapshot))
	req.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(snapshot)), nil
	}
}

func stripOpenAILegacyResponsesBeta(headers http.Header) {
	if headers == nil {
		return
	}

	preserved := make([]string, 0)
	for key, values := range headers {
		if !strings.EqualFold(strings.TrimSpace(key), "OpenAI-Beta") {
			continue
		}
		delete(headers, key)
		for _, value := range values {
			parts := strings.Split(value, ",")
			kept := parts[:0]
			for _, part := range parts {
				part = strings.TrimSpace(part)
				if part == "" || strings.EqualFold(part, "responses=experimental") {
					continue
				}
				kept = append(kept, part)
			}
			if len(kept) > 0 {
				preserved = append(preserved, strings.Join(kept, ", "))
			}
		}
	}
	for _, value := range preserved {
		headers.Add("OpenAI-Beta", value)
	}
}
