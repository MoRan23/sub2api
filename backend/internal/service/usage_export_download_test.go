package service

import (
	"context"
	"errors"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type exportDownloadTestRepo struct {
	UsageExportRepository
	file                *os.File
	ended               chan struct{}
	ends                atomic.Int64
	closedBeforeRelease atomic.Bool
	renew               bool
}

func (r *exportDownloadTestRepo) EndDownload(context.Context, string, string, string) error {
	_, err := r.file.Stat()
	r.closedBeforeRelease.Store(errors.Is(err, os.ErrClosed))
	if r.ends.Add(1) == 1 {
		close(r.ended)
	}
	return nil
}
func (r *exportDownloadTestRepo) RenewDownload(context.Context, string, string, string) (bool, error) {
	return r.renew, nil
}
func TestUsageExportDownloadLeaseCloseAndCancellation(t *testing.T) {
	for _, mode := range []string{"close", "cancel", "expired", "renew-failed"} {
		t.Run(mode, func(t *testing.T) {
			f, err := os.CreateTemp(t.TempDir(), "export")
			require.NoError(t, err)
			repo := &exportDownloadTestRepo{file: f, ended: make(chan struct{})}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			deadline := time.Now().Add(time.Minute)
			if mode == "expired" {
				deadline = time.Now().Add(-time.Second)
			}
			d := newUsageExportDownload(ctx, f, repo, "job", "claim", "token", deadline)
			t.Cleanup(func() { _ = d.Close() })
			switch mode {
			case "close":
				require.NoError(t, d.Close())
				require.NoError(t, d.Close())
			case "cancel":
				cancel()
			case "expired":
				_, err = d.Read(make([]byte, 1))
				require.ErrorIs(t, err, context.DeadlineExceeded)
			case "renew-failed":
				require.False(t, d.renew())
			}
			select {
			case <-repo.ended:
			case <-time.After(2 * time.Second):
				t.Fatal("download lease was not released")
			}
			require.True(t, repo.closedBeforeRelease.Load())
			require.EqualValues(t, 1, repo.ends.Load())
		})
	}
}
