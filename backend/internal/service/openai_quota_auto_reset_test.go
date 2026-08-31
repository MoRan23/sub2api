package service

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

func TestNormalizeOpenAIAutoResetCreditExtra(t *testing.T) {
	t.Run("历史账号默认关闭", func(t *testing.T) {
		account := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth}
		config := ResolveOpenAIAutoResetCreditConfig(account)
		require.False(t, config.Enabled)
		require.Equal(t, 1.0, config.Threshold5h)
		require.Equal(t, 1.0, config.Threshold7d)
	})

	t.Run("开启时补齐两个百分百阈值并剥离运行态", func(t *testing.T) {
		extra, err := normalizeOpenAIAutoResetCreditExtra(PlatformOpenAI, AccountTypeOAuth, false, map[string]any{
			OpenAIAutoResetCreditEnabledExtraKey: true,
			OpenAIAutoResetCreditStateExtraKey:   map[string]any{"status": "success"},
		})
		require.NoError(t, err)
		require.Equal(t, 1.0, extra[OpenAIAutoResetCredit5hThresholdExtraKey])
		require.Equal(t, 1.0, extra[OpenAIAutoResetCredit7dThresholdExtraKey])
		require.NotContains(t, extra, OpenAIAutoResetCreditStateExtraKey)
	})

	t.Run("阈值和账号类型严格校验", func(t *testing.T) {
		_, err := normalizeOpenAIAutoResetCreditExtra(PlatformOpenAI, AccountTypeOAuth, false, map[string]any{
			OpenAIAutoResetCreditEnabledExtraKey:     true,
			OpenAIAutoResetCredit5hThresholdExtraKey: 0.0009,
		})
		require.Error(t, err)

		_, err = normalizeOpenAIAutoResetCreditExtra(PlatformOpenAI, AccountTypeOAuth, true, map[string]any{
			OpenAIAutoResetCreditEnabledExtraKey: true,
		})
		require.Error(t, err)
	})
}

func TestShouldAutoPauseOpenAIAccountByQuota_AutoResetCreditStates(t *testing.T) {
	now := time.Now().UTC()
	baseExtra := map[string]any{
		OpenAIAutoResetCreditEnabledExtraKey:     true,
		OpenAIAutoResetCredit5hThresholdExtraKey: 1.0,
		OpenAIAutoResetCredit7dThresholdExtraKey: 1.0,
		"auto_pause_5h_threshold":                0.8,
		"auto_pause_7d_disabled":                 true,
		"codex_5h_used_percent":                  90.0,
		"codex_usage_updated_at":                 now.Format(time.RFC3339),
		"codex_5h_reset_at":                      now.Add(time.Hour).Format(time.RFC3339),
	}

	t.Run("卡状态未知时暂停并触发异步查询", func(t *testing.T) {
		account := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Extra: cloneOpenAIAutoResetExtra(baseExtra)}
		paused, decision := shouldAutoPauseOpenAIAccountByQuota(context.Background(), account)
		require.True(t, paused)
		require.Equal(t, "quota_auto_reset_credit_check_5h", decision.reason)
	})

	t.Run("明确有卡时允许继续到用卡阈值", func(t *testing.T) {
		extra := cloneOpenAIAutoResetExtra(baseExtra)
		extra[OpenAIAutoResetCreditStateExtraKey] = OpenAIAutoResetCreditState{
			Status: OpenAIAutoResetStatusAvailable, AvailableCount: 1, CheckedAt: now.Format(time.RFC3339),
		}
		account := &Account{ID: 2, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Extra: extra}
		paused, _ := shouldAutoPauseOpenAIAccountByQuota(context.Background(), account)
		require.False(t, paused)
	})

	t.Run("达到用卡阈值后即使有卡也退出调度", func(t *testing.T) {
		extra := cloneOpenAIAutoResetExtra(baseExtra)
		extra["codex_5h_used_percent"] = 100.0
		extra[OpenAIAutoResetCreditStateExtraKey] = OpenAIAutoResetCreditState{
			Status: OpenAIAutoResetStatusAvailable, AvailableCount: 1, CheckedAt: now.Format(time.RFC3339),
		}
		account := &Account{ID: 3, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Extra: extra}
		paused, decision := shouldAutoPauseOpenAIAccountByQuota(context.Background(), account)
		require.True(t, paused)
		require.Equal(t, "quota_auto_reset_pending_5h", decision.reason)
	})

	t.Run("自然窗口重置后清除动态阻塞", func(t *testing.T) {
		extra := cloneOpenAIAutoResetExtra(baseExtra)
		extra["codex_5h_used_percent"] = 100.0
		extra["codex_5h_reset_at"] = now.Add(-time.Second).Format(time.RFC3339)
		extra[OpenAIAutoResetCreditStateExtraKey] = OpenAIAutoResetCreditState{
			Status: OpenAIAutoResetStatusFailed, TriggerWindow: "5h", ErrorCode: "RESET_FAILED",
		}
		account := &Account{ID: 4, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Extra: extra}
		paused, _ := shouldAutoPauseOpenAIAccountByQuota(context.Background(), account)
		require.False(t, paused)
	})
}

func TestSelectOpenAIAutoResetCandidate_FailsClosed(t *testing.T) {
	candidates := []openAIAutoResetCreditCandidate{
		{ID: "later", ExpiresAt: "2026-09-02T00:00:00Z"},
		{ID: "earlier", ExpiresAt: "2026-09-01T00:00:00Z"},
	}
	selected, err := selectOpenAIAutoResetCandidate(candidates, 2, nil, "cycle-a")
	require.NoError(t, err)
	require.Equal(t, "earlier", selected.ID)

	_, err = selectOpenAIAutoResetCandidate([]openAIAutoResetCreditCandidate{
		{ExpiresAt: "2026-09-01T00:00:00Z"},
	}, 1, nil, "cycle-a")
	require.Error(t, err)

	_, err = selectOpenAIAutoResetCandidate(candidates, 2, &OpenAIAutoResetCreditState{
		AttemptCycleHash: "cycle-a", AttemptCreditHash: shortOpenAIAutoResetHash("missing"),
	}, "cycle-a")
	require.Error(t, err, "模糊结果后原卡消失时不得切换下一张卡")
}

func TestOpenAIAutoResetCycleHash_ToleratesOnlyInferredResetJitter(t *testing.T) {
	baseFetchedAt := time.Now().UTC().Unix()
	tests := []struct {
		name       string
		first      *OpenAIQuotaUsage
		second     *OpenAIQuotaUsage
		wantReused bool
	}{
		{
			name: "single inferred window",
			first: &OpenAIQuotaUsage{FetchedAt: baseFetchedAt, RateLimit: &OpenAIRateLimit{
				PrimaryWindow: &OpenAIRateLimitWindow{LimitWindowSeconds: 5 * 60 * 60, ResetAfterSeconds: 3600},
			}},
			second: &OpenAIQuotaUsage{FetchedAt: baseFetchedAt + 1, RateLimit: &OpenAIRateLimit{
				PrimaryWindow: &OpenAIRateLimitWindow{LimitWindowSeconds: 5 * 60 * 60, ResetAfterSeconds: 3600},
			}},
			wantReused: true,
		},
		{
			name: "two inferred windows",
			first: &OpenAIQuotaUsage{FetchedAt: baseFetchedAt, RateLimit: &OpenAIRateLimit{
				PrimaryWindow:   &OpenAIRateLimitWindow{LimitWindowSeconds: 5 * 60 * 60, ResetAfterSeconds: 3600},
				SecondaryWindow: &OpenAIRateLimitWindow{LimitWindowSeconds: 7 * 24 * 60 * 60, ResetAfterSeconds: 86400},
			}},
			second: &OpenAIQuotaUsage{FetchedAt: baseFetchedAt + 1, RateLimit: &OpenAIRateLimit{
				PrimaryWindow:   &OpenAIRateLimitWindow{LimitWindowSeconds: 5 * 60 * 60, ResetAfterSeconds: 3600},
				SecondaryWindow: &OpenAIRateLimitWindow{LimitWindowSeconds: 7 * 24 * 60 * 60, ResetAfterSeconds: 86400},
			}},
			wantReused: true,
		},
		{
			name: "explicit reset at is exact",
			first: &OpenAIQuotaUsage{FetchedAt: baseFetchedAt, RateLimit: &OpenAIRateLimit{
				PrimaryWindow: &OpenAIRateLimitWindow{LimitWindowSeconds: 5 * 60 * 60, ResetAt: baseFetchedAt + 3600},
			}},
			second: &OpenAIQuotaUsage{FetchedAt: baseFetchedAt + 1, RateLimit: &OpenAIRateLimit{
				PrimaryWindow: &OpenAIRateLimitWindow{LimitWindowSeconds: 5 * 60 * 60, ResetAt: baseFetchedAt + 3601},
			}},
			wantReused: false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			firstHash := shortOpenAIAutoResetHash(openAIAutoResetCycleSeed(test.first))
			secondRawHash := shortOpenAIAutoResetHash(openAIAutoResetCycleSeed(test.second))
			require.NotEqual(t, firstHash, secondRawHash)
			resolved := openAIAutoResetCycleHash(test.second, &OpenAIAutoResetCreditState{AttemptCycleHash: firstHash})
			if test.wantReused {
				require.Equal(t, firstHash, resolved)
			} else {
				require.Equal(t, secondRawHash, resolved)
			}
		})
	}
}

func TestOpenAIAutoResetCycleLockKeys_OverlapAcrossInferredBucketBoundary(t *testing.T) {
	width := int64(openAIAutoResetBucketWidth)
	baseFetchedAt := time.Now().UTC().Unix()
	firstResetAt := baseFetchedAt + 3600
	baseFetchedAt += (width - 1 - firstResetAt%width + width) % width
	first := &OpenAIQuotaUsage{FetchedAt: baseFetchedAt, RateLimit: &OpenAIRateLimit{
		PrimaryWindow:   &OpenAIRateLimitWindow{LimitWindowSeconds: 5 * 60 * 60, ResetAfterSeconds: 3600},
		SecondaryWindow: &OpenAIRateLimitWindow{LimitWindowSeconds: 7 * 24 * 60 * 60, ResetAfterSeconds: 86400},
	}}
	second := &OpenAIQuotaUsage{FetchedAt: baseFetchedAt + 1, RateLimit: &OpenAIRateLimit{
		PrimaryWindow:   &OpenAIRateLimitWindow{LimitWindowSeconds: 5 * 60 * 60, ResetAfterSeconds: 3600},
		SecondaryWindow: &OpenAIRateLimitWindow{LimitWindowSeconds: 7 * 24 * 60 * 60, ResetAfterSeconds: 86400},
	}}

	require.NotEqual(t, openAIAutoResetCycleSeed(first), openAIAutoResetCycleSeed(second))
	require.True(t, autoResetLockKeysOverlap(
		openAIAutoResetCycleLockKeys(10, first),
		openAIAutoResetCycleLockKeys(10, second),
	))

	explicitFirst := &OpenAIQuotaUsage{RateLimit: &OpenAIRateLimit{
		PrimaryWindow: &OpenAIRateLimitWindow{LimitWindowSeconds: 5 * 60 * 60, ResetAt: firstResetAt},
	}}
	explicitSecond := &OpenAIQuotaUsage{RateLimit: &OpenAIRateLimit{
		PrimaryWindow: &OpenAIRateLimitWindow{LimitWindowSeconds: 5 * 60 * 60, ResetAt: firstResetAt + 1},
	}}
	require.NotEqual(t, openAIAutoResetCycleSeed(explicitFirst), openAIAutoResetCycleSeed(explicitSecond), "explicit ResetAt remains an exact cycle identity")
	require.True(t, autoResetLockKeysOverlap(
		openAIAutoResetCycleLockKeys(10, explicitFirst),
		openAIAutoResetCycleLockKeys(10, explicitSecond),
	), "nearby exact observations must serialize even though their cycle identities remain exact")

	inferredOnly := &OpenAIQuotaUsage{FetchedAt: firstResetAt - 3600, RateLimit: &OpenAIRateLimit{
		PrimaryWindow: &OpenAIRateLimitWindow{LimitWindowSeconds: 5 * 60 * 60, ResetAfterSeconds: 3600},
	}}
	require.True(t, autoResetLockKeysOverlap(
		openAIAutoResetCycleLockKeys(10, inferredOnly),
		openAIAutoResetCycleLockKeys(10, explicitFirst),
	), "exact and inferred observations of the same reset instant must share a lock")
}

func autoResetLockKeysOverlap(left, right []string) bool {
	seen := make(map[string]struct{}, len(left))
	for _, key := range left {
		seen[key] = struct{}{}
	}
	for _, key := range right {
		if _, ok := seen[key]; ok {
			return true
		}
	}
	return false
}

func TestOpenAIQuotaAutoResetService_AssessesIndependentWindows(t *testing.T) {
	service := &OpenAIQuotaAutoResetService{}
	account := &Account{Extra: map[string]any{
		"auto_pause_5h_disabled": true,
		"auto_pause_7d_disabled": true,
	}}
	config := OpenAIAutoResetCreditConfig{Enabled: true, Threshold5h: 0.8, Threshold7d: 0.9}
	tests := []struct {
		name       string
		fiveHour   float64
		sevenDay   float64
		wantWindow string
	}{
		{name: "5h", fiveHour: 0.8, sevenDay: 0.2, wantWindow: "5h"},
		{name: "7d", fiveHour: 0.2, sevenDay: 0.9, wantWindow: "7d"},
		{name: "同时触发", fiveHour: 0.95, sevenDay: 0.95, wantWindow: "5h+7d"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assessment := service.buildAssessment(account, config, test.fiveHour, test.sevenDay)
			require.True(t, assessment.resetReached)
			require.Equal(t, test.wantWindow, assessment.triggerWindow)
		})
	}
}

type autoResetTestAccountRepo struct {
	AccountRepository
	mu             sync.Mutex
	account        *Account
	afterUpdate    func(map[string]any)
	respectContext bool
}

type autoResetSnapshotHookRepo struct {
	*autoResetTestAccountRepo
	getCalls           atomic.Int32
	afterSnapshot      func(int32)
	beforePreflightCAS func()
}

func (r *autoResetSnapshotHookRepo) GetByID(_ context.Context, id int64) (*Account, error) {
	r.mu.Lock()
	copy := *r.account
	copy.Extra = cloneOpenAIAutoResetExtra(r.account.Extra)
	r.mu.Unlock()
	call := r.getCalls.Add(1)
	if r.afterSnapshot != nil {
		r.afterSnapshot(call)
	}
	return &copy, nil
}

func (r *autoResetTestAccountRepo) GetByID(_ context.Context, id int64) (*Account, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	copy := *r.account
	copy.Extra = cloneOpenAIAutoResetExtra(r.account.Extra)
	return &copy, nil
}

func (r *autoResetTestAccountRepo) UpdateExtra(ctx context.Context, id int64, updates map[string]any) error {
	if r.respectContext && ctx.Err() != nil {
		return ctx.Err()
	}
	r.mu.Lock()
	if r.account.Extra == nil {
		r.account.Extra = make(map[string]any)
	}
	for key, value := range updates {
		r.account.Extra[key] = value
	}
	afterUpdate := r.afterUpdate
	r.mu.Unlock()
	if afterUpdate != nil {
		afterUpdate(updates)
	}
	return nil
}

func (r *autoResetTestAccountRepo) CompareAndUpdateOpenAIAutoResetPreflight(
	ctx context.Context,
	id int64,
	expectedState *OpenAIAutoResetCreditState,
	updates map[string]any,
) (bool, error) {
	if r.respectContext && ctx.Err() != nil {
		return false, ctx.Err()
	}
	r.mu.Lock()
	if r.account == nil || r.account.ID != id ||
		!isOpenAIAutoResetAccountEligible(r.account, ResolveOpenAIAutoResetCreditConfig(r.account)) {
		r.mu.Unlock()
		return false, nil
	}
	currentState := openAIAutoResetStateFromExtra(r.account.Extra)
	expectedJSON, err := json.Marshal(expectedState)
	if err != nil {
		r.mu.Unlock()
		return false, err
	}
	currentJSON, err := json.Marshal(currentState)
	if err != nil {
		r.mu.Unlock()
		return false, err
	}
	if string(currentJSON) != string(expectedJSON) {
		r.mu.Unlock()
		return false, nil
	}
	if r.account.Extra == nil {
		r.account.Extra = make(map[string]any)
	}
	for key, value := range updates {
		r.account.Extra[key] = value
	}
	afterUpdate := r.afterUpdate
	r.mu.Unlock()
	if afterUpdate != nil {
		afterUpdate(updates)
	}
	return true, nil
}

func (r *autoResetSnapshotHookRepo) CompareAndUpdateOpenAIAutoResetPreflight(
	ctx context.Context,
	id int64,
	expectedState *OpenAIAutoResetCreditState,
	updates map[string]any,
) (bool, error) {
	if r.beforePreflightCAS != nil {
		r.beforePreflightCAS()
	}
	return r.autoResetTestAccountRepo.CompareAndUpdateOpenAIAutoResetPreflight(ctx, id, expectedState, updates)
}

type autoResetTestQuota struct {
	usage         *OpenAIQuotaUsage
	usageSequence []*OpenAIQuotaUsage
	queryCalls    atomic.Int32
	onQuery       func(int32)
	queryErr      error
	cacheCalls    atomic.Int32
	resetCalls    atomic.Int32
	resetEntered  chan struct{}
	releaseReset  chan struct{}
	enterOnce     sync.Once
	mu            sync.Mutex
	resetArgs     [][2]string
	failFirst     bool
}

func (q *autoResetTestQuota) QueryUsage(context.Context, int64) (*OpenAIQuotaUsage, error) {
	call := q.queryCalls.Add(1)
	if q.onQuery != nil {
		q.onQuery(call)
	}
	if q.queryErr != nil {
		return nil, q.queryErr
	}
	usage := q.usage
	if len(q.usageSequence) > 0 {
		index := min(int(call)-1, len(q.usageSequence)-1)
		usage = q.usageSequence[index]
	}
	copy := *usage
	return &copy, nil
}

func (q *autoResetTestQuota) CacheResetCreditsSnapshot(context.Context, int64, *OpenAIRateLimitResetCredits) error {
	q.cacheCalls.Add(1)
	return nil
}

func (q *autoResetTestQuota) CachePostResetSnapshot(context.Context, int64, *OpenAIQuotaUsage) error {
	return nil
}

func (q *autoResetTestQuota) ResetCreditTargeted(_ context.Context, _ int64, creditID, redeemRequestID string) (*OpenAIQuotaResetResult, error) {
	if creditID == "" || redeemRequestID == "" {
		panic("targeted reset identifiers must be present")
	}
	call := q.resetCalls.Add(1)
	q.mu.Lock()
	q.resetArgs = append(q.resetArgs, [2]string{creditID, redeemRequestID})
	q.mu.Unlock()
	if q.failFirst && call == 1 {
		return nil, context.DeadlineExceeded
	}
	if q.resetEntered != nil {
		q.enterOnce.Do(func() { close(q.resetEntered) })
	}
	if q.releaseReset != nil {
		<-q.releaseReset
	}
	return &OpenAIQuotaResetResult{Code: "ok", WindowsReset: 2}, nil
}

type autoResetTestRecoverer struct{}

func (autoResetTestRecoverer) RecoverAccountState(context.Context, int64, AccountRecoveryOptions) (*SuccessfulTestRecoveryResult, error) {
	return &SuccessfulTestRecoveryResult{ClearedRateLimit: true}, nil
}

type autoResetHookedIdempotencyRepo struct {
	IdempotencyRepository
	afterCreate func()
}

type autoResetMarkSucceededFailRepo struct {
	IdempotencyRepository
}

func (r *autoResetMarkSucceededFailRepo) MarkSucceeded(context.Context, int64, int, string, time.Time) error {
	return errors.New("mark succeeded unavailable")
}

func (r *autoResetHookedIdempotencyRepo) CreateProcessing(ctx context.Context, record *IdempotencyRecord) (bool, error) {
	owner, err := r.IdempotencyRepository.CreateProcessing(ctx, record)
	if err == nil && r.afterCreate != nil {
		r.afterCreate()
	}
	return owner, err
}

func TestOpenAIQuotaAutoResetService_ConcurrentInstancesConsumeOnce(t *testing.T) {
	now := time.Now().UTC()
	account := &Account{
		ID: 99, Platform: PlatformOpenAI, Type: AccountTypeOAuth,
		Status: StatusActive, Schedulable: true,
		Extra: map[string]any{
			OpenAIAutoResetCreditEnabledExtraKey:     true,
			OpenAIAutoResetCredit5hThresholdExtraKey: 1.0,
			OpenAIAutoResetCredit7dThresholdExtraKey: 1.0,
			"codex_5h_used_percent":                  100.0,
			"codex_7d_used_percent":                  10.0,
			"codex_usage_updated_at":                 now.Format(time.RFC3339),
			"codex_5h_reset_at":                      now.Add(time.Hour).Format(time.RFC3339),
			"codex_7d_reset_at":                      now.Add(24 * time.Hour).Format(time.RFC3339),
		},
	}
	repo := &autoResetTestAccountRepo{account: account}
	usage := &OpenAIQuotaUsage{
		FetchedAt: now.Unix(),
		RateLimit: &OpenAIRateLimit{
			PrimaryWindow:   &OpenAIRateLimitWindow{UsedPercent: 100, LimitWindowSeconds: 5 * 60 * 60, ResetAfterSeconds: 3600, ResetAt: now.Add(time.Hour).Unix()},
			SecondaryWindow: &OpenAIRateLimitWindow{UsedPercent: 10, LimitWindowSeconds: 7 * 24 * 60 * 60, ResetAfterSeconds: 86400, ResetAt: now.Add(24 * time.Hour).Unix()},
		},
		RateLimitResetCredits: &OpenAIRateLimitResetCredits{
			AvailableCount: 1,
			Credits:        []OpenAIRateLimitResetCreditDetail{{ExpiresAt: now.Add(48 * time.Hour).Format(time.RFC3339)}},
		},
		autoResetCandidates: []openAIAutoResetCreditCandidate{{ID: "credit-sensitive-id", ExpiresAt: now.Add(48 * time.Hour).Format(time.RFC3339)}},
	}
	quota := &autoResetTestQuota{usage: usage, resetEntered: make(chan struct{}), releaseReset: make(chan struct{})}
	idempotencyRepo := newInMemoryIdempotencyRepo()
	config := DefaultIdempotencyConfig()
	config.ObserveOnly = false
	config.ProcessingTimeout = time.Second
	serviceA := NewOpenAIQuotaAutoResetService(repo, quota, autoResetTestRecoverer{}, NewIdempotencyCoordinator(idempotencyRepo, config), nil, nil, nil)
	serviceB := NewOpenAIQuotaAutoResetService(repo, quota, autoResetTestRecoverer{}, NewIdempotencyCoordinator(idempotencyRepo, config), nil, nil, nil)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_ = serviceA.evaluateAccount(context.Background(), account.ID)
	}()
	<-quota.resetEntered
	go func() {
		defer wg.Done()
		_ = serviceB.evaluateAccount(context.Background(), account.ID)
	}()
	time.Sleep(50 * time.Millisecond)
	close(quota.releaseReset)
	wg.Wait()

	require.Equal(t, int32(1), quota.resetCalls.Load())
	repo.mu.Lock()
	state := openAIAutoResetStateFromExtra(repo.account.Extra)
	repo.mu.Unlock()
	require.NotNil(t, state)
	require.Equal(t, OpenAIAutoResetStatusSuccess, state.Status)
	encodedState, err := json.Marshal(state)
	require.NoError(t, err)
	require.NotContains(t, string(encodedState), "credit-sensitive-id")
}

func TestOpenAIQuotaAutoResetService_StaleInstanceCannotOverwriteCompletedAttempt(t *testing.T) {
	now := time.Now().UTC()
	account := newAutoResetTestAccount(101, now)
	sharedRepo := &autoResetTestAccountRepo{account: account}
	bReadOldState := make(chan struct{})
	releaseB := make(chan struct{})
	bRepo := &autoResetSnapshotHookRepo{autoResetTestAccountRepo: sharedRepo}
	bRepo.afterSnapshot = func(call int32) {
		if call != 1 {
			return
		}
		close(bReadOldState)
		<-releaseB
	}

	aInitial := newAutoResetTestUsage(now, 100, 1)
	aInitial.autoResetCandidates[0].ID = "credit-a"
	aFresh := newAutoResetTestUsage(now.Add(time.Second), 0, 0)
	bStale := newAutoResetTestUsage(now, 100, 1)
	bStale.autoResetCandidates[0].ID = "credit-b"
	aQuota := &autoResetTestQuota{usageSequence: []*OpenAIQuotaUsage{aInitial, aFresh}}
	bQuota := &autoResetTestQuota{usage: bStale}
	idempotencyRepo := newInMemoryIdempotencyRepo()
	idempotencyConfig := DefaultIdempotencyConfig()
	idempotencyConfig.ObserveOnly = false
	idempotencyConfig.FailedRetryBackoff = 0
	serviceA := NewOpenAIQuotaAutoResetService(sharedRepo, aQuota, autoResetTestRecoverer{}, NewIdempotencyCoordinator(idempotencyRepo, idempotencyConfig), nil, nil, nil)
	serviceB := NewOpenAIQuotaAutoResetService(bRepo, bQuota, autoResetTestRecoverer{}, NewIdempotencyCoordinator(idempotencyRepo, idempotencyConfig), nil, nil, nil)

	bDone := make(chan error, 1)
	go func() {
		bDone <- serviceB.evaluateAccount(context.Background(), account.ID)
	}()
	<-bReadOldState
	require.NoError(t, serviceA.evaluateAccount(context.Background(), account.ID))
	close(releaseB)
	require.NoError(t, <-bDone)

	require.Equal(t, int32(1), aQuota.resetCalls.Load())
	require.Zero(t, bQuota.resetCalls.Load())
	sharedRepo.mu.Lock()
	stored := *sharedRepo.account
	stored.Extra = cloneOpenAIAutoResetExtra(sharedRepo.account.Extra)
	sharedRepo.mu.Unlock()
	state := openAIAutoResetStateFromExtra(stored.Extra)
	require.NotNil(t, state)
	require.Equal(t, OpenAIAutoResetStatusSuccess, state.Status)
	require.Equal(t, shortOpenAIAutoResetHash("credit-a"), state.AttemptCreditHash)
	require.Equal(t, 0.0, stored.Extra["codex_5h_used_percent"], "stale instance must not overwrite post-reset usage")
}

func TestOpenAIQuotaAutoResetService_StaleQueryErrorPreservesCompletedAttempt(t *testing.T) {
	now := time.Now().UTC()
	account := newAutoResetTestAccount(103, now)
	sharedRepo := &autoResetTestAccountRepo{account: account}
	bReadOldState := make(chan struct{})
	releaseB := make(chan struct{})
	bRepo := &autoResetSnapshotHookRepo{autoResetTestAccountRepo: sharedRepo}
	bRepo.afterSnapshot = func(call int32) {
		if call != 1 {
			return
		}
		close(bReadOldState)
		<-releaseB
	}

	aInitial := newAutoResetTestUsage(now, 100, 1)
	aInitial.autoResetCandidates[0].ID = "credit-a"
	aFresh := newAutoResetTestUsage(now.Add(time.Second), 0, 0)
	aQuota := &autoResetTestQuota{usageSequence: []*OpenAIQuotaUsage{aInitial, aFresh}}
	bQuota := &autoResetTestQuota{queryErr: context.DeadlineExceeded}
	idempotencyRepo := newInMemoryIdempotencyRepo()
	idempotencyConfig := DefaultIdempotencyConfig()
	idempotencyConfig.ObserveOnly = false
	serviceA := NewOpenAIQuotaAutoResetService(sharedRepo, aQuota, autoResetTestRecoverer{}, NewIdempotencyCoordinator(idempotencyRepo, idempotencyConfig), nil, nil, nil)
	serviceB := NewOpenAIQuotaAutoResetService(bRepo, bQuota, autoResetTestRecoverer{}, NewIdempotencyCoordinator(idempotencyRepo, idempotencyConfig), nil, nil, nil)

	bDone := make(chan error, 1)
	go func() { bDone <- serviceB.evaluateAccount(context.Background(), account.ID) }()
	<-bReadOldState
	require.NoError(t, serviceA.evaluateAccount(context.Background(), account.ID))
	close(releaseB)
	require.ErrorIs(t, <-bDone, context.DeadlineExceeded)

	sharedRepo.mu.Lock()
	state := openAIAutoResetStateFromExtra(sharedRepo.account.Extra)
	sharedRepo.mu.Unlock()
	require.NotNil(t, state)
	require.Equal(t, OpenAIAutoResetStatusSuccess, state.Status)
	require.Equal(t, shortOpenAIAutoResetHash("credit-a"), state.AttemptCreditHash)
}

func TestOpenAIQuotaAutoResetService_DifferentCandidatesShareCycleIdempotency(t *testing.T) {
	now := time.Now().UTC()
	account := newAutoResetTestAccount(102, now)
	sharedRepo := &autoResetTestAccountRepo{account: account}
	secondSnapshots := make(chan struct{}, 2)
	releaseSnapshots := make(chan struct{})
	newRepo := func() *autoResetSnapshotHookRepo {
		repo := &autoResetSnapshotHookRepo{autoResetTestAccountRepo: sharedRepo}
		repo.afterSnapshot = func(call int32) {
			if call != 2 {
				return
			}
			secondSnapshots <- struct{}{}
			<-releaseSnapshots
		}
		return repo
	}

	width := int64(openAIAutoResetBucketWidth)
	firstFetchedAt := now.Unix()
	firstResetAt := firstFetchedAt + 3600
	firstFetchedAt += (width - 1 - firstResetAt%width + width) % width
	firstFetched := time.Unix(firstFetchedAt, 0).UTC()
	usageA := newAutoResetTestUsage(firstFetched, 100, 1)
	usageA.autoResetCandidates[0].ID = "credit-a"
	usageB := newAutoResetTestUsage(firstFetched.Add(time.Second), 100, 1)
	usageB.autoResetCandidates[0].ID = "credit-b"
	require.NotEqual(t, openAIAutoResetCycleHash(usageA, nil), openAIAutoResetCycleHash(usageB, nil))
	require.True(t, autoResetLockKeysOverlap(
		openAIAutoResetCycleLockKeys(account.ID, usageA),
		openAIAutoResetCycleLockKeys(account.ID, usageB),
	))
	fresh := newAutoResetTestUsage(firstFetched.Add(2*time.Second), 0, 0)
	quotaA := &autoResetTestQuota{usageSequence: []*OpenAIQuotaUsage{usageA, fresh}}
	quotaB := &autoResetTestQuota{usageSequence: []*OpenAIQuotaUsage{usageB, fresh}}
	idempotencyRepo := newInMemoryIdempotencyRepo()
	idempotencyConfig := DefaultIdempotencyConfig()
	idempotencyConfig.ObserveOnly = false
	idempotencyConfig.FailedRetryBackoff = 0
	serviceA := NewOpenAIQuotaAutoResetService(newRepo(), quotaA, autoResetTestRecoverer{}, NewIdempotencyCoordinator(idempotencyRepo, idempotencyConfig), nil, nil, nil)
	serviceB := NewOpenAIQuotaAutoResetService(newRepo(), quotaB, autoResetTestRecoverer{}, NewIdempotencyCoordinator(idempotencyRepo, idempotencyConfig), nil, nil, nil)

	errs := make(chan error, 2)
	go func() { errs <- serviceA.evaluateAccount(context.Background(), account.ID) }()
	go func() { errs <- serviceB.evaluateAccount(context.Background(), account.ID) }()
	<-secondSnapshots
	<-secondSnapshots
	close(releaseSnapshots)
	require.NoError(t, <-errs)
	require.NoError(t, <-errs)
	require.Equal(t, int32(1), quotaA.resetCalls.Load()+quotaB.resetCalls.Load())

	sharedRepo.mu.Lock()
	state := openAIAutoResetStateFromExtra(sharedRepo.account.Extra)
	sharedRepo.mu.Unlock()
	require.NotNil(t, state)
	require.Equal(t, OpenAIAutoResetStatusSuccess, state.Status)
}

func TestOpenAIQuotaAutoResetService_SucceededReplayHasNoSideEffects(t *testing.T) {
	now := time.Now().UTC()
	account := newAutoResetTestAccount(105, now)
	sharedRepo := &autoResetTestAccountRepo{account: account}
	bReadyToExecute := make(chan struct{})
	releaseB := make(chan struct{})
	bRepo := &autoResetSnapshotHookRepo{autoResetTestAccountRepo: sharedRepo}
	bRepo.afterSnapshot = func(call int32) {
		if call != 3 {
			return
		}
		close(bReadyToExecute)
		<-releaseB
	}

	initial := newAutoResetTestUsage(now, 100, 1)
	initial.autoResetCandidates[0].ID = "credit-a"
	fresh := newAutoResetTestUsage(now.Add(time.Second), 0, 0)
	aQuota := &autoResetTestQuota{usageSequence: []*OpenAIQuotaUsage{initial, fresh}}
	bStale := newAutoResetTestUsage(now, 100, 1)
	bStale.autoResetCandidates[0].ID = "credit-a"
	bQuota := &autoResetTestQuota{usage: bStale}
	idempotencyRepo := newInMemoryIdempotencyRepo()
	idempotencyConfig := DefaultIdempotencyConfig()
	idempotencyConfig.ObserveOnly = false
	serviceA := NewOpenAIQuotaAutoResetService(sharedRepo, aQuota, autoResetTestRecoverer{}, NewIdempotencyCoordinator(idempotencyRepo, idempotencyConfig), nil, nil, nil)
	serviceB := NewOpenAIQuotaAutoResetService(bRepo, bQuota, autoResetTestRecoverer{}, NewIdempotencyCoordinator(idempotencyRepo, idempotencyConfig), nil, nil, nil)

	bDone := make(chan error, 1)
	go func() { bDone <- serviceB.evaluateAccount(context.Background(), account.ID) }()
	<-bReadyToExecute
	require.NoError(t, serviceA.evaluateAccount(context.Background(), account.ID))
	close(releaseB)
	require.NoError(t, <-bDone)

	require.Equal(t, int32(1), aQuota.resetCalls.Load())
	require.Zero(t, bQuota.resetCalls.Load())
	require.Equal(t, int32(1), bQuota.queryCalls.Load(), "replay must not run post-reset QueryUsage")
	sharedRepo.mu.Lock()
	stored := *sharedRepo.account
	stored.Extra = cloneOpenAIAutoResetExtra(sharedRepo.account.Extra)
	sharedRepo.mu.Unlock()
	require.Equal(t, 0.0, stored.Extra["codex_5h_used_percent"])
	state := openAIAutoResetStateFromExtra(stored.Extra)
	require.NotNil(t, state)
	require.Equal(t, OpenAIAutoResetStatusSuccess, state.Status)
	require.Equal(t, shortOpenAIAutoResetHash("credit-a"), state.AttemptCreditHash)
}

func TestOpenAIQuotaAutoResetService_MarkSucceededFailurePreservesTerminalState(t *testing.T) {
	now := time.Now().UTC()
	account := newAutoResetTestAccount(107, now)
	repo := &autoResetTestAccountRepo{account: account}
	initial := newAutoResetTestUsage(now, 100, 1)
	fresh := newAutoResetTestUsage(now.Add(time.Second), 0, 0)
	quota := &autoResetTestQuota{usageSequence: []*OpenAIQuotaUsage{initial, fresh}}
	idempotencyConfig := DefaultIdempotencyConfig()
	idempotencyConfig.ObserveOnly = false
	idempotencyRepo := &autoResetMarkSucceededFailRepo{IdempotencyRepository: newInMemoryIdempotencyRepo()}
	service := NewOpenAIQuotaAutoResetService(
		repo,
		quota,
		autoResetTestRecoverer{},
		NewIdempotencyCoordinator(idempotencyRepo, idempotencyConfig),
		nil, nil, nil,
	)

	require.NoError(t, service.evaluateAccount(context.Background(), account.ID))
	require.Equal(t, int32(1), quota.resetCalls.Load())
	repo.mu.Lock()
	stored := *repo.account
	stored.Extra = cloneOpenAIAutoResetExtra(repo.account.Extra)
	repo.mu.Unlock()
	require.Equal(t, 0.0, stored.Extra["codex_5h_used_percent"])
	state := openAIAutoResetStateFromExtra(stored.Extra)
	require.NotNil(t, state)
	require.Equal(t, OpenAIAutoResetStatusSuccess, state.Status)
}

func TestOpenAIQuotaAutoResetService_NonOwnerCannotOverwritePostResetUsage(t *testing.T) {
	now := time.Now().UTC()
	account := newAutoResetTestAccount(104, now)
	sharedRepo := &autoResetTestAccountRepo{account: account}
	bReadLatestState := make(chan struct{})
	releaseB := make(chan struct{})
	bRepo := &autoResetSnapshotHookRepo{autoResetTestAccountRepo: sharedRepo}
	bRepo.afterSnapshot = func(call int32) {
		if call != 2 {
			return
		}
		close(bReadLatestState)
		<-releaseB
	}

	aInitial := newAutoResetTestUsage(now, 100, 1)
	aInitial.autoResetCandidates[0].ID = "credit-a"
	aFresh := newAutoResetTestUsage(now.Add(time.Second), 0, 0)
	bStale := newAutoResetTestUsage(now, 100, 1)
	bStale.autoResetCandidates[0].ID = "credit-b"
	aQuota := &autoResetTestQuota{usageSequence: []*OpenAIQuotaUsage{aInitial, aFresh}}
	bQuota := &autoResetTestQuota{usage: bStale}
	idempotencyRepo := newInMemoryIdempotencyRepo()
	idempotencyConfig := DefaultIdempotencyConfig()
	idempotencyConfig.ObserveOnly = false
	serviceA := NewOpenAIQuotaAutoResetService(sharedRepo, aQuota, autoResetTestRecoverer{}, NewIdempotencyCoordinator(idempotencyRepo, idempotencyConfig), nil, nil, nil)
	serviceB := NewOpenAIQuotaAutoResetService(bRepo, bQuota, autoResetTestRecoverer{}, NewIdempotencyCoordinator(idempotencyRepo, idempotencyConfig), nil, nil, nil)

	bDone := make(chan error, 1)
	go func() { bDone <- serviceB.evaluateAccount(context.Background(), account.ID) }()
	<-bReadLatestState
	require.NoError(t, serviceA.evaluateAccount(context.Background(), account.ID))
	close(releaseB)
	require.NoError(t, <-bDone)

	require.Equal(t, int32(1), aQuota.resetCalls.Load())
	require.Zero(t, bQuota.resetCalls.Load())
	sharedRepo.mu.Lock()
	stored := *sharedRepo.account
	stored.Extra = cloneOpenAIAutoResetExtra(sharedRepo.account.Extra)
	sharedRepo.mu.Unlock()
	require.Equal(t, 0.0, stored.Extra["codex_5h_used_percent"])
	state := openAIAutoResetStateFromExtra(stored.Extra)
	require.NotNil(t, state)
	require.Equal(t, OpenAIAutoResetStatusSuccess, state.Status)
	require.Equal(t, shortOpenAIAutoResetHash("credit-a"), state.AttemptCreditHash)
}

func TestOpenAIQuotaAutoResetService_PreflightCASCannotOverwritePostResetUsage(t *testing.T) {
	now := time.Now().UTC()
	account := newAutoResetTestAccount(106, now)
	sharedRepo := &autoResetTestAccountRepo{account: account}
	bReadyToPersist := make(chan struct{})
	releaseB := make(chan struct{})
	bRepo := &autoResetSnapshotHookRepo{autoResetTestAccountRepo: sharedRepo}
	bRepo.beforePreflightCAS = func() {
		close(bReadyToPersist)
		<-releaseB
	}

	aInitial := newAutoResetTestUsage(now, 100, 1)
	aFresh := newAutoResetTestUsage(now.Add(time.Second), 0, 0)
	bStale := newAutoResetTestUsage(now, 50, 1)
	aQuota := &autoResetTestQuota{usageSequence: []*OpenAIQuotaUsage{aInitial, aFresh}}
	bQuota := &autoResetTestQuota{usage: bStale}
	serviceA := newAutoResetTestService(sharedRepo, aQuota)
	serviceB := newAutoResetTestService(bRepo, bQuota)

	bDone := make(chan error, 1)
	go func() { bDone <- serviceB.evaluateAccount(context.Background(), account.ID) }()
	<-bReadyToPersist
	require.NoError(t, serviceA.evaluateAccount(context.Background(), account.ID))
	close(releaseB)
	require.NoError(t, <-bDone)

	require.Equal(t, int32(1), aQuota.resetCalls.Load())
	require.Zero(t, bQuota.resetCalls.Load())
	sharedRepo.mu.Lock()
	stored := *sharedRepo.account
	stored.Extra = cloneOpenAIAutoResetExtra(sharedRepo.account.Extra)
	sharedRepo.mu.Unlock()
	require.Equal(t, 0.0, stored.Extra["codex_5h_used_percent"])
	state := openAIAutoResetStateFromExtra(stored.Extra)
	require.NotNil(t, state)
	require.Equal(t, OpenAIAutoResetStatusSuccess, state.Status)
}

func TestOpenAIQuotaAutoResetService_PreflightCASIncludesResetCreditSnapshot(t *testing.T) {
	now := time.Now().UTC()
	account := newAutoResetTestAccount(108, now)
	repo := &autoResetTestAccountRepo{account: account}
	usage := newAutoResetTestUsage(now, 50, 1)
	quota := &autoResetTestQuota{usage: usage}
	service := newAutoResetTestService(repo, quota)

	require.NoError(t, service.evaluateAccount(context.Background(), account.ID))
	require.Zero(t, quota.cacheCalls.Load(), "preflight must not perform a second naked cache write")
	repo.mu.Lock()
	storedCredits := repo.account.Extra[openaiQuotaResetCreditsKey]
	state := openAIAutoResetStateFromExtra(repo.account.Extra)
	repo.mu.Unlock()
	require.Equal(t, usage.RateLimitResetCredits, storedCredits)
	require.NotNil(t, state)
	require.Equal(t, OpenAIAutoResetStatusAvailable, state.Status)
}

func TestOpenAIQuotaAutoResetService_PreflightIncompleteCreditsPreserveCache(t *testing.T) {
	now := time.Now().UTC()
	account := newAutoResetTestAccount(109, now)
	oldCredits := &OpenAIRateLimitResetCredits{
		AvailableCount: 1,
		Credits:        []OpenAIRateLimitResetCreditDetail{{ExpiresAt: now.Add(24 * time.Hour).Format(time.RFC3339)}},
	}
	account.Extra[openaiQuotaResetCreditsKey] = oldCredits
	repo := &autoResetTestAccountRepo{account: account}
	usage := newAutoResetTestUsage(now, 50, 1)
	usage.RateLimitResetCredits.Credits = nil
	quota := &autoResetTestQuota{usage: usage}
	service := newAutoResetTestService(repo, quota)

	err := service.evaluateAccount(context.Background(), account.ID)
	require.Error(t, err)
	require.Equal(t, "OPENAI_QUOTA_RESET_CREDITS_REFRESH_FAILED", infraerrors.Reason(err))
	require.Zero(t, quota.cacheCalls.Load())
	repo.mu.Lock()
	storedCredits := repo.account.Extra[openaiQuotaResetCreditsKey]
	state := openAIAutoResetStateFromExtra(repo.account.Extra)
	repo.mu.Unlock()
	require.Equal(t, oldCredits, storedCredits)
	require.Nil(t, state)
}

func TestOpenAIQuotaAutoResetService_TimeoutRetryReusesRequestBody(t *testing.T) {
	now := time.Now().UTC()
	account := &Account{
		ID: 100, Platform: PlatformOpenAI, Type: AccountTypeOAuth,
		Status: StatusActive, Schedulable: true,
		Extra: map[string]any{
			OpenAIAutoResetCreditEnabledExtraKey:     true,
			OpenAIAutoResetCredit5hThresholdExtraKey: 1.0,
			OpenAIAutoResetCredit7dThresholdExtraKey: 1.0,
			"codex_5h_used_percent":                  100.0,
			"codex_usage_updated_at":                 now.Format(time.RFC3339),
			"codex_5h_reset_at":                      now.Add(time.Hour).Format(time.RFC3339),
		},
	}
	repo := &autoResetTestAccountRepo{account: account}
	expiresAt := now.Add(48 * time.Hour).Format(time.RFC3339)
	firstUsage := &OpenAIQuotaUsage{
		FetchedAt: now.Unix(),
		RateLimit: &OpenAIRateLimit{
			PrimaryWindow: &OpenAIRateLimitWindow{UsedPercent: 100, LimitWindowSeconds: 5 * 60 * 60, ResetAfterSeconds: 3600},
		},
		RateLimitResetCredits: &OpenAIRateLimitResetCredits{
			AvailableCount: 1,
			Credits:        []OpenAIRateLimitResetCreditDetail{{ExpiresAt: expiresAt}},
		},
		autoResetCandidates: []openAIAutoResetCreditCandidate{{ID: "retry-credit", ExpiresAt: expiresAt}},
	}
	secondUsage := *firstUsage
	secondUsage.FetchedAt++
	quota := &autoResetTestQuota{
		failFirst:     true,
		usageSequence: []*OpenAIQuotaUsage{firstUsage, &secondUsage},
	}
	idempotencyConfig := DefaultIdempotencyConfig()
	idempotencyConfig.ObserveOnly = false
	idempotencyConfig.FailedRetryBackoff = 0
	service := NewOpenAIQuotaAutoResetService(
		repo,
		quota,
		autoResetTestRecoverer{},
		NewIdempotencyCoordinator(newInMemoryIdempotencyRepo(), idempotencyConfig),
		nil, nil, nil,
	)

	require.Error(t, service.evaluateAccount(context.Background(), account.ID))
	require.NoError(t, service.evaluateAccount(context.Background(), account.ID))
	quota.mu.Lock()
	args := append([][2]string(nil), quota.resetArgs...)
	quota.mu.Unlock()
	require.Len(t, args, 2)
	require.Equal(t, args[0], args[1], "ResetAt 缺失且推算周期差一秒时必须复用相同 credit_id 与 redeem_request_id")
}

func TestOpenAIQuotaAutoResetService_RevalidatesCompleteEligibilityAfterQuery(t *testing.T) {
	parentID := int64(900)
	tests := []struct {
		name   string
		mutate func(*Account)
	}{
		{name: "inactive", mutate: func(account *Account) { account.Status = StatusDisabled }},
		{name: "unschedulable", mutate: func(account *Account) { account.Schedulable = false }},
		{name: "non openai", mutate: func(account *Account) { account.Platform = PlatformAnthropic }},
		{name: "non oauth", mutate: func(account *Account) { account.Type = AccountTypeSetupToken }},
		{name: "shadow", mutate: func(account *Account) { account.ParentAccountID = &parentID }},
		{name: "disabled", mutate: func(account *Account) { account.Extra[OpenAIAutoResetCreditEnabledExtraKey] = false }},
	}

	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			now := time.Now().UTC()
			account := newAutoResetTestAccount(int64(200+index), now)
			repo := &autoResetTestAccountRepo{account: account}
			quota := &autoResetTestQuota{usage: newAutoResetTestUsage(now, 100, 1)}
			quota.onQuery = func(call int32) {
				if call != 1 {
					return
				}
				repo.mu.Lock()
				test.mutate(repo.account)
				repo.mu.Unlock()
			}
			service := newAutoResetTestService(repo, quota)

			require.NoError(t, service.evaluateAccount(context.Background(), account.ID))
			require.Zero(t, quota.resetCalls.Load())
		})
	}
}

func TestOpenAIQuotaAutoResetService_RevalidatesBeforeIdempotentConsumption(t *testing.T) {
	now := time.Now().UTC()
	account := newAutoResetTestAccount(300, now)
	repo := &autoResetTestAccountRepo{account: account}
	quota := &autoResetTestQuota{usage: newAutoResetTestUsage(now, 100, 1)}
	repo.afterUpdate = func(updates map[string]any) {
		state, ok := updates[OpenAIAutoResetCreditStateExtraKey].(*OpenAIAutoResetCreditState)
		if !ok || state.Status != OpenAIAutoResetStatusResetting {
			return
		}
		repo.mu.Lock()
		repo.account.Schedulable = false
		repo.mu.Unlock()
	}
	service := newAutoResetTestService(repo, quota)

	require.NoError(t, service.evaluateAccount(context.Background(), account.ID))
	require.Zero(t, quota.resetCalls.Load())
}

func TestOpenAIQuotaAutoResetService_RevalidatesInsideIdempotentCallback(t *testing.T) {
	now := time.Now().UTC()
	account := newAutoResetTestAccount(350, now)
	repo := &autoResetTestAccountRepo{account: account}
	quota := &autoResetTestQuota{usage: newAutoResetTestUsage(now, 100, 1)}
	idempotencyRepo := &autoResetHookedIdempotencyRepo{IdempotencyRepository: newInMemoryIdempotencyRepo()}
	idempotencyRepo.afterCreate = func() {
		repo.mu.Lock()
		repo.account.Status = StatusDisabled
		repo.mu.Unlock()
	}
	idempotencyConfig := DefaultIdempotencyConfig()
	idempotencyConfig.ObserveOnly = false
	idempotencyConfig.FailedRetryBackoff = 0
	service := NewOpenAIQuotaAutoResetService(
		repo,
		quota,
		autoResetTestRecoverer{},
		NewIdempotencyCoordinator(idempotencyRepo, idempotencyConfig),
		nil, nil, nil,
	)

	require.NoError(t, service.evaluateAccount(context.Background(), account.ID))
	require.Zero(t, quota.resetCalls.Load())
}

func TestOpenAIQuotaAutoResetService_PersistsPostResetUsageSnapshot(t *testing.T) {
	now := time.Now().UTC()
	account := newAutoResetTestAccount(400, now)
	account.Extra["auto_pause_5h_threshold"] = 0.8
	account.Extra["auto_pause_7d_disabled"] = true
	account.Extra["codex_usage_updated_at"] = now.Add(-time.Hour).Format(time.RFC3339)
	repo := &autoResetTestAccountRepo{account: account}
	stale := newAutoResetTestUsage(now, 100, 1)
	fresh := newAutoResetTestUsage(now.Add(time.Second), 0, 0)
	quota := &autoResetTestQuota{usageSequence: []*OpenAIQuotaUsage{stale, fresh}}
	service := newAutoResetTestService(repo, quota)

	require.NoError(t, service.evaluateAccount(context.Background(), account.ID))
	require.Equal(t, int32(1), quota.resetCalls.Load())

	repo.mu.Lock()
	stored := *repo.account
	stored.Extra = cloneOpenAIAutoResetExtra(repo.account.Extra)
	repo.mu.Unlock()
	require.Equal(t, 0.0, stored.Extra["codex_5h_used_percent"])
	state := openAIAutoResetStateFromExtra(stored.Extra)
	require.NotNil(t, state)
	require.Equal(t, OpenAIAutoResetStatusSuccess, state.Status)
	paused, _ := shouldAutoPauseOpenAIAccountByQuota(context.Background(), &stored)
	require.False(t, paused, "post-reset 的新额度快照应替换旧 100% 快照")
}

func TestOpenAIQuotaAutoResetService_CompletionPersistsAfterWorkerContextCanceled(t *testing.T) {
	now := time.Now().UTC()
	account := newAutoResetTestAccount(401, now)
	repo := &autoResetTestAccountRepo{account: account, respectContext: true}
	stale := newAutoResetTestUsage(now, 100, 1)
	fresh := newAutoResetTestUsage(now.Add(time.Second), 0, 0)
	ctx, cancel := context.WithCancel(context.Background())
	quota := &autoResetTestQuota{usageSequence: []*OpenAIQuotaUsage{stale, fresh}}
	quota.onQuery = func(call int32) {
		if call == 2 {
			cancel()
		}
	}
	service := newAutoResetTestService(repo, quota)

	require.NoError(t, service.evaluateAccount(ctx, account.ID))
	repo.mu.Lock()
	state := openAIAutoResetStateFromExtra(repo.account.Extra)
	repo.mu.Unlock()
	require.NotNil(t, state)
	require.Equal(t, OpenAIAutoResetStatusSuccess, state.Status)
}

func newAutoResetTestAccount(id int64, now time.Time) *Account {
	return &Account{
		ID: id, Platform: PlatformOpenAI, Type: AccountTypeOAuth,
		Status: StatusActive, Schedulable: true,
		Extra: map[string]any{
			OpenAIAutoResetCreditEnabledExtraKey:     true,
			OpenAIAutoResetCredit5hThresholdExtraKey: 1.0,
			OpenAIAutoResetCredit7dThresholdExtraKey: 1.0,
			"codex_5h_used_percent":                  100.0,
			"codex_usage_updated_at":                 now.Format(time.RFC3339),
			"codex_5h_reset_at":                      now.Add(time.Hour).Format(time.RFC3339),
		},
	}
}

func newAutoResetTestUsage(now time.Time, usedPercent float64, available int) *OpenAIQuotaUsage {
	expiresAt := now.Add(48 * time.Hour).Format(time.RFC3339)
	usage := &OpenAIQuotaUsage{
		FetchedAt: now.Unix(),
		RateLimit: &OpenAIRateLimit{
			PrimaryWindow: &OpenAIRateLimitWindow{
				UsedPercent: usedPercent, LimitWindowSeconds: 5 * 60 * 60,
				ResetAfterSeconds: 3600,
			},
		},
		RateLimitResetCredits: &OpenAIRateLimitResetCredits{AvailableCount: available},
	}
	if available > 0 {
		usage.RateLimitResetCredits.Credits = []OpenAIRateLimitResetCreditDetail{{ExpiresAt: expiresAt}}
		usage.autoResetCandidates = []openAIAutoResetCreditCandidate{{ID: "test-credit", ExpiresAt: expiresAt}}
	}
	return usage
}

func newAutoResetTestService(repo AccountRepository, quota openAIAutoResetQuota) *OpenAIQuotaAutoResetService {
	config := DefaultIdempotencyConfig()
	config.ObserveOnly = false
	config.FailedRetryBackoff = 0
	return NewOpenAIQuotaAutoResetService(
		repo,
		quota,
		autoResetTestRecoverer{},
		NewIdempotencyCoordinator(newInMemoryIdempotencyRepo(), config),
		nil, nil, nil,
	)
}
