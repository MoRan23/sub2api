package service

import (
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
)

// installationObservationCapacity is the fixed ring-buffer size. Observation is
// a live diagnostic view, not durable storage, so a bounded in-memory window is
// sufficient and self-limiting.
const installationObservationCapacity = 500

// InstallationObservationEntry captures the outbound Codex identity emitted for
// a single OpenAI OAuth request. It is only produced while observation is on.
type InstallationObservationEntry struct {
	Timestamp                    time.Time `json:"timestamp"`
	AccountID                    int64     `json:"account_id"`
	AccountName                  string    `json:"account_name"`
	Pinned                       bool      `json:"pinned"`
	Rotated                      bool      `json:"rotated"`
	ClientReportedInstallationID string    `json:"client_reported_installation_id"`
	OutboundInstallationID       string    `json:"outbound_installation_id"`
	UserAgent                    string    `json:"user_agent"`
	Originator                   string    `json:"originator"`
	OpenAIBeta                   string    `json:"openai_beta"`
	Version                      string    `json:"version"`
	InboundEndpoint              string    `json:"inbound_endpoint"`
}

// installationObserver is a process-level ring buffer, gated by an atomic flag.
// When disabled, Record is a cheap no-op (single atomic load) so the normal
// request path pays nothing. Disabling clears the buffer so nothing lingers.
type installationObserver struct {
	enabled atomic.Bool

	mu   sync.Mutex
	ring []InstallationObservationEntry
	head int
	size int
}

var globalInstallationObserver = &installationObserver{
	ring: make([]InstallationObservationEntry, installationObservationCapacity),
}

// SetInstallationObservationEnabled publishes the observation toggle. Turning it
// off clears the buffer immediately (observation retains data only while on).
func SetInstallationObservationEnabled(enabled bool) {
	globalInstallationObserver.setEnabled(enabled)
}

// IsInstallationObservationEnabled reports the current observation state.
func IsInstallationObservationEnabled() bool {
	return globalInstallationObserver.enabled.Load()
}

// SnapshotInstallationObservations returns the most recent entries (newest
// first), capped at limit (<=0 or oversized means "all buffered").
func SnapshotInstallationObservations(limit int) []InstallationObservationEntry {
	return globalInstallationObserver.snapshot(limit)
}

func (o *installationObserver) setEnabled(enabled bool) {
	o.enabled.Store(enabled)
	if !enabled {
		o.mu.Lock()
		o.head = 0
		o.size = 0
		o.mu.Unlock()
	}
}

func (o *installationObserver) record(entry InstallationObservationEntry) {
	if !o.enabled.Load() {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	// Re-check under lock: a concurrent disable may have raced the atomic load,
	// and we must not repopulate a buffer that was just cleared.
	if !o.enabled.Load() {
		return
	}
	o.ring[o.head] = entry
	o.head = (o.head + 1) % len(o.ring)
	if o.size < len(o.ring) {
		o.size++
	}
}

func (o *installationObserver) snapshot(limit int) []InstallationObservationEntry {
	o.mu.Lock()
	defer o.mu.Unlock()
	n := o.size
	if limit > 0 && limit < n {
		n = limit
	}
	out := make([]InstallationObservationEntry, 0, n)
	// Walk backwards from the most recently written slot.
	for i := 0; i < n; i++ {
		idx := (o.head - 1 - i + len(o.ring)) % len(o.ring)
		out = append(out, o.ring[idx])
	}
	return out
}

// recordInstallationObservation writes a per-request entry to the ring buffer
// when observation is on. outbound is the finalized upstream header set (its
// UA / originator / OpenAI-Beta / version reflect what actually goes upstream);
// c supplies the inbound endpoint. No-op for non-OAuth accounts and when
// observation is off (checked first for near-zero overhead).
func (s *OpenAIGatewayService) recordInstallationObservation(c *gin.Context, account *Account, pin installationIDResolution, outbound http.Header) {
	if !globalInstallationObserver.enabled.Load() || account == nil {
		return
	}
	entry := InstallationObservationEntry{
		Timestamp:                    time.Now(),
		AccountID:                    account.ID,
		AccountName:                  account.Name,
		Pinned:                       pin.Enabled,
		Rotated:                      pin.Rotated,
		ClientReportedInstallationID: pin.ClientID,
		OutboundInstallationID:       pin.OutboundID,
	}
	if !pin.Enabled {
		// Passthrough mode: the outbound value is whatever the client reported.
		entry.OutboundInstallationID = pin.ClientID
	}
	if outbound != nil {
		entry.UserAgent = strings.TrimSpace(outbound.Get("user-agent"))
		entry.Originator = strings.TrimSpace(outbound.Get("originator"))
		entry.OpenAIBeta = strings.TrimSpace(outbound.Get("openai-beta"))
		entry.Version = strings.TrimSpace(outbound.Get("version"))
	}
	if c != nil && c.Request != nil {
		entry.InboundEndpoint = c.Request.Method + " " + c.FullPath()
	}
	globalInstallationObserver.record(entry)
}
