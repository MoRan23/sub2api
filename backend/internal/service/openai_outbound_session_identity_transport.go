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
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
)

const openAIWSOutboundIdentityDigestDomain = "sub2api/openai-ws-outbound-identity/v2"

const openAIOutboundSessionIdentityRequestSnapshotKey = "openai_outbound_session_identity_enabled_snapshot"

func (s *OpenAIGatewayService) openAIOutboundSessionIdentityTransportEnabled(ctx context.Context) bool {
	if s == nil || s.settingService == nil {
		return false
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return s.settingService.IsOpenAIUUIDv7SessionIdentityEnabled(ctx)
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
	enabled := s.openAIOutboundSessionIdentityTransportEnabled(ctx)
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
	turnMetadata := firstHeaderValueCaseInsensitive(headers, openAIWSTurnMetadataHeader)
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
	if strings.TrimSpace(turnMetadata) != "" {
		if rewritten, err := rewriteOpenAICodexTurnMetadata(turnMetadata, identity); err == nil {
			headers.Set(openAIWSTurnMetadataHeader, rewritten)
		}
	}
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

func firstHeaderValueCaseInsensitive(headers http.Header, name string) string {
	if headers == nil {
		return ""
	}
	for key, values := range headers {
		if !strings.EqualFold(key, name) {
			continue
		}
		for _, value := range values {
			if strings.TrimSpace(value) != "" {
				return value
			}
		}
	}
	return ""
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
	if raw, ok := root["client_metadata"]; ok {
		var decoded map[string]json.RawMessage
		if json.Unmarshal(raw, &decoded) == nil && decoded != nil {
			metadata = decoded
		}
	}
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
	}
	if turnMetadata == "" {
		if raw, ok := root[openAIWSTurnMetadataHeader]; ok {
			turnMetadata = normalizeOpenAICodexTurnMetadataRaw(raw)
		}
	}
	rewrittenTurnMetadata, err := rewriteOpenAICodexTurnMetadata(turnMetadata, identity)
	if err != nil {
		return body, err
	}
	metadata[openAIWSTurnMetadataHeader] = mustMarshalJSONString(rewrittenTurnMetadata)
	if _, present := root[openAIWSTurnMetadataHeader]; present {
		root[openAIWSTurnMetadataHeader] = mustMarshalJSONString(rewrittenTurnMetadata)
	}

	encodedMetadata, err := marshalJSONWithoutHTMLEscape(metadata)
	if err != nil {
		return body, fmt.Errorf("encode OpenAI Codex client metadata: %w", err)
	}
	root["client_metadata"] = encodedMetadata
	out, err := marshalJSONWithoutHTMLEscape(root)
	if err != nil {
		return body, fmt.Errorf("encode OpenAI Codex turn identity body: %w", err)
	}
	return out, nil
}

func normalizeOpenAICodexTurnMetadataRaw(raw json.RawMessage) string {
	if len(raw) == 0 || !utf8.Valid(raw) {
		return ""
	}
	var encoded string
	if json.Unmarshal(raw, &encoded) == nil {
		return strings.TrimSpace(encoded)
	}
	trimmed := strings.TrimSpace(string(raw))
	if strings.HasPrefix(trimmed, "{") {
		return trimmed
	}
	return ""
}

func rewriteOpenAICodexTurnMetadata(raw string, identity OpenAICodexTurnIdentity) (string, error) {
	metadata := make(map[string]json.RawMessage)
	trimmed := strings.TrimSpace(raw)
	if trimmed != "" {
		if err := json.Unmarshal([]byte(trimmed), &metadata); err != nil || metadata == nil {
			metadata = make(map[string]json.RawMessage)
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
