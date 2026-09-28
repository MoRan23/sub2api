package service

import (
	"context"
	"errors"
	"time"
)

const (
	CandyTestMaxConcurrent = 3
	// Candy tests may legitimately spend several minutes waiting for a long
	// reasoning response. Keep the persisted deadline and the worker context at
	// the same half-hour window; the repository mirrors this value in its SQL
	// lease/timeout predicates.
	CandyTestTimeout          = 30 * time.Minute
	CandyTestLease            = 30 * time.Second
	CandyTestHeartbeat        = 5 * time.Second
	CandyTestMaxResponseBytes = 1 << 20
)

var (
	ErrCandyTestNotFound            = errors.New("candy test not found")
	ErrCandyTestInvalidRequest      = errors.New("invalid candy test request")
	ErrCandyTestIdempotencyConflict = errors.New("candy test idempotency key reused with different request")
)

type CandyTestModelOption struct {
	CatalogSource    string   `json:"catalog_source,omitempty"`
	ID               string   `json:"id"`
	DisplayName      string   `json:"display_name"`
	ReasoningEfforts []string `json:"reasoning_efforts"`
}

type CandyTestAccountOptions struct {
	UpstreamKind    string                 `json:"upstream_kind,omitempty"`
	CatalogSource   string                 `json:"catalog_source,omitempty"`
	RouteGeneration string                 `json:"-"`
	AccountID       int64                  `json:"account_id"`
	AccountName     string                 `json:"account_name"`
	Models          []CandyTestModelOption `json:"models"`
	SkipReason      string                 `json:"skip_reason,omitempty"`
}

type CandyTestOptions struct {
	Models   []CandyTestModelOption    `json:"models"`
	Accounts []CandyTestAccountOptions `json:"accounts"`
}

type CandyTestCreateRequest struct {
	AccountIDs      []int64 `json:"account_ids"`
	Model           string  `json:"model"`
	ReasoningEffort string  `json:"reasoning_effort"`
	IdempotencyKey  string  `json:"idempotency_key"`
}

// CandyTestItem contains only benchmark data, never credentials or request headers.
type CandyTestItem struct {
	ExpectedUpstreamKind    string              `json:"-"`
	ExpectedRouteGeneration string              `json:"-"`
	ID                      int64               `json:"id"`
	BatchID                 string              `json:"batch_id"`
	AccountID               int64               `json:"account_id"`
	AccountName             string              `json:"account_name"`
	Model                   string              `json:"model"`
	ReasoningEffort         string              `json:"reasoning_effort"`
	PromptVersion           string              `json:"prompt_version"`
	Status                  string              `json:"status"`
	Answers                 map[string]int      `json:"answers,omitempty"`
	ResponseText            string              `json:"response_text,omitempty"`
	FailureCode             string              `json:"failure_code,omitempty"`
	Execution               *CandyTestExecution `json:"execution,omitempty"`
	CreatedAt               time.Time           `json:"created_at"`
	StartedAt               *time.Time          `json:"started_at"`
	FinishedAt              *time.Time          `json:"finished_at"`
	CancelRequested         bool                `json:"cancel_requested"`
	ClaimID                 string              `json:"-"`
	LeaseUntil              *time.Time          `json:"-"`
}

type CandyTestExecution struct {
	UpstreamKind        string       `json:"upstream_kind,omitempty"`
	ResponseText        string       `json:"-"`
	RequestedModel      string       `json:"requested_model"`
	ActualModel         string       `json:"actual_model"`
	UpstreamModel       string       `json:"upstream_model"`
	ReasoningEffort     string       `json:"reasoning_effort"`
	ModelConflict       bool         `json:"model_conflict"`
	ModelEvidenceSource string       `json:"model_evidence_source"`
	Usage               *OpenAIUsage `json:"usage,omitempty"`
	Completed           bool         `json:"completed"`
	DurationMs          int64        `json:"duration_ms"`
}

type CandyTestBatch struct {
	ID              string           `json:"id"`
	Model           string           `json:"model"`
	ReasoningEffort string           `json:"reasoning_effort"`
	PromptVersion   string           `json:"prompt_version"`
	CreatedAt       time.Time        `json:"created_at"`
	FinishedAt      *time.Time       `json:"finished_at"`
	Total           int              `json:"total"`
	RetainedTotal   int              `json:"retained_total"`
	Counts          map[string]int   `json:"counts"`
	Items           []*CandyTestItem `json:"items"`
	Page            int              `json:"page"`
	PageSize        int              `json:"page_size"`
}

type CandyTestSummary struct {
	Latest *CandyTestItem `json:"latest,omitempty"`
	Active *CandyTestItem `json:"active,omitempty"`
}

// CandyTestExecutor must never mutate account operational state or retry inference.
type CandyTestExecutor interface {
	Options(context.Context, []int64) (*CandyTestOptions, error)
	Execute(context.Context, *CandyTestItem) (*CandyTestExecution, error)
}

// CandyTestFailure exposes a safe machine-readable code; upstream error text is not persisted.
type CandyTestFailure interface{ CandyTestFailureCode() string }

type CandyTestRepository interface {
	Create(context.Context, *CandyTestCreateRequest, []*CandyTestItem) (*CandyTestBatch, error)
	GetBatch(context.Context, string, int, int) (*CandyTestBatch, error)
	Cancel(context.Context, string, []int64) error
	History(context.Context, int64) ([]*CandyTestItem, error)
	Summaries(context.Context, []int64) (map[int64]*CandyTestSummary, error)
	Claim(context.Context) (*CandyTestItem, error)
	Heartbeat(context.Context, int64, string) (bool, error)
	Complete(context.Context, *CandyTestItem) (bool, error)
}
