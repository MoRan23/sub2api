package service

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/codexnative"
	"github.com/google/uuid"
	"github.com/tidwall/gjson"
)

// SetCodexTelemetryService is called during dependency construction. Telemetry
// owns no inference identity and remains independent of fingerprint collection.
func (s *OpenAIGatewayService) SetCodexTelemetryService(telemetry *CodexTelemetryService) {
	s.codexTelemetry = telemetry
}

func (s *OpenAIGatewayService) beginCodexTelemetryFromWire(
	ctx context.Context, account *Account, headers http.Header, body []byte, proxyURL string, websocket bool,
) *CodexTelemetryAttempt {
	if s == nil || !s.codexTelemetry.Enabled() || account == nil || !account.IsOpenAIOAuth() || s.isAgentIdentityAccount(ctx, account) {
		return nil
	}
	profile := finalFingerprintCodexWireProfile(headers, body)
	flatKind, _ := ParseCodexWireRequestKind(gjson.GetBytes(body, "request_kind").String())
	generate := gjson.GetBytes(body, "generate")
	if profile.RequestKind.internal() || flatKind.internal() || (generate.Type == gjson.False) || IsExplicitImageGenerationIntent("/responses", "", body) {
		return nil
	}
	input := codexTelemetryInputFromWire(account, headers, body, proxyURL, websocket, profile)
	transportCtx := withOpenAINativeHTTPAccountScope(ctx, account, s.accountRepo, "telemetry")
	input.nativeHTTPScope, _ = codexnative.ScopeFromContext(transportCtx)
	input.nativeHTTPScope.SourceUserAgent = input.UserAgent
	return s.codexTelemetry.Begin(ctx, input)
}

// codexTelemetryInputFromWire copies scalar values from the already-built wire
// request. It never consults an identity plan, client request, or observer cache.
// Per-frame metadata takes precedence on a reused WebSocket connection.
func codexTelemetryInputFromWire(account *Account, headers http.Header, body []byte, proxyURL string, websocket bool, profile CodexWireProfile) CodexTelemetryInput {
	identity := func(value string, names ...string) string {
		if websocket && value != "" {
			return codexTelemetryUUID(value)
		}
		for _, name := range names {
			if values := headerValuesCaseInsensitive(headers, name); len(values) > 0 {
				return codexTelemetryUUID(values[0])
			}
		}
		return codexTelemetryUUID(value)
	}
	userAgent := codexTelemetryHeader(headers, "User-Agent")
	version := codexTelemetryHeader(headers, "version")
	if version == "" {
		version = codexClientVersionFromUA(userAgent)
	}
	auth := strings.TrimSpace(codexTelemetryHeader(headers, "Authorization"))
	token := ""
	if scheme, value, ok := strings.Cut(auth, " "); ok && strings.EqualFold(scheme, "Bearer") {
		token = strings.TrimSpace(value)
	}
	var review *bool
	if profile.AutoReviewEnabled != nil {
		value := *profile.AutoReviewEnabled
		review = &value
	}
	subagent := profile.SubagentKind
	if subagent == "" {
		subagent = profile.SubagentHeader
	}
	if subagent == "" {
		subagent = codexTelemetryHeader(headers, "x-openai-subagent")
	}
	approval := profile.ExtraMetadata["approval_policy"]
	if approval == "" {
		approval = gjson.GetBytes(body, "approval_policy").String()
	}
	reviewer := codexTelemetryReviewField(headers, body, "approvals_reviewer").String()
	if reviewer != "user" && reviewer != "auto_review" {
		reviewer = ""
	}
	// These fields are independent from auto_review_enabled. Codex's latter
	// combines the approval policy and reviewer; it carries no V2 extension state.
	guardianV2 := codexTelemetryOptionalBool(codexTelemetryReviewField(headers, body, "guardian_v2_enabled").String())
	return CodexTelemetryInput{
		AccountID: account.ID, AccountName: account.Name,
		AccessToken: token, ChatGPTAccountID: codexTelemetryHeader(headers, "Chatgpt-Account-Id"), ProxyURL: proxyURL,
		UserAgent: userAgent, Originator: codexTelemetryHeader(headers, "originator"), Version: version,
		SessionID: identity(profile.SessionID, "session-id", "session_id"),
		ThreadID:  identity(profile.ThreadID, "thread-id", "thread_id"), TurnID: profile.TurnID.Value,
		ParentThreadID: identity(profile.TurnLineage.ParentThreadID, "x-codex-parent-thread-id"),
		ParentTurnID:   profile.TurnLineage.ParentTurnID.Value, RootTurnID: profile.TurnLineage.RootTurnID.Value,
		ForkedFromThreadID: codexTelemetryUUID(profile.TurnLineage.ForkedFromThreadID),
		ThreadSource:       profile.ThreadSource, TurnTrigger: profile.TurnTrigger,
		AgentName: profile.AgentName, SubagentKind: subagent, Sandbox: profile.Sandbox, SandboxMode: profile.SandboxMode,
		OpenAISubagent: firstNonEmptyString(codexTelemetryHeader(headers, "x-openai-subagent"), profile.SubagentHeader),
		ApprovalPolicy: approval, ApprovalsReviewer: reviewer, AutoReviewEnabled: review, GuardianV2Enabled: guardianV2,
		Model: gjson.GetBytes(body, "model").String(), Effort: gjson.GetBytes(body, "reasoning.effort").String(),
		ServiceTier: gjson.GetBytes(body, "service_tier").String(), WebSocket: websocket, StartedAt: time.Now(),
	}
}

// Read only these scalar review fields from the same final carriers as the wire
// profile. The generic extra-metadata map accepts strings, whereas a final wire
// boolean is also meaningful here. An explicit malformed value remains unknown.
func codexTelemetryReviewField(headers http.Header, body []byte, key string) gjson.Result {
	root := gjson.ParseBytes(body)
	client := root.Get("client_metadata")
	for _, carrier := range []gjson.Result{client.Get(openAIWSTurnMetadataHeader), root.Get(openAIWSTurnMetadataHeader)} {
		if carrier.Type == gjson.String {
			carrier = gjson.Parse(carrier.String())
		}
		if value := carrier.Get(key); value.Exists() {
			return value
		}
	}
	for _, carrier := range []gjson.Result{client, root} {
		if value := carrier.Get(key); value.Exists() {
			return value
		}
	}
	for _, raw := range headerValuesCaseInsensitive(headers, openAIWSTurnMetadataHeader) {
		carrier := gjson.Parse(raw)
		if carrier.Type == gjson.String {
			carrier = gjson.Parse(carrier.String())
		}
		if value := carrier.Get(key); value.Exists() {
			return value
		}
	}
	return gjson.Result{}
}

func codexTelemetryOptionalBool(value string) *bool {
	switch value {
	case "true":
		return boolPointer(true)
	case "false":
		return boolPointer(false)
	default:
		return nil
	}
}

func codexTelemetryHeader(headers http.Header, name string) string {
	for _, value := range headerValuesCaseInsensitive(headers, name) {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func codexTelemetryUUID(value string) string {
	id, err := uuid.Parse(strings.TrimSpace(value))
	if err != nil || id == uuid.Nil {
		return ""
	}
	return id.String()
}

// codexTelemetryResultFromResponse extracts only response metadata. The raw
// upstream frame is neither retained nor sent to either telemetry endpoint.
func codexTelemetryResultFromResponse(raw []byte, status string, httpStatus int, firstEventAt, firstTokenAt time.Time) CodexTelemetryResult {
	root := gjson.ParseBytes(raw)
	response := root.Get("response")
	if !response.IsObject() {
		response = root
	}
	result := CodexTelemetryResult{
		Status: status, HTTPStatus: httpStatus, ResponseID: response.Get("id").String(),
		ServiceTier: response.Get("service_tier").String(),
		InputTokens: response.Get("usage.input_tokens").Int(), CachedInputTokens: response.Get("usage.input_tokens_details.cached_tokens").Int(),
		OutputTokens: response.Get("usage.output_tokens").Int(), ReasoningOutputTokens: response.Get("usage.output_tokens_details.reasoning_tokens").Int(),
		FirstEventAt: firstEventAt, FirstTokenAt: firstTokenAt, FinishedAt: time.Now(),
	}
	switch response.Get("status").String() {
	case "completed":
		result.Status = "completed"
	case "failed", "incomplete":
		result.Status = "failed"
	case "cancelled", "canceled":
		// A server cancellation is not proof of an explicit client interrupt.
		result.Status = "cancelled"
	case "":
		// Some terminal events omit response.status; keep their event outcome.
	default:
		result.Status = "failed"
	}
	if root.Get("type").String() == "error" || root.Get("error").IsObject() || httpStatus >= 400 {
		result.Status = "failed"
	}
	return result
}
