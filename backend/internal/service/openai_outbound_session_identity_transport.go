package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	openAIWSOutboundIdentityDigestDomain     = "sub2api/openai-ws-outbound-identity/v2"
	openAIWSOutboundIdentityPlanDigestDomain = "sub2api/openai-ws-outbound-identity-plan/v3"
)

const openAIOutboundSessionIdentityRequestSnapshotKey = "openai_outbound_session_identity_enabled_snapshot"

func (s *OpenAIGatewayService) openAIOutboundSessionIdentityTransportEnabled(ctx context.Context) bool {
	return s.openAICodexFingerprintPolicyForRequest(ctx, nil).TurnIdentityNormalizationEnabled()
}

// openAIOutboundSessionIdentityTransportEnabledForRequest snapshots the
// feature flag so a request cannot mix legacy and V2 projections when an
// administrator toggles the setting between builder phases.
func (s *OpenAIGatewayService) openAIOutboundSessionIdentityTransportEnabledForRequest(ctx context.Context, c *gin.Context) bool {
	if c == nil {
		return s.openAIOutboundSessionIdentityTransportEnabled(ctx)
	}
	if cached, ok := c.Get(openAIOutboundSessionIdentityRequestSnapshotKey); ok {
		if enabled, valid := cached.(bool); valid {
			return enabled
		}
	}
	enabled := s.openAICodexFingerprintPolicyForRequest(ctx, c).TurnIdentityNormalizationEnabled()
	c.Set(openAIOutboundSessionIdentityRequestSnapshotKey, enabled)
	return enabled
}

func (s *OpenAIGatewayService) openAIOutboundSessionIdentityModeEnabledForAccount(ctx context.Context, c *gin.Context, account *Account) bool {
	return account != nil && account.IsOpenAIOAuth() &&
		s.openAIOutboundSessionIdentityTransportEnabledForRequest(ctx, c)
}

// resolveOpenAICodexTurnIdentityForTransport applies the OAuth-only V2 gate,
// resolves the complete logical tuple once, and maps it to the hierarchical
// UUIDv7 identity. API-key transports return before reading the setting or
// touching either identity store.
func (s *OpenAIGatewayService) resolveOpenAICodexTurnIdentityForTransport(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	body []byte,
	callerSeed string,
) (OpenAICodexTurnIdentity, OpenAICodexLogicalTurnIdentity, bool, error) {
	if account == nil || !account.IsOpenAIOAuth() {
		return OpenAICodexTurnIdentity{}, OpenAICodexLogicalTurnIdentity{}, false, nil
	}
	return s.resolveOpenAICodexTurnIdentityForTransportSnapshot(
		ctx,
		c,
		account,
		body,
		callerSeed,
		"",
		s.openAIOutboundSessionIdentityTransportEnabledForRequest(ctx, c),
	)
}

func (s *OpenAIGatewayService) resolveOpenAICodexTurnIdentityForTransportSnapshot(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	body []byte,
	callerSeed string,
	explicitTurnMetadata string,
	enabled bool,
) (OpenAICodexTurnIdentity, OpenAICodexLogicalTurnIdentity, bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if !enabled || account == nil || !account.IsOpenAIOAuth() {
		return OpenAICodexTurnIdentity{}, OpenAICodexLogicalTurnIdentity{}, false, nil
	}
	logical := ResolveOpenAICodexLogicalTurnIdentityWithTurnMetadata(c, body, callerSeed, explicitTurnMetadata)
	identity, ok, err := s.resolveOpenAICodexLogicalIdentityForTransport(ctx, c, account, logical, enabled)
	return identity, logical, ok, err
}

func (s *OpenAIGatewayService) resolveOpenAICodexLogicalIdentityForTransport(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	logical OpenAICodexLogicalTurnIdentity,
	enabled bool,
) (OpenAICodexTurnIdentity, bool, error) {
	if !enabled || account == nil || !account.IsOpenAIOAuth() {
		return OpenAICodexTurnIdentity{}, false, nil
	}
	if strings.TrimSpace(logical.SessionKey) == "" {
		return OpenAICodexTurnIdentity{}, false, nil
	}
	identity, ok, err := s.resolveOpenAICodexTurnIdentity(ctx, c, account, logical)
	if err != nil {
		if errors.Is(err, errOpenAIOutboundSessionIdentityNamespace) {
			return OpenAICodexTurnIdentity{}, false, err
		}
		// UUID generation and shared-store failures are request-path fail-open
		// conditions. The mapper has already recorded bounded metrics/log fields.
		return OpenAICodexTurnIdentity{}, false, nil
	}
	return identity, ok, nil
}

// The compatibility wrapper keeps existing narrow callers compiling while
// transports migrate to the tuple-aware API. forceCallerSeed is intentionally
// ignored in V2: caller seeds are fallbacks and can no longer replace an
// explicit Codex session/thread tuple.
func (s *OpenAIGatewayService) resolveOpenAIOutboundSessionIdentityForTransport(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	body []byte,
	callerSeed string,
	forceCallerSeed bool,
) (OpenAIOutboundSessionIdentity, string, bool, error) {
	_ = forceCallerSeed
	identity, logical, ok, err := s.resolveOpenAICodexTurnIdentityForTransport(ctx, c, account, body, callerSeed)
	return identity, logical.SessionKey, ok, err
}

func (s *OpenAIGatewayService) resolveOpenAIOutboundSessionIdentityForTransportSnapshot(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	body []byte,
	callerSeed string,
	forceCallerSeed bool,
	enabled bool,
) (OpenAIOutboundSessionIdentity, string, bool, error) {
	_ = forceCallerSeed
	identity, logical, ok, err := s.resolveOpenAICodexTurnIdentityForTransportSnapshot(ctx, c, account, body, callerSeed, "", enabled)
	return identity, logical.SessionKey, ok, err
}

func resolveOpenAIWSFrameLogicalIdentity(body []byte) OpenAICodexLogicalTurnIdentity {
	logical := ResolveOpenAICodexLogicalTurnIdentity(nil, body, "")
	if !logical.Explicit {
		return OpenAICodexLogicalTurnIdentity{}
	}
	return logical
}

// Before a socket owns an identity, the first usable turn may establish it
// through the normal prompt_cache_key fallback. Once a pair is pinned, only an
// explicit tuple is allowed to change connection compatibility; later prompt
// cache changes remain cache-policy inputs.
func resolveOpenAIWSFrameLogicalIdentityForPinnedState(body []byte, identityPinned bool) OpenAICodexLogicalTurnIdentity {
	if identityPinned {
		return resolveOpenAIWSFrameLogicalIdentity(body)
	}
	return ResolveOpenAICodexLogicalTurnIdentity(nil, body, "")
}

func openAICodexLogicalTurnIdentityEqual(left, right OpenAICodexLogicalTurnIdentity) bool {
	return left.SessionKey == right.SessionKey &&
		left.ThreadKey == right.ThreadKey &&
		left.ParentThreadKey == right.ParentThreadKey &&
		left.ForkedFromThreadKey == right.ForkedFromThreadKey &&
		left.Relation == right.Relation
}

func openAICodexLogicalTurnIdentityKey(logical OpenAICodexLogicalTurnIdentity) string {
	if strings.TrimSpace(logical.SessionKey) == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(strings.Join([]string{
		"sub2api/openai-codex-logical-turn/v2",
		logical.SessionKey,
		logical.ThreadKey,
		logical.ParentThreadKey,
		logical.ForkedFromThreadKey,
		string(logical.Relation),
	}, "\x00")))
	return hex.EncodeToString(sum[:])
}

// Deprecated compatibility helpers now only report explicit tuple changes.
// prompt_cache_key is intentionally identity-neutral once a WS connection has
// pinned a V2 identity.
func resolveOpenAIWSFrameLogicalKey(body []byte, _ string) string {
	return openAICodexLogicalTurnIdentityKey(resolveOpenAIWSFrameLogicalIdentity(body))
}

func advanceOpenAIWSFrameLogicalKey(frameKey, previousFrameKey, pinnedLogicalKey string) (string, bool) {
	frameKey = strings.TrimSpace(frameKey)
	if frameKey == "" {
		return previousFrameKey, false
	}
	if frameKey == previousFrameKey {
		return previousFrameKey, false
	}
	return frameKey, frameKey != pinnedLogicalKey
}

func openAIWSOutboundIdentityDigest(identity OpenAICodexTurnIdentity) string {
	if strings.TrimSpace(identity.SessionID) == "" || strings.TrimSpace(identity.ThreadID) == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(strings.Join([]string{
		openAIWSOutboundIdentityDigestDomain,
		identity.SessionID,
		identity.ThreadID,
		identity.ParentThreadID,
		identity.ForkedFromThreadID,
		string(identity.Relation),
	}, "\x00")))
	return hex.EncodeToString(sum[:])
}

// openAIWSOutboundIdentityPlanDigest scopes pooled sockets to the identity
// values that will actually be sent in the upstream handshake. In particular,
// SafePair may preserve a valid client User-Agent instead of the plan's
// fallback triplet, so pre-projection plan fields are not a reliable pool key.
func openAIWSOutboundIdentityPlanDigest(headers http.Header, plan OpenAIOAuthIdentityPlan) string {
	parts := []string{
		openAIWSOutboundIdentityPlanDigestDomain,
		string(plan.InstallationPolicy),
		fmt.Sprintf("%t", plan.InstallationEnabled),
		fmt.Sprintf("%t", plan.TurnIdentityRequested),
		fmt.Sprintf("%t", plan.TurnIdentityEnabled),
		fmt.Sprintf("%t", plan.ClientIdentityEnabled),
		normalizeOpenAIWSBetaFeatures(headers),
		strings.TrimSpace(plan.TurnIdentity.SessionID),
		strings.TrimSpace(plan.TurnIdentity.ThreadID),
		strings.TrimSpace(plan.TurnIdentity.ParentThreadID),
		strings.TrimSpace(plan.TurnIdentity.ForkedFromThreadID),
		string(plan.TurnIdentity.Relation),
		strings.TrimSpace(plan.WireProfile.Revision),
		strings.TrimSpace(plan.WireProfile.Commit),
	}
	for _, name := range [...]string{
		"user-agent",
		"originator",
		"version",
		codexInstallationIDKey,
		"session-id",
		"thread-id",
		"x-client-request-id",
		"x-codex-parent-thread-id",
		"parent-thread-id",
		"forked-from-thread-id",
		"session_id",
		"thread_id",
		"conversation_id",
		"conversation-id",
	} {
		parts = append(parts, name)
		values := openAIWSFinalIdentityHeaderValues(headers, name)
		parts = append(parts, strconv.Itoa(len(values)))
		parts = append(parts, values...)
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:])
}

func openAIWSFinalIdentityHeaderValues(headers http.Header, name string) []string {
	if headers == nil {
		return nil
	}
	canonicalName := http.CanonicalHeaderKey(name)
	values := append([]string(nil), headers[canonicalName]...)
	variantNames := make([]string, 0)
	for candidate := range headers {
		if candidate != canonicalName && strings.EqualFold(candidate, name) {
			variantNames = append(variantNames, candidate)
		}
	}
	sort.Strings(variantNames)
	for _, candidate := range variantNames {
		values = append(values, headers[candidate]...)
	}
	return values
}

func openAIWSOutboundIdentityHeaderValueForLog(headers http.Header, key, identityDigest string) string {
	identityDigest = strings.TrimSpace(identityDigest)
	if identityDigest == "" {
		return openAIWSHeaderValueForLog(headers, key)
	}
	if len(identityDigest) > 12 {
		identityDigest = identityDigest[:12]
	}
	return "uuidv7:" + identityDigest
}

func clearOpenAIOutboundSessionIdentityHeaders(headers http.Header) {
	deleteOpenAICodexIdentityHeaders(headers)
}

// ApplyOpenAIOutboundSessionIdentityHeaders is retained as the public name
// used throughout the gateway, but now emits only Codex-native identity
// headers and rewrites any direct turn-metadata compatibility projection.
func ApplyOpenAIOutboundSessionIdentityHeaders(headers http.Header, identity OpenAIOutboundSessionIdentity) {
	applyOpenAICodexTurnIdentityHeaders(headers, identity, false)
}

func applyOpenAIOutboundSessionIdentityCompactHeaders(headers http.Header, identity OpenAIOutboundSessionIdentity) {
	applyOpenAICodexTurnIdentityHeaders(headers, identity, true)
}

func applyOpenAICodexTurnIdentityHeaders(headers http.Header, identity OpenAICodexTurnIdentity, compact bool) {
	if headers == nil {
		return
	}
	deleteOpenAICodexIdentityHeaders(headers)
	if sessionID := strings.TrimSpace(identity.SessionID); sessionID != "" {
		headers.Set("session-id", sessionID)
	}
	if threadID := strings.TrimSpace(identity.ThreadID); threadID != "" {
		headers.Set("thread-id", threadID)
		if !compact {
			headers.Set("x-client-request-id", threadID)
		}
	}
	if parentThreadID := strings.TrimSpace(identity.ParentThreadID); parentThreadID != "" {
		headers.Set("x-codex-parent-thread-id", parentThreadID)
	}
	applyOpenAICodexExistingTurnMetadataHeader(headers, identity)
}

type openAICodexMetadataProjection struct {
	installationID    string
	turnIdentity      OpenAICodexTurnIdentity
	requestTurn       OpenAICodexRequestTurnSnapshot
	wireProfile       CodexWireProfile
	installation      bool
	stableTurn        bool
	requestTurnActive bool
	wireActive        bool
}

func openAICodexMetadataProjectionFromPlan(plan OpenAIOAuthIdentityPlan) openAICodexMetadataProjection {
	profile := cloneCodexWireProfile(plan.WireProfile)
	if profile.Revision == "" {
		profile = newCodexWireProfile()
	}
	installation := plan.InstallationPolicy == OpenAIOAuthInstallationAccountPin &&
		plan.InstallationEnabled && strings.TrimSpace(plan.InstallationID) != ""
	if installation {
		profile.InstallationID = plan.InstallationID
	}
	stableTurn := plan.TurnIdentityEnabled &&
		strings.TrimSpace(plan.TurnIdentity.SessionID) != "" &&
		strings.TrimSpace(plan.TurnIdentity.ThreadID) != "" &&
		(!profile.Finalized || profile.RequestKind != CodexWireRequestMemory)
	if plan.TurnIdentityEnabled {
		profile.SessionID = plan.TurnIdentity.SessionID
		profile.ThreadID = plan.TurnIdentity.ThreadID
		profile.TurnLineage.ParentThreadID = plan.TurnIdentity.ParentThreadID
		profile.TurnLineage.ForkedFromThreadID = plan.TurnIdentity.ForkedFromThreadID
	}
	if ValidateOpenAICodexWindowSnapshot(plan.Window) == nil && plan.Window.ThreadID == plan.TurnIdentity.ThreadID {
		profile.WindowID = plan.Window.WindowID()
	}
	requestTurnActive := plan.TurnIdentityRequested &&
		(!profile.Finalized || profile.RequestKind != CodexWireRequestMemory) &&
		(openAICodexRequestTurnSnapshotValid(plan.RequestTurn) ||
			(profile.RequestKind.valid() && openAICodexRequestTurnSnapshotValidForWire(plan.RequestTurn, profile.RequestKind)))
	if requestTurnActive {
		if turnID, valid := plan.RequestTurn.codexTurnID(profile.RequestKind); valid {
			profile.TurnID = turnID
			profile.turnIDPresent = true
			profile.turnIDCandidates = appendCodexTurnIDCandidate(profile.turnIDCandidates, turnID.Value)
		}
		if !profile.TurnStartedAtSet && plan.RequestTurn.StartedAtUnixMS > 0 {
			profile.TurnStartedAtUnixMS = plan.RequestTurn.StartedAtUnixMS
			profile.TurnStartedAtSet = true
		}
	}
	return openAICodexMetadataProjection{
		installationID:    plan.InstallationID,
		turnIdentity:      plan.TurnIdentity,
		requestTurn:       plan.RequestTurn,
		wireProfile:       profile,
		installation:      installation,
		stableTurn:        stableTurn,
		requestTurnActive: requestTurnActive,
		wireActive:        plan.TurnIdentityRequested && profile.Finalized,
	}
}

func (projection openAICodexMetadataProjection) enabled() bool {
	return projection.installation || projection.stableTurn || projection.requestTurnActive || projection.wireActive
}

func (projection openAICodexMetadataProjection) createsTurnMetadata() bool {
	return projection.stableTurn || projection.requestTurnActive || projection.wireActive
}

func applyOpenAICodexIdentityHeadersForPlan(headers http.Header, plan OpenAIOAuthIdentityPlan, compact bool) {
	if headers == nil {
		return
	}
	projection := openAICodexMetadataProjectionFromPlan(plan)
	if projection.wireActive {
		deleteOpenAICodexIdentityHeaders(headers)
		deleteOpenAIHeaderEqualFold(headers, "x-codex-window-id")
		deleteOpenAIHeaderEqualFold(headers, "x-openai-subagent")
	}
	if projection.installation {
		deleteOpenAIHeaderEqualFold(headers, codexInstallationIDKey)
		headers.Set(codexInstallationIDKey, projection.installationID)
	}
	if projection.stableTurn {
		deleteOpenAICodexIdentityHeaders(headers)
		headers.Set("session-id", projection.turnIdentity.SessionID)
		headers.Set("thread-id", projection.turnIdentity.ThreadID)
		if !compact {
			headers.Set("x-client-request-id", projection.turnIdentity.ThreadID)
		}
		if projection.turnIdentity.ParentThreadID != "" {
			headers.Set("x-codex-parent-thread-id", projection.turnIdentity.ParentThreadID)
		}
	}
	if windowID := strings.TrimSpace(projection.wireProfile.WindowID); windowID != "" {
		deleteOpenAIHeaderEqualFold(headers, "x-codex-window-id")
		headers.Set("x-codex-window-id", windowID)
	}
	if subagent := strings.TrimSpace(projection.wireProfile.SubagentHeader); subagent != "" {
		deleteOpenAIHeaderEqualFold(headers, "x-openai-subagent")
		headers.Set("x-openai-subagent", subagent)
	}
	if parentThreadID := strings.TrimSpace(projection.wireProfile.TurnLineage.ParentThreadID); parentThreadID != "" {
		deleteOpenAIHeaderEqualFold(headers, "x-codex-parent-thread-id")
		headers.Set("x-codex-parent-thread-id", parentThreadID)
	}
	applyOpenAICodexCanonicalTurnMetadataHeader(headers, projection, projection.createsTurnMetadata())
}

func applyOpenAICodexCanonicalTurnMetadataHeader(headers http.Header, projection openAICodexMetadataProjection, create bool) {
	if headers == nil || !projection.enabled() {
		return
	}
	values := headerValuesCaseInsensitive(headers, openAIWSTurnMetadataHeader)
	if len(values) == 0 && !create {
		return
	}
	base := "{}"
	validValues := 0
	for _, value := range values {
		if isOpenAICodexTurnMetadataObject(value) {
			validValues++
			if validValues == 1 {
				base = value
			}
		}
	}
	if len(values) > 0 && (validValues != len(values) || len(values) != 1) {
		observeOpenAICodexMetadataRebuilt(openAICodexMetadataCarrierHeaderTurnMetadata)
	}
	rewritten, err := rewriteOpenAICodexTurnMetadataProjectionForCarrier(base, projection, true, false)
	if err != nil {
		return
	}
	deleteOpenAIHeaderEqualFold(headers, openAIWSTurnMetadataHeader)
	headers.Set(openAIWSTurnMetadataHeader, rewritten)
}

func isOpenAICodexTurnMetadataObject(raw string) bool {
	var metadata map[string]json.RawMessage
	return json.Unmarshal([]byte(strings.TrimSpace(raw)), &metadata) == nil && metadata != nil
}

func deleteOpenAICodexIdentityHeaders(headers http.Header) {
	if headers == nil {
		return
	}
	identityHeaders := [...]string{
		"session-id",
		"session_id",
		"thread-id",
		"thread_id",
		"conversation_id",
		"conversation-id",
		"x-client-request-id",
		"x-codex-parent-thread-id",
	}
	for key := range headers {
		for _, identityHeader := range identityHeaders {
			if strings.EqualFold(key, identityHeader) {
				delete(headers, key)
				break
			}
		}
	}
}

// MergeOpenAIOutboundSessionIdentityBody projects the complete identity into
// a Responses/WS body. Compact callers deliberately never invoke it.
func MergeOpenAIOutboundSessionIdentityBody(body []byte, identity OpenAIOutboundSessionIdentity) ([]byte, error) {
	return mergeOpenAICodexTurnIdentityBody(body, identity)
}

func mergeOpenAICodexTurnIdentityBody(body []byte, identity OpenAICodexTurnIdentity) ([]byte, error) {
	if len(body) == 0 {
		return body, nil
	}
	if !utf8.Valid(body) {
		return body, errors.New("decode OpenAI Codex turn identity body: invalid UTF-8")
	}
	if strings.TrimSpace(identity.SessionID) == "" || strings.TrimSpace(identity.ThreadID) == "" {
		return body, nil
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(body, &root); err != nil || root == nil {
		if err == nil {
			err = errors.New("expected object")
		}
		return body, fmt.Errorf("decode OpenAI Codex turn identity body: %w", err)
	}
	metadata := make(map[string]json.RawMessage)
	metadataWritable := true
	if raw, ok := root["client_metadata"]; ok {
		var decoded map[string]json.RawMessage
		if json.Unmarshal(raw, &decoded) == nil && decoded != nil {
			metadata = decoded
		} else {
			metadataWritable = false
		}
	}
	if metadataWritable {
		for _, field := range []string{
			"session_id", "session-id", "thread_id", "thread-id",
			"conversation_id", "conversation-id", "parent_thread_id",
			"forked_from_thread_id", "x-codex-parent-thread-id",
		} {
			delete(metadata, field)
		}
		metadata["session_id"] = mustMarshalJSONString(identity.SessionID)
		metadata["thread_id"] = mustMarshalJSONString(identity.ThreadID)
		if identity.ParentThreadID != "" {
			metadata["x-codex-parent-thread-id"] = mustMarshalJSONString(identity.ParentThreadID)
		}

		turnMetadata := ""
		if raw, ok := metadata[openAIWSTurnMetadataHeader]; ok {
			turnMetadata = normalizeOpenAICodexTurnMetadataRaw(raw)
			if rewritten, rewriteErr := rewriteOpenAICodexTurnMetadata(turnMetadata, identity); rewriteErr == nil {
				metadata[openAIWSTurnMetadataHeader] = mustMarshalJSONString(rewritten)
			}
		} else {
			raw, rootMetadataPresent := root[openAIWSTurnMetadataHeader]
			if rootMetadataPresent {
				turnMetadata = normalizeOpenAICodexTurnMetadataRaw(raw)
			}
			rewritten, rewriteErr := rewriteOpenAICodexTurnMetadata(turnMetadata, identity)
			if rewriteErr != nil && !rootMetadataPresent {
				return body, rewriteErr
			}
			if rewriteErr == nil {
				metadata[openAIWSTurnMetadataHeader] = mustMarshalJSONString(rewritten)
			}
		}

		encodedMetadata, err := marshalJSONWithoutHTMLEscape(metadata)
		if err != nil {
			return body, fmt.Errorf("encode OpenAI Codex client metadata: %w", err)
		}
		root["client_metadata"] = encodedMetadata
	}
	if raw, present := root[openAIWSTurnMetadataHeader]; present {
		turnMetadata := normalizeOpenAICodexTurnMetadataRaw(raw)
		if rewritten, rewriteErr := rewriteOpenAICodexTurnMetadata(turnMetadata, identity); rewriteErr == nil {
			root[openAIWSTurnMetadataHeader] = mustMarshalJSONString(rewritten)
		}
	}
	out, err := marshalJSONWithoutHTMLEscape(root)
	if err != nil {
		return body, fmt.Errorf("encode OpenAI Codex turn identity body: %w", err)
	}
	return out, nil
}

func mergeOpenAICodexIdentityBodyForPlan(body []byte, plan OpenAIOAuthIdentityPlan, existingOnly bool) ([]byte, error) {
	if len(body) == 0 || !utf8.Valid(body) {
		return body, nil
	}
	projection := openAICodexMetadataProjectionFromPlan(plan)
	if !projection.enabled() {
		return body, nil
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(body, &root); err != nil || root == nil {
		if existingOnly {
			return body, nil
		}
		if err == nil {
			err = errors.New("expected object")
		}
		return body, fmt.Errorf("decode OpenAI Codex identity body: %w", err)
	}

	metadata := make(map[string]json.RawMessage)
	metadataPresent := false
	metadataObject := false
	if raw, present := root["client_metadata"]; present {
		metadataPresent = true
		if json.Unmarshal(raw, &metadata) == nil && metadata != nil {
			metadataObject = true
		} else {
			metadata = make(map[string]json.RawMessage)
			if !existingOnly {
				observeOpenAICodexMetadataRebuilt(openAICodexMetadataCarrierClientMetadataContainer)
			}
		}
	}
	metadataWritable := metadataObject || (!existingOnly && (metadataPresent || projection.enabled()))
	metadataModified := metadataWritable && !metadataObject
	if metadataWritable {
		if !existingOnly {
			if projection.wireActive {
				applyOpenAICodexStrictFlatWireProfile(metadata, projection)
				metadataModified = true
			} else if projection.installation {
				metadata[codexInstallationIDKey] = mustMarshalJSONString(projection.installationID)
				metadataModified = true
			}
			if !projection.wireActive && projection.stableTurn {
				deleteOpenAICodexFlatTurnAliases(metadata)
				metadata["session_id"] = mustMarshalJSONString(projection.turnIdentity.SessionID)
				metadata["thread_id"] = mustMarshalJSONString(projection.turnIdentity.ThreadID)
				if projection.turnIdentity.ParentThreadID != "" {
					metadata["x-codex-parent-thread-id"] = mustMarshalJSONString(projection.turnIdentity.ParentThreadID)
				}
				metadataModified = true
			}
			if !projection.wireActive && projection.requestTurnActive {
				deleteOpenAICodexFlatRequestTurnAliases(metadata)
				metadata["turn_id"] = mustMarshalJSONString(projection.requestTurn.ID)
				metadataModified = true
			}
		}

		rawNested, nestedPresent := metadata[openAIWSTurnMetadataHeader]
		if nestedPresent || (!existingOnly && projection.createsTurnMetadata()) {
			base := "{}"
			if nestedPresent {
				if candidate, valid := normalizeOpenAICodexNestedTurnMetadataObject(rawNested); valid {
					base = candidate
				} else {
					observeOpenAICodexMetadataRebuilt(openAICodexMetadataCarrierClientTurnMetadata)
				}
			} else if rawRoot, present := root[openAIWSTurnMetadataHeader]; present {
				if candidate := normalizeOpenAICodexTurnMetadataRaw(rawRoot); candidate != "" && isOpenAICodexTurnMetadataObject(candidate) {
					base = candidate
				}
			}
			rewritten, err := rewriteOpenAICodexTurnMetadataProjectionForCarrier(base, projection, true, true)
			if err != nil {
				return body, err
			}
			metadata[openAIWSTurnMetadataHeader] = mustMarshalJSONString(rewritten)
			metadataModified = true
		}
	}

	rootModified := false
	if projection.requestTurnActive {
		_, hasTurnID := root["turn_id"]
		_, hasTurnStartedAt := root["turn_started_at_unix_ms"]
		if hasTurnID || hasTurnStartedAt {
			root["turn_id"] = mustMarshalJSONString(projection.requestTurn.ID)
			root["turn_started_at_unix_ms"] = json.RawMessage(strconv.FormatInt(projection.requestTurn.StartedAtUnixMS, 10))
			rootModified = true
		}
	}
	if rawRoot, present := root[openAIWSTurnMetadataHeader]; present {
		base := normalizeOpenAICodexTurnMetadataRaw(rawRoot)
		if !isOpenAICodexTurnMetadataObject(base) {
			base = "{}"
			observeOpenAICodexMetadataRebuilt(openAICodexMetadataCarrierBodyTurnMetadata)
		}
		rewritten, err := rewriteOpenAICodexTurnMetadataProjectionForCarrier(base, projection, true, true)
		if err != nil {
			return body, err
		}
		root[openAIWSTurnMetadataHeader] = mustMarshalJSONString(rewritten)
		rootModified = true
	}
	if metadataModified {
		encodedMetadata, err := marshalJSONWithoutHTMLEscape(metadata)
		if err != nil {
			return body, fmt.Errorf("encode OpenAI Codex client metadata: %w", err)
		}
		root["client_metadata"] = encodedMetadata
		rootModified = true
	}
	if !rootModified {
		return body, nil
	}
	out, err := marshalJSONWithoutHTMLEscape(root)
	if err != nil {
		return body, fmt.Errorf("encode OpenAI Codex identity body: %w", err)
	}
	return out, nil
}

func deleteOpenAICodexFlatTurnAliases(metadata map[string]json.RawMessage) {
	for _, field := range []string{
		"session_id", "session-id", "thread_id", "thread-id",
		"conversation_id", "conversation-id", "parent_thread_id", "parent-thread-id",
		"forked_from_thread_id", "forked-from-thread-id", "x-codex-parent-thread-id",
	} {
		delete(metadata, field)
	}
}

func deleteOpenAICodexFlatRequestTurnAliases(metadata map[string]json.RawMessage) {
	for _, field := range []string{
		"turn_id", "turn-id", "turn_started_at_unix_ms", "turn-started-at-unix-ms",
	} {
		delete(metadata, field)
	}
}

func applyOpenAICodexStrictFlatWireProfile(metadata map[string]json.RawMessage, projection openAICodexMetadataProjection) {
	if metadata == nil {
		return
	}
	for _, field := range []string{
		"installation_id", "x-codex-installation-id",
		"session_id", "session-id", "thread_id", "thread-id",
		"conversation_id", "conversation-id", "agent_name",
		"turn_id", "turn-id", "window_id", "x-codex-window-id",
		"request_kind", "compaction", "code_mode_tool_names", "tool_namespaces_info",
		"turn_started_at_unix_ms", "turn-started-at-unix-ms",
		"forked_from_thread_id", "forked-from-thread-id",
		"parent_thread_id", "parent-thread-id", "x-codex-parent-thread-id",
		"parent_turn_id", "root_turn_id", "subagent_kind", "x-openai-subagent",
		"thread_source", "sandbox", "sandbox_mode", "auto_review_enabled",
		"node_repl_auto_review_required", "node_repl_disabled", "workspaces",
	} {
		delete(metadata, field)
	}
	profile := projection.wireProfile
	putCodexFlatString(metadata, codexInstallationIDKey, profile.InstallationID)
	putCodexFlatString(metadata, "session_id", profile.SessionID)
	putCodexFlatString(metadata, "thread_id", profile.ThreadID)
	putCodexFlatString(metadata, "x-codex-window-id", profile.WindowID)
	if profile.RequestKind != CodexWireRequestMemory && profile.TurnID.ValidFor(profile.RequestKind) {
		putCodexFlatString(metadata, "turn_id", profile.TurnID.Value)
	}
	putCodexFlatString(metadata, "x-openai-subagent", profile.SubagentHeader)
	putCodexFlatString(metadata, "x-codex-parent-thread-id", profile.TurnLineage.ParentThreadID)
	if profile.TurnLineage.ParentTurnID.ValidFor(profile.RequestKind) {
		putCodexFlatString(metadata, "parent_turn_id", profile.TurnLineage.ParentTurnID.Value)
	}
	if profile.TurnLineage.RootTurnID.ValidFor(profile.RequestKind) {
		putCodexFlatString(metadata, "root_turn_id", profile.TurnLineage.RootTurnID.Value)
	}
}

func putCodexFlatString(metadata map[string]json.RawMessage, key, value string) {
	if value = strings.TrimSpace(value); value != "" {
		metadata[key] = mustMarshalJSONString(value)
	}
}

// mergeOpenAIOAuthPassthroughIdentityBody applies the account-owned identity
// without re-encoding the entire Responses payload. Only client_metadata and
// an existing top-level turn-metadata carrier may change; all other bytes are
// left as produced by the passthrough normalizer.
func mergeOpenAIOAuthPassthroughIdentityBody(body []byte, plan OpenAIOAuthIdentityPlan) ([]byte, error) {
	if len(body) == 0 || !utf8.Valid(body) || !gjson.ParseBytes(body).IsObject() {
		return body, nil
	}

	projection := openAICodexMetadataProjectionFromPlan(plan)
	if !projection.enabled() {
		return body, nil
	}

	out := body
	clientMetadata := gjson.GetBytes(out, "client_metadata")
	metadata := make(map[string]json.RawMessage)
	if clientMetadata.IsObject() {
		if err := json.Unmarshal([]byte(clientMetadata.Raw), &metadata); err != nil {
			return body, fmt.Errorf("decode OpenAI OAuth passthrough client_metadata: %w", err)
		}
	} else if clientMetadata.Exists() {
		observeOpenAICodexMetadataRebuilt(openAICodexMetadataCarrierClientMetadataContainer)
	}

	if projection.installation {
		metadata[codexInstallationIDKey] = mustMarshalJSONString(projection.installationID)
	}
	if projection.wireActive {
		applyOpenAICodexStrictFlatWireProfile(metadata, projection)
	} else if projection.stableTurn {
		deleteOpenAICodexFlatTurnAliases(metadata)
		metadata["session_id"] = mustMarshalJSONString(projection.turnIdentity.SessionID)
		metadata["thread_id"] = mustMarshalJSONString(projection.turnIdentity.ThreadID)
		if projection.turnIdentity.ParentThreadID != "" {
			metadata["x-codex-parent-thread-id"] = mustMarshalJSONString(projection.turnIdentity.ParentThreadID)
		}
	}
	if !projection.wireActive && projection.requestTurnActive {
		deleteOpenAICodexFlatRequestTurnAliases(metadata)
		metadata["turn_id"] = mustMarshalJSONString(projection.requestTurn.ID)
	}

	rawNested, nestedPresent := metadata[openAIWSTurnMetadataHeader]
	if nestedPresent || projection.createsTurnMetadata() {
		base := "{}"
		if nestedPresent {
			if candidate, valid := normalizeOpenAICodexNestedTurnMetadataObject(rawNested); valid {
				base = candidate
			} else {
				observeOpenAICodexMetadataRebuilt(openAICodexMetadataCarrierClientTurnMetadata)
			}
		} else {
			rootMetadata := gjson.GetBytes(out, openAIWSTurnMetadataHeader)
			if rootMetadata.Exists() {
				if candidate := normalizeOpenAICodexTurnMetadataRaw(json.RawMessage(rootMetadata.Raw)); isOpenAICodexTurnMetadataObject(candidate) {
					base = candidate
				}
			}
		}
		rewritten, err := rewriteOpenAICodexTurnMetadataProjectionForCarrier(base, projection, true, true)
		if err != nil {
			return body, err
		}
		metadata[openAIWSTurnMetadataHeader] = mustMarshalJSONString(rewritten)
	}

	encodedMetadata, err := marshalJSONWithoutHTMLEscape(metadata)
	if err != nil {
		return body, fmt.Errorf("encode OpenAI OAuth passthrough client_metadata: %w", err)
	}
	out, err = sjson.SetRawBytes(out, "client_metadata", encodedMetadata)
	if err != nil {
		return body, fmt.Errorf("splice OpenAI OAuth passthrough client_metadata: %w", err)
	}
	if projection.requestTurnActive {
		rootTurnID := gjson.GetBytes(out, "turn_id")
		rootTurnStartedAt := gjson.GetBytes(out, "turn_started_at_unix_ms")
		if rootTurnID.Exists() || rootTurnStartedAt.Exists() {
			out, err = sjson.SetBytes(out, "turn_id", projection.requestTurn.ID)
			if err != nil {
				return body, fmt.Errorf("splice OpenAI OAuth passthrough turn_id: %w", err)
			}
			out, err = sjson.SetBytes(out, "turn_started_at_unix_ms", projection.requestTurn.StartedAtUnixMS)
			if err != nil {
				return body, fmt.Errorf("splice OpenAI OAuth passthrough turn_started_at_unix_ms: %w", err)
			}
		}
	}

	rootMetadata := gjson.GetBytes(out, openAIWSTurnMetadataHeader)
	if rootMetadata.Exists() {
		base := normalizeOpenAICodexTurnMetadataRaw(json.RawMessage(rootMetadata.Raw))
		if !isOpenAICodexTurnMetadataObject(base) {
			base = "{}"
			observeOpenAICodexMetadataRebuilt(openAICodexMetadataCarrierBodyTurnMetadata)
		}
		rewritten, rewriteErr := rewriteOpenAICodexTurnMetadataProjectionForCarrier(base, projection, true, true)
		if rewriteErr != nil {
			return body, rewriteErr
		}
		out, err = sjson.SetBytes(out, openAIWSTurnMetadataHeader, rewritten)
		if err != nil {
			return body, fmt.Errorf("splice OpenAI OAuth passthrough turn metadata: %w", err)
		}
	}
	return out, nil
}

func normalizeOpenAICodexTurnMetadataRaw(raw json.RawMessage) string {
	if len(raw) == 0 || !utf8.Valid(raw) {
		return ""
	}
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return ""
	}
	var encoded string
	if strings.HasPrefix(trimmed, `"`) && json.Unmarshal(raw, &encoded) == nil {
		return strings.TrimSpace(encoded)
	}
	return trimmed
}

func normalizeOpenAICodexNestedTurnMetadataObject(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 || !utf8.Valid(raw) {
		return "", false
	}
	var encoded string
	if json.Unmarshal(raw, &encoded) != nil {
		return "", false
	}
	encoded = strings.TrimSpace(encoded)
	return encoded, isOpenAICodexTurnMetadataObject(encoded)
}

func rewriteOpenAICodexTurnMetadata(raw string, identity OpenAICodexTurnIdentity) (string, error) {
	return rewriteOpenAICodexTurnMetadataForCarrier(raw, identity, true)
}

func rewriteOpenAICodexTurnMetadataForCarrier(raw string, identity OpenAICodexTurnIdentity, includeToolNamespaces bool) (string, error) {
	return rewriteOpenAICodexTurnMetadataProjectionForCarrier(raw, openAICodexMetadataProjection{
		turnIdentity: identity,
		stableTurn:   strings.TrimSpace(identity.SessionID) != "" && strings.TrimSpace(identity.ThreadID) != "",
	}, false, includeToolNamespaces)
}

func rewriteOpenAICodexTurnMetadataProjection(raw string, projection openAICodexMetadataProjection, rebuildInvalid bool) (string, error) {
	return rewriteOpenAICodexTurnMetadataProjectionForCarrier(raw, projection, rebuildInvalid, true)
}

func rewriteOpenAICodexTurnMetadataProjectionForCarrier(
	raw string,
	projection openAICodexMetadataProjection,
	rebuildInvalid bool,
	includeToolNamespaces bool,
) (string, error) {
	baseProfile := newCodexWireProfile()
	var baseMetadata map[string]json.RawMessage
	trimmed := strings.TrimSpace(raw)
	if trimmed != "" {
		if err := json.Unmarshal([]byte(trimmed), &baseMetadata); err != nil || baseMetadata == nil {
			if !rebuildInvalid {
				return "", errors.New("decode OpenAI Codex turn metadata: expected JSON object")
			}
		} else {
			baseProfile = ParseCodexWireProfile(trimmed)
		}
	}
	profile := cloneCodexWireProfile(projection.wireProfile)
	if profile.Revision == "" {
		profile = newCodexWireProfile()
	}
	if !profile.Finalized {
		mergeCodexWireProfileMissing(&profile, baseProfile)
		// Compatibility projection still freezes reserved identity fields, but
		// each carrier owns its non-reserved extension values.
		mergeCodexWireExtras(&profile, baseProfile.ExtraMetadata, true)
	} else {
		// Conversion and passthrough layers may add valid non-identity metadata
		// after capture. Keep it bounded without allowing the carrier to replace
		// any finalized identity or lineage field.
		// Reserved identity fields remain frozen by the plan, while unknown
		// extension fields retain the value carried by this specific header/body
		// carrier. This avoids collapsing unrelated passthrough metadata merely
		// because another carrier used the same extension key.
		mergeCodexWireExtras(&profile, baseProfile.ExtraMetadata, true)
		if len(profile.Workspaces) == 0 {
			profile.Workspaces = append(json.RawMessage(nil), baseProfile.Workspaces...)
		}
		if includeToolNamespaces && profile.ToolNamespacesInfoAllowed && len(profile.ToolNamespacesInfo) == 0 {
			profile.ToolNamespacesInfo = append(json.RawMessage(nil), baseProfile.ToolNamespacesInfo...)
		}
	}
	if projection.installation {
		profile.InstallationID = projection.installationID
	}
	if projection.stableTurn {
		profile.SessionID = projection.turnIdentity.SessionID
		profile.ThreadID = projection.turnIdentity.ThreadID
		profile.TurnLineage.ParentThreadID = projection.turnIdentity.ParentThreadID
		profile.TurnLineage.ForkedFromThreadID = projection.turnIdentity.ForkedFromThreadID
	}
	if projection.requestTurnActive {
		if turnID, valid := projection.requestTurn.codexTurnID(profile.RequestKind); valid {
			profile.TurnID = turnID
			profile.turnIDPresent = true
			profile.turnIDMalformed = false
			profile.turnIDCandidates = []string{turnID.Value}
		}
		if projection.requestTurn.StartedAtUnixMS > 0 {
			profile.TurnStartedAtUnixMS = projection.requestTurn.StartedAtUnixMS
			profile.TurnStartedAtSet = true
		}
	}
	encoded, err := profile.MarshalNestedJSON(includeToolNamespaces)
	if err != nil {
		return "", fmt.Errorf("encode OpenAI Codex turn metadata: %w", err)
	}
	if !profile.Finalized && len(baseMetadata) > 0 {
		var projected map[string]json.RawMessage
		if err := json.Unmarshal([]byte(encoded), &projected); err != nil {
			return "", fmt.Errorf("decode projected OpenAI Codex turn metadata: %w", err)
		}
		for key, value := range baseMetadata {
			if _, reserved := codexWireReservedMetadataKeys[key]; reserved {
				continue
			}
			if _, present := projected[key]; !present {
				projected[key] = append(json.RawMessage(nil), value...)
			}
		}
		legacyEncoded, err := marshalJSONWithoutHTMLEscape(projected)
		if err != nil {
			return "", fmt.Errorf("encode compatible OpenAI Codex turn metadata: %w", err)
		}
		encoded = escapeNonASCIIJSON(legacyEncoded)
	}
	return encoded, nil
}

func mustMarshalJSONString(value string) json.RawMessage {
	encoded, _ := json.Marshal(strings.TrimSpace(value))
	return encoded
}

func marshalJSONWithoutHTMLEscape(value any) ([]byte, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimSpace(buffer.Bytes()), nil
}

func escapeNonASCIIJSON(raw []byte) string {
	if len(raw) == 0 {
		return ""
	}
	var out strings.Builder
	out.Grow(len(raw))
	for len(raw) > 0 {
		r, size := utf8.DecodeRune(raw)
		raw = raw[size:]
		if r < utf8.RuneSelf {
			out.WriteByte(byte(r))
			continue
		}
		if r <= 0xffff {
			_, _ = fmt.Fprintf(&out, "\\u%04x", r)
			continue
		}
		high, low := utf16.EncodeRune(r)
		_, _ = fmt.Fprintf(&out, "\\u%04x\\u%04x", high, low)
	}
	return out.String()
}
