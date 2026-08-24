package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/google/uuid"
)

const (
	openAIAutoResetScanInterval  = time.Minute
	openAIAutoResetSnapshotTTL   = openAIProbeCacheTTL
	openAIAutoResetBatchSize     = 100
	openAIAutoResetWorkerCount   = 4
	openAIAutoResetQueueCapacity = 1024
	openAIAutoResetAttemptTTL    = 8 * 24 * time.Hour
	openAIAutoResetCycleJitter   = 5
	openAIAutoResetBucketWidth   = openAIAutoResetCycleJitter*2 + 1
	openAIAutoResetLeaderLockKey = "jobs:openai-auto-reset-credit"
)

const (
	OpenAIAutoResetStatusChecking  = "checking"
	OpenAIAutoResetStatusAvailable = "available"
	OpenAIAutoResetStatusResetting = "resetting"
	OpenAIAutoResetStatusSuccess   = "success"
	OpenAIAutoResetStatusNoCredit  = "no_credit"
	OpenAIAutoResetStatusFailed    = "failed"
)

var errOpenAIAutoResetEligibilityChanged = errors.New("openai auto reset eligibility changed")

// OpenAIAutoResetCreditState 是可返回管理端的脱敏运行态。Attempt* 仅保存不可逆
// 指纹，用于重启后拒绝切换到另一张卡；不会保存卡 ID 或兑换 ID。
type OpenAIAutoResetCreditState struct {
	Status            string `json:"status"`
	TriggerWindow     string `json:"trigger_window,omitempty"`
	AvailableCount    int    `json:"available_count"`
	CheckedAt         string `json:"checked_at,omitempty"`
	LastResultAt      string `json:"last_result_at,omitempty"`
	ErrorCode         string `json:"error_code,omitempty"`
	AttemptCycleHash  string `json:"attempt_cycle_hash,omitempty"`
	AttemptCreditHash string `json:"attempt_credit_hash,omitempty"`
}

type openAIAutoResetQuota interface {
	QueryUsage(ctx context.Context, accountID int64) (*OpenAIQuotaUsage, error)
	CacheResetCreditsSnapshot(ctx context.Context, accountID int64, credits *OpenAIRateLimitResetCredits) error
	ResetCreditTargeted(ctx context.Context, accountID int64, creditID, redeemRequestID string) (*OpenAIQuotaResetResult, error)
}

// openAIAutoResetPreflightCASRepository is deliberately narrower than
// AccountRepository so the pre-consumption snapshot writes can require a
// database-side eligibility and state compare-and-swap without expanding every
// account repository test double.
type openAIAutoResetPreflightCASRepository interface {
	CompareAndUpdateOpenAIAutoResetPreflight(
		ctx context.Context,
		accountID int64,
		expectedState *OpenAIAutoResetCreditState,
		updates map[string]any,
	) (bool, error)
}

type openAIAutoResetContextKey struct{}

func withOpenAIAutoResetContext(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, openAIAutoResetContextKey{}, true)
}

func isOpenAIAutoResetContext(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	value, _ := ctx.Value(openAIAutoResetContextKey{}).(bool)
	return value
}

type openAIAutoResetRecovery interface {
	RecoverAccountState(ctx context.Context, accountID int64, options AccountRecoveryOptions) (*SuccessfulTestRecoveryResult, error)
}

// OpenAIQuotaAutoResetService 通过小型去重队列承接实时信号，并用分钟扫描补偿
// 重启、漏事件和多实例读取；真正消费仍由 PostgreSQL 幂等记录串行化。
type OpenAIQuotaAutoResetService struct {
	accountRepo AccountRepository
	quota       openAIAutoResetQuota
	recoverer   openAIAutoResetRecovery
	idempotency *IdempotencyCoordinator
	audit       *AuditLogService
	settings    *SettingService
	leaderLock  LeaderLockCache

	ctx     context.Context
	cancel  context.CancelFunc
	queue   chan int64
	pending sync.Map
	owner   string
	start   sync.Once
	stop    sync.Once
	wg      sync.WaitGroup
}

func NewOpenAIQuotaAutoResetService(
	accountRepo AccountRepository,
	quota openAIAutoResetQuota,
	recoverer openAIAutoResetRecovery,
	idempotency *IdempotencyCoordinator,
	audit *AuditLogService,
	settings *SettingService,
	leaderLock LeaderLockCache,
) *OpenAIQuotaAutoResetService {
	ctx, cancel := context.WithCancel(context.Background())
	return &OpenAIQuotaAutoResetService{
		accountRepo: accountRepo,
		quota:       quota,
		recoverer:   recoverer,
		idempotency: idempotency,
		audit:       audit,
		settings:    settings,
		leaderLock:  leaderLock,
		ctx:         ctx,
		cancel:      cancel,
		queue:       make(chan int64, openAIAutoResetQueueCapacity),
		owner:       uuid.NewString(),
	}
}

func (s *OpenAIQuotaAutoResetService) Start() {
	if s == nil || s.accountRepo == nil || s.quota == nil || s.idempotency == nil {
		return
	}
	s.start.Do(func() {
		setOpenAIAutoResetNotifier(s)
		for range openAIAutoResetWorkerCount {
			s.wg.Add(1)
			go s.runWorker()
		}
		s.wg.Add(1)
		go s.runScanner()
	})
}

func (s *OpenAIQuotaAutoResetService) Stop() {
	if s == nil {
		return
	}
	s.stop.Do(func() {
		clearOpenAIAutoResetNotifier(s)
		s.cancel()
		s.wg.Wait()
	})
}

// Notify 是请求热路径的非阻塞入口。同一账号尚在队列时只保留一个任务；队列
// 满时丢弃本次信号，分钟扫描仍会补偿，因此不会反向拖慢网关请求。
func (s *OpenAIQuotaAutoResetService) Notify(accountID int64) {
	if s == nil || accountID <= 0 {
		return
	}
	if _, loaded := s.pending.LoadOrStore(accountID, struct{}{}); loaded {
		return
	}
	select {
	case <-s.ctx.Done():
		s.pending.Delete(accountID)
	case s.queue <- accountID:
	default:
		s.pending.Delete(accountID)
		slog.Warn("openai_auto_reset_queue_full", "account_id", accountID)
	}
}

func (s *OpenAIQuotaAutoResetService) runWorker() {
	defer s.wg.Done()
	for {
		select {
		case <-s.ctx.Done():
			return
		case accountID := <-s.queue:
			ctx, cancel := context.WithTimeout(s.ctx, 50*time.Second)
			if err := s.evaluateAccount(ctx, accountID); err != nil && !errors.Is(err, context.Canceled) {
				slog.Warn("openai_auto_reset_evaluate_failed", "account_id", accountID, "error_code", infraerrors.Reason(err))
			}
			cancel()
			s.pending.Delete(accountID)
		}
	}
}

func (s *OpenAIQuotaAutoResetService) runScanner() {
	defer s.wg.Done()
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	select {
	case <-s.ctx.Done():
		return
	case <-timer.C:
		s.scanEnabledAccounts(s.ctx)
	}
	ticker := time.NewTicker(openAIAutoResetScanInterval)
	defer ticker.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			s.scanEnabledAccounts(s.ctx)
		}
	}
}

func (s *OpenAIQuotaAutoResetService) scanEnabledAccounts(ctx context.Context) {
	release, scan := s.tryAcquireScanLock(ctx)
	if !scan {
		return
	}
	if release != nil {
		defer release()
	}
	for page := 1; ; page++ {
		accounts, pageInfo, err := s.accountRepo.ListWithFilters(ctx, pagination.PaginationParams{
			Page: page, PageSize: openAIAutoResetBatchSize,
		}, PlatformOpenAI, AccountTypeOAuth, StatusActive, "", 0, "")
		if err != nil {
			slog.Warn("openai_auto_reset_scan_failed", "page", page, "error", err)
			return
		}
		for i := range accounts {
			account := &accounts[i]
			if account.Schedulable && ResolveOpenAIAutoResetCreditConfig(account).Enabled {
				s.Notify(account.ID)
			}
		}
		if len(accounts) < openAIAutoResetBatchSize || pageInfo == nil || page >= pageInfo.Pages {
			return
		}
	}
}

// Redis 锁异常时允许重复扫描，避免协调设施故障导致所有实例同时停止补偿；
// 消费唯一性由数据库幂等记录负责，扫描锁只用于削减重复查询。
func (s *OpenAIQuotaAutoResetService) tryAcquireScanLock(ctx context.Context) (func(), bool) {
	if s.leaderLock == nil {
		return func() {}, true
	}
	ok, err := s.leaderLock.TryAcquireLeaderLock(ctx, openAIAutoResetLeaderLockKey, s.owner, 55*time.Second)
	if err != nil {
		slog.Warn("openai_auto_reset_leader_lock_unavailable", "error", err)
		return func() {}, true
	}
	if !ok {
		return nil, false
	}
	return func() {
		releaseCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = s.leaderLock.ReleaseLeaderLock(releaseCtx, openAIAutoResetLeaderLockKey, s.owner)
	}, true
}

type openAIAutoResetAssessment struct {
	triggerWindow string
	resetReached  bool
	pauseReached  bool
	utilization5h float64
	utilization7d float64
	threshold5h   float64
	threshold7d   float64
}

func (s *OpenAIQuotaAutoResetService) evaluateAccount(ctx context.Context, accountID int64) error {
	ctx = withOpenAIAutoResetContext(ctx)
	account, err := s.accountRepo.GetByID(ctx, accountID)
	if err != nil || account == nil {
		return err
	}
	if account.IsShadow() {
		if account.ParentAccountID != nil {
			s.Notify(*account.ParentAccountID)
		}
		return nil
	}
	config := ResolveOpenAIAutoResetCreditConfig(account)
	if !isOpenAIAutoResetAccountEligible(account, config) {
		return nil
	}

	now := time.Now()
	assessment := s.assessExtra(account, config, now)
	state := openAIAutoResetStateFromExtra(account.Extra)
	needsQuery := openAIAutoResetSnapshotStale(account.Extra, now) || assessment.resetReached
	if assessment.pauseReached && !assessment.resetReached {
		needsQuery = needsQuery || state == nil || state.Status == OpenAIAutoResetStatusChecking || state.Status == OpenAIAutoResetStatusFailed || openAIAutoResetStateStale(state, now)
	}
	if !needsQuery {
		if !assessment.pauseReached && state != nil && state.TriggerWindow != "" {
			nextState := *state
			nextState.TriggerWindow = ""
			nextState.ErrorCode = ""
			nextState.CheckedAt = now.UTC().Format(time.RFC3339)
			if nextState.AvailableCount > 0 {
				nextState.Status = OpenAIAutoResetStatusAvailable
			} else {
				nextState.Status = OpenAIAutoResetStatusNoCredit
			}
			_, persistErr := s.persistOpenAIAutoResetPreflightCAS(ctx, accountID, nil, now, state, &nextState)
			return persistErr
		}
		return nil
	}

	// Do not persist a checking state from the initial snapshot: another instance
	// may finish while this query is in flight, and that write would erase its
	// durable attempt provenance.
	usage, err := s.quota.QueryUsage(ctx, accountID)
	if err != nil {
		return err
	}
	if usage == nil {
		return infraerrors.Conflict("RESET_CREDIT_QUERY_FAILED", "reset credit query returned an empty result")
	}
	if usage.RateLimitResetCredits == nil {
		return infraerrors.Conflict("RESET_CREDIT_DETAILS_UNAVAILABLE", "reset credit details are unavailable")
	}

	// QueryUsage 期间其他实例可能已经记录或完成同周期 attempt。这里的重读既
	// 复核管理员配置，也替换首次读取的 state；不可再用旧快照选择另一张卡。
	account, err = s.accountRepo.GetByID(ctx, accountID)
	if err != nil || account == nil {
		return err
	}
	config = ResolveOpenAIAutoResetCreditConfig(account)
	if !isOpenAIAutoResetAccountEligible(account, config) {
		return nil
	}
	state = openAIAutoResetStateFromExtra(account.Extra)
	assessment = s.assessUsage(usage, account, config, now)
	available := usage.RateLimitResetCredits.AvailableCount
	cycleHash := openAIAutoResetCycleHash(usage, state)
	if isCompletedOpenAIAutoResetAttempt(state, cycleHash) {
		return nil
	}
	if !assessment.resetReached {
		status := OpenAIAutoResetStatusNoCredit
		if available > 0 {
			status = OpenAIAutoResetStatusAvailable
		}
		_, persistErr := s.persistOpenAIAutoResetPreflightCAS(ctx, accountID, usage, now, state, &OpenAIAutoResetCreditState{
			Status:         status,
			TriggerWindow:  assessment.triggerWindow,
			AvailableCount: available,
			CheckedAt:      now.UTC().Format(time.RFC3339),
		})
		return persistErr
	}
	if available <= 0 {
		_, persistErr := s.persistOpenAIAutoResetPreflightCAS(ctx, accountID, usage, now, state, &OpenAIAutoResetCreditState{
			Status:         OpenAIAutoResetStatusNoCredit,
			TriggerWindow:  assessment.triggerWindow,
			AvailableCount: 0,
			CheckedAt:      now.UTC().Format(time.RFC3339),
			LastResultAt:   now.UTC().Format(time.RFC3339),
			ErrorCode:      "NO_RESET_CREDIT",
		})
		return persistErr
	}

	candidate, selectErr := selectOpenAIAutoResetCandidate(usage.autoResetCandidates, available, state, cycleHash)
	if selectErr != nil {
		return selectErr
	}
	creditHash := shortOpenAIAutoResetHash(candidate.ID)
	// Inferred reset instants use overlapping bucket locks. Concurrent observations
	// within the accepted jitter always share at least one sorted lock key, including
	// observations straddling a bucket boundary. The exact cycle and credit remain in
	// the payload fingerprint, so the loser fails closed rather than changing cards.
	cycleLockKeys := openAIAutoResetCycleLockKeys(accountID, usage)
	redeemKey := fmt.Sprintf("oarc:redeem:v3:%d:%s", accountID, cycleHash)
	redeemRequestID := uuid.NewSHA1(uuid.NameSpaceURL, []byte(redeemKey)).String()
	resetting := &OpenAIAutoResetCreditState{
		Status:            OpenAIAutoResetStatusResetting,
		TriggerWindow:     assessment.triggerWindow,
		AvailableCount:    available,
		CheckedAt:         now.UTC().Format(time.RFC3339),
		AttemptCycleHash:  cycleHash,
		AttemptCreditHash: creditHash,
	}
	account, err = s.accountRepo.GetByID(ctx, accountID)
	if err != nil || account == nil {
		return err
	}
	if !isOpenAIAutoResetAccountEligible(account, ResolveOpenAIAutoResetCreditConfig(account)) {
		return nil
	}
	attemptPersisted := false
	ownerExecuted := false
	executeOptions := IdempotencyExecuteOptions{
		Scope:      "openai_auto_reset_credit",
		ActorScope: fmt.Sprintf("account:%d", accountID),
		Method:     http.MethodPost,
		Route:      "/system/openai/reset-credit/auto",
		Payload: map[string]any{
			"account_id":  accountID,
			"credit_hash": creditHash,
			"cycle_hash":  cycleHash,
		},
		TTL:        openAIAutoResetAttemptTTL,
		RequireKey: true,
	}
	result, err := s.executeOpenAIAutoResetCycleLocks(ctx, cycleLockKeys, executeOptions, func(execCtx context.Context) (any, error) {
		ownerExecuted = true
		latestAccount, loadErr := s.accountRepo.GetByID(execCtx, accountID)
		if loadErr != nil {
			return nil, loadErr
		}
		if !isOpenAIAutoResetAccountEligible(latestAccount, ResolveOpenAIAutoResetCreditConfig(latestAccount)) {
			return nil, errOpenAIAutoResetEligibilityChanged
		}
		if persistErr := s.persistFreshUsageAndState(execCtx, accountID, usage, now, resetting); persistErr != nil {
			return nil, persistErr
		}
		attemptPersisted = true
		latestAccount, loadErr = s.accountRepo.GetByID(execCtx, accountID)
		if loadErr != nil {
			return nil, loadErr
		}
		if !isOpenAIAutoResetAccountEligible(latestAccount, ResolveOpenAIAutoResetCreditConfig(latestAccount)) {
			return nil, errOpenAIAutoResetEligibilityChanged
		}
		resetResult, resetErr := s.quota.ResetCreditTargeted(execCtx, accountID, candidate.ID, redeemRequestID)
		if resetErr != nil {
			return nil, resetErr
		}
		if resetResult == nil {
			return nil, infraerrors.InternalServer("OPENAI_AUTO_RESET_EMPTY_RESULT", "automatic reset returned an empty result")
		}
		// 幂等表只保存脱敏结果，避免上游返回的卡 ID 被持久化到响应体列。
		consumeResult := openAIAutoResetConsumeResult{Code: resetResult.Code, WindowsReset: resetResult.WindowsReset}
		if completeErr := s.completeOpenAIAutoResetOwner(
			execCtx,
			accountID,
			assessment,
			available,
			resetting,
			cycleHash,
			creditHash,
			consumeResult,
		); completeErr != nil {
			return nil, completeErr
		}
		return consumeResult, nil
	})
	if err != nil {
		if errors.Is(err, errOpenAIAutoResetEligibilityChanged) {
			return nil
		}
		// 另一个实例已持有同一周期的兑换时保持 resetting，等待下一轮读取同一
		// 幂等结果；不能把并发冲突误报成上游消费失败，更不能改选下一张卡。
		reason := infraerrors.Reason(err)
		if reason == infraerrors.Reason(ErrIdempotencyInProgress) ||
			reason == infraerrors.Reason(ErrIdempotencyRetryBackoff) ||
			reason == infraerrors.Reason(ErrIdempotencyKeyConflict) {
			return nil
		}
		if !attemptPersisted {
			return err
		}
		latestAccount, loadErr := s.accountRepo.GetByID(ctx, accountID)
		if loadErr != nil || latestAccount == nil {
			// The durable state is unknown, so a blind failure write could overwrite a
			// terminal state committed before an idempotency-store failure.
			return err
		}
		latestState := openAIAutoResetStateFromExtra(latestAccount.Extra)
		if isCompletedOpenAIAutoResetAttempt(latestState, cycleHash) &&
			latestState.AttemptCreditHash == creditHash {
			slog.Warn("openai_auto_reset_idempotency_mark_failed_after_terminal_state",
				"account_id", accountID,
				"error_code", infraerrors.Reason(err),
			)
			return nil
		}
		if latestState == nil ||
			latestState.Status != OpenAIAutoResetStatusResetting ||
			latestState.AttemptCycleHash != cycleHash ||
			latestState.AttemptCreditHash != creditHash {
			return err
		}
		failed := *latestState
		failed.Status = OpenAIAutoResetStatusFailed
		failed.ErrorCode = infraerrors.Reason(err)
		failed.LastResultAt = time.Now().UTC().Format(time.RFC3339)
		updated, persistErr := s.persistOpenAIAutoResetPreflightCAS(ctx, accountID, nil, time.Now(), latestState, &failed)
		if persistErr != nil {
			return persistErr
		}
		if updated {
			s.recordAudit(accountID, assessment, available, "failed", 0, failed.ErrorCode)
			slog.Warn("openai_auto_reset_credit_failed",
				"account_id", accountID,
				"trigger_window", failed.TriggerWindow,
				"available_count", failed.AvailableCount,
				"error_code", failed.ErrorCode,
			)
		}
		return err
	}
	if result == nil {
		return infraerrors.InternalServer("OPENAI_AUTO_RESET_EMPTY_IDEMPOTENCY_RESULT", "automatic reset returned an empty idempotency result")
	}
	if result.Replayed || !ownerExecuted {
		return nil
	}
	return nil
}

func (s *OpenAIQuotaAutoResetService) completeOpenAIAutoResetOwner(
	ctx context.Context,
	accountID int64,
	assessment openAIAutoResetAssessment,
	available int,
	resetting *OpenAIAutoResetCreditState,
	cycleHash string,
	creditHash string,
	consumeResult openAIAutoResetConsumeResult,
) error {
	completionCtx, cancelCompletion := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancelCompletion()
	if strings.EqualFold(strings.TrimSpace(consumeResult.Code), "no_credit") {
		noCreditAt := time.Now().UTC().Format(time.RFC3339)
		noCredit := &OpenAIAutoResetCreditState{
			Status:            OpenAIAutoResetStatusNoCredit,
			TriggerWindow:     assessment.triggerWindow,
			AvailableCount:    0,
			CheckedAt:         noCreditAt,
			LastResultAt:      noCreditAt,
			ErrorCode:         "NO_RESET_CREDIT",
			AttemptCycleHash:  cycleHash,
			AttemptCreditHash: creditHash,
		}
		if err := s.persistState(completionCtx, accountID, noCredit); err != nil {
			return err
		}
		s.recordAudit(accountID, assessment, available, "no_credit", 0, noCredit.ErrorCode)
		return nil
	}

	postCtx, cancelPost := context.WithTimeout(completionCtx, 8*time.Second)
	post := RunOpenAIQuotaResetPostProcess(postCtx, accountID, s.quota, s.recoverer, s.accountRepo.GetByID)
	cancelPost()
	if !post.AccountStateRecovered || post.WarningCode != "" {
		code := post.WarningCode
		if code == "" {
			code = OpenAIQuotaResetWarningAccountRecoveryFailed
		}
		failed := *resetting
		failed.Status = OpenAIAutoResetStatusFailed
		failed.ErrorCode = code
		failed.LastResultAt = time.Now().UTC().Format(time.RFC3339)
		if err := s.persistState(completionCtx, accountID, &failed); err != nil {
			return err
		}
		s.recordAudit(accountID, assessment, available, "recovery_failed", consumeResult.WindowsReset, code)
		return nil
	}

	successNow := time.Now()
	successAt := successNow.UTC().Format(time.RFC3339)
	success := &OpenAIAutoResetCreditState{
		Status:            OpenAIAutoResetStatusSuccess,
		TriggerWindow:     assessment.triggerWindow,
		AvailableCount:    max(0, available-1),
		CheckedAt:         successAt,
		LastResultAt:      successAt,
		AttemptCycleHash:  cycleHash,
		AttemptCreditHash: creditHash,
	}
	if post.Quota != nil && post.Quota.RateLimitResetCredits != nil {
		success.AvailableCount = post.Quota.RateLimitResetCredits.AvailableCount
	}
	updates := buildOpenAIAutoResetUsageUpdates(post.Quota, successNow)
	if updates == nil {
		updates = make(map[string]any, 1)
	}
	updates[OpenAIAutoResetCreditStateExtraKey] = success
	if err := s.accountRepo.UpdateExtra(completionCtx, accountID, updates); err != nil {
		return err
	}
	s.recordAudit(accountID, assessment, available, "success", consumeResult.WindowsReset, "")
	slog.Info("openai_auto_reset_credit_success",
		"account_id", accountID,
		"trigger_window", assessment.triggerWindow,
		"threshold_5h", assessment.threshold5h,
		"threshold_7d", assessment.threshold7d,
		"utilization_5h", assessment.utilization5h,
		"utilization_7d", assessment.utilization7d,
		"windows_reset", consumeResult.WindowsReset,
	)
	return nil
}

func isOpenAIAutoResetAccountEligible(account *Account, config OpenAIAutoResetCreditConfig) bool {
	return account != nil &&
		account.IsOpenAIOAuth() &&
		!account.IsShadow() &&
		account.IsActive() &&
		account.Schedulable &&
		config.Enabled
}

func hasOpenAIAutoResetAttemptForCycle(state *OpenAIAutoResetCreditState, cycleHash string) bool {
	return state != nil &&
		state.AttemptCycleHash == cycleHash &&
		state.AttemptCreditHash != ""
}

func isCompletedOpenAIAutoResetAttempt(state *OpenAIAutoResetCreditState, cycleHash string) bool {
	if !hasOpenAIAutoResetAttemptForCycle(state, cycleHash) {
		return false
	}
	return state.Status == OpenAIAutoResetStatusSuccess || state.Status == OpenAIAutoResetStatusNoCredit
}

func (s *OpenAIQuotaAutoResetService) executeOpenAIAutoResetCycleLocks(
	ctx context.Context,
	lockKeys []string,
	options IdempotencyExecuteOptions,
	execute func(context.Context) (any, error),
) (*IdempotencyExecuteResult, error) {
	if len(lockKeys) == 0 {
		return nil, infraerrors.InternalServer("OPENAI_AUTO_RESET_CYCLE_LOCK_MISSING", "automatic reset cycle lock is missing")
	}
	var executeAt func(context.Context, int) (*IdempotencyExecuteResult, error)
	executeAt = func(execCtx context.Context, index int) (*IdempotencyExecuteResult, error) {
		current := options
		current.IdempotencyKey = lockKeys[index]
		return s.idempotency.Execute(execCtx, current, func(innerCtx context.Context) (any, error) {
			if index+1 == len(lockKeys) {
				return execute(innerCtx)
			}
			nested, err := executeAt(innerCtx, index+1)
			if err != nil {
				return nil, err
			}
			if nested == nil {
				return nil, infraerrors.InternalServer("OPENAI_AUTO_RESET_EMPTY_IDEMPOTENCY_RESULT", "automatic reset returned an empty idempotency result")
			}
			return nested.Data, nil
		})
	}
	return executeAt(ctx, 0)
}

type openAIAutoResetConsumeResult struct {
	Code         string `json:"code"`
	WindowsReset int    `json:"windows_reset"`
}

func decodeOpenAIAutoResetConsumeResult(value any) openAIAutoResetConsumeResult {
	if typed, ok := value.(openAIAutoResetConsumeResult); ok {
		return typed
	}
	raw, _ := json.Marshal(value)
	var decoded openAIAutoResetConsumeResult
	_ = json.Unmarshal(raw, &decoded)
	return decoded
}

func (s *OpenAIQuotaAutoResetService) assessExtra(account *Account, config OpenAIAutoResetCreditConfig, now time.Time) openAIAutoResetAssessment {
	utilization5h, _ := resolveOpenAIQuotaUtilization(account.Extra, "5h", now)
	utilization7d, _ := resolveOpenAIQuotaUtilization(account.Extra, "7d", now)
	return s.buildAssessment(account, config, utilization5h, utilization7d)
}

func (s *OpenAIQuotaAutoResetService) assessUsage(usage *OpenAIQuotaUsage, account *Account, config OpenAIAutoResetCreditConfig, now time.Time) openAIAutoResetAssessment {
	updates := buildOpenAIAutoResetUsageUpdates(usage, now)
	utilization5h := readOpenAIQuotaUsedPercent(updates, "5h") / 100
	utilization7d := readOpenAIQuotaUsedPercent(updates, "7d") / 100
	return s.buildAssessment(account, config, utilization5h, utilization7d)
}

func (s *OpenAIQuotaAutoResetService) buildAssessment(account *Account, config OpenAIAutoResetCreditConfig, utilization5h, utilization7d float64) openAIAutoResetAssessment {
	assessment := openAIAutoResetAssessment{
		utilization5h: utilization5h,
		utilization7d: utilization7d,
		threshold5h:   config.Threshold5h,
		threshold7d:   config.Threshold7d,
	}
	reset5h := utilization5h >= config.Threshold5h
	reset7d := utilization7d >= config.Threshold7d
	assessment.resetReached = reset5h || reset7d
	assessment.triggerWindow = joinOpenAIAutoResetWindows(reset5h, reset7d)

	pause5h, pause7d := resolveOpenAIQuotaAutoPauseThresholds(context.Background(), account)
	if s.settings != nil {
		pause5h, pause7d = resolveOpenAIQuotaAutoPauseThresholds(
			withOpenAIQuotaAutoPauseSettings(context.Background(), s.settings.GetOpenAIQuotaAutoPauseSettings(context.Background())),
			account,
		)
	}
	pauseReached5h := !resolveAccountExtraBool(account.Extra, "auto_pause_5h_disabled") && pause5h > 0 && utilization5h >= pause5h
	pauseReached7d := !resolveAccountExtraBool(account.Extra, "auto_pause_7d_disabled") && pause7d > 0 && utilization7d >= pause7d
	assessment.pauseReached = pauseReached5h || pauseReached7d || assessment.resetReached
	if assessment.triggerWindow == "" {
		assessment.triggerWindow = joinOpenAIAutoResetWindows(pauseReached5h, pauseReached7d)
	}
	return assessment
}

func joinOpenAIAutoResetWindows(fiveHour, sevenDay bool) string {
	switch {
	case fiveHour && sevenDay:
		return "5h+7d"
	case fiveHour:
		return "5h"
	case sevenDay:
		return "7d"
	default:
		return ""
	}
}

func buildOpenAIAutoResetUsageUpdates(usage *OpenAIQuotaUsage, now time.Time) map[string]any {
	if usage == nil || usage.RateLimit == nil {
		return nil
	}
	rateLimit := usage.RateLimit
	snapshot := &OpenAICodexUsageSnapshot{UpdatedAt: now.UTC().Format(time.RFC3339)}
	applyWindow := func(window *OpenAIRateLimitWindow, primary bool) {
		if window == nil {
			return
		}
		used := window.UsedPercent
		resetAfter := int(window.ResetAfterSeconds)
		windowMinutes := int(window.LimitWindowSeconds / 60)
		if primary {
			snapshot.PrimaryUsedPercent = &used
			snapshot.PrimaryResetAfterSeconds = &resetAfter
			snapshot.PrimaryWindowMinutes = &windowMinutes
		} else {
			snapshot.SecondaryUsedPercent = &used
			snapshot.SecondaryResetAfterSeconds = &resetAfter
			snapshot.SecondaryWindowMinutes = &windowMinutes
		}
	}
	applyWindow(rateLimit.PrimaryWindow, true)
	applyWindow(rateLimit.SecondaryWindow, false)
	return buildCodexUsageExtraUpdates(snapshot, now)
}

func (s *OpenAIQuotaAutoResetService) persistFreshUsageAndState(ctx context.Context, accountID int64, usage *OpenAIQuotaUsage, now time.Time, state *OpenAIAutoResetCreditState) error {
	updates := buildOpenAIAutoResetUsageUpdates(usage, now)
	if state != nil {
		if updates == nil {
			updates = make(map[string]any, 1)
		}
		updates[OpenAIAutoResetCreditStateExtraKey] = state
	}
	if len(updates) > 0 {
		if err := s.accountRepo.UpdateExtra(ctx, accountID, updates); err != nil {
			return err
		}
	}
	return s.quota.CacheResetCreditsSnapshot(ctx, accountID, usage.RateLimitResetCredits)
}

func (s *OpenAIQuotaAutoResetService) persistOpenAIAutoResetPreflightCAS(
	ctx context.Context,
	accountID int64,
	usage *OpenAIQuotaUsage,
	now time.Time,
	expectedState *OpenAIAutoResetCreditState,
	nextState *OpenAIAutoResetCreditState,
) (bool, error) {
	repo, ok := s.accountRepo.(openAIAutoResetPreflightCASRepository)
	if !ok {
		// A non-atomic fallback would recreate the read/check/write race this path
		// exists to close. Custom repositories therefore fail closed and skip the
		// observational write until they implement the narrow CAS capability.
		return false, nil
	}
	updates := buildOpenAIAutoResetUsageUpdates(usage, now)
	if usage != nil {
		credits := usage.RateLimitResetCredits
		if credits == nil || (credits.AvailableCount > 0 && len(credits.Credits) == 0) {
			return false, infraerrors.New(
				http.StatusBadGateway,
				"OPENAI_QUOTA_RESET_CREDITS_REFRESH_FAILED",
				"failed to refresh reset-credit expiration details; cached data was preserved",
			)
		}
		if updates == nil {
			updates = make(map[string]any, 1)
		}
		// The reset-credit display cache is part of the same CAS payload. Writing it
		// afterward through CacheResetCreditsSnapshot would reopen a loser-write race.
		updates[openaiQuotaResetCreditsKey] = credits
	}
	if nextState != nil {
		if updates == nil {
			updates = make(map[string]any, 1)
		}
		updates[OpenAIAutoResetCreditStateExtraKey] = nextState
	}
	if len(updates) == 0 {
		return false, nil
	}
	updated, err := repo.CompareAndUpdateOpenAIAutoResetPreflight(ctx, accountID, expectedState, updates)
	return updated, err
}

func selectOpenAIAutoResetCandidate(candidates []openAIAutoResetCreditCandidate, available int, previous *OpenAIAutoResetCreditState, cycleHash string) (openAIAutoResetCreditCandidate, error) {
	if available <= 0 {
		return openAIAutoResetCreditCandidate{}, infraerrors.Conflict("OPENAI_AUTO_RESET_NO_CREDIT", "no reset credit is available")
	}
	if len(candidates) < available {
		return openAIAutoResetCreditCandidate{}, infraerrors.Conflict("OPENAI_AUTO_RESET_CREDIT_DETAILS_INCOMPLETE", "reset credit details are incomplete")
	}
	for _, candidate := range candidates {
		if _, err := time.Parse(time.RFC3339, candidate.ExpiresAt); err != nil {
			return openAIAutoResetCreditCandidate{}, infraerrors.Conflict("OPENAI_AUTO_RESET_CREDIT_EXPIRY_INVALID", "reset credit expiration is invalid")
		}
	}
	sorted := append([]openAIAutoResetCreditCandidate(nil), candidates...)
	sort.SliceStable(sorted, func(i, j int) bool {
		left, leftErr := time.Parse(time.RFC3339, sorted[i].ExpiresAt)
		right, rightErr := time.Parse(time.RFC3339, sorted[j].ExpiresAt)
		if leftErr != nil {
			return false
		}
		if rightErr != nil {
			return true
		}
		return left.Before(right)
	})
	if previous != nil && previous.AttemptCycleHash == cycleHash && previous.AttemptCreditHash != "" {
		for _, candidate := range sorted {
			if shortOpenAIAutoResetHash(candidate.ID) == previous.AttemptCreditHash {
				if strings.TrimSpace(candidate.ID) == "" {
					break
				}
				return candidate, nil
			}
		}
		return openAIAutoResetCreditCandidate{}, infraerrors.Conflict("OPENAI_AUTO_RESET_ORIGINAL_CREDIT_UNAVAILABLE", "the original reset credit cannot be confirmed; refusing to switch credits")
	}
	if len(sorted) == 0 || strings.TrimSpace(sorted[0].ID) == "" {
		return openAIAutoResetCreditCandidate{}, infraerrors.Conflict("OPENAI_AUTO_RESET_CREDIT_ID_MISSING", "the earliest reset credit has no official id")
	}
	return sorted[0], nil
}

func openAIAutoResetCycleSeed(usage *OpenAIQuotaUsage) string {
	fiveHour, sevenDay, _, _ := openAIAutoResetCycleWindows(usage)
	return fmt.Sprintf("5h:%d|7d:%d", fiveHour, sevenDay)
}

// openAIAutoResetCycleHash keeps the original cycle hash when ResetAt is absent
// and the inferred reset instant only moved by a few seconds. FetchedAt and
// ResetAfterSeconds are independently rounded by the upstream, so an ambiguous
// retry can otherwise produce a new idempotency key for the same quota window.
func openAIAutoResetCycleHash(usage *OpenAIQuotaUsage, previous *OpenAIAutoResetCreditState) string {
	fiveHour, sevenDay, inferred5h, inferred7d := openAIAutoResetCycleWindows(usage)
	current := shortOpenAIAutoResetHash(fmt.Sprintf("5h:%d|7d:%d", fiveHour, sevenDay))
	if previous == nil || previous.AttemptCycleHash == "" || previous.AttemptCycleHash == current || (!inferred5h && !inferred7d) {
		return current
	}

	fiveHourDeltas := []int64{0}
	sevenDayDeltas := []int64{0}
	if inferred5h {
		fiveHourDeltas = openAIAutoResetCycleDeltas()
	}
	if inferred7d {
		sevenDayDeltas = openAIAutoResetCycleDeltas()
	}
	for _, fiveHourDelta := range fiveHourDeltas {
		for _, sevenDayDelta := range sevenDayDeltas {
			candidate := fmt.Sprintf("5h:%d|7d:%d", fiveHour+fiveHourDelta, sevenDay+sevenDayDelta)
			if shortOpenAIAutoResetHash(candidate) == previous.AttemptCycleHash {
				return previous.AttemptCycleHash
			}
		}
	}
	return current
}

func openAIAutoResetCycleLockKeys(accountID int64, usage *OpenAIQuotaUsage) []string {
	fiveHour, sevenDay, inferred5h, inferred7d := openAIAutoResetCycleWindows(usage)
	fiveHourBuckets := openAIAutoResetWindowLockBuckets(fiveHour, inferred5h)
	sevenDayBuckets := openAIAutoResetWindowLockBuckets(sevenDay, inferred7d)
	keys := make([]string, 0, len(fiveHourBuckets)*len(sevenDayBuckets))
	seen := make(map[string]struct{}, cap(keys))
	for _, fiveHourBucket := range fiveHourBuckets {
		for _, sevenDayBucket := range sevenDayBuckets {
			seed := fmt.Sprintf("5h:%s|7d:%s", fiveHourBucket, sevenDayBucket)
			key := fmt.Sprintf("oarc:v3:%d:%s", accountID, shortOpenAIAutoResetHash(seed))
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	return keys
}

func openAIAutoResetWindowLockBuckets(resetAt int64, inferred bool) []string {
	if resetAt <= 0 {
		return []string{"none"}
	}
	minimum := (resetAt - int64(openAIAutoResetCycleJitter)) / int64(openAIAutoResetBucketWidth)
	maximum := (resetAt + int64(openAIAutoResetCycleJitter)) / int64(openAIAutoResetBucketWidth)
	capacity := maximum - minimum + 1
	if !inferred {
		capacity++
	}
	buckets := make([]string, 0, capacity)
	for bucket := minimum; bucket <= maximum; bucket++ {
		buckets = append(buckets, fmt.Sprintf("coarse:%d", bucket))
	}
	if !inferred {
		// Keep an exact lock for exact-to-exact replay identity, while the shared
		// coarse locks serialize exact and inferred observations of the same window.
		buckets = append(buckets, fmt.Sprintf("exact:%d", resetAt))
	}
	return buckets
}

func openAIAutoResetCycleDeltas() []int64 {
	deltas := make([]int64, 0, openAIAutoResetCycleJitter*2+1)
	for delta := -int64(openAIAutoResetCycleJitter); delta <= int64(openAIAutoResetCycleJitter); delta++ {
		deltas = append(deltas, delta)
	}
	return deltas
}

func openAIAutoResetCycleWindows(usage *OpenAIQuotaUsage) (fiveHour, sevenDay int64, inferred5h, inferred7d bool) {
	if usage == nil || usage.RateLimit == nil {
		return 0, 0, false, false
	}
	for _, window := range []*OpenAIRateLimitWindow{usage.RateLimit.PrimaryWindow, usage.RateLimit.SecondaryWindow} {
		if window == nil {
			continue
		}
		resetAt := window.ResetAt
		inferred := false
		if resetAt <= 0 {
			resetAt = usage.FetchedAt + window.ResetAfterSeconds
			inferred = true
		}
		if window.LimitWindowSeconds <= 6*60*60 {
			fiveHour = resetAt
			inferred5h = inferred
		} else {
			sevenDay = resetAt
			inferred7d = inferred
		}
	}
	return fiveHour, sevenDay, inferred5h, inferred7d
}

func shortOpenAIAutoResetHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:12])
}

func openAIAutoResetSnapshotStale(extra map[string]any, now time.Time) bool {
	if len(extra) == 0 {
		return true
	}
	raw, ok := extra["codex_usage_updated_at"]
	if !ok {
		return true
	}
	updatedAt, err := parseTime(fmt.Sprint(raw))
	return err != nil || now.Sub(updatedAt) >= openAIAutoResetSnapshotTTL
}

func openAIAutoResetStateFromExtra(extra map[string]any) *OpenAIAutoResetCreditState {
	if len(extra) == 0 {
		return nil
	}
	raw, ok := extra[OpenAIAutoResetCreditStateExtraKey]
	if !ok || raw == nil {
		return nil
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	var state OpenAIAutoResetCreditState
	if err := json.Unmarshal(encoded, &state); err != nil || state.Status == "" {
		return nil
	}
	return &state
}

func openAIAutoResetStateStale(state *OpenAIAutoResetCreditState, now time.Time) bool {
	if state == nil || state.CheckedAt == "" {
		return true
	}
	checkedAt, err := time.Parse(time.RFC3339, state.CheckedAt)
	return err != nil || now.Sub(checkedAt) >= openAIAutoResetSnapshotTTL
}

func (s *OpenAIQuotaAutoResetService) persistState(ctx context.Context, accountID int64, state *OpenAIAutoResetCreditState) error {
	if state == nil {
		return nil
	}
	return s.accountRepo.UpdateExtra(ctx, accountID, map[string]any{OpenAIAutoResetCreditStateExtraKey: state})
}

func (s *OpenAIQuotaAutoResetService) recordAudit(accountID int64, assessment openAIAutoResetAssessment, available int, resultCode string, windowsReset int, errorCode string) {
	if s.audit == nil {
		return
	}
	statusCode := http.StatusOK
	if resultCode != "success" {
		statusCode = http.StatusConflict
	}
	s.audit.Record(&AuditLog{
		ActorEmail: "system",
		ActorRole:  "system",
		AuthMethod: "system",
		Action:     "system.openai.reset_credit.auto",
		Method:     "SYSTEM",
		Path:       fmt.Sprintf("/system/openai/accounts/%d/auto-reset-credit", accountID),
		StatusCode: statusCode,
		Extra: map[string]any{
			"account_id":      accountID,
			"trigger_window":  assessment.triggerWindow,
			"threshold_5h":    assessment.threshold5h,
			"threshold_7d":    assessment.threshold7d,
			"utilization_5h":  assessment.utilization5h,
			"utilization_7d":  assessment.utilization7d,
			"available_count": available,
			"result_code":     resultCode,
			"windows_reset":   windowsReset,
			"error_code":      errorCode,
		},
	})
}

var openAIAutoResetNotifierRegistry struct {
	sync.RWMutex
	service *OpenAIQuotaAutoResetService
}

func setOpenAIAutoResetNotifier(service *OpenAIQuotaAutoResetService) {
	openAIAutoResetNotifierRegistry.Lock()
	openAIAutoResetNotifierRegistry.service = service
	openAIAutoResetNotifierRegistry.Unlock()
}

func clearOpenAIAutoResetNotifier(service *OpenAIQuotaAutoResetService) {
	openAIAutoResetNotifierRegistry.Lock()
	if openAIAutoResetNotifierRegistry.service == service {
		openAIAutoResetNotifierRegistry.service = nil
	}
	openAIAutoResetNotifierRegistry.Unlock()
}

func notifyOpenAIAutoReset(accountID int64) {
	openAIAutoResetNotifierRegistry.RLock()
	service := openAIAutoResetNotifierRegistry.service
	openAIAutoResetNotifierRegistry.RUnlock()
	if service != nil {
		service.Notify(accountID)
	}
}

// NotifyOpenAIAutoResetCredit 供额度查询入口发送轻量信号；不执行同步上游请求。
func NotifyOpenAIAutoResetCredit(accountID int64) {
	notifyOpenAIAutoReset(accountID)
}
