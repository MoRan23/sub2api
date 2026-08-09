package service

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

const (
	openAIOutboundSessionV1Pattern       = "openai-outbound-session:v1:*"
	openAIOutboundSessionV1CleanupLock   = "openai-outbound-session:v2:migration:v1-cleanup:lock"
	openAIOutboundSessionV1CleanupMarker = "openai-outbound-session:v2:migration:v1-cleanup:complete"

	openAIOutboundSessionV1CleanupBatchSize   = int64(250)
	openAIOutboundSessionV1CleanupLockTTL     = 5 * time.Minute
	openAIOutboundSessionV1CleanupVerifyDelay = 60 * time.Second
	openAIOutboundSessionV1CleanupRetryMin    = 5 * time.Second
	openAIOutboundSessionV1CleanupRetryMax    = 5 * time.Minute
)

var (
	errOpenAIOutboundSessionV1CleanupLockHeld = errors.New("OpenAI outbound session V1 cleanup lock is held")
	errOpenAIOutboundSessionV1CleanupLockLost = errors.New("OpenAI outbound session V1 cleanup lock was lost")
	errOpenAIOutboundSessionV1KeysRemain      = errors.New("OpenAI outbound session V1 keys remain after verification delay")

	openAIOutboundSessionV1CleanupReleaseScript = redis.NewScript(`
if redis.call('GET', KEYS[1]) == ARGV[1] then
  return redis.call('DEL', KEYS[1])
end
return 0
`)
	openAIOutboundSessionV1CleanupRenewScript = redis.NewScript(`
if redis.call('GET', KEYS[1]) == ARGV[1] then
  return redis.call('PEXPIRE', KEYS[1], ARGV[2])
end
return 0
`)
	openAIOutboundSessionV1CleanupClearMarkerScript = redis.NewScript(`
if redis.call('GET', KEYS[1]) == ARGV[1] then
  return redis.call('DEL', KEYS[1])
end
return 0
`)
	openAIOutboundSessionV1CleanupMarkCompleteScript = redis.NewScript(`
if redis.call('GET', KEYS[1]) ~= ARGV[1] then
  return 0
end
redis.call('SET', KEYS[2], ARGV[2])
return 1
`)
)

type OpenAIOutboundSessionV1CleanupMetrics struct {
	Attempts       int64
	Deleted        int64
	Failures       int64
	Completed      bool
	LastDurationMS int64
}

type openAIOutboundSessionV1CleanupMetricsStore struct {
	attempts, deleted, failures, completed, lastDurationMS atomic.Int64
}

var openAIOutboundSessionV1CleanupMetrics openAIOutboundSessionV1CleanupMetricsStore

func SnapshotOpenAIOutboundSessionV1CleanupMetrics() OpenAIOutboundSessionV1CleanupMetrics {
	m := &openAIOutboundSessionV1CleanupMetrics
	return OpenAIOutboundSessionV1CleanupMetrics{
		Attempts: m.attempts.Load(), Deleted: m.deleted.Load(), Failures: m.failures.Load(),
		Completed: m.completed.Load() == 1, LastDurationMS: m.lastDurationMS.Load(),
	}
}

// OpenAIOutboundSessionV1CleanupWorker performs a one-way, non-blocking cache
// migration. It intentionally runs regardless of the UUIDv7 feature flag so a
// deployment can clean V1 before enabling V2.
type OpenAIOutboundSessionV1CleanupWorker struct {
	rdb        *redis.Client
	instanceID string

	batchSize  int64
	lockTTL    time.Duration
	verifyWait time.Duration
	retryMin   time.Duration
	retryMax   time.Duration

	startOnce sync.Once
	stopOnce  sync.Once
	cancel    context.CancelFunc
	wg        sync.WaitGroup
}

func NewOpenAIOutboundSessionV1CleanupWorker(rdb *redis.Client) *OpenAIOutboundSessionV1CleanupWorker {
	return &OpenAIOutboundSessionV1CleanupWorker{
		rdb: rdb, instanceID: uuid.NewString(), batchSize: openAIOutboundSessionV1CleanupBatchSize,
		lockTTL: openAIOutboundSessionV1CleanupLockTTL, verifyWait: openAIOutboundSessionV1CleanupVerifyDelay,
		retryMin: openAIOutboundSessionV1CleanupRetryMin, retryMax: openAIOutboundSessionV1CleanupRetryMax,
	}
}

func ProvideOpenAIOutboundSessionV1CleanupWorker(rdb *redis.Client) *OpenAIOutboundSessionV1CleanupWorker {
	worker := NewOpenAIOutboundSessionV1CleanupWorker(rdb)
	worker.Start()
	return worker
}

func (w *OpenAIOutboundSessionV1CleanupWorker) Start() {
	if w == nil || w.rdb == nil {
		return
	}
	w.startOnce.Do(func() {
		ctx, cancel := context.WithCancel(context.Background())
		w.cancel = cancel
		w.wg.Add(1)
		go w.run(ctx)
	})
}

func (w *OpenAIOutboundSessionV1CleanupWorker) Stop() {
	if w == nil {
		return
	}
	w.stopOnce.Do(func() {
		if w.cancel != nil {
			w.cancel()
		}
	})
	w.wg.Wait()
}

func (w *OpenAIOutboundSessionV1CleanupWorker) run(ctx context.Context) {
	defer w.wg.Done()
	backoff := w.retryMin
	if backoff <= 0 {
		backoff = openAIOutboundSessionV1CleanupRetryMin
	}
	for {
		complete, _, err := w.cleanupOnce(ctx)
		if complete {
			return
		}
		if err != nil && !errors.Is(err, errOpenAIOutboundSessionV1CleanupLockHeld) && !errors.Is(err, context.Canceled) {
			logger.LegacyPrintf("service.openai_outbound_session_v1_cleanup", "[OpenAIIdentityV1Cleanup] attempt failed reason=%s", openAIOutboundSessionV1CleanupErrorReason(err))
		}
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		backoff *= 2
		if w.retryMax > 0 && backoff > w.retryMax {
			backoff = w.retryMax
		}
	}
}

func (w *OpenAIOutboundSessionV1CleanupWorker) cleanupOnce(ctx context.Context) (complete bool, deleted int64, err error) {
	if w == nil || w.rdb == nil {
		return false, 0, errors.New("redis unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	started := time.Now()
	openAIOutboundSessionV1CleanupMetrics.attempts.Add(1)
	defer func() {
		openAIOutboundSessionV1CleanupMetrics.lastDurationMS.Store(time.Since(started).Milliseconds())
		if err != nil && !errors.Is(err, errOpenAIOutboundSessionV1CleanupLockHeld) && !errors.Is(err, context.Canceled) {
			openAIOutboundSessionV1CleanupMetrics.failures.Add(1)
		}
	}()

	markedComplete, markerErr := w.reconcileCompletionMarker(ctx)
	if markerErr != nil {
		return false, 0, markerErr
	}
	if markedComplete {
		return true, 0, nil
	}

	locked, lockErr := w.rdb.SetNX(ctx, openAIOutboundSessionV1CleanupLock, w.instanceID, w.lockTTL).Result()
	if lockErr != nil {
		return false, 0, lockErr
	}
	if !locked {
		return false, 0, errOpenAIOutboundSessionV1CleanupLockHeld
	}
	defer func() {
		releaseCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, _ = openAIOutboundSessionV1CleanupReleaseScript.Run(releaseCtx, w.rdb, []string{openAIOutboundSessionV1CleanupLock}, w.instanceID).Result()
	}()

	deleted, err = w.deleteV1Keys(ctx)
	if err != nil {
		return false, deleted, err
	}
	openAIOutboundSessionV1CleanupMetrics.deleted.Add(deleted)
	if renewErr := w.renewLock(ctx); renewErr != nil {
		return false, deleted, renewErr
	}
	if w.verifyWait > 0 {
		timer := time.NewTimer(w.verifyWait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return false, deleted, ctx.Err()
		case <-timer.C:
		}
	}
	remaining, scanErr := w.hasV1Keys(ctx)
	if scanErr != nil {
		return false, deleted, scanErr
	}
	if remaining {
		return false, deleted, errOpenAIOutboundSessionV1KeysRemain
	}
	if markErr := w.markComplete(ctx); markErr != nil {
		return false, deleted, markErr
	}
	openAIOutboundSessionV1CleanupMetrics.completed.Store(1)
	logger.LegacyPrintf("service.openai_outbound_session_v1_cleanup", "[OpenAIIdentityV1Cleanup] completed deleted=%d duration_ms=%d", deleted, time.Since(started).Milliseconds())
	return true, deleted, nil
}

// reconcileCompletionMarker keeps the one-way marker honest across rolling
// upgrades. A delayed V1 writer may publish after the original final scan; in
// that case a later worker removes the observed marker and runs cleanup again.
func (w *OpenAIOutboundSessionV1CleanupWorker) reconcileCompletionMarker(ctx context.Context) (bool, error) {
	marker, err := w.rdb.Get(ctx, openAIOutboundSessionV1CleanupMarker).Result()
	if errors.Is(err, redis.Nil) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	remaining, err := w.hasV1Keys(ctx)
	if err != nil {
		return false, err
	}
	if !remaining {
		openAIOutboundSessionV1CleanupMetrics.completed.Store(1)
		return true, nil
	}

	openAIOutboundSessionV1CleanupMetrics.completed.Store(0)
	_, err = openAIOutboundSessionV1CleanupClearMarkerScript.Run(
		ctx,
		w.rdb,
		[]string{openAIOutboundSessionV1CleanupMarker},
		marker,
	).Result()
	if err != nil {
		return false, err
	}
	return false, nil
}

// markComplete validates lock ownership and writes the completion marker in a
// single Redis script. A worker whose lease expired during the verification
// delay must not certify another worker's migration attempt.
func (w *OpenAIOutboundSessionV1CleanupWorker) markComplete(ctx context.Context) error {
	result, err := openAIOutboundSessionV1CleanupMarkCompleteScript.Run(
		ctx,
		w.rdb,
		[]string{openAIOutboundSessionV1CleanupLock, openAIOutboundSessionV1CleanupMarker},
		w.instanceID,
		w.instanceID+":"+time.Now().UTC().Format(time.RFC3339Nano),
	).Int64()
	if err != nil {
		return err
	}
	if result != 1 {
		return errOpenAIOutboundSessionV1CleanupLockLost
	}
	return nil
}

func (w *OpenAIOutboundSessionV1CleanupWorker) deleteV1Keys(ctx context.Context) (int64, error) {
	var deleted int64
	// Deleting during SCAN can move the cursor past surviving keys. Repeat
	// complete passes until an independent scan observes a true zero set.
	for pass := 0; pass < 100; pass++ {
		removed, err := w.deleteV1KeysPass(ctx)
		deleted += removed
		if err != nil {
			return deleted, err
		}
		remaining, err := w.hasV1Keys(ctx)
		if err != nil {
			return deleted, err
		}
		if !remaining {
			return deleted, nil
		}
	}
	return deleted, errOpenAIOutboundSessionV1KeysRemain
}

func (w *OpenAIOutboundSessionV1CleanupWorker) deleteV1KeysPass(ctx context.Context) (int64, error) {
	var cursor uint64
	var deleted int64
	batchSize := w.batchSize
	if batchSize <= 0 {
		batchSize = openAIOutboundSessionV1CleanupBatchSize
	}
	for {
		keys, next, err := w.rdb.Scan(ctx, cursor, openAIOutboundSessionV1Pattern, batchSize).Result()
		if err != nil {
			return deleted, err
		}
		if len(keys) > 0 {
			removed, unlinkErr := w.rdb.Unlink(ctx, keys...).Result()
			if unlinkErr != nil {
				removed, err = w.rdb.Del(ctx, keys...).Result()
				if err != nil {
					return deleted, err
				}
			}
			deleted += removed
		}
		if err := w.renewLock(ctx); err != nil {
			return deleted, err
		}
		cursor = next
		if cursor == 0 {
			return deleted, nil
		}
	}
}

func (w *OpenAIOutboundSessionV1CleanupWorker) hasV1Keys(ctx context.Context) (bool, error) {
	var cursor uint64
	for {
		keys, next, err := w.rdb.Scan(ctx, cursor, openAIOutboundSessionV1Pattern, 10).Result()
		if err != nil {
			return false, err
		}
		if len(keys) > 0 {
			return true, nil
		}
		cursor = next
		if cursor == 0 {
			return false, nil
		}
	}
}

func (w *OpenAIOutboundSessionV1CleanupWorker) renewLock(ctx context.Context) error {
	result, err := openAIOutboundSessionV1CleanupRenewScript.Run(
		ctx,
		w.rdb,
		[]string{openAIOutboundSessionV1CleanupLock},
		w.instanceID,
		w.lockTTL.Milliseconds(),
	).Int64()
	if err != nil {
		return err
	}
	if result != 1 {
		return errOpenAIOutboundSessionV1CleanupLockLost
	}
	return nil
}

func openAIOutboundSessionV1CleanupErrorReason(err error) string {
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return "canceled"
	case errors.Is(err, errOpenAIOutboundSessionV1KeysRemain):
		return "verification_failed"
	case errors.Is(err, errOpenAIOutboundSessionV1CleanupLockHeld):
		return "lock_held"
	case errors.Is(err, errOpenAIOutboundSessionV1CleanupLockLost):
		return "lock_lost"
	default:
		return "redis_error"
	}
}
