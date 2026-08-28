package service

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// openAIOAuthWSWireFinalizeOptions contains only values known after the
// response.create payload has passed model mapping and request policy.
type openAIOAuthWSWireFinalizeOptions struct {
	RequestKind      CodexWireRequestKind
	FinalModel       string
	FinalServiceTier string
}

// finalizeOpenAIOAuthWSWirePlan is the single WebSocket counterpart to the
// HTTP Responses finalizer. It is intentionally store-free: callers first
// resolve a request/connection plan, then freeze the final frame shape here.
func (s *OpenAIGatewayService) finalizeOpenAIOAuthWSWirePlan(
	c *gin.Context,
	account *Account,
	plan OpenAIOAuthIdentityPlan,
	payload []byte,
	options openAIOAuthWSWireFinalizeOptions,
) (OpenAIOAuthIdentityPlan, error) {
	if account == nil || !account.UsesOpenAICodexProtocol() {
		return plan, nil
	}
	model := strings.TrimSpace(options.FinalModel)
	serviceTier := strings.TrimSpace(options.FinalServiceTier)
	if len(payload) > 0 && (model == "" || serviceTier == "") {
		fields := gjson.GetManyBytes(payload, "model", "service_tier")
		if model == "" {
			model = strings.TrimSpace(fields[0].String())
		}
		if serviceTier == "" {
			serviceTier = strings.TrimSpace(fields[1].String())
		}
	}

	observedCapabilities := s.openAICodexModelCapabilities(plan.CredentialOwnerNamespace, model)
	modelCapabilities := effectiveCodexModelCapabilities(
		observedCapabilities,
		explicitOpenAIResponsesLiteWS(c, payload),
	)
	if modelCapabilities.UseResponsesLite {
		// Validate before a WS connection is acquired or dialed. The physical
		// frame projector below remains the only place that mutates the payload.
		if _, _, err := normalizeOpenAIResponsesLiteToolsPayload(payload); err != nil {
			return plan, err
		}
	}
	if !plan.TurnIdentityRequested {
		// Responses Lite is a model transport capability, not a fingerprint
		// feature. Preserve it when turn identity is disabled while leaving the
		// caller-owned identity and metadata plan untouched.
		plan.WireProfile.ToolNamespacesAllowed = modelCapabilities.UseResponsesLite
		plan.WireProfile.ToolNamespacesInfoAllowed = false
		SetOpenAIOAuthIdentityPlan(c, plan)
		return plan, nil
	}

	kind, kindErr := openAICodexWSWireRequestKind(payload, plan, options.RequestKind)
	if kindErr != nil {
		return plan, kindErr
	}
	finalizeOptions := FinalizeOpenAICodexWirePlanOptions{
		RequestKind:       string(kind),
		ModelCapabilities: modelCapabilities,
		MetadataProfile:   s.codexMetadataProfileSnapshot(),
		FinalModel:        model,
		FinalServiceTier:  serviceTier,
	}
	if kind == CodexWireRequestCompaction && HasCompactionTriggerInInput(payload) {
		// compaction_trigger is the server-observed remote-v2 shape. Its physical
		// implementation is authoritative, while the four bounded analytics fields
		// remain faithful to a complete client snapshot when one is present.
		compaction := DefaultCodexCompactionTurnMetadata(CodexCompactionImplementationRemoteV2)
		compaction.Trigger = "auto"
		compaction.Reason = "context_limit"
		compaction.Phase = "mid_turn"
		if inbound, valid := parseCodexCompactionMetadata(plan.WireProfile.Compaction); valid {
			compaction = inbound
		}
		compaction.Implementation = CodexCompactionImplementationRemoteV2
		finalizeOptions.Compaction = &compaction
		finalizeOptions.CompactionMode = CodexCompactionModeRemoteV2
	} else if kind == CodexWireRequestCompaction && openAICodexWSLocalResponsesShape(payload) {
		if _, local := plan.WireProfile.localResponsesCompactionCandidate(); local {
			finalizeOptions.CompactionMode = CodexCompactionModeLocalResponses
		}
	}

	finalPlan, err := FinalizeOpenAICodexWirePlanWithOptions(plan, finalizeOptions)
	if err != nil {
		return plan, err
	}
	if kind == CodexWireRequestPrewarm {
		// Prewarm keeps the stable account/session/thread/window tuple but has no
		// dispatched request turn or workspace payload of its own.
		finalPlan.RequestTurn = OpenAICodexRequestTurnSnapshot{}
		finalPlan.WireProfile.Workspaces = nil
	}
	SetOpenAIOAuthIdentityPlan(c, finalPlan)
	return finalPlan, nil
}

func openAIOAuthWSWireFinalizeErrorReason(fallback string, err error) string {
	var validationErr *openAIResponsesLiteValidationError
	if errors.As(err, &validationErr) {
		return validationErr.Error()
	}
	return fallback
}

func openAICodexWSWireRequestKind(payload []byte, plan OpenAIOAuthIdentityPlan, explicit ...CodexWireRequestKind) (CodexWireRequestKind, error) {
	forced := CodexWireRequestKind("")
	if len(explicit) > 0 && explicit[0].valid() {
		forced = explicit[0]
	} else if len(payload) > 0 {
		generate := gjson.GetBytes(payload, "generate")
		if HasCompactionTriggerInInput(payload) {
			forced = CodexWireRequestCompaction
		} else if generate.Exists() && generate.Type == gjson.False {
			forced = CodexWireRequestPrewarm
		}
	}
	captured := CodexWireRequestKind("")
	if plan.WireProfile.RequestKind == CodexWireRequestMemory {
		captured = CodexWireRequestMemory
	} else if plan.TurnIdentityRequested && forced == "" && openAICodexWSLocalResponsesShape(payload) {
		if _, local := plan.WireProfile.localResponsesCompactionCandidate(); local {
			captured = CodexWireRequestCompaction
		}
	}
	return resolveOpenAICodexWireRequestKind(captured, CodexWireRequestTurn, forced)
}

func openAICodexWSLocalResponsesShape(payload []byte) bool {
	if len(payload) == 0 || !gjson.ValidBytes(payload) ||
		strings.TrimSpace(gjson.GetBytes(payload, "type").String()) != "response.create" ||
		HasCompactionTriggerInInput(payload) {
		return false
	}
	generate := gjson.GetBytes(payload, "generate")
	return !generate.Exists() || generate.Type != gjson.False
}

// applyOpenAICodexWSRoutingHint projects only the value frozen by the final
// plan. It is soft affinity and therefore intentionally excluded from the
// pooled-socket digest.
func applyOpenAICodexWSRoutingHint(headers http.Header, account *Account, plan OpenAIOAuthIdentityPlan) {
	if headers == nil {
		return
	}
	deleteOpenAIHeaderEqualFold(headers, openAICodexRoutingHintHeader)
	if account == nil || !account.UsesOpenAICodexProtocol() {
		return
	}
	if hint := strings.TrimSpace(plan.WireProfile.RoutingHint); hint != "" {
		headers.Set(openAICodexRoutingHintHeader, hint)
	}
}

// projectOpenAIOAuthWSFrame applies the already-finalized plan and the common
// turn-state guard. The send timestamp is deliberately not added here; every
// caller stamps immediately before its physical Write.
func (s *OpenAIGatewayService) projectOpenAIOAuthWSFrame(
	c *gin.Context,
	account *Account,
	plan OpenAIOAuthIdentityPlan,
	payload []byte,
) ([]byte, error) {
	if account == nil || !account.UsesOpenAICodexProtocol() {
		return payload, nil
	}
	finalPayload := payload
	var err error
	if plan.WireProfile.ToolNamespacesAllowed {
		finalPayload, _, err = normalizeOpenAIResponsesLiteToolsPayload(finalPayload)
		if err != nil {
			return payload, err
		}
	}
	finalPayload, err = applyOpenAIResponsesLiteWSMarker(finalPayload, plan.WireProfile.ToolNamespacesAllowed)
	if err != nil {
		return payload, err
	}
	projected, err := ApplyOpenAIOAuthIdentityPlan(nil, finalPayload, plan)
	if err != nil {
		return payload, err
	}
	return s.guardOpenAICodexTurnStateEchoForPlan(c, account, plan, nil, projected), nil
}

func explicitOpenAIResponsesLiteWS(c *gin.Context, payload []byte) bool {
	if isOpenAIResponsesLiteWebSocketPayload(payload) {
		return true
	}
	return c != nil && c.Request != nil && hasOpenAIResponsesLiteHeader(c.Request.Header)
}

func applyOpenAIResponsesLiteWSMarker(payload []byte, enabled bool) ([]byte, error) {
	if len(payload) == 0 || !gjson.ValidBytes(payload) || !gjson.ParseBytes(payload).IsObject() {
		return payload, nil
	}
	path := "client_metadata." + responsesLiteWSMetadataKey
	if enabled {
		return sjson.SetBytes(payload, path, "true")
	}
	return sjson.DeleteBytes(payload, path)
}

// stripOpenAICodexPrewarmWorkspaces prevents the finalized projector from
// inheriting workspaces from the ordinary-turn payload used as the prewarm
// template. Other bounded environment metadata remains available.
func stripOpenAICodexPrewarmWorkspaces(payload []byte) ([]byte, error) {
	if len(payload) == 0 {
		return payload, nil
	}
	raw := gjson.GetBytes(payload, "client_metadata.x-codex-turn-metadata")
	if !raw.Exists() || raw.Type != gjson.String {
		return payload, nil
	}
	metadata := strings.TrimSpace(raw.String())
	if !gjson.Valid(metadata) || !gjson.Parse(metadata).IsObject() {
		return payload, nil
	}
	updatedMetadata, err := sjson.DeleteBytes([]byte(metadata), "workspaces")
	if err != nil {
		return payload, err
	}
	updated, err := sjson.SetBytes(payload, "client_metadata.x-codex-turn-metadata", string(updatedMetadata))
	if err != nil {
		return payload, err
	}
	return updated, nil
}

func inheritOpenAICodexWSWireEnvironment(target *CodexWireProfile, source CodexWireProfile) {
	if target == nil {
		return
	}
	for destination, candidate := range map[*string]string{
		&target.AgentName:    source.AgentName,
		&target.ThreadSource: source.ThreadSource,
		&target.Sandbox:      source.Sandbox,
		&target.SandboxMode:  source.SandboxMode,
	} {
		if strings.TrimSpace(*destination) == "" && strings.TrimSpace(candidate) != "" {
			*destination = candidate
		}
	}
	if target.AutoReviewEnabled == nil && source.AutoReviewEnabled != nil {
		target.AutoReviewEnabled = boolPointer(*source.AutoReviewEnabled)
	}
	if target.NodeREPLAutoReviewRequired == nil && source.NodeREPLAutoReviewRequired != nil {
		target.NodeREPLAutoReviewRequired = boolPointer(*source.NodeREPLAutoReviewRequired)
	}
	if target.NodeREPLDisabled == nil && source.NodeREPLDisabled != nil {
		target.NodeREPLDisabled = boolPointer(*source.NodeREPLDisabled)
	}
	if len(target.Workspaces) == 0 && len(source.Workspaces) > 0 {
		target.Workspaces = append(json.RawMessage(nil), source.Workspaces...)
	}
	if len(target.ToolNamespacesInfo) == 0 && len(source.ToolNamespacesInfo) > 0 {
		target.ToolNamespacesInfo = append(json.RawMessage(nil), source.ToolNamespacesInfo...)
	}
	mergeCodexWireExtras(target, source.ExtraMetadata, false)
}

var errOpenAIOAuthWSWirePlanUnavailable = errors.New("OpenAI OAuth websocket wire plan is unavailable")
