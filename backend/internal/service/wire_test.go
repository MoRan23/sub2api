package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/zeromicro/go-zero/core/collection"
)

func TestProvideTimingWheelService_ReturnsError(t *testing.T) {
	original := newTimingWheel
	t.Cleanup(func() { newTimingWheel = original })

	newTimingWheel = func(_ time.Duration, _ int, _ collection.Execute) (*collection.TimingWheel, error) {
		return nil, errors.New("boom")
	}

	svc, err := ProvideTimingWheelService()
	if err == nil {
		t.Fatalf("期望返回 error，但得到 nil")
	}
	if svc != nil {
		t.Fatalf("期望返回 nil svc，但得到非空")
	}
}

func TestProvideTimingWheelService_Success(t *testing.T) {
	svc, err := ProvideTimingWheelService()
	if err != nil {
		t.Fatalf("期望 err 为 nil，但得到: %v", err)
	}
	if svc == nil {
		t.Fatalf("期望 svc 非空，但得到 nil")
	}
	svc.Stop()
}

type candyProviderBlockingRepository struct {
	CandyTestRepository
	entered chan struct{}
	stopped chan struct{}
}

func (r *candyProviderBlockingRepository) Claim(ctx context.Context) (*CandyTestItem, error) {
	r.entered <- struct{}{}
	<-ctx.Done()
	r.stopped <- struct{}{}
	return nil, ctx.Err()
}

func TestProvideAccountCandyTestService_DefersStartUntilReady(t *testing.T) {
	repo := &candyProviderBlockingRepository{
		entered: make(chan struct{}, CandyTestMaxConcurrent),
		stopped: make(chan struct{}, CandyTestMaxConcurrent),
	}
	// The empty transport is never called: the fixture only exercises lifecycle
	// wiring against blocked repository operations, without network or storage.
	svc := ProvideAccountCandyTestService(repo, &AccountCandyTestTransport{})
	t.Cleanup(svc.Stop)
	select {
	case <-repo.entered:
		t.Fatal("dependency construction must not execute recovered jobs before transports are ready")
	case <-time.After(50 * time.Millisecond):
	}
	svc.Start()
	deadline := time.After(3 * time.Second)
	for range CandyTestMaxConcurrent {
		select {
		case <-repo.entered:
		case <-deadline:
			t.Fatal("provider did not start the candy queue workers")
		}
	}
	svc.Stop()
	for range CandyTestMaxConcurrent {
		select {
		case <-repo.stopped:
		default:
			t.Fatal("Stop returned while a queue repository call was still running")
		}
	}
}
