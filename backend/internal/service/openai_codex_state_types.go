package service

import (
	"context"
	"net/http"
	"sync"
	"time"
)

const (
	CodexTurnStateLifetime        = 240 * time.Second
	CodexTurnStateRefreshAhead    = 30 * time.Second
	CodexTurnStateActiveWindow    = 30 * time.Minute
	CodexTurnStateScanInterval    = 30 * time.Second
	CodexTurnStateDueInterval     = time.Second
	CodexTurnStateCollectTimeout  = 12 * time.Second
	CodexTurnStateRetryInterval   = 5 * time.Second
	CodexTurnStateCollectInterval = 30 * time.Second
)

// CodexTurnStateKey always refers to the actual credential owner and final wire model.
type CodexTurnStateKey struct {
	OwnerAccountID int64
	OSFamily       string // Compatibility field; runtime keys always leave this empty.
	Model          string
	Generation     string
	// Set only on cancellation notifications, never on repository lookup keys.
	CollectorAttemptID string `json:",omitempty"`
}

// CodexTurnStateRecord contains encrypted state only. Empty timestamps mean unset.
type CodexTurnStateRecord struct {
	// A transient publication fence, checked transactionally against settings.
	// It is never persisted as part of a token record or exposed by JSON APIs.
	ModelPolicyRevision     string `json:"-"`
	BusinessInFlight        bool   `json:"-"`
	OwnerAccountID          int64
	OSFamily                string
	Model                   string
	Generation              string
	Version                 int64
	EncryptedToken          string
	EncryptedCookieBundle   string `json:"-"`
	AuthorizationGeneration string `json:"-"`
	CookieBundleExpiresAt   *time.Time
	IssuedAt                time.Time
	ExpiresAt               time.Time
	TokenLength             int
	CipherBlocks            int
	Source                  string
	Shape                   string
	RefreshReason           string
	DemandReason            string
	DemandAt                time.Time
	HistoryProofObservedAt  time.Time
	CollectionStatus        string
	CollectionReason        string
	LastBusinessAt          time.Time
	LastCollectedAt         time.Time
	NextCollectAt           time.Time
	CollectorPaused         bool
	LastError               string
	CollectorProxyID        int64
	CollectorExtendedCount  int
	LastCollectorProxyID    int64
	CollectorAttemptID      string `json:"-"`
}

func (r CodexTurnStateRecord) Key() CodexTurnStateKey {
	return CodexTurnStateKey{OwnerAccountID: r.OwnerAccountID, Model: r.Model, Generation: r.Generation}
}

// Cache identity is private request-local state, independent of scheduling CAS
// versions. It is never persisted, exported, or used as historical evidence.
type codexTurnStateCacheIdentity struct {
	encryptedToken          string
	issuedAt                time.Time
	expiresAt               time.Time
	shape                   string
	tokenLength             int
	cipherBlocks            int
	encryptedCookieBundle   string
	cookieBundleExpiresAt   time.Time
	authorizationGeneration string
}

func (r CodexTurnStateRecord) cacheIdentity() codexTurnStateCacheIdentity {
	var cookieExpiry time.Time
	if r.CookieBundleExpiresAt != nil {
		cookieExpiry = *r.CookieBundleExpiresAt
	}
	return codexTurnStateCacheIdentity{r.EncryptedToken, r.IssuedAt, r.ExpiresAt, r.Shape, r.TokenLength, r.CipherBlocks, r.EncryptedCookieBundle, cookieExpiry, r.AuthorizationGeneration}
}

func (i codexTurnStateCacheIdentity) matches(r *CodexTurnStateRecord) bool {
	if r == nil {
		return false
	}
	other := r.cacheIdentity()
	return i.encryptedToken == other.encryptedToken && i.issuedAt.Equal(other.issuedAt) &&
		i.expiresAt.Equal(other.expiresAt) && i.shape == other.shape && i.tokenLength == other.tokenLength && i.cipherBlocks == other.cipherBlocks &&
		i.encryptedCookieBundle == other.encryptedCookieBundle && i.cookieBundleExpiresAt.Equal(other.cookieBundleExpiresAt) &&
		i.authorizationGeneration == other.authorizationGeneration
}

// Implementations must guard writes against the live account generation and
// enabled flag. Business leases survive process loss only until leaseUntil.
// SaveCAS increments Version on success; stale generations/versions return false.
type CodexTurnStateRepository interface {
	BeginBusiness(context.Context, CodexTurnStateKey, string, time.Time, time.Time) (*CodexTurnStateRecord, error)
	MarkBusinessSent(context.Context, CodexTurnStateKey, time.Time) error
	EndBusiness(context.Context, CodexTurnStateKey, string) error
	Get(context.Context, CodexTurnStateKey) (*CodexTurnStateRecord, error)
	SaveCAS(context.Context, CodexTurnStateRecord, int64) (bool, error)
	ListActive(context.Context, time.Time, int) ([]CodexTurnStateRecord, error)
	ListByAccount(context.Context, int64) ([]CodexTurnStateRecord, error)
	ListByAccounts(context.Context, []int64) ([]CodexTurnStateRecord, error)
	HasBusiness(context.Context, CodexTurnStateKey, time.Time) (bool, error)
	AcquireCollector(context.Context, int64, string, time.Duration) (bool, error)
	ReleaseCollector(context.Context, int64, string) error
	PublishCancel(context.Context, CodexTurnStateKey) error
	SubscribeCancels(context.Context, func(CodexTurnStateKey)) error
}

// Cooldown is account-wide and must outlive every OS credential generation.
// Extending it is monotonic; successful business traffic never clears it.
type CodexTurnStateCooldownRepository interface {
	GetCollectorCooldowns(context.Context, []int64) (map[int64]time.Time, error)
	ExtendCollectorCooldown(context.Context, int64, time.Time) error
}

type CodexTurnStateSnapshot struct {
	Token                   string
	EncryptedCookieBundle   string `json:"-"`
	AuthorizationGeneration string `json:"-"`
	CookieBundleExpiresAt   *time.Time
	Version                 int64
	Source                  string
	TokenLength             int
	CipherBlocks            int
	ExpiresAt               time.Time
}

// Public identity and Snapshot fields are frozen by Prepare. Callers must not
// modify them. Response candidates are private and synchronized for WS readers.
type CodexTurnStateAttempt struct {
	OwnerAccountID          int64
	AuthorizationGeneration string
	OSFamily                string
	Model                   string
	Generation              string
	// Enabled=false is a passive fingerprint observation: no runtime lease,
	// cached snapshot, retained response candidates, publication or collection.
	Enabled              bool
	AccountEnabled       bool
	MaintenanceReason    string
	policyRevision       string
	validationReason     string
	Snapshot             CodexTurnStateSnapshot
	key                  CodexTurnStateKey
	id                   string
	accountType          string
	baseVersion          int64
	baseCacheIdentity    codexTurnStateCacheIdentity
	credentialEpoch      string
	businessSentAt       time.Time
	historyProof         *CodexTurnStateHistoryProof
	historyDelivered     bool
	historyPhysicalBound bool
	historyService       *CodexTurnStateService
	preparedAt           time.Time
	mu                   sync.Mutex
	candidates           []string
	// A delivered WS anomaly may precede its successful-write callback. Retain
	// only validated envelope metadata until that callback proves the send.
	pendingAnomaly     *CodexTurnStateShape
	anomalyPublication bool
	finished           bool
	wireObservation    *codexTurnStateWireObservation
	safeObservation    CodexTurnStateSafeObservation
	modelEvidence      codexModelEvidenceObserver
	// Cookie admission is independent of cache maintenance. Passive attempts keep
	// only validated target metadata, never a token, until successful delivery.
	cookieAttempt          codexTurnStateCookieAttempt
	cookieTarget           CodexTurnStateShape
	cookieCredentialsBound bool
	cookieResponseComplete bool
	cookieResponseFailed   bool
}

type CodexTurnStateSafeObservation struct {
	CodexModelEvidence
	IssuedAt         time.Time `json:"-"`
	ExpiresAt        time.Time `json:"-"`
	EnvelopeValid    bool      `json:"-"`
	ObservedAt       time.Time
	TokenLength      int
	CipherBlocks     int
	Shape            string
	ResponseSource   string
	ObservedShape    string
	ValidationReason string
	RefreshReason    string
}

func (a *CodexTurnStateAttempt) SafeObservation() CodexTurnStateSafeObservation {
	if a == nil {
		return CodexTurnStateSafeObservation{}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	value := a.safeObservation
	// Request diagnostics can retain response metadata without a ticket. The
	// summary index still requires ObservedAt, so this cannot replace an older
	// ticket's model/cookie evidence when the new response contains no state.
	value.CodexModelEvidence = a.modelEvidence.snapshot(a.Model)
	return value
}

type CodexTurnStateCollectRequest struct {
	Account *Account
	Model   string
	ProxyID int64
	onSend  func(time.Time)
	// Created only by the maintenance service; callers cannot grant trust with
	// a wire header. The native adapter invokes it at the final send boundary.
	validateModelPolicy func(context.Context) bool
}

type CodexTurnStateCollectResult struct {
	ModelEvidence CodexModelEvidence
	completed     bool
	Tokens        []string
	StatusCode    int
	RetryAfter    time.Duration
	Observation   *CodexTurnStateSafeObservation `json:"-"`
	observationID string
	requestSentAt time.Time
	cookieAttempt codexTurnStateCookieAttempt
}

type CodexTurnStateCollector interface {
	Collect(context.Context, CodexTurnStateCollectRequest) (CodexTurnStateCollectResult, error)
}

// CodexTurnStateCollectorHTTPDo must use ONLY req.ProxyID and the supplied
// account's credentials. It must not enter gateway billing, telemetry, root
// pools, retry/failover, or business-proxy fallback.
type CodexTurnStateCollectorHTTPDo func(context.Context, CodexTurnStateCollectRequest, *http.Request) (*http.Response, error)

type CodexTurnStateModelStatus struct {
	CacheScope             string              `json:"cache_scope"`
	CookieBundleExpiresAt  *time.Time          `json:"cookie_bundle_expires_at,omitempty"`
	LatestResponseEvidence *CodexModelEvidence `json:"latest_response_evidence,omitempty"`
	OSFamily               string              `json:"os_family"`
	Model                  string              `json:"model"`
	ModelAllowed           bool                `json:"model_allowed"`
	CacheAvailable         bool                `json:"cache_available"`
	CollectionStatus       string              `json:"collection_status"`
	CollectionReason       string              `json:"collection_reason,omitempty"`
	State                  string              `json:"state"`
	Shape                  string              `json:"shape"`
	Source                 string              `json:"source"`
	TokenLength            int                 `json:"token_length"`
	CipherBlocks           int                 `json:"cipher_blocks"`
	ExpiresAt              *time.Time          `json:"expires_at,omitempty"`
	RemainingSeconds       int64               `json:"remaining_seconds"`
	LastBusinessAt         *time.Time          `json:"last_business_at,omitempty"`
	LastCollectedAt        *time.Time          `json:"last_collected_at,omitempty"`
	NextCollectAt          *time.Time          `json:"next_collect_at,omitempty"`
	CollectorPaused        bool                `json:"collector_paused"`
	LastError              string              `json:"last_error,omitempty"`
	RefreshReason          string              `json:"refresh_reason,omitempty"`
	CollectorProxyID       *int64              `json:"collector_proxy_id,omitempty"`
	LastCollectorProxyID   *int64              `json:"last_collector_proxy_id,omitempty"`
	CollectorExtendedCount int                 `json:"collector_extended_count"`
}

type CodexTurnStateStatus struct {
	CacheScope          string                           `json:"cache_scope"`
	OSFamily            string                           `json:"os_family"`
	AccountID           int64                            `json:"account_id"`
	OwnerAccountID      int64                            `json:"owner_account_id"`
	Inherited           bool                             `json:"inherited"`
	Enabled             bool                             `json:"enabled"`
	AccountType         string                           `json:"account_type"`
	ResolvedAccountType string                           `json:"resolved_account_type"`
	ExpectedLength      int                              `json:"expected_length"`
	Reason              string                           `json:"reason,omitempty"`
	CollectorProxyID    *int64                           `json:"collector_proxy_id"`
	CollectorProxyIDs   []int64                          `json:"collector_proxy_ids"`
	Models              []CodexTurnStateModelStatus      `json:"models"`
	ObservationEnabled  bool                             `json:"observation_enabled"`
	ObservationScope    string                           `json:"observation_scope"`
	Observations        []CodexTurnStateModelObservation `json:"observations"`
}

// CodexTurnStateModelObservation is a process-local diagnostic summary. It
// contains no token, ciphertext, hash, or credential/configuration identifier.
type CodexTurnStateModelObservation struct {
	CodexModelEvidence
	OSFamily                 string     `json:"os_family"`
	Model                    string     `json:"model"`
	RequestSource            string     `json:"request_source"`
	ObservedAt               time.Time  `json:"observed_at"`
	ResponseLength           int        `json:"response_length"`
	ResponseShape            string     `json:"response_shape"`
	ResponseObservedShape    string     `json:"response_observed_shape,omitempty"`
	ResponseCipherBlocks     int        `json:"response_cipher_blocks,omitempty"`
	ResponseValidationReason string     `json:"response_validation_reason,omitempty"`
	ResponseSource           string     `json:"response_source,omitempty"`
	OutboundLength           int        `json:"outbound_length"`
	ObservationID            string     `json:"observation_id,omitempty"`
	RequestSentAt            *time.Time `json:"request_sent_at,omitempty"`
	OutboundAction           string     `json:"outbound_action,omitempty"`
	OutboundSource           string     `json:"outbound_source,omitempty"`
	MaintenanceReason        string     `json:"maintenance_reason,omitempty"`
	BusinessDelivered        *bool      `json:"business_delivered,omitempty"`
	SnapshotVersion          int64      `json:"snapshot_version,omitempty"`
	SnapshotExpiresAt        *time.Time `json:"snapshot_expires_at,omitempty"`
}

type CodexTurnStateBatchStatus struct {
	Items  map[string]*CodexTurnStateStatus `json:"items"`
	Models []string                         `json:"models"`
}
