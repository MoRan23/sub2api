package service

// Excel wire compatibility is adapted from excel-codex-bridge 9254d3f
// (Unlicense). It translates protocols; it never executes client tools.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/google/uuid"
)

// A compatibility template, not a claim that a locally captured Excel session
// or a particular installed Office version produced this request.
const openAIExcelUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36 Edg/131.0.0.0"

const (
	openAIExcelMaxWireBytes  = 64 << 20
	openAIExcelMaxItemBytes  = 16 << 20
	openAIExcelTransportTool = "run_officejs"
)

// ErrOpenAIExcelHistoryNotFound denotes an expired or absent native call.
var ErrOpenAIExcelHistoryNotFound = errors.New("excel native tool history is unavailable")

var ErrOpenAIExcelHistoryStorageUnavailable = errors.New("excel native tool history storage is unavailable")

// OpenAIExcelRequestError describes an unsupported request, not a transport or
// authorization failure. Callers must not fail over or pause an account for it.
type OpenAIExcelRequestError struct {
	Code    string
	Param   string
	Message string
	cause   error
}

func (e *OpenAIExcelRequestError) Error() string { return e.Message }
func (e *OpenAIExcelRequestError) Unwrap() error { return e.cause }

// OpenAIExcelNativeHistoryStore stores native items in an account, authorization,
// route and tenant/session isolated scope. Implementations must encrypt values.
type OpenAIExcelNativeHistoryStore interface {
	LoadExcelNativeCall(context.Context, string, string) (json.RawMessage, error)
	StoreExcelNativeCall(context.Context, string, string, json.RawMessage) error
}

type OpenAIExcelWireOptions struct {
	HistoryScope string
	History      OpenAIExcelNativeHistoryStore
	// ObserveModel sees the original upstream declaration before translation.
	ObserveModel func(string)
}

type OpenAIExcelWireState struct {
	options  OpenAIExcelWireOptions
	tools    map[string]openAIExcelTool
	parallel bool
	required bool
	model    string
}

func OpenAIExcelSupportedModels() []string {
	return []string{"gpt-5.6-luna", "gpt-5.6-terra", "gpt-5.6-sol", "gpt-6-sol", "gpt-6-luna", "gpt-6-astra"}
}

func OpenAIExcelReasoningEfforts() []string {
	return []string{"low", "medium", "high", "xhigh"}
}

func openAIExcelJSON(raw []byte, target any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(target); err != nil {
		return err
	}
	var tail any
	if err := dec.Decode(&tail); err != io.EOF {
		return errors.New("multiple JSON values")
	}
	return nil
}

func openAIExcelString(value any) string      { s, _ := value.(string); return s }
func openAIExcelMap(value any) map[string]any { m, _ := value.(map[string]any); return m }

type openAIExcelParsedRequest struct {
	source   map[string]any
	model    string
	effort   string
	tools    map[string]openAIExcelTool
	required bool
}

// ValidateOpenAIExcelRequest performs local validation before any attachment
// upload. It never reads native history or performs network requests.
func ValidateOpenAIExcelRequest(body []byte) error {
	_, err := parseOpenAIExcelRequest(body)
	return err
}

func parseOpenAIExcelRequest(body []byte) (parsed *openAIExcelParsedRequest, resultErr error) {
	defer func() {
		var requestError *OpenAIExcelRequestError
		if resultErr != nil && !errors.As(resultErr, &requestError) {
			resultErr = &OpenAIExcelRequestError{Code: "invalid_request_error", Message: resultErr.Error(), cause: resultErr}
		}
	}()
	if len(body) > openAIExcelMaxWireBytes {
		return nil, errors.New("excel request exceeds size limit")
	}
	var source map[string]any
	if err := openAIExcelJSON(body, &source); err != nil || source == nil {
		return nil, errors.New("invalid Excel Responses request")
	}
	model := strings.TrimSpace(openAIExcelString(source["model"]))
	supported := false
	for _, allowed := range OpenAIExcelSupportedModels() {
		if model == allowed {
			supported = true
			break
		}
	}
	if !supported {
		return nil, &OpenAIExcelRequestError{Code: "model_not_found", Param: "model", Message: fmt.Sprintf("model %q is not supported by Excel upstream", model)}
	}
	if previous := openAIExcelString(source["previous_response_id"]); previous != "" {
		return nil, errors.New("excel upstream requires full history instead of previous_response_id")
	}
	if stream, present := source["stream"]; present {
		if _, ok := stream.(bool); !ok {
			return nil, errors.New("excel stream must be boolean")
		}
	}
	effort := openAIExcelString(openAIExcelMap(source["reasoning"])["effort"])
	if effort == "" {
		effort = openAIExcelString(source["reasoning_effort"])
	}
	if effort == "" {
		effort = "medium"
	}
	switch effort {
	case "low", "medium", "high", "xhigh":
	default:
		return nil, &OpenAIExcelRequestError{Code: "invalid_request_error", Param: "reasoning.effort", Message: fmt.Sprintf("reasoning effort %q is not supported by Excel upstream", effort)}
	}
	tools, err := openAIExcelParseTools(source)
	if err != nil {
		return nil, err
	}
	required := source["tool_choice"] == "required" || openAIExcelMap(source["tool_choice"]) != nil
	if required && len(tools) == 0 {
		return nil, errors.New("excel tool_choice requires a declared client tool")
	}
	return &openAIExcelParsedRequest{source: source, model: model, effort: effort, tools: tools, required: required}, nil
}

// PrepareOpenAIExcelWire accepts a canonical Responses body. Attachments must
// already have been prepared by the transport layer. Headers are cloned.
func PrepareOpenAIExcelWire(ctx context.Context, body []byte, headers http.Header, options OpenAIExcelWireOptions) (wireResult []byte, headerResult http.Header, stateResult *OpenAIExcelWireState, resultErr error) {
	defer func() {
		if resultErr == nil || errors.Is(resultErr, context.Canceled) || errors.Is(resultErr, context.DeadlineExceeded) || errors.Is(resultErr, ErrOpenAIExcelHistoryStorageUnavailable) {
			return
		}
		var requestError *OpenAIExcelRequestError
		if !errors.As(resultErr, &requestError) {
			resultErr = &OpenAIExcelRequestError{Code: "invalid_request_error", Message: resultErr.Error(), cause: resultErr}
		}
	}()
	parsed, err := parseOpenAIExcelRequest(body)
	if err != nil {
		return nil, nil, nil, err
	}
	source, model := parsed.source, parsed.model
	state := &OpenAIExcelWireState{options: options, parallel: source["parallel_tool_calls"] != false, model: model, required: parsed.required, tools: parsed.tools}
	items, err := state.translateHistory(ctx, source["input"])
	if err != nil {
		return nil, nil, nil, err
	}
	identityItems := make([]any, len(items))
	copy(identityItems, items)
	prologue := make([]any, 0, 3)
	if instructions := openAIExcelString(source["instructions"]); strings.TrimSpace(instructions) != "" {
		prologue = append(prologue, openAIExcelMessage("developer", instructions))
	}
	protocol, err := state.toolInstructions()
	if err != nil {
		return nil, nil, nil, err
	}
	prologue = append(prologue, openAIExcelMessage("developer", protocol))
	if reminder := state.toolProtocolReminder(); reminder != "" {
		prologue = append(prologue, openAIExcelMessage("developer", reminder))
	}
	items = append(prologue, items...)
	output := map[string]any{"model": model, "model_selection": "explicit", "stream": source["stream"] == true, "store": false, "input": items, "reasoning_effort": parsed.effort}
	if len(parsed.tools) == 0 {
		// The upstream model may know about host-side tools from surrounding
		// prompts. Explicitly disable tool calls so those names cannot become
		// undeclared native calls when the request has no client catalog.
		output["tool_choice"] = "none"
	}
	if management, ok := source["context_management"].([]any); ok {
		output["context_management"] = management
	} else {
		output["context_management"] = []any{map[string]any{"type": "compaction", "compact_threshold": 200000}}
	}
	cacheKey := openAIExcelCacheKey(source)
	if cacheKey != "" {
		output["prompt_cache_key"] = cacheKey
	}
	conversation := cacheKey
	if conversation == "" && len(identityItems) > 0 {
		conversation = openAIExcelDigest(identityItems[0])
	}
	if conversation == "" {
		conversation = openAIExcelDigest(source["input"])
	}
	turn, iteration := openAIExcelTurnIdentity(identityItems)
	// Scope binds these identities to the selected account and upstream route.
	conversation = options.HistoryScope + "/" + conversation
	metadata := make(map[string]any)
	for key, value := range openAIExcelMap(source["metadata"]) {
		if len(key) > 64 {
			continue
		}
		switch value.(type) {
		case string, json.Number, bool:
			text := fmt.Sprint(value)
			if len(text) <= 512 {
				metadata[key] = text
			}
		}
	}
	// Internal identities cannot be overridden by caller metadata.
	metadata["task_id"] = uuid.NewSHA1(uuid.NameSpaceURL, []byte("sub2api/excel/"+conversation)).String()
	metadata["turn_id"] = uuid.NewSHA1(uuid.NameSpaceURL, []byte("sub2api/excel/"+conversation+"/turn/"+turn)).String()
	metadata["agent_iteration"] = strconv.Itoa(iteration)
	output["metadata"] = metadata
	wire, err := json.Marshal(output)
	if err != nil {
		return nil, nil, nil, err
	}
	if len(wire) > openAIExcelMaxWireBytes {
		return nil, nil, nil, errors.New("excel request exceeds size limit")
	}
	wireHeaders, err := openAIExcelHeaders(headers, source["stream"] == true)
	if err != nil {
		return nil, nil, nil, err
	}
	// Canonical HTTP identity projection may have placed the installation ID in
	// client_metadata. Excel does not accept that Codex body container, so carry
	// the already-frozen value on its equivalent header instead of dropping it.
	if installation := strings.TrimSpace(openAIExcelString(openAIExcelMap(source["client_metadata"])["x-codex-installation-id"])); installation != "" {
		if len(installation) > 256 || strings.ContainsAny(installation, "\r\n\x00") {
			return nil, nil, nil, errors.New("invalid Excel installation identity")
		}
		for key := range wireHeaders {
			if strings.EqualFold(key, "x-codex-installation-id") {
				delete(wireHeaders, key)
			}
		}
		wireHeaders.Set("X-Codex-Installation-ID", installation)
	}
	return wire, wireHeaders, state, nil
}

// PrepareOpenAIExcelHeaders also serves the Images and attachment HTTP paths.
// Callers may replace Content-Type with a multipart boundary after preparation.
func PrepareOpenAIExcelHeaders(original http.Header) (http.Header, error) {
	return openAIExcelHeaders(original, false)
}

func openAIExcelHeaders(original http.Header, stream bool) (http.Header, error) {
	headers := original.Clone()
	if headers == nil {
		headers = make(http.Header)
	}
	auth := strings.TrimSpace(headers.Get("Authorization"))
	if len(auth) < 8 || !strings.EqualFold(auth[:7], "Bearer ") || strings.TrimSpace(auth[7:]) == "" {
		return nil, errors.New("excel upstream requires OAuth credentials")
	}
	accountID := strings.TrimSpace(headers.Get("ChatGPT-Account-ID"))
	if accountID == "" {
		return nil, errors.New("excel upstream requires a ChatGPT account ID")
	}
	for _, key := range []string{"OpenAI-Beta", "Originator", "Cookie", "X-Codex-Turn-State", "X-Codex-Lite", "X-Codex-Responses-Lite", responsesLiteHeader, "Host", "Content-Length", "User-Agent"} {
		for actual := range headers {
			if strings.EqualFold(actual, key) {
				delete(headers, actual)
			}
		}
	}
	headers.Set("X-OpenAI-Account-ID", accountID)
	headers.Set("X-Basispoints-Auth-Mode", "chatgpt")
	headers.Del("X-OpenAI-Account-User-ID")
	parts := strings.Split(strings.TrimSpace(auth[7:]), ".")
	if len(parts) == 3 {
		if payload, err := base64.RawURLEncoding.DecodeString(parts[1]); err == nil {
			var claims map[string]any
			if json.Unmarshal(payload, &claims) == nil {
				if userID := openAIExcelString(openAIExcelMap(claims["https://api.openai.com/auth"])["chatgpt_account_user_id"]); userID != "" && !strings.ContainsAny(userID, "\r\n") {
					headers.Set("X-OpenAI-Account-User-ID", userID)
				}
			}
		}
	}
	for key, value := range map[string]string{
		"X-OpenAI-Internal-Basispoints-Client-Agent-Profile": "excel",
		"X-OpenAI-Internal-Basispoints-Client-Editor":        "excel",
		"X-OpenAI-Internal-Basispoints-Client-Host":          "office",
		"X-OpenAI-Internal-Basispoints-Client-Platform":      "excel",
		"X-OpenAI-Internal-Basispoints-Client-Product":       "basispoints-excel-plugin",
		"X-OpenAI-Internal-Basispoints-Client-Runtime":       "desktop",
		"X-OpenAI-Internal-Basispoints-Office-Host":          "Excel",
		"X-Stainless-Lang": "js", "X-Stainless-Package-Version": "6.31.0", "X-Stainless-Retry-Count": "0", "X-Stainless-Runtime": "browser:chrome",
	} {
		headers.Set(key, value)
	}
	// Excel's Basispoints client is a Windows WebView2/Chromium client. Keep
	// its UA independent from the incoming Codex OS profile; the native
	// transport consequently selects the Windows TLS profile as well.
	headers.Set("User-Agent", openAIExcelUserAgent)
	// The Excel add-in is a Windows desktop WebView2 client. Its platform
	// metadata and final UA deliberately do not inherit the caller's Codex OS;
	// this also makes the native transport select the Windows TLS profile.
	platform, osName, arch := "PC", "Windows", "x64"
	headers.Set("X-OpenAI-Internal-Basispoints-Client-Platform-Class", platform)
	headers.Set("X-OpenAI-Internal-Basispoints-Office-Platform", platform)
	headers.Set("X-Stainless-OS", osName)
	headers.Set("X-Stainless-Arch", arch)
	headers.Set("Origin", "https://bps.openai.com")
	headers.Set("Accept-Encoding", "identity")
	headers.Set("Content-Type", "application/json")
	if stream {
		headers.Set("Accept", "text/event-stream")
	} else {
		headers.Set("Accept", "application/json")
	}
	return headers, nil
}

func openAIExcelMessage(role, text string) map[string]any {
	kind := "input_text"
	if role == "assistant" {
		kind = "output_text"
	}
	return map[string]any{"type": "message", "role": role, "content": []any{map[string]any{"type": kind, "text": text}}}
}

func openAIExcelDigest(value any) string {
	raw, _ := json.Marshal(value)
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}

func openAIExcelCacheKey(source map[string]any) string {
	for _, key := range []string{"prompt_cache_key", "promptCacheKey", "session_id", "sessionId"} {
		if s := strings.TrimSpace(openAIExcelString(source[key])); s != "" {
			return s
		}
	}
	metadata := openAIExcelMap(source["client_metadata"])
	for _, key := range []string{"session_id", "sessionId"} {
		if s := strings.TrimSpace(openAIExcelString(metadata[key])); s != "" {
			return s
		}
	}
	return ""
}

func openAIExcelTurnIdentity(items []any) (string, int) {
	last := -1
	for i, item := range items {
		if openAIExcelString(openAIExcelMap(item)["role"]) == "user" {
			last = i
		}
	}
	end := last + 1
	if end == 0 && len(items) > 0 {
		end = 1
	}
	rounds := 1
	inResults := false
	for _, item := range items[last+1:] {
		kind := openAIExcelString(openAIExcelMap(item)["type"])
		result := kind == "function_call_output" || kind == "custom_tool_call_output"
		if result && !inResults {
			rounds++
		}
		inResults = result
	}
	return openAIExcelDigest(items[:end]), rounds
}
