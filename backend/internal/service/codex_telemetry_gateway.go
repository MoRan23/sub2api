package service

import (
	"context"
	"encoding/xml"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/codexnative"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/gin-gonic/gin"
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
	if ctx == nil {
		ctx = context.Background()
	}
	if s == nil || !s.codexTelemetry.Enabled() || account == nil || !account.IsOpenAIOAuth() || s.isAgentIdentityAccount(ctx, account) {
		return nil
	}
	profile := finalFingerprintCodexWireProfile(headers, body)
	flatKind, _ := ParseCodexWireRequestKind(gjson.GetBytes(body, "request_kind").String())
	generate := gjson.GetBytes(body, "generate")
	if profile.RequestKind.internal() || flatKind.internal() || (generate.Type == gjson.False) || IsExplicitImageGenerationIntent("/responses", "", body) {
		return nil
	}
	owner, err := resolveOpenAIInstallationIdentityAccount(ctx, s.accountRepo, account)
	if err != nil || owner == nil {
		s.skipCodexTelemetryIdentity(account, headers, "identity_unavailable")
		return nil
	}
	if !IsOpenAIOAuthOSProfileOwner(owner) {
		return nil
	}
	snapshot, frozen := ctx.Value(codexTelemetryGatewayContextKey{}).(codexTelemetryGatewaySnapshot)
	if frozen && websocket {
		// The physical socket uses the frozen route, which can differ from an
		// account object refreshed while that socket was already in use.
		proxyURL = snapshot.route.ProxyURL
	}
	input := codexTelemetryInputFromWire(account, headers, body, proxyURL, websocket, profile)
	input.OwnerAccountID = owner.ID
	input.CredentialOS = account.OpenAIOAuthCredentialOS
	input.AuthorizationGeneration = account.OpenAIOAuthAuthorizationGeneration
	if frozen {
		if snapshot.ownerID != 0 && snapshot.ownerID != owner.ID {
			s.skipCodexTelemetryIdentity(account, headers, "stale_identity")
			return nil
		}
		input.SamplingID = snapshot.samplingID
		if snapshot.credentialOS != "" {
			input.CredentialOS, input.AuthorizationGeneration = snapshot.credentialOS, snapshot.authorizationGeneration
		}
		if snapshot.route.ProxyURL != proxyURL {
			s.skipCodexTelemetryIdentity(account, headers, "proxy_unavailable")
			return nil // An unidentified route must never become a direct send.
		}
		if snapshot.route.ProxyID > 0 {
			id := snapshot.route.ProxyID
			input.ProxyID = &id
		}
		// The final UA determines the observed OS. A disagreement with the
		// frozen selection cannot borrow that selection's installation pool.
		if snapshot.os != "" && snapshot.os != input.OSFamily {
			input.OSFamily = "unknown"
		}
		input.ManagedInstallation = owner.IsOpenAIInstallationPinEnabled() &&
			NormalizeOpenAIOSFamily(input.OSFamily) != "" && input.InstallationID != "" &&
			input.InstallationID == snapshot.installationID
	} else if proxyURL != "" {
		if account.ProxyID == nil || account.Proxy == nil || account.Proxy.URL() != proxyURL {
			s.skipCodexTelemetryIdentity(account, headers, "proxy_unavailable")
			return nil
		}
		id := *account.ProxyID
		input.ProxyID = &id
	}
	if input.SamplingID == "" {
		input.SamplingID = uuid.NewString()
	}
	if _, managed := s.accountRepo.(OpenAIOAuthOSCredentialsReader); managed &&
		(NormalizeOpenAIOSFamily(input.CredentialOS) == "" || input.AuthorizationGeneration == "") {
		// The response cannot establish which authorization sent this request.
		// Never infer it from a newer database read after the physical send.
		s.skipCodexTelemetryIdentity(account, headers, "missing_oauth_authorization_scope")
		return nil
	}
	transportCtx := withOpenAINativeHTTPAccountScope(ctx, account, s.accountRepo, "telemetry")
	input.nativeHTTPScope, _ = codexnative.ScopeFromContext(transportCtx)
	input.nativeHTTPScope.SourceUserAgent = input.UserAgent
	return s.codexTelemetry.Begin(ctx, input)
}

// Identity failures are local diagnostics only: an unreliable owner or route
// must not create a pool or send telemetry under a guessed identity. Keep only
// the original business account and final wire UA, never credentials or a body.
func (s *OpenAIGatewayService) skipCodexTelemetryIdentity(account *Account, headers http.Header, reason string) {
	if s == nil || s.codexTelemetry == nil || account == nil {
		return
	}
	switch reason {
	case "identity_unavailable", "stale_identity", "proxy_unavailable", "missing_oauth_authorization_scope":
	default:
		return
	}
	userAgent := strings.Clone(codexTelemetryHeader(headers, "User-Agent"))
	profile := codexTelemetryProfile{
		client: codexTelemetryClient{localID: account.ID, name: account.Name, userAgent: userAgent},
		input: CodexTelemetryInput{AccountID: account.ID, AccountName: account.Name, UserAgent: userAgent,
			OSFamily: openai.DetectOSFamilyFromUserAgent(userAgent)},
		observationEnabled: true, source: "observed", reasons: []string{reason},
	}
	telemetry := s.codexTelemetry
	telemetry.mu.Lock()
	defer telemetry.mu.Unlock()
	if !telemetry.enabledLocked() {
		return
	}
	telemetry.nextAttempt++
	telemetry.counters.Attempts++
	telemetry.skipLocked(profile, telemetry.nextAttempt, reason)
}

// codexTelemetryInputFromWire copies scalar values from the already-built wire
// request. It never generates an identity or consults an observer cache.
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
		AccountID: account.ID, AccountName: strings.Clone(account.Name),
		OSFamily: openai.DetectOSFamilyFromUserAgent(userAgent), InstallationID: identity(profile.InstallationID, codexInstallationIDKey),
		Shell: codexTelemetryShellFromBody(body), ReturnedToolCallIDs: codexTelemetryReturnedToolCallIDs(body),
		AccessToken: strings.Clone(token), ChatGPTAccountID: strings.Clone(codexTelemetryHeader(headers, "Chatgpt-Account-Id")), ProxyURL: strings.Clone(proxyURL),
		UserAgent: strings.Clone(userAgent), Originator: strings.Clone(codexTelemetryHeader(headers, "originator")), Version: strings.Clone(version),
		SessionID: identity(profile.SessionID, "session-id", "session_id"),
		ThreadID:  identity(profile.ThreadID, "thread-id", "thread_id"), TurnID: strings.Clone(profile.TurnID.Value),
		ParentThreadID: identity(profile.TurnLineage.ParentThreadID, "x-codex-parent-thread-id"),
		ParentTurnID:   strings.Clone(profile.TurnLineage.ParentTurnID.Value), RootTurnID: strings.Clone(profile.TurnLineage.RootTurnID.Value),
		ForkedFromThreadID: codexTelemetryUUID(profile.TurnLineage.ForkedFromThreadID),
		ThreadSource:       strings.Clone(profile.ThreadSource), TurnTrigger: strings.Clone(profile.TurnTrigger),
		AgentName: strings.Clone(profile.AgentName), SubagentKind: strings.Clone(subagent), Sandbox: strings.Clone(profile.Sandbox), SandboxMode: strings.Clone(profile.SandboxMode),
		OpenAISubagent: strings.Clone(firstNonEmptyString(codexTelemetryHeader(headers, "x-openai-subagent"), profile.SubagentHeader)),
		ApprovalPolicy: strings.Clone(approval), ApprovalsReviewer: strings.Clone(reviewer), AutoReviewEnabled: review, GuardianV2Enabled: guardianV2,
		Model: strings.Clone(gjson.GetBytes(body, "model").String()), Effort: strings.Clone(gjson.GetBytes(body, "reasoning.effort").String()),
		ServiceTier: strings.Clone(gjson.GetBytes(body, "service_tier").String()), WebSocket: websocket, StartedAt: time.Now(),
	}
}

type codexTelemetryGatewayContextKey struct{}

type codexTelemetryGatewaySnapshot struct {
	ownerID                 int64
	os                      string
	installationID          string
	samplingID              string
	route                   OpenAIEgressRoute
	credentialOS            string
	authorizationGeneration string
}

type codexTelemetrySamplingCursor struct {
	mu      sync.Mutex
	key, id string
}

// Preserve the request/connection's existing identity and route snapshots. The
// sampling cursor is independent of turn_id: a tool loop can reuse turn_id for
// several Responses calls, while a gateway retry must retain one sampling ID.
func withCodexTelemetryGatewayContext(ctx context.Context, c *gin.Context, account *Account, samplingKey string, plans ...*OpenAIOAuthIdentityPlan) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if c == nil || account == nil {
		return ctx
	}
	const cursorKey = "codex_telemetry_sampling_cursor"
	value, found := c.Get(cursorKey)
	cursor, valid := value.(*codexTelemetrySamplingCursor)
	if !found || !valid {
		cursor = &codexTelemetrySamplingCursor{}
		c.Set(cursorKey, cursor)
	}
	cursor.mu.Lock()
	if cursor.id == "" || cursor.key != samplingKey {
		cursor.key, cursor.id = samplingKey, uuid.NewString()
	}
	samplingID := cursor.id
	cursor.mu.Unlock()
	snapshot := codexTelemetryGatewaySnapshot{samplingID: samplingID, route: OpenAIOutboundRouteForAccount(c, account)}
	snapshot.credentialOS, snapshot.authorizationGeneration = account.OpenAIOAuthCredentialOS, account.OpenAIOAuthAuthorizationGeneration
	plan, hasPlan := OpenAIOAuthIdentityPlanFromContext(c)
	if len(plans) > 0 && plans[0] != nil {
		plan, hasPlan = *plans[0], true
	}
	if hasPlan {
		snapshot.ownerID, snapshot.os, snapshot.installationID = plan.OSOwnerID, plan.OSFamily, plan.OSProfile.InstallationID
		if plan.CredentialOS != "" {
			snapshot.credentialOS, snapshot.authorizationGeneration = plan.CredentialOS, plan.AuthorizationGeneration
		}
	} else if selection, ok := openAIOAuthOSSelectionFromContext(ctx); ok {
		snapshot.ownerID, snapshot.os, snapshot.installationID = selection.OwnerID, selection.Profile.OSFamily, selection.Profile.InstallationID
		snapshot.credentialOS, snapshot.authorizationGeneration = selection.CredentialOS, selection.AuthorizationGeneration
	}
	return context.WithValue(ctx, codexTelemetryGatewayContextKey{}, snapshot)
}

// Only trailing tool outputs belong to the newly submitted sampling. Earlier
// tool results are conversation history and must not clear current pending work.
func codexTelemetryReturnedToolCallIDs(body []byte) []string {
	if len(body) > openAIRequestOSBodyLimit {
		return nil
	}
	input := gjson.GetBytes(body, "input")
	if !input.IsArray() {
		return nil
	}
	var ids []string
	seen := make(map[string]struct{})
	count := 0
	input.ForEach(func(_, item gjson.Result) bool {
		count++
		if count > openAIRequestOSMessageLimit {
			ids = nil
			return false
		}
		switch item.Get("type").String() {
		case "function_call_output", "custom_tool_call_output":
			id := item.Get("call_id").String()
			if id == "" || len(id) > 256 || len(ids) >= 128 {
				return true
			}
			if _, exists := seen[id]; !exists {
				ids = append(ids, strings.Clone(id))
				seen[id] = struct{}{}
			}
		default:
			ids = nil
			clear(seen)
		}
		return true
	})
	return ids
}

// Like OS detection, shell evidence is accepted only from standalone environment
// blocks in the final consecutive user messages, never tools, skills or history.
func codexTelemetryShellFromBody(body []byte) string {
	if len(body) > openAIRequestOSBodyLimit || !gjson.ValidBytes(body) {
		return ""
	}
	root := gjson.ParseBytes(body)
	messages := root.Get("input")
	if root.Get("messages").Exists() {
		if messages.Exists() {
			return ""
		}
		messages = root.Get("messages")
	}
	if !messages.IsArray() {
		return ""
	}
	var current []gjson.Result
	count := 0
	messages.ForEach(func(_, message gjson.Result) bool {
		count++
		if count > openAIRequestOSMessageLimit {
			return false
		}
		kind := message.Get("type").String()
		if message.Get("role").String() != "user" || (kind != "" && kind != "message") {
			current = nil
		} else {
			current = append(current, message)
		}
		return true
	})
	if count > openAIRequestOSMessageLimit {
		return ""
	}
	selected, total, parts, envs := "", 0, 0, 0
	observe := func(text string) bool {
		if len(text) > openAIRequestOSTextLimit || len(text) > openAIRequestOSTotalTextLimit-total {
			return false
		}
		total += len(text)
		text = strings.TrimSpace(text)
		if strings.HasPrefix(text, "<environment_context>") {
			envs++
			if envs > openAIRequestOSEnvironmentLimit {
				return false
			}
			selected = codexTelemetryShellFromEnvironment(text)
		}
		return true
	}
	for _, message := range current {
		content := message.Get("content")
		if content.Type == gjson.String {
			if openAIRequestOSContentAllowed(message, gjson.Result{}, 0) && !observe(content.String()) {
				return ""
			}
		} else if content.IsArray() {
			valid := true
			content.ForEach(func(index, part gjson.Result) bool {
				parts++
				if parts > openAIRequestOSPartLimit {
					valid = false
					return false
				}
				kind := part.Get("type").String()
				if (kind == "text" || kind == "input_text") && openAIRequestOSContentAllowed(message, part, int(index.Int())) && part.Get("text").Type == gjson.String {
					valid = observe(part.Get("text").String())
				}
				return valid
			})
			if !valid {
				return ""
			}
		}
	}
	return selected
}

func codexTelemetryShellFromEnvironment(text string) string {
	if !strings.HasSuffix(text, "</environment_context>") || strings.Contains(text, "```") || strings.Contains(text, "~~~") || strings.Contains(text, "<![CDATA[") {
		return ""
	}
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), ">") {
			return ""
		}
	}
	decoder := xml.NewDecoder(strings.NewReader(text))
	depth, nodes, roots := 0, 0, 0
	shell, field, fields := "", "", 0
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		nodes++
		if err != nil || nodes > openAIRequestOSXMLNodeLimit {
			return ""
		}
		switch token := token.(type) {
		case xml.StartElement:
			depth++
			if depth > openAIRequestOSXMLDepthLimit || token.Name.Space != "" {
				return ""
			}
			if depth == 1 {
				roots++
				if roots != 1 || token.Name.Local != "environment_context" {
					return ""
				}
			}
			if depth == 2 && token.Name.Local == "shell" {
				field = "shell"
				fields++
			} else if depth > 2 && field == "shell" {
				return ""
			}
		case xml.EndElement:
			if depth == 2 {
				field = ""
			}
			depth--
		case xml.CharData:
			if depth == 0 && strings.TrimSpace(string(token)) != "" {
				return ""
			}
			if field == "shell" {
				shell += string(token)
			}
		case xml.Comment, xml.Directive, xml.ProcInst:
			return ""
		}
	}
	if depth != 0 || roots != 1 || fields != 1 {
		return ""
	}
	shell = strings.TrimSpace(strings.ToLower(shell))
	shell = strings.ReplaceAll(shell, "\\", "/")
	if last := strings.LastIndexByte(shell, '/'); last >= 0 {
		shell = shell[last+1:]
	}
	shell = strings.TrimSuffix(shell, ".exe")
	switch shell {
	case "powershell", "pwsh", "cmd", "bash", "zsh", "sh", "fish":
		return strings.Clone(shell)
	default:
		return ""
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
