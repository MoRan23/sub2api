package service

import (
	"context"
	"encoding/json"
	"io"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
)

// UsageExportSpec is a small, frozen description; it never retains an HTTP request.
type UsageExportSpec struct {
	Filters   usagestats.UsageLogFilters `json:"filters"`
	SortBy    string                     `json:"sort_by"`
	SortOrder string                     `json:"sort_order"`
	Language  string                     `json:"language"`
	Timezone  string                     `json:"timezone"`
}

type UsageExportOwner struct {
	UserID     int64  `json:"user_id"`
	Kind       string `json:"kind"`
	KeyID      string `json:"key_id,omitempty"`
	Revision   string `json:"revision,omitempty"`
	Restricted bool   `json:"restricted"`
	ClientIP   string `json:"client_ip,omitempty"`
}

type UsageExportJob struct {
	ID          string           `json:"id"`
	Owner       UsageExportOwner `json:"-"`
	Spec        UsageExportSpec  `json:"-"`
	Status      string           `json:"status"`
	MaxID       int64            `json:"-"`
	RowsWritten int64            `json:"rows_written"`
	TotalRows   int64            `json:"total_rows"`
	SizeBytes   int64            `json:"size_bytes"`
	FileKey     string           `json:"-"`
	ClaimToken  string           `json:"-"`
	Attempts    int              `json:"-"`
	ErrorCode   string           `json:"error_code,omitempty"`
	CreatedAt   time.Time        `json:"created_at"`
	ExpiresAt   *time.Time       `json:"expires_at,omitempty"`
	LeaseUntil  *time.Time       `json:"-"`
}

type UsageExportCursor struct {
	ID        int64
	CreatedAt time.Time
	Model     string
}
type UsageExportArtifact struct {
	JobID         string
	ClaimToken    string
	FileKey       string
	ReservedBytes int64
}
type UsageExportSource interface {
	PrepareUsageExport(context.Context, UsageExportSpec) (maxID, total int64, err error)
	ReadUsageExport(context.Context, UsageExportSpec, int64, *UsageExportCursor, int, bool) ([]UsageLog, *UsageExportCursor, error)
}

type UsageExportRepository interface {
	CanCreate(context.Context, int64) error
	Create(context.Context, *UsageExportJob) error
	Get(context.Context, string) (*UsageExportJob, error)
	List(context.Context, UsageExportOwner) ([]UsageExportJob, error)
	Claim(context.Context, string, int64) (*UsageExportJob, error)
	Heartbeat(context.Context, string, string, int64) (bool, error)
	Finish(context.Context, *UsageExportJob) (bool, error)
	Cancel(context.Context, string) error
	Delete(context.Context, string, func(UsageExportArtifact) error) error
	SetTicket(context.Context, string, string) error
	PeekByTicket(context.Context, string) (*UsageExportJob, error)
	GetByTicket(context.Context, string, func(*UsageExportJob) error) (*UsageExportJob, error)
	RenewDownload(context.Context, string, string, string) (bool, error)
	EndDownload(context.Context, string, string, string) error
	Expired(context.Context) ([]UsageExportJob, error)
	CleanupArtifacts(context.Context) ([]UsageExportArtifact, error)
	ReleaseArtifact(context.Context, UsageExportArtifact, func(UsageExportArtifact) error) error
}

// UsageExportWriter is independently testable without the worker, database or HTTP stack.
type UsageExportWriter interface {
	WriteRow([]any) error
	Close() error
}
type usageExportWriteCloser interface {
	io.Writer
	io.Closer
}

func usageExportJSON(v any) []byte { b, _ := json.Marshal(v); return b }
