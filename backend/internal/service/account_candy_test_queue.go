package service

import (
	"context"
	"errors"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// AccountCandyTestService owns only benchmark jobs; it has no account mutation,
// billing, telemetry, rate-limit or gateway scheduling dependency.
type AccountCandyTestService struct {
	repo      CandyTestRepository
	executor  CandyTestExecutor
	startOnce sync.Once
	stopOnce  sync.Once
	ctx       context.Context
	cancel    context.CancelFunc
	wg        sync.WaitGroup
}

func NewAccountCandyTestService(repo CandyTestRepository, executor CandyTestExecutor) *AccountCandyTestService {
	ctx, cancel := context.WithCancel(context.Background())
	return &AccountCandyTestService{repo: repo, executor: executor, ctx: ctx, cancel: cancel}
}

func candyAccountIDs(ids []int64) ([]int64, error) {
	if len(ids) == 0 || len(ids) > 1000 {
		return nil, ErrCandyTestInvalidRequest
	}
	seen := make(map[int64]bool, len(ids))
	out := make([]int64, 0, len(ids))
	for _, id := range ids {
		if id <= 0 {
			return nil, ErrCandyTestInvalidRequest
		}
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out, nil
}

func (s *AccountCandyTestService) Options(ctx context.Context, ids []int64) (*CandyTestOptions, error) {
	ids, err := candyAccountIDs(ids)
	if err != nil {
		return nil, err
	}
	return s.executor.Options(ctx, ids)
}

func (s *AccountCandyTestService) Create(ctx context.Context, request *CandyTestCreateRequest) (*CandyTestBatch, error) {
	if request == nil {
		return nil, ErrCandyTestInvalidRequest
	}
	copyRequest := *request
	var err error
	copyRequest.AccountIDs, err = candyAccountIDs(request.AccountIDs)
	if err != nil {
		return nil, err
	}
	copyRequest.Model = strings.TrimSpace(request.Model)
	copyRequest.ReasoningEffort = strings.TrimSpace(request.ReasoningEffort)
	copyRequest.IdempotencyKey = strings.TrimSpace(request.IdempotencyKey)
	if copyRequest.Model == "" || len(copyRequest.Model) > 256 || len(copyRequest.ReasoningEffort) > 32 || copyRequest.IdempotencyKey == "" || len(copyRequest.IdempotencyKey) > 128 {
		return nil, ErrCandyTestInvalidRequest
	}
	options, err := s.executor.Options(ctx, copyRequest.AccountIDs)
	if err != nil {
		return nil, err
	}
	if options == nil {
		return nil, ErrCandyTestInvalidRequest
	}
	byAccount := make(map[int64]CandyTestAccountOptions, len(options.Accounts))
	for _, account := range options.Accounts {
		byAccount[account.AccountID] = account
	}
	items := make([]*CandyTestItem, 0, len(copyRequest.AccountIDs))
	for _, id := range copyRequest.AccountIDs {
		account, ok := byAccount[id]
		if !ok {
			return nil, ErrCandyTestInvalidRequest
		}
		item := &CandyTestItem{AccountID: id, AccountName: account.AccountName, Status: "queued"}
		item.ExpectedUpstreamKind = account.UpstreamKind
		item.ExpectedRouteGeneration = account.RouteGeneration
		item.Execution = &CandyTestExecution{UpstreamKind: account.UpstreamKind}
		skip := account.SkipReason
		if skip == "" {
			skip = "unsupported_model"
			for _, model := range account.Models {
				if model.ID != copyRequest.Model {
					continue
				}
				skip = "unsupported_reasoning_effort"
				if copyRequest.ReasoningEffort == "" {
					skip = ""
				} else {
					for _, effort := range model.ReasoningEfforts {
						if effort == copyRequest.ReasoningEffort {
							skip = ""
							break
						}
					}
				}
				break
			}
		}
		if skip != "" {
			item.Status = "skipped"
			item.FailureCode = candySafeFailureCode(skip, "unsupported_account")
		}
		items = append(items, item)
	}
	return s.repo.Create(ctx, &copyRequest, items)
}

func (s *AccountCandyTestService) Batch(ctx context.Context, id string, page, size int) (*CandyTestBatch, error) {
	return s.repo.GetBatch(ctx, id, page, size)
}
func (s *AccountCandyTestService) Cancel(ctx context.Context, id string, itemIDs []int64) error {
	if len(itemIDs) > 0 {
		var err error
		itemIDs, err = candyAccountIDs(itemIDs)
		if err != nil {
			return err
		}
	}
	return s.repo.Cancel(ctx, id, itemIDs)
}
func (s *AccountCandyTestService) History(ctx context.Context, id int64) ([]*CandyTestItem, error) {
	return s.repo.History(ctx, id)
}
func (s *AccountCandyTestService) Summaries(ctx context.Context, ids []int64) (map[int64]*CandyTestSummary, error) {
	return s.repo.Summaries(ctx, ids)
}

func (s *AccountCandyTestService) Start() {
	if s == nil || s.repo == nil || s.executor == nil {
		return
	}
	s.startOnce.Do(func() {
		s.wg.Add(CandyTestMaxConcurrent)
		for range CandyTestMaxConcurrent {
			go s.worker()
		}
	})
}

func (s *AccountCandyTestService) Stop() {
	if s == nil {
		return
	}
	s.stopOnce.Do(func() { s.cancel() })
	done := make(chan struct{})
	go func() { s.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		// An uncooperative transport cannot prevent server shutdown. Its lease
		// expires without heartbeat; another instance never replays the item.
	}
}

func (s *AccountCandyTestService) worker() {
	defer s.wg.Done()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		if s.ctx.Err() != nil {
			return
		}
		ctx, cancel := context.WithTimeout(s.ctx, 5*time.Second)
		item, err := s.repo.Claim(ctx)
		cancel()
		if err == nil && item != nil {
			s.execute(item)
			continue
		}
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *AccountCandyTestService) execute(item *CandyTestItem) {
	deadline := time.Now().Add(CandyTestTimeout)
	if item.StartedAt != nil {
		deadline = item.StartedAt.Add(CandyTestTimeout)
	}
	ctx, cancel := context.WithDeadline(s.ctx, deadline)
	heartbeatDone := make(chan struct{})
	go func() {
		defer close(heartbeatDone)
		ticker := time.NewTicker(CandyTestHeartbeat)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				hbctx, hbcancel := context.WithTimeout(ctx, CandyTestHeartbeat)
				owned, err := s.repo.Heartbeat(hbctx, item.ID, item.ClaimID)
				hbcancel()
				if err != nil || !owned {
					cancel()
					return
				}
			}
		}
	}()
	execution, err := s.executeOnce(ctx, item)
	contextErr := ctx.Err()
	cancel()
	<-heartbeatDone
	item.Status = "failed"
	item.FailureCode = "execution_failed"
	if execution != nil {
		item.Execution = execution
		if len(execution.ResponseText) <= CandyTestMaxResponseBytes {
			item.ResponseText = execution.ResponseText
		}
	}
	switch {
	case errors.Is(contextErr, context.DeadlineExceeded):
		item.FailureCode = "timeout"
	case contextErr != nil:
		item.FailureCode = "execution_interrupted"
	case err != nil:
		var failure CandyTestFailure
		if errors.As(err, &failure) {
			item.FailureCode = candySafeFailureCode(failure.CandyTestFailureCode(), "execution_failed")
		}
	case execution == nil:
		item.FailureCode = "execution_failed"
	case len(execution.ResponseText) > CandyTestMaxResponseBytes:
		item.FailureCode = "response_too_large"
	case !execution.Completed:
		item.FailureCode = "missing_terminal"
	default:
		counts, gradeErr := GradeCandyAnswer(execution.ResponseText)
		if gradeErr != nil {
			item.FailureCode = "invalid_answer_format"
			var gradeFailure *CandyAnswerGradeError
			if errors.As(gradeErr, &gradeFailure) {
				item.FailureCode = gradeFailure.Code
			}
		} else {
			item.Status = "abnormal"
			if counts == ExpectedCandyAnswerCounts() {
				item.Status = "normal"
			}
			item.FailureCode = ""
			item.Answers = map[string]int{"q1_fixed": counts[0], "q2_adaptive": counts[1], "q3_fixed": counts[2], "q3_adaptive": counts[3]}
		}
	}
	// Completion uses a fresh short context so cancellation can be recorded, but
	// ownership/lease/cancellation are still enforced atomically by the repository.
	completeCtx, completeCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer completeCancel()
	_, _ = s.repo.Complete(completeCtx, item)
}

func (s *AccountCandyTestService) executeOnce(ctx context.Context, item *CandyTestItem) (execution *CandyTestExecution, err error) {
	defer func() {
		if recover() != nil {
			execution = nil
			err = errors.New("candy executor failed")
		}
	}()
	return s.executor.Execute(ctx, item)
}

var candyFailureCodePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,95}$`)

func candySafeFailureCode(code, fallback string) string {
	if candyFailureCodePattern.MatchString(code) {
		return code
	}
	return fallback
}
