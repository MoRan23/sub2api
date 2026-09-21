package service

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

var (
	ErrCodexTelemetryPolicyDisabled = errors.New("Codex telemetry policy disabled")
	ErrCodexTelemetryPolicyChanged  = errors.New("Codex telemetry policy changed")
	ErrCodexTelemetryInvalidState   = errors.New("invalid Codex telemetry state")
)

// CodexTelemetryPolicy is the shared publication fence. A node's environment
// override must never be passed to SyncPolicy: it only disables that node.
type CodexTelemetryPolicy struct {
	Epoch       int64 `json:"epoch"`
	Enabled     bool  `json:"enabled"`
	Simulation  bool  `json:"simulation"`
	Observation bool  `json:"observation"`
}

// InstallationID is the ID actually sent on the wire. Empty means that the
// request did not carry an installation identity; no managed ID is invented.
type CodexTelemetryPoolKey struct {
	OwnerAccountID int64  `json:"owner_account_id"`
	OSFamily       string `json:"os_family"`
	InstallationID string `json:"installation_id"`
}

type CodexTelemetryPool struct {
	Key             CodexTelemetryPoolKey `json:"key"`
	ID              string                `json:"id"`
	Seed            string                `json:"seed"`
	TemplateVersion int                   `json:"template_version"`
	Version         int64                 `json:"version"`
	CreatedAt       time.Time             `json:"created_at"`
	LastBusinessAt  time.Time             `json:"last_business_at"`
}

// Activities contain only explicitly selected telemetry facts and simulation
// decisions. Never marshal CodexTelemetryInput or upstream request/response
// objects into Data: they contain credentials or business content.
type CodexTelemetryActivity struct {
	Kind      string          `json:"kind"`
	Key       string          `json:"key"`
	Data      json.RawMessage `json:"data"`
	DueAt     time.Time       `json:"due_at,omitempty"`
	UpdatedAt time.Time       `json:"updated_at"`
}

func CodexTelemetryActivityMapKey(kind, key string) string { return kind + "\x00" + key }

// CodexTelemetryBatch stores a frozen, allowlisted payload and routing IDs, not
// bearer tokens or proxy URLs. The sender resolves credentials immediately
// before dispatch and verifies Pool.Key against the live account identity.
type CodexTelemetryBatch struct {
	ID            string             `json:"id"`
	Sequence      int64              `json:"sequence"`
	Pool          CodexTelemetryPool `json:"pool"`
	PolicyEpoch   int64              `json:"policy_epoch"`
	AccountID     int64              `json:"account_id"`
	ProxyID       *int64             `json:"proxy_id,omitempty"`
	Type          string             `json:"type"`
	Source        string             `json:"source"`
	UserAgent     string             `json:"user_agent"`
	Originator    string             `json:"originator"`
	ClientVersion string             `json:"client_version"`
	Payload       json.RawMessage    `json:"payload"`
	Metadata      json.RawMessage    `json:"metadata"`
	CreatedAt     time.Time          `json:"created_at"`
	NotBefore     time.Time          `json:"not_before"`
	Status        string             `json:"status"`
	ClaimID       string             `json:"claim_id"`
	LeaseUntil    time.Time          `json:"lease_until"`
	StartedAt     time.Time          `json:"started_at"`
	CompletedAt   time.Time          `json:"completed_at"`
	HTTPStatus    int                `json:"http_status"`
	ErrorCode     string             `json:"error_code"`
}

// The callback runs while holding the policy shared lock and pool update lock.
// Mutate Activities (delete removes a row), Pool.LastBusinessAt/TemplateVersion,
// and append Batches. Everything commits atomically, or nothing does. It must
// not perform network I/O or recursively call the store.
type CodexTelemetryPoolTransaction struct {
	Policy     CodexTelemetryPolicy
	Pool       CodexTelemetryPool
	Activities map[string]CodexTelemetryActivity
	Batches    []CodexTelemetryBatch
}

type CodexTelemetryBatchResult struct {
	Status     string
	HTTPStatus int
	ErrorCode  string // Fixed local code only; never a raw error message or body.
}

type CodexTelemetryStore interface {
	SyncPolicy(context.Context, bool, bool, bool) (CodexTelemetryPolicy, error)
	ReadPolicy(context.Context) (CodexTelemetryPolicy, error)
	TransactPool(context.Context, CodexTelemetryPoolKey, time.Time, func(*CodexTelemetryPoolTransaction) error) (*CodexTelemetryPool, error)
	ListDuePools(context.Context, time.Time, int) ([]CodexTelemetryPoolKey, error)
	ClaimBatches(context.Context, time.Time, int) ([]CodexTelemetryBatch, error)
	MarkSending(context.Context, string, string, int64, time.Time) (bool, error)
	CompleteBatch(context.Context, string, string, CodexTelemetryBatchResult, time.Time) (bool, error)
	Maintain(context.Context, time.Time) error
}
