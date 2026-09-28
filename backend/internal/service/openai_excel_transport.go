package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

const openAIExcelResponsesURL = "https://bps.openai.com/basispoints/api/responses"

type openAIExcelGatewayContextKey struct{}

type openAIExcelPreparationError struct{ cause error }

func (e *openAIExcelPreparationError) Error() string { return e.cause.Error() }
func (e *openAIExcelPreparationError) Unwrap() error { return e.cause }

type openAIExcelResponseError struct{ cause error }

func (e *openAIExcelResponseError) Error() string {
	return "Excel upstream response could not be translated safely"
}
func (e *openAIExcelResponseError) Unwrap() error { return e.cause }

// The gateway context is only used synchronously while preparing a physical
// request. Stream readers retain the independent response evidence state.
func withOpenAIExcelGatewayContext(request *http.Request, c *gin.Context) *http.Request {
	if request == nil || c == nil {
		return request
	}
	return request.WithContext(context.WithValue(request.Context(), openAIExcelGatewayContextKey{}, c))
}

func openAIExcelRequestBody(request *http.Request) ([]byte, error) {
	if request == nil || request.GetBody == nil {
		return nil, errors.New("excel upstream requires a replayable request body")
	}
	body, err := requestReplayBytes(request)
	if err != nil {
		return nil, err
	}
	if body == nil {
		return nil, errors.New("excel upstream requires a replayable request body")
	}
	return body, nil
}

// prepareOpenAIExcelUpstream applies the protocol adapter after the ordinary
// account, model, timezone and OS identity plan has been finalized. No account
// setting is rewritten and no network retry is performed here.
func (s *OpenAIGatewayService) prepareOpenAIExcelUpstream(request *http.Request, proxyURL string, account *Account) (*http.Request, *OpenAIExcelWireState, string, error) {
	if request == nil || account == nil {
		return request, nil, "", errors.New("OpenAI upstream request or account is missing")
	}
	body, err := openAIExcelRequestBody(request)
	if err != nil {
		return nil, nil, "", err
	}
	scope := s.openAIExcelHistoryScope(request.Context(), account, request.Header, body)
	if account.IsOpenAIExcelUpstreamEnabled() && strings.TrimSpace(scope) == "" {
		return nil, nil, "", ErrOpenAIExcelStateUnavailable
	}
	if err := s.validateOpenAIBackendRequest(request, account, scope); err != nil {
		return nil, nil, "", err
	}
	if !account.IsOpenAIExcelUpstreamEnabled() {
		if err := s.validateOpenAIBackendIngressSource(request.Context(), account, scope); err != nil {
			return nil, nil, "", err
		}
		return request, nil, scope, nil
	}
	if isOpenAIExcelImageRequest(request.Context()) {
		contentType := request.Header.Get("Content-Type")
		headers, headerErr := PrepareOpenAIExcelHeaders(request.Header)
		if headerErr != nil {
			return nil, nil, "", headerErr
		}
		request = request.Clone(request.Context())
		request.Header = headers
		request.Header.Set("Content-Type", contentType)
		return request, nil, scope, nil
	}
	if err := s.validateOpenAIBackendIngressSource(request.Context(), account, scope); err != nil {
		return nil, nil, "", err
	}
	if request.URL == nil || !strings.HasSuffix(strings.TrimRight(request.URL.Path, "/"), "/responses") {
		return nil, nil, "", errors.New("excel upstream does not support this endpoint")
	}
	if err := ValidateOpenAIExcelRequest(body); err != nil {
		return nil, nil, "", err
	}
	request, err = s.prepareOpenAIExcelAttachments(request, proxyURL, account, scope)
	if err != nil {
		return nil, nil, "", err
	}
	body, err = openAIExcelRequestBody(request)
	if err != nil {
		return nil, nil, "", err
	}
	options := OpenAIExcelWireOptions{HistoryScope: scope}
	if s.excelState != nil {
		options.History = s.excelState
	}
	if evidence, ok := request.Context().Value(openAIResponseEvidenceRequestKey{}).(*openAIResponseEvidenceState); ok {
		options.ObserveModel = func(model string) {
			payload, _ := json.Marshal(map[string]string{"model": model})
			observeOpenAIResponseEvidenceEvent(evidence, payload)
		}
	}
	body, headers, state, err := PrepareOpenAIExcelWire(request.Context(), body, request.Header, options)
	if err != nil {
		return nil, nil, "", err
	}
	request = request.Clone(request.Context())
	request.URL, _ = url.Parse(openAIExcelResponsesURL)
	request.Host = request.URL.Host
	request.Header = headers
	setOpenAIRequestBodySnapshot(request, body)
	if c, ok := request.Context().Value(openAIExcelGatewayContextKey{}).(*gin.Context); ok {
		SetOpsUpstreamModel(c, gjson.GetBytes(body, "model").String())
		// Bypass the pre-adapter observer entry point: this is the actual wire.
		if !isOpenAICandyTestContext(c) && shouldRecordFingerprintObservationRequest(c, account) {
			s.recordFingerprintObservationWithBody(c, account, installationIDResolutionFromContext(c, account), request.Header, body)
		}
	}
	return request, state, scope, nil
}

func rejectOpenAIExcelCompact(c *gin.Context, account *Account) error {
	pathCompact := false
	if c != nil && c.Request != nil && c.Request.URL != nil {
		pathCompact = strings.HasSuffix(strings.TrimRight(c.Request.URL.Path, "/"), "/responses/compact")
	}
	if account == nil || !account.IsOpenAIExcelUpstreamEnabled() || (!isOpenAIResponsesCompactPath(c) && !isOpenAINativeCompactionV2(c) && !pathCompact) {
		return nil
	}
	err := errors.New("excel upstream does not support explicit compact requests")
	if c != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"type": "invalid_request_error", "code": "excel_compact_unsupported", "message": err.Error()}})
	}
	return err
}

func rejectOpenAIExcelContinuation(c *gin.Context, account *Account, body []byte) error {
	if account == nil || !account.IsOpenAIExcelUpstreamEnabled() || strings.TrimSpace(gjson.GetBytes(body, "previous_response_id").String()) == "" {
		return nil
	}
	err := &OpenAIExcelRequestError{Code: "excel_continuation_unsupported", Param: "previous_response_id", Message: "Excel upstream requires full history instead of previous_response_id"}
	if c != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"type": "invalid_request_error", "code": err.Code, "param": err.Param, "message": err.Message}})
	}
	return err
}

// An Excel capability denial must not revoke an otherwise valid shared OAuth
// credential. Only explicit authentication error codes retain that behavior.
func isOpenAIExcelCapabilityForbidden(account *Account, status int, body []byte) bool {
	if account == nil || !account.IsOpenAIExcelUpstreamEnabled() || status != http.StatusForbidden {
		return false
	}
	code := strings.ToLower(strings.TrimSpace(extractUpstreamErrorCode(body)))
	if code == "" {
		code = strings.ToLower(gjson.GetBytes(body, "response.error.code").String())
	}
	switch code {
	case "invalid_token", "invalid_api_key", "token_expired", "expired_token", "invalid_authentication", "authentication_error", "account_deactivated", "account_disabled", "workspace_deactivated", "deactivated_workspace", "token_revoked":
		return false
	default:
		return true
	}
}

func (s *OpenAIGatewayService) excelModelsResponse(account *Account) (*OpenAIModelsResponse, error) {
	models := make([]map[string]any, 0, len(OpenAIExcelSupportedModels()))
	for _, id := range OpenAIExcelSupportedModels() {
		descriptor := newConfiguredCodexModelDescriptor(id)
		medium := "medium"
		descriptor.DefaultReasoningLevel = &medium
		descriptor.MultiAgentReasoningEffort = &medium
		descriptor.SupportedReasoningLevels = nil
		for _, effort := range OpenAIExcelReasoningEfforts() {
			descriptor.SupportedReasoningLevels = append(descriptor.SupportedReasoningLevels, configuredCodexReasoningLevel{Effort: effort, Description: configuredCodexReasoningLevelDescription(effort)})
		}
		descriptor.Description = "Excel upstream built-in compatibility catalog; not live upstream discovery."
		descriptor.UseResponsesLite = false
		descriptor.ContextWindow = 272000
		descriptor.MaxContextWindow = 272000
		descriptor.AutoCompactTokenLimit = 180000
		descriptor.ToolMode = nil
		descriptor.SupportsSearchTool = false
		descriptor.InputModalities = []string{"text", "image"}
		descriptor.Guardian = nil
		descriptor.AutoReviewModelOverride = nil
		descriptor.ServiceTiers = []configuredCodexServiceTier{}
		descriptor.AdditionalSpeedTiers = []string{}
		encoded, err := json.Marshal(descriptor)
		if err != nil {
			return nil, fmt.Errorf("encode Excel model descriptor: %w", err)
		}
		var entry map[string]any
		if err := json.Unmarshal(encoded, &entry); err != nil {
			return nil, fmt.Errorf("decode Excel model descriptor: %w", err)
		}
		entry["id"], entry["object"], entry["created"] = id, "model", 0
		entry["owned_by"], entry["source"] = "openai-excel", "excel_builtin"
		entry["supports_previous_response_id"] = false
		models = append(models, entry)
	}
	body, err := json.Marshal(map[string]any{"object": "list", "data": models, "models": models, "source": "excel_builtin", "upstream": "excel"})
	if err != nil {
		return nil, fmt.Errorf("encode Excel model catalog: %w", err)
	}
	return &OpenAIModelsResponse{Body: body, ETag: codexModelsManifestBodyETag(body)}, nil
}
