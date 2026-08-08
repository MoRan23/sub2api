package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

const openAIWSOutboundIdentityDigestDomain = "sub2api/openai-ws-outbound-identity/v1"

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

// openAIOutboundSessionIdentityTransportEnabledForRequest captures the switch
// on first use by a Gin request. OpenAI request construction has builder and
// post-build phases; both must see one value or a concurrent admin toggle can
// leave a hybrid of legacy and UUIDv7 aliases. Non-request callers retain live
// reads from the setting service.
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

// resolveOpenAIOutboundSessionIdentityForTransport gates the UUIDv7 identity
// resolver behind the runtime setting.  Keeping the gate at the transport
// boundary lets the resolver remain a small, reusable mapping primitive while
// preserving the legacy hash behavior when the opt-in setting is unavailable
// (including the lightweight service instances used by existing tests).
func (s *OpenAIGatewayService) resolveOpenAIOutboundSessionIdentityForTransport(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	body []byte,
	callerSeed string,
	forceCallerSeed bool,
) (OpenAIOutboundSessionIdentity, string, bool, error) {
	return s.resolveOpenAIOutboundSessionIdentityForTransportSnapshot(
		ctx,
		c,
		account,
		body,
		callerSeed,
		forceCallerSeed,
		s.openAIOutboundSessionIdentityTransportEnabledForRequest(ctx, c),
	)
}

// resolveOpenAIOutboundSessionIdentityForTransportSnapshot is used by
// connection-oriented transports after they capture the runtime setting once.
// Every turn on that connection must use the same mode snapshot; re-reading a
// concurrently toggled setting could produce identity headers with a legacy
// mode flag (or the reverse).
func (s *OpenAIGatewayService) resolveOpenAIOutboundSessionIdentityForTransportSnapshot(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	body []byte,
	callerSeed string,
	forceCallerSeed bool,
	enabled bool,
) (OpenAIOutboundSessionIdentity, string, bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if !enabled {
		return OpenAIOutboundSessionIdentity{}, "", false, nil
	}
	logicalKey := ResolveOpenAIOutboundSessionLogicalKey(c, body, callerSeed)
	// Some transports have already selected a connection/frame-scoped key
	// before entering this helper. Preserve that selection instead of letting a
	// copied request header win when the general resolver runs again. Compact
	// callers use the same mechanism for their historical prompt-cache
	// final-writer precedence.
	if forceCallerSeed {
		// An invalid selected key is explicit but unusable. Do not
		// fall through to a copied header/body signal and accidentally create a
		// different UUID mapping; the caller will retain its legacy wire value.
		logicalKey = sanitizeSessionID(callerSeed)
	}
	identity, ok, err := s.resolveOpenAIOutboundSessionIdentity(ctx, c, account, logicalKey)
	if err != nil {
		if errors.Is(err, errOpenAIOutboundSessionIdentityNamespace) {
			return OpenAIOutboundSessionIdentity{}, logicalKey, false, err
		}
		// UUID generation and primary-store failures are fail-open by contract;
		// callers retain their legacy value. Namespace/credential-owner failures
		// are returned above because they indicate an invalid account topology.
		slog.WarnContext(ctx, "openai outbound UUIDv7 identity resolution failed", "reason", "resolver_error")
		return OpenAIOutboundSessionIdentity{}, logicalKey, false, nil
	}
	return identity, logicalKey, ok, nil
}

// resolveOpenAIWSFrameLogicalKey resolves the only per-frame signal that the
// legacy WS isolate helper received: prompt_cache_key.  Handshake headers are
// connection-scoped, while body-only client_metadata/turn metadata were never
// part of the old 16-hex coverage and therefore cannot create or switch a
// UUIDv7 identity.
func resolveOpenAIWSFrameLogicalKey(body []byte, promptCacheKey string) string {
	if key := sanitizeSessionID(promptCacheKey); key != "" {
		return key
	}
	// The legacy WS seed is prompt_cache_key.  Most callers extract it before
	// reaching this helper, but the body fallback keeps the transport boundary
	// correct for bridges and frame-level callers that only have raw JSON.  Do
	// not inspect client_metadata or turn metadata here: those fields were not
	// inputs to the old isolate helper and must not expand UUIDv7 coverage.
	_, _, bodyPromptCacheKey := openAIOutboundSessionBodySignals(body)
	return sanitizeSessionID(bodyPromptCacheKey)
}

// advanceOpenAIWSFrameLogicalKey compares explicit per-frame keys in the same
// domain. The first handshake may be pinned by a higher-priority HTTP header,
// so comparing every later body key directly with the pinned key would falsely
// treat an unchanged first-frame body key as a session switch.
func advanceOpenAIWSFrameLogicalKey(frameKey, previousFrameKey, pinnedLogicalKey string) (string, bool) {
	frameKey = sanitizeSessionID(frameKey)
	if frameKey == "" {
		return previousFrameKey, false
	}
	if frameKey == previousFrameKey {
		return previousFrameKey, false
	}
	return frameKey, frameKey != pinnedLogicalKey
}

// openAIWSOutboundIdentityDigest is an internal compatibility token for pooled
// WebSocket handshakes.  It is deliberately empty outside UUIDv7 mode, so the
// legacy pool behavior remains byte-for-byte compatible.  The random pair is
// hashed before entering pool metadata or logs.
func openAIWSOutboundIdentityDigest(identity OpenAIOutboundSessionIdentity) string {
	sessionID := strings.TrimSpace(identity.SessionID)
	threadID := strings.TrimSpace(identity.ThreadID)
	if sessionID == "" || threadID == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(openAIWSOutboundIdentityDigestDomain + "\x00" + sessionID + "\x00" + threadID))
	return hex.EncodeToString(sum[:])
}

func openAIWSOutboundIdentityHeaderValueForLog(headers http.Header, key, identityDigest string) string {
	identityDigest = strings.TrimSpace(identityDigest)
	if identityDigest == "" {
		return openAIWSHeaderValueForLog(headers, key)
	}
	// A short one-way token is sufficient to correlate the two identity headers
	// and connection-pool decisions without putting either UUID on the log wire.
	if len(identityDigest) > 12 {
		identityDigest = identityDigest[:12]
	}
	return "uuidv7:" + identityDigest
}

func clearOpenAIOutboundSessionIdentityHeaders(headers http.Header) {
	deleteOpenAIOutboundSessionIdentityHeaders(headers)
}

// applyOpenAIOutboundSessionIdentityCompactHeaders keeps compact's wire
// surface aligned with its legacy writer. Compact accepts the session_id
// protocol header; the UUIDv7 thread/conversation aliases and client request
// correlation header belong to the full Responses protocol and must not be
// introduced on this unary path.
func applyOpenAIOutboundSessionIdentityCompactHeaders(headers http.Header, identity OpenAIOutboundSessionIdentity) {
	if headers == nil {
		return
	}
	deleteOpenAIOutboundSessionIdentityHeaders(headers)
	headers.Set("session_id", strings.TrimSpace(identity.SessionID))
}
