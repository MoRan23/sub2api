package service

import (
	"context"
	"testing"
	"time"
)

type osProfileBackfillBlockingRepo struct {
	AccountRepository
	started chan struct{}
	exited  chan struct{}
}

func (r *osProfileBackfillBlockingRepo) BackfillOpenAIOAuthOSProfiles(ctx context.Context) error {
	close(r.started)
	<-ctx.Done()
	close(r.exited)
	return ctx.Err()
}

func TestOpenAIOAuthOSProfileBackfillShutdownCancelsRepository(t *testing.T) {
	repo := &osProfileBackfillBlockingRepo{started: make(chan struct{}), exited: make(chan struct{})}
	worker := NewOpenAIOAuthOSProfileBackfill(repo)
	worker.Start()
	worker.Start()
	select {
	case <-repo.started:
	case <-time.After(time.Second):
		t.Fatal("startup repair did not run")
	}
	worker.Stop()
	worker.Stop()
	select {
	case <-repo.exited:
	default:
		t.Fatal("shutdown returned before repository repair stopped")
	}
}
