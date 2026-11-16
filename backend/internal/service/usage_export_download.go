package service

import (
	"context"
	"io"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

// UsageExportDownload owns an open descriptor and a short, renewable artifact
// lease. File cleanup never holds a database transaction across a network read.
type UsageExportDownload struct {
	*os.File
	repo                UsageExportRepository
	jobID, claim, token string
	ctx                 context.Context
	cancel              context.CancelFunc
	deadline            atomic.Int64
	once                sync.Once
	closeErr            error
}
type usageExportDownloadReader struct{ download *UsageExportDownload }

func (r usageExportDownloadReader) Read(p []byte) (int, error) { return r.download.Read(p) }

// Hide os.File's promoted WriteTo so io.Copy cannot bypass lease checks.
func (d *UsageExportDownload) WriteTo(w io.Writer) (int64, error) {
	return io.Copy(w, usageExportDownloadReader{download: d})
}

func newUsageExportDownload(parent context.Context, f *os.File, repo UsageExportRepository, id, claim, token string, deadline time.Time) *UsageExportDownload {
	ctx, cancel := context.WithTimeout(parent, 30*time.Minute)
	d := &UsageExportDownload{File: f, repo: repo, jobID: id, claim: claim, token: token, ctx: ctx, cancel: cancel}
	d.deadline.Store(deadline.UnixNano())
	go d.maintain()
	return d
}
func (d *UsageExportDownload) Read(p []byte) (int, error) {
	err := d.ctx.Err()
	if err == nil && time.Now().UnixNano() >= d.deadline.Load() {
		err = context.DeadlineExceeded
	}
	if err != nil {
		_ = d.Close()
		return 0, err
	}
	return d.File.Read(p)
}
func (d *UsageExportDownload) Close() error {
	d.once.Do(func() {
		// Release the descriptor before making the bytes eligible for cleanup.
		d.closeErr = d.File.Close()
		d.cancel()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = d.repo.EndDownload(ctx, d.jobID, d.claim, d.token)
	})
	return d.closeErr
}
func (d *UsageExportDownload) maintain() {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-d.ctx.Done():
			_ = d.Close()
			return
		case <-ticker.C:
			if !d.renew() {
				return
			}
		}
	}
}
func (d *UsageExportDownload) renew() bool {
	until := time.Now().Add(50 * time.Second)
	ctx, cancel := context.WithTimeout(d.ctx, 3*time.Second)
	ok, err := d.repo.RenewDownload(ctx, d.jobID, d.claim, d.token)
	cancel()
	if err != nil || !ok {
		_ = d.Close()
		return false
	}
	d.deadline.Store(until.UnixNano())
	return true
}
