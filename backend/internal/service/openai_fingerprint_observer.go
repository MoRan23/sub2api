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
// for one request while fingerprint observation is enabled. Every hierarchical
// field is normalized independently so malformed, legacy, and unprojected
// values stay empty rather than being presented as a server-owned UUIDv7.
type FingerprintObservationEntry struct {
	SequenceID                   uint64    `json:"sequence_id"`
	Timestamp                    time.Time `json:"timestamp"`
	UserID                       int64     `json:"user_id"`
	Username                     string    `json:"username"`
	Email                        string    `json:"email"`
	APIKeyID                     int64     `json:"api_key_id"`
	APIKeyName                   string    `json:"api_key_name"`
	AccountID                    int64     `json:"account_id"`
	AccountName                  string    `json:"account_name"`
	Pinned                       bool      `json:"pinned"`
	ClientReportedInstallationID string    `json:"client_reported_installation_id"`
	OutboundInstallationID       string    `json:"outbound_installation_id"`
	SessionID                    string    `json:"session_id"`
	ThreadID                     string    `json:"thread_id"`
	ParentThreadID               string    `json:"parent_thread_id"`
	ForkedFromThreadID           string    `json:"forked_from_thread_id"`
	UserAgent                    string    `json:"user_agent"`
	Originator                   string    `json:"originator"`
	OpenAIBeta                   string    `json:"openai_beta"`
	Version                      string    `json:"version"`
	InboundEndpoint              string    `json:"inbound_endpoint"`
}

// FingerprintObservationThreadNode groups final wire observations for one
// UUIDv7 thread. Observations and child threads are ordered newest first.
type FingerprintObservationThreadNode struct {
	ThreadID           string                        `json:"thread_id"`
	ParentThreadID     string                        `json:"parent_thread_id"`
	ForkedFromThreadID string                        `json:"forked_from_thread_id"`
	Relation           OpenAICodexTurnRelation       `json:"relation"`
	FirstObservedAt    time.Time                     `json:"first_observed_at"`
	LastObservedAt     time.Time                     `json:"last_observed_at"`
	ObservationCount   int                           `json:"observation_count"`
	Observations       []FingerprintObservationEntry `json:"observations"`
}

// FingerprintObservationSessionNode is the pagination unit exposed by the
// admin fingerprint view. Actor fields are snapshots from the newest record in
// the group; the grouping key itself uses only immutable numeric IDs plus the
// outbound session UUID.
type FingerprintObservationSessionNode struct {
	UserID                 int64                               `json:"user_id"`
	Username               string                              `json:"username"`
	Email                  string                              `json:"email"`
	APIKeyID               int64                               `json:"api_key_id"`
	APIKeyName             string                              `json:"api_key_name"`
	SessionID              string                              `json:"session_id"`
	FirstObservedAt        time.Time                           `json:"first_observed_at"`
	LastObservedAt         time.Time                           `json:"last_observed_at"`
	ObservationCount       int                                 `json:"observation_count"`
	RootThread             *FingerprintObservationThreadNode   `json:"root_thread"`
	ChildThreads           []*FingerprintObservationThreadNode `json:"child_threads"`
	UnthreadedObservations []FingerprintObservationEntry       `json:"unthreaded_observations"`
}

// FingerprintObservationPage is a stable, sequence-bounded session snapshot.
// SnapshotSeq is returned by page one and can be supplied for later pages so
// newly arriving records cannot move existing groups between pages.
type FingerprintObservationPage struct {
	Items       []FingerprintObservationSessionNode `json:"items"`
	Total       int                                 `json:"total"`
	Page        int                                 `json:"page"`
	PageSize    int                                 `json:"page_size"`
	Pages       int                                 `json:"pages"`
	SnapshotSeq uint64                              `json:"snapshot_seq"`
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
	seq  uint64
}

var globalFingerprintObserver = &fingerprintObserver{
	ring: make([]FingerprintObservationEntry, fingerprintObservationCapacity),
}

// SetFingerprintObservationEnabled publishes the observation toggle. Disabling
// observation synchronously clears and scrubs the ring buffer.
func SetFingerprintObservationEnabled(enabled bool) {
	o := globalFingerprintObserver
	if o == nil {
		if !enabled {
			globalFingerprintObservationSnapshotStore.clear()
		}
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.setEnabledLocked(enabled)
	if !enabled {
		// Keep the observer lock held while clearing snapshots. Snapshot creation
		// takes the locks in the same order, so a concurrent creator cannot copy
		// old ring entries and install them after this scrub has completed.
		globalFingerprintObservationSnapshotStore.clear()
	}
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

// SnapshotFingerprintObservationSessions returns a page of root sessions.
// Invalid pagination values are normalized here as well as in the HTTP
// handler so internal callers receive the same bounded behavior.
func SnapshotFingerprintObservationSessions(page, pageSize int, snapshotSeq uint64) FingerprintObservationPage {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 20
	} else if pageSize > 100 {
		pageSize = 100
	}

	entries, highWater := globalFingerprintObserver.snapshotThrough(snapshotSeq)
	sessions := aggregateFingerprintObservationSessions(entries)
	total := len(sessions)
	pages := (total + pageSize - 1) / pageSize
	if pages < 1 {
		pages = 1
	}
	start := total
	if page <= pages {
		start = (page - 1) * pageSize
	}
	end := start + pageSize
	if end > total {
		end = total
	}
	items := make([]FingerprintObservationSessionNode, end-start)
	copy(items, sessions[start:end])
	return FingerprintObservationPage{
		Items:       items,
		Total:       total,
		Page:        page,
		PageSize:    pageSize,
		Pages:       pages,
		SnapshotSeq: highWater,
	}
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
	o.setEnabledLocked(enabled)
}

func (o *fingerprintObserver) setEnabledLocked(enabled bool) {
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
	o.seq++
	entry.SequenceID = o.seq
	o.ring[o.head] = entry
	o.head = (o.head + 1) % len(o.ring)
	if o.size < len(o.ring) {
		o.size++
	}
}

func (o *fingerprintObserver) snapshotThrough(snapshotSeq uint64) ([]FingerprintObservationEntry, uint64) {
	if o == nil {
		return []FingerprintObservationEntry{}, 0
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	highWater := snapshotSeq
	if highWater == 0 || highWater > o.seq {
		highWater = o.seq
	}
	if len(o.ring) == 0 || o.size == 0 {
		return []FingerprintObservationEntry{}, highWater
	}
	out := make([]FingerprintObservationEntry, 0, o.size)
	for i := 0; i < o.size; i++ {
		idx := (o.head - 1 - i + len(o.ring)) % len(o.ring)
		entry := o.ring[idx]
		if entry.SequenceID <= highWater {
			out = append(out, entry)
		}
	}
	return out, highWater
}

type fingerprintObservationSessionGroupKey struct {
	UserID    int64
	APIKeyID  int64
	SessionID string
	UniqueSeq uint64
}

type fingerprintObservationSessionAccumulator struct {
	node     FingerprintObservationSessionNode
	threads  map[string]*FingerprintObservationThreadNode
	root     *FingerprintObservationThreadNode
	children []*FingerprintObservationThreadNode
}

func aggregateFingerprintObservationSessions(entries []FingerprintObservationEntry) []FingerprintObservationSessionNode {
	if len(entries) == 0 {
		return []FingerprintObservationSessionNode{}
	}
	groups := make(map[fingerprintObservationSessionGroupKey]*fingerprintObservationSessionAccumulator, len(entries))
	ordered := make([]*fingerprintObservationSessionAccumulator, 0, len(entries))
	for _, entry := range entries {
		key := fingerprintObservationSessionGroupKey{
			UserID:    entry.UserID,
			APIKeyID:  entry.APIKeyID,
			SessionID: entry.SessionID,
		}
		if entry.SessionID == "" {
			// Legacy/disabled identity rows must remain distinct. Coalescing all
			// empty IDs would create a synthetic shared session that never existed.
			key.UniqueSeq = entry.SequenceID
		}
		group := groups[key]
		if group == nil {
			group = &fingerprintObservationSessionAccumulator{
				node: FingerprintObservationSessionNode{
					UserID:                 entry.UserID,
					Username:               entry.Username,
					Email:                  entry.Email,
					APIKeyID:               entry.APIKeyID,
					APIKeyName:             entry.APIKeyName,
					SessionID:              entry.SessionID,
					FirstObservedAt:        entry.Timestamp,
					LastObservedAt:         entry.Timestamp,
					ChildThreads:           []*FingerprintObservationThreadNode{},
					UnthreadedObservations: []FingerprintObservationEntry{},
				},
				threads: make(map[string]*FingerprintObservationThreadNode),
			}
			groups[key] = group
			ordered = append(ordered, group)
		}
		group.node.ObservationCount++
		if entry.Timestamp.Before(group.node.FirstObservedAt) {
			group.node.FirstObservedAt = entry.Timestamp
		}
		if entry.Timestamp.After(group.node.LastObservedAt) {
			group.node.LastObservedAt = entry.Timestamp
		}
		if entry.ThreadID == "" {
			group.node.UnthreadedObservations = append(group.node.UnthreadedObservations, entry)
			continue
		}
		thread := group.threads[entry.ThreadID]
		if thread == nil {
			relation := OpenAICodexTurnRelationDescendant
			if entry.SessionID != "" && entry.ThreadID == entry.SessionID {
				relation = OpenAICodexTurnRelationRoot
			}
			thread = &FingerprintObservationThreadNode{
				ThreadID:           entry.ThreadID,
				ParentThreadID:     entry.ParentThreadID,
				ForkedFromThreadID: entry.ForkedFromThreadID,
				Relation:           relation,
				FirstObservedAt:    entry.Timestamp,
				LastObservedAt:     entry.Timestamp,
				Observations:       []FingerprintObservationEntry{},
			}
			group.threads[entry.ThreadID] = thread
			if relation == OpenAICodexTurnRelationRoot {
				group.root = thread
			} else {
				group.children = append(group.children, thread)
			}
		}
		thread.ObservationCount++
		thread.Observations = append(thread.Observations, entry)
		if thread.ParentThreadID == "" {
			thread.ParentThreadID = entry.ParentThreadID
		}
		if thread.ForkedFromThreadID == "" {
			thread.ForkedFromThreadID = entry.ForkedFromThreadID
		}
		if entry.Timestamp.Before(thread.FirstObservedAt) {
			thread.FirstObservedAt = entry.Timestamp
		}
		if entry.Timestamp.After(thread.LastObservedAt) {
			thread.LastObservedAt = entry.Timestamp
		}
	}

	out := make([]FingerprintObservationSessionNode, 0, len(ordered))
	for _, group := range ordered {
		group.node.RootThread = group.root
		group.node.ChildThreads = group.children
		out = append(out, group.node)
	}
	return out
}

func (o *fingerprintObserver) snapshot(limit int) []FingerprintObservationEntry {
	if o == nil {
		return []FingerprintObservationEntry{}
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.snapshotLocked(limit)
}

func (o *fingerprintObserver) snapshotLocked(limit int) []FingerprintObservationEntry {
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

// setFingerprintObservationOutboundIdentity marks the hierarchical UUIDs owned
// by the final server-side writer for this request. The observer uses them only as
// provenance: values are still read back from the finalized wire headers/body.
// This prevents a client-supplied UUIDv7 from being retained while legacy mode
// is active or when no server-owned identity was created.
func setFingerprintObservationOutboundIdentity(c *gin.Context, identity OpenAICodexTurnIdentity) {
	if c == nil {
		return
	}
	if ValidateOpenAICodexTurnIdentity(identity) != nil {
		// An invalid replacement must not leave trusted identity from an earlier
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
// identity for client_metadata observation fallback.
func clearFingerprintObservationOutboundIdentity(c *gin.Context) {
	if c != nil {
		c.Set(fingerprintObservationOutboundIdentityContextKey, nil)
	}
}

func fingerprintObservationOutboundIdentityFromContext(c *gin.Context) (OpenAICodexTurnIdentity, bool) {
	if c == nil {
		return OpenAICodexTurnIdentity{}, false
	}
	raw, ok := c.Get(fingerprintObservationOutboundIdentityContextKey)
	if !ok {
		return OpenAICodexTurnIdentity{}, false
	}
	identity, ok := raw.(OpenAICodexTurnIdentity)
	if !ok || ValidateOpenAICodexTurnIdentity(identity) != nil {
		return OpenAICodexTurnIdentity{}, false
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
// must invoke it only at the last header-writing boundary for a physical
// OpenAI OAuth send.
func (s *OpenAIGatewayService) recordFingerprintObservation(c *gin.Context, account *Account, pin installationIDResolution, outbound http.Header) {
	s.recordFingerprintObservationWithBody(c, account, pin, outbound, nil)
}

// recordFingerprintObservationWithBody is used by the compatibility bridges,
// where the final Responses body is available alongside the finalized headers.
// Headers remain authoritative; client_metadata is only a fallback for a
// schema path that carries server-owned identity in the body but not aliases
// in the wire header set.
func (s *OpenAIGatewayService) recordFingerprintObservationWithBody(c *gin.Context, account *Account, pin installationIDResolution, outbound http.Header, body []byte) {
	if !globalFingerprintObserver.enabled.Load() || account == nil || !account.IsOpenAIOAuth() {
		return
	}
	trustedIdentity, hasTrustedIdentity := fingerprintObservationOutboundIdentityFromContext(c)
	actor := fingerprintObservationActorFromContext(c)
	entry := FingerprintObservationEntry{
		Timestamp:                    time.Now(),
		UserID:                       actor.UserID,
		Username:                     actor.Username,
		Email:                        actor.Email,
		APIKeyID:                     actor.APIKeyID,
		APIKeyName:                   actor.APIKeyName,
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
	parentHeaderPresent := false
	forkHeaderPresent := false
	if outbound != nil {
		if actual := strings.TrimSpace(outbound.Get(codexInstallationIDKey)); actual != "" {
			entry.OutboundInstallationID = actual
		}
		if hasTrustedIdentity {
			entry.SessionID, sessionHeaderPresent = fingerprintObservationHeaderUUID(outbound, trustedIdentity.SessionID,
				"session-id", "session_id")
			entry.ThreadID, threadHeaderPresent = fingerprintObservationHeaderUUID(outbound, trustedIdentity.ThreadID,
				"thread-id", "thread_id", "conversation_id", "conversation-id", "x-client-request-id")
			entry.ParentThreadID, parentHeaderPresent = fingerprintObservationHeaderUUID(outbound, trustedIdentity.ParentThreadID,
				"x-codex-parent-thread-id", "parent-thread-id", "parent_thread_id")
			entry.ForkedFromThreadID, forkHeaderPresent = fingerprintObservationHeaderUUID(outbound, trustedIdentity.ForkedFromThreadID,
				"x-codex-forked-from-thread-id", "forked-from-thread-id", "forked_from_thread_id")
			if !sessionHeaderPresent {
				entry.SessionID, sessionHeaderPresent = fingerprintObservationTurnMetadataHeaderUUID(outbound, trustedIdentity.SessionID, "session_id")
			}
			if !threadHeaderPresent {
				entry.ThreadID, threadHeaderPresent = fingerprintObservationTurnMetadataHeaderUUID(outbound, trustedIdentity.ThreadID, "thread_id")
			}
			if !parentHeaderPresent {
				entry.ParentThreadID, parentHeaderPresent = fingerprintObservationTurnMetadataHeaderUUID(outbound, trustedIdentity.ParentThreadID, "parent_thread_id")
			}
			if !forkHeaderPresent {
				entry.ForkedFromThreadID, forkHeaderPresent = fingerprintObservationTurnMetadataHeaderUUID(outbound, trustedIdentity.ForkedFromThreadID, "forked_from_thread_id")
			}
		}
		entry.UserAgent = strings.TrimSpace(outbound.Get("user-agent"))
		entry.Originator = strings.TrimSpace(outbound.Get("originator"))
		entry.OpenAIBeta = strings.TrimSpace(outbound.Get("openai-beta"))
		entry.Version = strings.TrimSpace(outbound.Get("version"))
	}
	if plan, ok := OpenAIOAuthIdentityPlanFromContext(c); ok &&
		plan.ProjectionMode == OpenAIOAuthIdentityProjectionAlphaSearch {
		// Native alpha/search strips every standalone installation header. Its
		// final turn-metadata header is authoritative even when account pinning is
		// disabled and the legacy observer fallback selected the client value.
		entry.OutboundInstallationID = fingerprintObservationTurnMetadataHeaderInstallationID(outbound)
	}
	if hasTrustedIdentity && len(body) > 0 {
		bodyIdentity := parseFingerprintObservationBodyIdentity(body)
		if entry.SessionID == "" && !sessionHeaderPresent {
			entry.SessionID, _ = bodyIdentity.uuid(trustedIdentity.SessionID, "session_id", "session_id")
		}
		if entry.ThreadID == "" && !threadHeaderPresent {
			entry.ThreadID, _ = bodyIdentity.uuid(trustedIdentity.ThreadID, "thread_id", "thread_id")
		}
		if entry.ParentThreadID == "" && !parentHeaderPresent {
			entry.ParentThreadID, _ = bodyIdentity.uuid(trustedIdentity.ParentThreadID, "parent_thread_id",
				"x-codex-parent-thread-id", "parent_thread_id")
		}
		if entry.ForkedFromThreadID == "" && !forkHeaderPresent {
			entry.ForkedFromThreadID, _ = bodyIdentity.uuid(trustedIdentity.ForkedFromThreadID, "forked_from_thread_id",
				"forked_from_thread_id")
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

type fingerprintObservationActor struct {
	UserID     int64
	Username   string
	Email      string
	APIKeyID   int64
	APIKeyName string
}

func fingerprintObservationActorFromContext(c *gin.Context) fingerprintObservationActor {
	if c == nil {
		return fingerprintObservationActor{}
	}
	apiKey := getAPIKeyFromContext(c)
	if apiKey == nil {
		return fingerprintObservationActor{}
	}
	actor := fingerprintObservationActor{
		UserID:     apiKey.UserID,
		APIKeyID:   apiKey.ID,
		APIKeyName: apiKey.Name,
	}
	if apiKey.User != nil {
		if actor.UserID == 0 {
			actor.UserID = apiKey.User.ID
		}
		actor.Username = apiKey.User.Username
		actor.Email = apiKey.User.Email
	}
	return actor
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

func fingerprintObservationTurnMetadataHeaderInstallationID(headers http.Header) string {
	if headers == nil {
		return ""
	}
	resolved := ""
	for key, values := range headers {
		if !strings.EqualFold(key, openAIWSTurnMetadataHeader) {
			continue
		}
		for _, value := range values {
			var metadata map[string]any
			if json.Unmarshal([]byte(value), &metadata) != nil || metadata == nil {
				continue
			}
			raw, exists := metadata[codexTurnMetadataInstallationIDKey]
			if !exists {
				continue
			}
			installationID, ok := raw.(string)
			installationID = strings.TrimSpace(installationID)
			if !ok || installationID == "" {
				continue
			}
			if resolved != "" && resolved != installationID {
				return ""
			}
			resolved = installationID
		}
	}
	return resolved
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
	// Preserve-client projections deliberately do not invoke the account pin
	// resolver. The immutable plan still carries the captured client value, so
	// observations can report body-only/nested installation IDs without
	// populating the resolver cache or changing later pin decisions.
	if plan, ok := OpenAIOAuthIdentityPlanFromContext(c); ok &&
		plan.InstallationPolicy == OpenAIOAuthInstallationPreserve {
		return installationIDResolution{ClientID: plan.Capture.ClientInstallationID}
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

func fingerprintObservationTurnMetadataHeaderUUID(headers http.Header, expected, field string) (string, bool) {
	expected = NormalizeFingerprintObservationUUIDv7(expected)
	if headers == nil || expected == "" {
		return "", false
	}
	fieldPresent := false
	for key, values := range headers {
		if !strings.EqualFold(key, "x-codex-turn-metadata") {
			continue
		}
		for _, value := range values {
			if strings.TrimSpace(value) == "" {
				continue
			}
			var metadata map[string]any
			if json.Unmarshal([]byte(value), &metadata) != nil || metadata == nil {
				return "", true
			}
			raw, exists := metadata[field]
			if !exists {
				continue
			}
			fieldPresent = true
			value, ok := raw.(string)
			if !ok || NormalizeFingerprintObservationUUIDv7(value) != expected {
				return "", true
			}
		}
	}
	if fieldPresent {
		return expected, true
	}
	return "", false
}

type fingerprintObservationBodyIdentity struct {
	metadata            map[string]any
	turnMetadata        map[string]any
	turnMetadataPresent bool
	turnMetadataInvalid bool
}

func parseFingerprintObservationBodyIdentity(body []byte) fingerprintObservationBodyIdentity {
	var root map[string]any
	if len(body) == 0 || json.Unmarshal(body, &root) != nil || root == nil {
		return fingerprintObservationBodyIdentity{}
	}
	metadata, ok := root["client_metadata"].(map[string]any)
	if !ok || metadata == nil {
		return fingerprintObservationBodyIdentity{}
	}
	parsed := fingerprintObservationBodyIdentity{metadata: metadata}
	rawTurnMetadata, exists := metadata["x-codex-turn-metadata"]
	if !exists {
		return parsed
	}
	parsed.turnMetadataPresent = true
	switch value := rawTurnMetadata.(type) {
	case string:
		if strings.TrimSpace(value) == "" || json.Unmarshal([]byte(value), &parsed.turnMetadata) != nil || parsed.turnMetadata == nil {
			parsed.turnMetadataInvalid = true
		}
	case map[string]any:
		parsed.turnMetadata = value
	default:
		parsed.turnMetadataInvalid = true
	}
	return parsed
}

func (b fingerprintObservationBodyIdentity) uuid(expected, turnField string, flatFields ...string) (string, bool) {
	expected = NormalizeFingerprintObservationUUIDv7(expected)
	if expected == "" || b.metadata == nil {
		return "", false
	}
	if b.turnMetadataPresent {
		if b.turnMetadataInvalid {
			return "", true
		}
		if raw, exists := b.turnMetadata[turnField]; exists {
			value, ok := raw.(string)
			if !ok || NormalizeFingerprintObservationUUIDv7(value) != expected {
				return "", true
			}
			return expected, true
		}
	}
	present := false
	for _, field := range flatFields {
		raw, exists := b.metadata[field]
		if !exists {
			continue
		}
		value, ok := raw.(string)
		if ok && strings.TrimSpace(value) == "" {
			continue
		}
		present = true
		if !ok || NormalizeFingerprintObservationUUIDv7(value) != expected {
			return "", true
		}
	}
	if present {
		return expected, true
	}
	return "", false
}

// shouldRecordFingerprintObservationRequest deliberately keeps the observer
// narrow: only turn-carrying Codex OAuth transports participate. Images,
// embeddings, Live/profile probes, and unrelated gateway transports remain
// outside the observation ring.
func shouldRecordFingerprintObservationRequest(c *gin.Context, account *Account) bool {
	if c == nil || c.Request == nil || account == nil || !account.IsOpenAIOAuth() {
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
	if isFingerprintObservationAlphaSearchPath(path) {
		return true
	}
	if isFingerprintObservationResponsesRoot(path) || isFingerprintObservationCompactPath(path) {
		return true
	}
	return false
}

func isFingerprintObservationAlphaSearchPath(path string) bool {
	switch path {
	case "/alpha/search", "/v1/alpha/search", "/openai/v1/alpha/search", "/backend-api/codex/alpha/search":
		return true
	default:
		return false
	}
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
