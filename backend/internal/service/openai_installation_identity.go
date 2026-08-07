package service

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// codexInstallationIDKey is the header / client_metadata key that carries the
// Codex installation identifier on both the inbound (client) and outbound
// (upstream) sides.
const codexInstallationIDKey = "x-codex-installation-id"

// installationPinContextKey caches the per-request installation resolution in
// the gin context so the body-transform stage and the header stage emit the
// exact same value (essential in rotate mode where each call would otherwise
// mint a different UUID).
const installationPinContextKey = "openai_resolved_installation_id"

// installationPinRegistry is the process-level authoritative store of every
// account's pinned installation_id.
//
// Account structs reach the forward path through a scheduler snapshot cache,
// and pin writes are deliberately scheduler-neutral (they do not bust that
// cache — see schedulerNeutralExtraKeyPrefixes). The registry therefore shields
// the pinned value from snapshot staleness: once an account seizes a value the
// registry serves it consistently for the process lifetime, while the DB copy
// exists only for restart durability.
type installationPinRegistry struct {
	mu     sync.RWMutex
	values map[int64]string
}

var globalInstallationPinRegistry = &installationPinRegistry{values: make(map[int64]string)}

func (r *installationPinRegistry) get(id int64) (string, bool) {
	if r == nil || id <= 0 {
		return "", false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	v, ok := r.values[id]
	return v, ok && v != ""
}

func (r *installationPinRegistry) set(id int64, value string) {
	if r == nil || id <= 0 || strings.TrimSpace(value) == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.values[id] = value
}

func (r *installationPinRegistry) clear(id int64) {
	if r == nil || id <= 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.values, id)
}

// ClearPinnedInstallationIDFromRegistry drops an account's runtime pinned value
// so the next request re-seizes one. Called when an admin clears the persisted
// pin to force recapture.
func ClearPinnedInstallationIDFromRegistry(accountID int64) {
	globalInstallationPinRegistry.clear(accountID)
}

// installationIDResolution is the resolved outbound installation identity for a
// single request.
type installationIDResolution struct {
	// Enabled reports whether pinning is active for this account. When false the
	// caller must fall back to the legacy passthrough behavior.
	Enabled bool
	// Rotated reports that OutboundID was freshly regenerated this request.
	Rotated bool
	// OutboundID is the value to emit upstream (empty when !Enabled).
	OutboundID string
	// ClientID is what the inbound client reported (header/body), kept for the
	// observation panel so operators can see the value being suppressed.
	ClientID string
	// NeedsPersist reports that OutboundID should be written back to the account
	// for restart durability.
	NeedsPersist bool
}

// resolveOutboundInstallationID computes the outbound installation_id for an
// account given whatever the inbound client reported.
//
// Precedence when pinning is on and rotation is off:
//  1. runtime registry (authoritative, snapshot-proof);
//  2. persisted account value (seeds the registry after a restart);
//  3. the first request's client-reported value, else a generated UUIDv4.
//
// The account's real openai_device_id is intentionally NOT consulted: the pin
// is defined as "the value from this account's first request" so shared upstream
// accounts converge on one installation_id regardless of imported device data.
func resolveOutboundInstallationID(account *Account, clientReportedID string) installationIDResolution {
	res := installationIDResolution{ClientID: strings.TrimSpace(clientReportedID)}
	if account == nil || !account.IsOpenAIInstallationPinEnabled() {
		return res
	}
	res.Enabled = true

	if account.IsOpenAIInstallationRotateEnabled() {
		// Rotation mints a fresh value every request. Intra-request body/header
		// parity comes from the gin-context cache (installationPinContextKey), so
		// rotation deliberately does NOT touch the registry or DB: the stable
		// seized value survives untouched, and turning rotation back off cleanly
		// resumes it instead of freezing on the last random UUID.
		res.OutboundID = uuid.NewString()
		res.Rotated = true
		return res
	}

	if v, ok := globalInstallationPinRegistry.get(account.ID); ok {
		res.OutboundID = v
		// Reconcile the DB copy if it drifted or was never written.
		res.NeedsPersist = account.GetPinnedOpenAIInstallationID() != v
		return res
	}
	if persisted := account.GetPinnedOpenAIInstallationID(); persisted != "" {
		res.OutboundID = persisted
		globalInstallationPinRegistry.set(account.ID, persisted)
		return res
	}

	// First request for this account: seize the client-reported value, or mint a
	// UUIDv4 when none was usable. This mirrors Codex's resolve_installation_id
	// (core/src/installation_id.rs): a stored value that parses as a UUID is
	// returned in canonical lowercase form, and anything that does not parse is
	// discarded in favor of a fresh Uuid::new_v4(). Normalizing here keeps the
	// pinned identity indistinguishable from a real Codex client's own file.
	seized := normalizeCodexInstallationID(res.ClientID)
	if seized == "" {
		seized = uuid.NewString()
	}
	res.OutboundID = seized
	res.NeedsPersist = true
	globalInstallationPinRegistry.set(account.ID, seized)
	return res
}

// normalizeCodexInstallationID mirrors the reuse/normalize half of Codex's
// resolve_installation_id: a value that parses as a UUID is returned in its
// canonical lowercase hyphenated form (Rust does Uuid::parse_str(..).to_string()),
// and anything that does not parse yields "" so the caller mints a fresh v4
// (Codex's behavior for invalid installation_id file contents).
func normalizeCodexInstallationID(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	parsed, err := uuid.Parse(raw)
	if err != nil {
		return ""
	}
	return parsed.String()
}

// resolveInstallationIDForRequest resolves the outbound installation identity
// once per request and caches it in the gin context, so every stage (body and
// header, HTTP and WS) emits the same value. It also persists newly seized
// values best-effort for restart durability.
func (s *OpenAIGatewayService) resolveInstallationIDForRequest(ctx context.Context, c *gin.Context, account *Account, clientReportedID string) installationIDResolution {
	if c != nil {
		if cached, ok := c.Get(installationPinContextKey); ok {
			if res, ok := cached.(installationIDResolution); ok {
				return res
			}
		}
	}
	res := resolveOutboundInstallationID(account, clientReportedID)
	if c != nil {
		c.Set(installationPinContextKey, res)
	}
	if res.Enabled && res.NeedsPersist && account != nil {
		s.persistPinnedInstallationID(ctx, account, res.OutboundID)
	}
	return res
}

// persistPinnedInstallationID writes the pinned value to accounts.extra without
// blocking the request. The write is scheduler-neutral so it does not trigger a
// scheduler bucket rebuild.
func (s *OpenAIGatewayService) persistPinnedInstallationID(ctx context.Context, account *Account, id string) {
	if s == nil || s.accountRepo == nil || account == nil || account.ID <= 0 {
		return
	}
	if strings.TrimSpace(id) == "" {
		return
	}
	accountID := account.ID
	go func() {
		defer func() {
			if r := recover(); r != nil {
				logger.LegacyPrintf("service.openai_gateway", "[Installation] persist pinned id panic account_id=%d recover=%v", accountID, r)
			}
		}()
		bg, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if err := s.accountRepo.UpdateExtra(bg, accountID, map[string]any{openAIPinnedInstallationIDKey: id}); err != nil {
			logger.LegacyPrintf("service.openai_gateway", "[Installation] persist pinned id failed account_id=%d err=%v", accountID, err)
		}
	}()
}

// extractClientInstallationID reads the installation_id the inbound client
// reported, preferring the header and falling back to body client_metadata.
func extractClientInstallationID(c *gin.Context, reqBody map[string]any) string {
	if c != nil && c.Request != nil {
		if v := strings.TrimSpace(c.Request.Header.Get(codexInstallationIDKey)); v != "" {
			return v
		}
	}
	if reqBody != nil {
		if cm, ok := reqBody["client_metadata"].(map[string]any); ok {
			if v, ok := cm[codexInstallationIDKey].(string); ok && strings.TrimSpace(v) != "" {
				return strings.TrimSpace(v)
			}
		}
	}
	return ""
}

// enforceCodexInstallationIDInBody force-sets client_metadata[installation] to
// the account-owned value, overwriting whatever the client sent. Returns true
// when the body was mutated. Unlike applyCodexClientMetadata (which is additive
// and never overrides), this is authoritative and used only when pinning is on.
func enforceCodexInstallationIDInBody(reqBody map[string]any, id string) bool {
	id = strings.TrimSpace(id)
	if reqBody == nil || id == "" {
		return false
	}
	switch existing := reqBody["client_metadata"].(type) {
	case map[string]any:
		if cur, ok := existing[codexInstallationIDKey].(string); ok && cur == id {
			return false
		}
		existing[codexInstallationIDKey] = id
		reqBody["client_metadata"] = existing
		return true
	case map[string]string:
		next := make(map[string]any, len(existing)+1)
		for k, v := range existing {
			next[k] = v
		}
		next[codexInstallationIDKey] = id
		reqBody["client_metadata"] = next
		return true
	default:
		reqBody["client_metadata"] = map[string]any{codexInstallationIDKey: id}
		return true
	}
}
