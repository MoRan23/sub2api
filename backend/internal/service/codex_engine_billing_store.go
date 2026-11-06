package service

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

var (
	ErrCodexEngineBillingNotFound  = errors.New("engine billing job not found")
	ErrCodexEngineBillingConflict  = errors.New("engine billing identity or event conflict")
	ErrCodexEngineBillingClaimLost = errors.New("engine billing job claim expired or changed")
)

const (
	CodexEngineBillingPendingState       = "pending"
	CodexEngineBillingBlockedState       = "blocked"
	CodexEngineBillingSealedState        = "sealed"
	CodexEngineBillingLinkedState        = "linked"
	CodexEngineBillingNotDispatchedState = "not_dispatched"
)

// CodexEngineBillingPending is persisted before dispatch. Snapshot contains only
// the immutable billing inputs needed to price later events; never credentials,
// request bodies, prompts, headers or account/API-key objects.
type CodexEngineBillingPending struct {
	ClientRequestID string
	AccountID       int64
	UserID          int64
	APIKeyID        int64
	IdentityDigest  string
	InstanceID      string
	Snapshot        json.RawMessage
	NextAttemptAt   time.Time
}

type CodexEngineBillingJob struct {
	CodexEngineBillingPending
	ID               int64
	ReceiptID        string
	State            string
	Cursor           string
	Revision         int64
	ConsumedSequence int64
	SettlementCursor int64
	ClaimToken       string
	LeaseUntil       *time.Time
	AttemptCount     int
	LinkedJobID      *int64
	TerminalSeal     json.RawMessage
	SourceSeal       json.RawMessage
	LastError        string
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// CodexEngineBillingProgress releases the lease after a bounded polling pass.
// Pending includes unknown receipts and receipts still awaiting a terminal seal.
// Only a verified protocol seal may move a job to sealed. Blocked retains all
// recovery data (e.g. after an account's upstream credentials changed).
type CodexEngineBillingProgress struct {
	JobID            int64
	ClaimToken       string
	Cursor           string
	Revision         int64
	ConsumedSequence int64
	SettlementCursor int64
	NextAttemptAt    time.Time
	State            string
	TerminalSeal     json.RawMessage
	SourceSeal       json.RawMessage
	LastError        string
}

// CodexEngineBillingEvent describes one confirmed, immutable usage event. Its
// fingerprint covers the upstream event facts, not newly computed prices. The
// store derives the billing request ID from the original job/receipt/event IDs
// and commits the ledger, original billing effects and usage log atomically.
type CodexEngineBillingEvent struct {
	JobID             int64
	ClaimToken        string
	EventID           string
	Fingerprint       string
	Sequence          int64
	Summary           CodexEngineBillingSummaryGroup
	Platform          string
	PlatformQuotaCost float64
	Command           UsageBillingCommand
	UsageLog          UsageLog
}

// Received events are immutable validated upstream facts. Receiving them is
// independent of whether all required pricing dimensions are already known.
type CodexEngineBillingReceivedEvent struct {
	EventID     string
	Sequence    int64
	Fingerprint string
	Summary     CodexEngineBillingSummaryGroup
	Raw         json.RawMessage
}

// StageEvents saves every event and its receipt cursor in one transaction. It
// does not release the claim or assert that the received events were charged.
type CodexEngineBillingReceivedPage struct {
	JobID            int64
	ClaimToken       string
	Cursor           string
	Revision         int64
	ConsumedSequence int64
	Events           []CodexEngineBillingReceivedEvent
}

type CodexEngineBillingStore interface {
	CreatePending(context.Context, *CodexEngineBillingPending) (*CodexEngineBillingJob, error)
	Get(context.Context, int64) (*CodexEngineBillingJob, error)
	// Only the sending path with proof that no request was dispatched may call
	// this. It preserves the audit row without inventing an upstream seal.
	MarkNotDispatched(context.Context, int64) error
	// BindReceipt returns the original canonical job when a new client request
	// observes an already-bound receipt. Its billing identity/snapshot wins.
	BindReceipt(ctx context.Context, jobID int64, instanceID, receiptID, claimToken string) (*CodexEngineBillingJob, error)
	ClaimDue(context.Context, int, time.Duration) ([]*CodexEngineBillingJob, error)
	SaveProgress(context.Context, *CodexEngineBillingProgress) error
	StageEvents(context.Context, *CodexEngineBillingReceivedPage) error
	ReadPendingReceived(ctx context.Context, jobID int64, claimToken string, afterSequence int64, limit int) ([]CodexEngineBillingReceivedEvent, error)
	ApplyEvent(context.Context, *CodexEngineBillingEvent) (*UsageBillingApplyResult, error)
	// AckReceivedSettled follows the committed ledger and successful cache effects.
	// Until then the received event remains pending, including after a restart.
	AckReceivedSettled(ctx context.Context, jobID int64, claimToken, eventID, fingerprint string, sequence int64) error
	ReadSummary(ctx context.Context, jobID, throughSequence int64) ([]CodexEngineBillingSummaryGroup, error)
}
