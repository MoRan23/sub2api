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
	openAIWSOutboundIdentityPlanDigestDomain = "sub2api/openai-ws-outbound-identity-plan/v2"
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
	}
	for _, name := range [...]string{
		"user-agent",
		"originator",
		"version",
		codexInstallationIDKey,
		"x-codex-window-id",
		"session-id",
		"thread-id",
		"x-client-request-id",
		"x-codex-parent-thread-id",
		"session_id",
		"thread_id",
		"conversation_id",
		"conversation-id",
		openAIWSTurnMetadataHeader,
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

// mergeOpenAIOAuthPassthroughIdentityBody applies the account-owned identity
// without re-encoding the entire Responses payload. Only client_metadata and
// an existing top-level turn-metadata carrier may change; all other bytes are
// left as produced by the passthrough normalizer.
func mergeOpenAIOAuthPassthroughIdentityBody(body []byte, plan OpenAIOAuthIdentityPlan) ([]byte, error) {
	if len(body) == 0 || !utf8.Valid(body) || !gjson.ParseBytes(body).IsObject() {
		return body, nil
	}

	projectInstallation := plan.InstallationPolicy == OpenAIOAuthInstallationAccountPin &&
		plan.InstallationEnabled && strings.TrimSpace(plan.InstallationID) != ""
	projectTurn := plan.TurnIdentityEnabled &&
		strings.TrimSpace(plan.TurnIdentity.SessionID) != "" &&
		strings.TrimSpace(plan.TurnIdentity.ThreadID) != ""
	if !projectInstallation && !projectTurn {
		return body, nil
	}

	out := body
	clientMetadata := gjson.GetBytes(out, "client_metadata")
	metadataWritable := !clientMetadata.Exists() || clientMetadata.IsObject()
	if metadataWritable {
		metadata := make(map[string]json.RawMessage)
		if clientMetadata.IsObject() {
			if err := json.Unmarshal([]byte(clientMetadata.Raw), &metadata); err != nil {
				return body, fmt.Errorf("decode OpenAI OAuth passthrough client_metadata: %w", err)
			}
		}

		if projectInstallation {
			metadata[codexInstallationIDKey] = mustMarshalJSONString(plan.InstallationID)
		}
		if projectTurn {
			for _, field := range []string{
				"session_id", "session-id", "thread_id", "thread-id",
				"conversation_id", "conversation-id", "parent_thread_id",
				"forked_from_thread_id", "x-codex-parent-thread-id",
			} {
				delete(metadata, field)
			}
			metadata["session_id"] = mustMarshalJSONString(plan.TurnIdentity.SessionID)
			metadata["thread_id"] = mustMarshalJSONString(plan.TurnIdentity.ThreadID)
			if plan.TurnIdentity.ParentThreadID != "" {
				metadata["x-codex-parent-thread-id"] = mustMarshalJSONString(plan.TurnIdentity.ParentThreadID)
			}
		}

		if raw, present := metadata[openAIWSTurnMetadataHeader]; present {
			if rewritten, changed := rewriteOpenAIOAuthPassthroughTurnMetadata(
				normalizeOpenAICodexTurnMetadataRaw(raw), plan, projectInstallation, projectTurn,
			); changed {
				metadata[openAIWSTurnMetadataHeader] = mustMarshalJSONString(rewritten)
			}
		} else if projectTurn {
			turnMetadata := ""
			rootMetadata := gjson.GetBytes(out, openAIWSTurnMetadataHeader)
			rootMetadataPresent := rootMetadata.Exists()
			if rootMetadataPresent {
				turnMetadata = normalizeOpenAICodexTurnMetadataRaw(json.RawMessage(rootMetadata.Raw))
			}
			if rewritten, changed := rewriteOpenAIOAuthPassthroughTurnMetadata(
				turnMetadata, plan, projectInstallation, true,
			); changed || !rootMetadataPresent {
				metadata[openAIWSTurnMetadataHeader] = mustMarshalJSONString(rewritten)
			}
		}

		encodedMetadata, err := marshalJSONWithoutHTMLEscape(metadata)
		if err != nil {
			return body, fmt.Errorf("encode OpenAI OAuth passthrough client_metadata: %w", err)
		}
		out, err = sjson.SetRawBytes(out, "client_metadata", encodedMetadata)
		if err != nil {
			return body, fmt.Errorf("splice OpenAI OAuth passthrough client_metadata: %w", err)
		}
	}

	if projectTurn || projectInstallation {
		rootMetadata := gjson.GetBytes(out, openAIWSTurnMetadataHeader)
		if rootMetadata.Exists() {
			raw := normalizeOpenAICodexTurnMetadataRaw(json.RawMessage(rootMetadata.Raw))
			if rewritten, changed := rewriteOpenAIOAuthPassthroughTurnMetadata(
				raw, plan, projectInstallation, projectTurn,
			); changed {
				var setErr error
				out, setErr = sjson.SetBytes(out, openAIWSTurnMetadataHeader, rewritten)
				if setErr != nil {
					return body, fmt.Errorf("splice OpenAI OAuth passthrough turn metadata: %w", setErr)
				}
			}
		}
	}

	return out, nil
}

func rewriteOpenAIOAuthPassthroughTurnMetadata(
	raw string,
	plan OpenAIOAuthIdentityPlan,
	projectInstallation bool,
	projectTurn bool,
) (string, bool) {
	rewritten := raw
	changed := false
	if projectTurn {
		withTurn, err := rewriteOpenAICodexTurnMetadata(rewritten, plan.TurnIdentity)
		if err != nil {
			return raw, false
		}
		rewritten = withTurn
		changed = true
	}
	if projectInstallation {
		if withInstallation, installationChanged := rewriteCodexTurnMetadataInstallationID(rewritten, plan.InstallationID); installationChanged {
			rewritten = withInstallation
			changed = true
		}
	}
	return rewritten, changed
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

func rewriteOpenAICodexTurnMetadata(raw string, identity OpenAICodexTurnIdentity) (string, error) {
	metadata := make(map[string]json.RawMessage)
	trimmed := strings.TrimSpace(raw)
	if trimmed != "" {
		if err := json.Unmarshal([]byte(trimmed), &metadata); err != nil || metadata == nil {
			return "", errors.New("decode OpenAI Codex turn metadata: expected JSON object")
		}
	}
	for _, field := range []string{
		"session_id", "session-id", "thread_id", "thread-id",
		"conversation_id", "conversation-id", "parent_thread_id", "forked_from_thread_id",
	} {
		delete(metadata, field)
	}
	metadata["session_id"] = mustMarshalJSONString(identity.SessionID)
	metadata["thread_id"] = mustMarshalJSONString(identity.ThreadID)
	if identity.ParentThreadID != "" {
		metadata["parent_thread_id"] = mustMarshalJSONString(identity.ParentThreadID)
	}
	if identity.ForkedFromThreadID != "" {
		metadata["forked_from_thread_id"] = mustMarshalJSONString(identity.ForkedFromThreadID)
	}
	encoded, err := marshalJSONWithoutHTMLEscape(metadata)
	if err != nil {
		return "", fmt.Errorf("encode OpenAI Codex turn metadata: %w", err)
	}
	return escapeNonASCIIJSON(encoded), nil
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
