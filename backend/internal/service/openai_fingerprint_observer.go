package service

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// fingerprintObservationCapacity is the fixed process-local ring size. The
// observer is a live diagnostic view rather than durable storage, so keeping a
// bounded window avoids retaining request metadata indefinitely.
const fingerprintObservationCapacity = 500

const fingerprintObservationOutboundIdentityContextKey = "fingerprint_observation_outbound_identity"

// FingerprintObservationEntry captures the final OpenAI OAuth identity emitted
// for one request while fingerprint observation is enabled. SessionID and
// ThreadID are intentionally normalized independently: compact requests only
// have a session ID, while full Responses/compatibility/WS requests normally
// carry both values. Invalid or legacy (non-UUIDv7) values are returned empty.
type FingerprintObservationEntry struct {
	Timestamp                    time.Time `json:"timestamp"`
	AccountID                    int64     `json:"account_id"`
	AccountName                  string    `json:"account_name"`
	Pinned                       bool      `json:"pinned"`
	ClientReportedInstallationID string    `json:"client_reported_installation_id"`
	OutboundInstallationID       string    `json:"outbound_installation_id"`
	SessionID                    string    `json:"session_id"`
	ThreadID                     string    `json:"thread_id"`
	UserAgent                    string    `json:"user_agent"`
	Originator                   string    `json:"originator"`
	OpenAIBeta                   string    `json:"openai_beta"`
	Version                      string    `json:"version"`
	InboundEndpoint              string    `json:"inbound_endpoint"`
}

// fingerprintObserver is a process-level ring buffer gated by an atomic
// switch. Record keeps the normal request path to one atomic load when the
// feature is disabled. Turning the switch off also scrubs every slot, not just
// the logical head/size, so sensitive diagnostic values do not remain in heap
// memory after the observation window is closed.
type fingerprintObserver struct {
	enabled atomic.Bool

	mu   sync.Mutex
	ring []FingerprintObservationEntry
	head int
	size int
}

var globalFingerprintObserver = &fingerprintObserver{
	ring: make([]FingerprintObservationEntry, fingerprintObservationCapacity),
}

// SetFingerprintObservationEnabled publishes the observation toggle. Disabling
// observation synchronously clears and scrubs the ring buffer.
func SetFingerprintObservationEnabled(enabled bool) {
	globalFingerprintObserver.setEnabled(enabled)
}

// IsFingerprintObservationEnabled reports the current observation state.
func IsFingerprintObservationEnabled() bool {
	return globalFingerprintObserver.enabled.Load()
}

// SnapshotFingerprintObservations returns the newest entries first. A positive
// limit caps the result; a non-positive limit returns all buffered entries.
func SnapshotFingerprintObservations(limit int) []FingerprintObservationEntry {
	return globalFingerprintObserver.snapshot(limit)
}

func (o *fingerprintObserver) setEnabled(enabled bool) {
	if o == nil {
		return
	}
	// Serialize state transitions with record/snapshot. The atomic flag keeps
	// the disabled hot path cheap, while the lock gives concurrent enable/
	// disable calls a deterministic order and prevents an older disable from
	// clearing a newer enable's state after it returns.
	o.mu.Lock()
	defer o.mu.Unlock()
	o.enabled.Store(enabled)
	if !enabled {
		var zero FingerprintObservationEntry
		for i := range o.ring {
			o.ring[i] = zero
		}
		o.head = 0
		o.size = 0
	}
}

func (o *fingerprintObserver) record(entry FingerprintObservationEntry) {
	if o == nil || !o.enabled.Load() {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	// A disable may have raced the fast-path load above. Re-check while holding
	// the same lock used by setEnabled so a disabled observer stays empty.
	if !o.enabled.Load() || len(o.ring) == 0 {
		return
	}
	o.ring[o.head] = entry
	o.head = (o.head + 1) % len(o.ring)
	if o.size < len(o.ring) {
		o.size++
	}
}

func (o *fingerprintObserver) snapshot(limit int) []FingerprintObservationEntry {
	if o == nil {
		return []FingerprintObservationEntry{}
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if len(o.ring) == 0 || o.size == 0 {
		return []FingerprintObservationEntry{}
	}
	n := o.size
	if limit > 0 && limit < n {
		n = limit
	}
	out := make([]FingerprintObservationEntry, 0, n)
	for i := 0; i < n; i++ {
		idx := (o.head - 1 - i + len(o.ring)) % len(o.ring)
		out = append(out, o.ring[idx])
	}
	return out
}

// NormalizeFingerprintObservationUUIDv7 returns raw when it is a canonical
// lowercase RFC4122 UUIDv7; malformed, legacy, non-canonical, or empty values
// become "". This is intentionally independent for session and thread IDs.
func NormalizeFingerprintObservationUUIDv7(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || trimmed != raw {
		return ""
	}
	id, err := uuid.Parse(trimmed)
	if err != nil || id.Version() != uuid.Version(7) || id.Variant() != uuid.RFC4122 || id.String() != trimmed {
		return ""
	}
	return trimmed
}

// ValidateFingerprintObservationUUIDv7 reports whether raw is a canonical
// lowercase RFC4122 UUIDv7.
func ValidateFingerprintObservationUUIDv7(raw string) bool {
	return NormalizeFingerprintObservationUUIDv7(raw) != ""
}

// setFingerprintObservationOutboundIdentity marks the UUID pair owned by the
// final server-side writer for this request. The observer uses the pair only as
// provenance: values are still read back from the finalized wire headers/body.
// This prevents a client-supplied UUIDv7 from being retained while legacy mode
// is active or when no server-owned identity was created.
func setFingerprintObservationOutboundIdentity(c *gin.Context, identity OpenAIOutboundSessionIdentity) {
	if c == nil {
		return
	}
	if ValidateOpenAIOutboundSessionIdentity(identity) != nil {
		// An invalid replacement must not leave a trusted pair from an earlier
		// build on a reused context.
		clearFingerprintObservationOutboundIdentity(c)
		return
	}
	c.Set(fingerprintObservationOutboundIdentityContextKey, identity)
}

// clearFingerprintObservationOutboundIdentity removes the request-local
// provenance marker before a new outbound build starts. Gin contexts are
// normally request-scoped, but Responses retries, compatibility bridges, and
// WS reconnects can reuse one context for multiple final wire builds. Without
// clearing, a later legacy/fallback build could accidentally use an earlier
// UUID pair for client_metadata observation fallback.
func clearFingerprintObservationOutboundIdentity(c *gin.Context) {
	if c != nil {
		c.Set(fingerprintObservationOutboundIdentityContextKey, nil)
	}
}

func fingerprintObservationOutboundIdentityFromContext(c *gin.Context) (OpenAIOutboundSessionIdentity, bool) {
	if c == nil {
		return OpenAIOutboundSessionIdentity{}, false
	}
	raw, ok := c.Get(fingerprintObservationOutboundIdentityContextKey)
	if !ok {
		return OpenAIOutboundSessionIdentity{}, false
	}
	identity, ok := raw.(OpenAIOutboundSessionIdentity)
	if !ok || ValidateOpenAIOutboundSessionIdentity(identity) != nil {
		return OpenAIOutboundSessionIdentity{}, false
	}
	return identity, true
}

// Package-local aliases keep the validator convenient for the observer's
// focused tests and match the naming style of the surrounding OpenAI helpers.
func normalizeFingerprintUUIDv7(raw string) string {
	return NormalizeFingerprintObservationUUIDv7(raw)
}

func normalizeFingerprintObservationUUIDv7(raw string) string {
	return NormalizeFingerprintObservationUUIDv7(raw)
}

func validateFingerprintUUIDv7(raw string) bool {
	return ValidateFingerprintObservationUUIDv7(raw)
}

// recordFingerprintObservation writes a finalized per-request entry. Callers
// must invoke it only at the last header-writing boundary for the covered
// OpenAI Responses, Messages/Chat, or OAuth WebSocket transports.
func (s *OpenAIGatewayService) recordFingerprintObservation(c *gin.Context, account *Account, pin installationIDResolution, outbound http.Header) {
	s.recordFingerprintObservationWithBody(c, account, pin, outbound, nil)
}

// recordFingerprintObservationWithBody is used by the compatibility bridges,
// where the final Responses body is available alongside the finalized headers.
// Headers remain authoritative; client_metadata is only a fallback for a
// schema path that carries the server-owned pair in the body but not aliases
// in the wire header set.
func (s *OpenAIGatewayService) recordFingerprintObservationWithBody(c *gin.Context, account *Account, pin installationIDResolution, outbound http.Header, body []byte) {
	if !globalFingerprintObserver.enabled.Load() || account == nil || !account.IsOpenAIOAuth() || account.IsOpenAIPassthroughEnabled() {
		return
	}
	trustedIdentity, hasTrustedIdentity := fingerprintObservationOutboundIdentityFromContext(c)
	entry := FingerprintObservationEntry{
		Timestamp:                    time.Now(),
		AccountID:                    account.ID,
		AccountName:                  account.Name,
		Pinned:                       pin.Enabled,
		ClientReportedInstallationID: pin.ClientID,
		OutboundInstallationID:       pin.OutboundID,
	}
	if !pin.Enabled {
		// Without installation pinning the client-reported value is forwarded
		// unchanged, including body-only client_metadata values. Preserve the old
		// observer semantics; a finalized outbound header still wins below.
		entry.OutboundInstallationID = pin.ClientID
	}
	sessionHeaderPresent := false
	threadHeaderPresent := false
	if outbound != nil {
		if actual := strings.TrimSpace(outbound.Get(codexInstallationIDKey)); actual != "" {
			entry.OutboundInstallationID = actual
		}
		if hasTrustedIdentity {
			entry.SessionID, sessionHeaderPresent = fingerprintObservationHeaderUUID(outbound, trustedIdentity.SessionID,
				"session-id", "session_id")
			entry.ThreadID, threadHeaderPresent = fingerprintObservationHeaderUUID(outbound, trustedIdentity.ThreadID,
				"thread-id", "thread_id", "conversation_id", "conversation-id", "x-client-request-id")
		}
		entry.UserAgent = strings.TrimSpace(outbound.Get("user-agent"))
		entry.Originator = strings.TrimSpace(outbound.Get("originator"))
		entry.OpenAIBeta = strings.TrimSpace(outbound.Get("openai-beta"))
		entry.Version = strings.TrimSpace(outbound.Get("version"))
	}
	if hasTrustedIdentity && len(body) > 0 &&
		((entry.SessionID == "" && !sessionHeaderPresent) || (entry.ThreadID == "" && !threadHeaderPresent)) {
		bodySessionID, bodyThreadID := fingerprintObservationBodyUUIDs(body)
		if entry.SessionID == "" && !sessionHeaderPresent && bodySessionID == trustedIdentity.SessionID {
			entry.SessionID = bodySessionID
		}
		if entry.ThreadID == "" && !threadHeaderPresent && bodyThreadID == trustedIdentity.ThreadID {
			entry.ThreadID = bodyThreadID
		}
	}
	if c != nil && c.Request != nil {
		path := strings.TrimSpace(c.FullPath())
		if path == "" && c.Request.URL != nil {
			path = c.Request.URL.Path
		}
		entry.InboundEndpoint = c.Request.Method + " " + path
	}
	globalFingerprintObserver.record(entry)
}

// recordFingerprintObservationFromContext obtains the installation resolver's
// per-request snapshot and is used by final outbound writers that do not retain
// the local pin value returned by the shared installation boundary.
func (s *OpenAIGatewayService) recordFingerprintObservationFromContext(c *gin.Context, account *Account, outbound http.Header) {
	s.recordFingerprintObservationFromContextWithBody(c, account, outbound, nil)
}

func (s *OpenAIGatewayService) recordFingerprintObservationFromContextWithBody(c *gin.Context, account *Account, outbound http.Header, body []byte) {
	if !shouldRecordFingerprintObservationRequest(c, account) {
		return
	}
	pin := installationIDResolutionFromContext(c, account)
	s.recordFingerprintObservationWithBody(c, account, pin, outbound, body)
}

// recordFingerprintObservationAfterOpenAIWSHandshake records only a successful
// physical upstream WebSocket dial. Header construction happens before pool
// acquisition and can be followed by connection reuse, a failed dial, or a
// prewarmed connection; none of those are a new handshake that belongs in the
// request-scoped observation ring. The caller supplies the finalized outbound
// request headers (the pool's HandshakeHeaders are the upstream response
// headers and therefore are not suitable for this purpose).
func (s *OpenAIGatewayService) recordFingerprintObservationAfterOpenAIWSHandshake(
	c *gin.Context,
	account *Account,
	lease *openAIWSConnLease,
	outbound http.Header,
) {
	if lease == nil || lease.Reused() || lease.IsPrewarmed() {
		return
	}
	s.recordFingerprintObservationFromContext(c, account, outbound)
}

func installationIDResolutionFromContext(c *gin.Context, account *Account) installationIDResolution {
	if c == nil {
		return installationIDResolution{}
	}
	if cached, ok := c.Get(installationPinContextKey); ok {
		if requestCache, valid := cached.(installationIDRequestCache); valid &&
			(account == nil || requestCache.SourceAccountID == account.ID) {
			return requestCache.Resolution
		}
	}
	return installationIDResolution{}
}

func fingerprintObservationHeaderUUID(headers http.Header, expected string, names ...string) (string, bool) {
	if headers == nil {
		return "", false
	}
	expected = NormalizeFingerprintObservationUUIDv7(expected)
	if expected == "" {
		return "", false
	}
	present := false
	for key, values := range headers {
		acceptedAlias := false
		for _, name := range names {
			if strings.EqualFold(key, name) {
				acceptedAlias = true
				break
			}
		}
		if !acceptedAlias {
			continue
		}
		for _, value := range values {
			if strings.TrimSpace(value) == "" {
				continue
			}
			present = true
			// A conflicting or malformed final alias makes the wire ambiguous.
			// Do not let a different matching alias hide that conflict or allow
			// client_metadata to fill past it.
			if NormalizeFingerprintObservationUUIDv7(value) != expected {
				return "", true
			}
		}
	}
	if present {
		return expected, true
	}
	return "", false
}

func fingerprintObservationBodyUUIDs(body []byte) (sessionID, threadID string) {
	var root map[string]any
	if len(body) == 0 || json.Unmarshal(body, &root) != nil || root == nil {
		return "", ""
	}
	metadata, ok := root["client_metadata"].(map[string]any)
	if !ok {
		return "", ""
	}
	rawSessionID, _ := metadata["session_id"].(string)
	rawThreadID, _ := metadata["thread_id"].(string)
	return NormalizeFingerprintObservationUUIDv7(rawSessionID), NormalizeFingerprintObservationUUIDv7(rawThreadID)
}

// shouldRecordFingerprintObservationRequest deliberately keeps the observer
// narrow: Images, embeddings, alpha/search, and unrelated gateway transports
// must not add rows merely because they happen to use OpenAI OAuth.
func shouldRecordFingerprintObservationRequest(c *gin.Context, account *Account) bool {
	if c == nil || c.Request == nil || account == nil || !account.IsOpenAIOAuth() || account.IsOpenAIPassthroughEnabled() {
		return false
	}
	path := ""
	if c.Request.URL != nil {
		path = strings.ToLower(strings.TrimSpace(c.Request.URL.Path))
	}
	if path == "" {
		path = strings.ToLower(strings.TrimSpace(c.FullPath()))
	}
	path = strings.TrimRight(path, "/")
	if c.Request.Method == http.MethodGet && isFingerprintObservationResponsesRoot(path) {
		return true // OAuth Responses WebSocket handshake.
	}
	if c.Request.Method != http.MethodPost {
		return false
	}
	if path == "/v1/messages" || path == "/openai/v1/messages" ||
		path == "/v1/chat/completions" || path == "/openai/v1/chat/completions" || path == "/chat/completions" {
		return true
	}
	if isFingerprintObservationResponsesRoot(path) || isFingerprintObservationCompactPath(path) {
		return true
	}
	return false
}

func isFingerprintObservationResponsesRoot(path string) bool {
	switch path {
	case "/responses", "/v1/responses", "/openai/v1/responses", "/backend-api/codex/responses":
		return true
	default:
		return false
	}
}

func isFingerprintObservationCompactPath(path string) bool {
	for _, root := range [...]string{
		"/responses/compact",
		"/v1/responses/compact",
		"/openai/v1/responses/compact",
		"/backend-api/codex/responses/compact",
	} {
		if path == root || strings.HasPrefix(path, root+"/") {
			return true
		}
	}
	return false
}
