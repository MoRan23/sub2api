package service

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"
)

type AttributionProbe interface {
	Probe(context.Context, int64, string, string) (*CandyTestExecution, error)
}
type ModelAttributionService struct {
	repo     AttributionRepository
	accounts AccountRepository
	probe    AttributionProbe
	analyzer AttributionAnalyzer
	start    sync.Once
	stop     sync.Once
	cancel   context.CancelFunc
	wg       sync.WaitGroup
	wake     chan struct{}
}

func NewModelAttributionService(repo AttributionRepository, accounts AccountRepository, probe *AccountCandyTestTransport) *ModelAttributionService {
	return &ModelAttributionService{repo: repo, accounts: accounts, probe: probe, analyzer: NewModelTraceClient(), wake: make(chan struct{}, 1)}
}
func (s *ModelAttributionService) Start() {
	s.start.Do(func() {
		ctx, cancel := context.WithCancel(context.Background())
		s.cancel = cancel
		s.wg.Add(1)
		go s.schedule(ctx)
		s.wg.Add(1)
		go s.worker(ctx)
	})
}
func (s *ModelAttributionService) Stop() {
	s.stop.Do(func() {
		if s.cancel != nil {
			s.cancel()
		}
		s.wg.Wait()
	})
}
func (s *ModelAttributionService) schedule(ctx context.Context) {
	defer s.wg.Done()
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	var nextPeriodic time.Time
	for {
		if err := s.repo.EnqueueNewAccounts(ctx); err != nil && ctx.Err() == nil {
			slog.Warn("new_account_tests_schedule_failed")
		}
		if !time.Now().Before(nextPeriodic) {
			if _, err := s.repo.Enqueue(ctx, nil, false); err != nil && !errors.Is(err, ErrAttributionDisabled) && ctx.Err() == nil {
				slog.Warn("model_attribution_schedule_failed")
			}
			nextPeriodic = time.Now().Add(30 * time.Second)
		}
		s.notify()
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
func (s *ModelAttributionService) worker(ctx context.Context) {
	defer s.wg.Done()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		j, err := s.repo.Claim(ctx)
		if err != nil && ctx.Err() == nil {
			slog.Warn("model_attribution_claim_failed")
		}
		if err == nil && j != nil {
			s.wg.Add(1)
			go func() {
				defer s.wg.Done()
				s.execute(ctx, j)
			}()
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-s.wake:
		}
	}
}

func (s *ModelAttributionService) notify() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *ModelAttributionService) execute(parent context.Context, j *AttributionJob) {
	started := time.Now()
	deadline := started.Add(10 * time.Minute)
	if j.StartedAt != nil {
		deadline = j.StartedAt.Add(10 * time.Minute)
	}
	ctx, cancel := context.WithDeadline(parent, deadline)
	done := make(chan struct{})
	var validationFailure error // Read only after the monitor has closed done.
	go func() {
		defer close(done)
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				check, release := context.WithTimeout(ctx, 5*time.Second)
				ok, err := s.repo.Heartbeat(check, j)
				if err == nil && ok {
					ok, err = s.repo.Validate(check, j)
				}
				release()
				if err != nil || !ok {
					validationFailure = err
					cancel()
					return
				}
			}
		}
	}()
	// Even panics release the lease through a terminal result, never by replaying inference.
	defer func() {
		if recover() != nil {
			j.Status = "failed"
			j.Reason = "internal_error"
		}
		contextErr := ctx.Err()
		cancel()
		<-done
		if diagnosticRateLimitError(validationFailure) {
			j.Status = "skipped"
			j.Reason = ErrDiagnosticRateLimited.Error()
		} else if contextErr != nil {
			j.Status = "failed"
			j.Reason = "interrupted"
			if errors.Is(contextErr, context.DeadlineExceeded) {
				j.Reason = "timeout"
			}
		}
		j.Result.DurationMS = time.Since(started).Milliseconds()
		saveCtx, saveCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer saveCancel()
		if err := s.repo.Finish(saveCtx, j); err != nil {
			slog.Error("model_attribution_finish_failed", "job_id", j.ID)
		}
	}()
	j.Status = "abnormal"
	valid, err := s.repo.Validate(ctx, j)
	if err != nil || !valid {
		j.Status = "skipped"
		j.Reason = "configuration_or_authorization_changed"
		if diagnosticRateLimitError(err) {
			j.Reason = ErrDiagnosticRateLimited.Error()
		}
		return
	}
	c, err := s.repo.Config(ctx)
	if err != nil {
		j.Reason = "configuration_unavailable"
		return
	}
	models, err := s.analyzer.Models(ctx, c.BaseURL)
	if err != nil {
		j.Reason = safeAttributionAnalyzerError(err)
		return
	}
	known := false
	for _, m := range models {
		if m == j.Snapshot.Policy.Model {
			known = true
		}
	}
	if !known {
		j.Reason = "model_not_enrolled"
		return
	}
	challenges, err := s.analyzer.Challenges(ctx, c.BaseURL)
	if err != nil {
		j.Reason = safeAttributionAnalyzerError(err)
		return
	}
	outputs := make([]AttributionOutput, 0, 3)
	probeCtx := context.WithValue(ctx, diagnosticRetryValidationKey{}, func(check context.Context) error {
		valid, err := s.repo.Validate(check, j)
		if diagnosticRateLimitError(err) {
			return err
		}
		if err != nil || !valid {
			return candyTestError("configuration_or_authorization_changed")
		}
		return nil
	})
	for _, challenge := range challenges {
		valid, err = s.repo.Validate(ctx, j)
		if err != nil || !valid {
			j.Status = "failed"
			j.Reason = "configuration_or_authorization_changed"
			if diagnosticRateLimitError(err) {
				j.Status = "skipped"
				j.Reason = ErrDiagnosticRateLimited.Error()
			}
			return
		}
		execution, e := s.probe.Probe(probeCtx, j.AccountID, j.Snapshot.Policy.Model, challenge.Prompt)
		if execution != nil {
			j.Result.Retries += execution.Retries
			j.Result.Usage = append(j.Result.Usage, execution.Usage)
			j.Result.ActualModels = append(j.Result.ActualModels, execution.ActualModel)
			j.Result.UpstreamModels = append(j.Result.UpstreamModels, execution.UpstreamModel)
		}
		if e != nil || execution == nil || !execution.Completed {
			j.Status = "failed"
			j.Reason = "probe_failed"
			var failure CandyTestFailure
			if errors.As(e, &failure) {
				j.Reason = failure.CandyTestFailureCode()
			}
			if diagnosticRateLimitError(e) {
				j.Status = "skipped"
				j.Reason = ErrDiagnosticRateLimited.Error()
			}
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				j.Reason = "timeout"
			} else if ctx.Err() != nil {
				j.Reason = "interrupted"
			}
			return
		}
		if execution.ActualModel != j.Snapshot.Policy.Model {
			j.Status = "failed"
			j.Reason = "probe_model_changed"
			return
		}
		outputs = append(outputs, AttributionOutput{Text: execution.ResponseText, ExpectedCount: challenge.ExpectedCount})
	}
	analysis, err := s.analyzer.Analyze(ctx, c.BaseURL, outputs)
	if err != nil {
		j.Reason = safeAttributionAnalyzerError(err)
		return
	}
	// Retain bounded numerical evidence for abnormal results such as tied winners
	// or a rejected answer. Unknown model labels and invalid numbers are omitted.
	j.Result.Analysis = attributionSafeAnalysis(analysis, models)
	if err = ValidateAttributionAnalysis(analysis, j.Snapshot.Policy.Model, models); err != nil {
		j.Reason = err.Error()
		return
	}
	j.Result.Analysis = analysis
	j.Status = "mismatch"
	if analysis.Prediction == j.Snapshot.Policy.Model {
		j.Status = "passed"
	}
}
func safeAttributionAnalyzerError(err error) string {
	// The concrete HTTP client only returns fixed codes, but alternate adapters
	// must not leak arbitrary response bodies/URLs into persistent diagnostics.
	code := err.Error()
	switch code {
	case "modeltrace_unavailable", "modeltrace_response_invalid", "modeltrace_bank_invalid", "modeltrace_challenges_invalid":
		return code
	}
	return "modeltrace_failed"
}
func (s *ModelAttributionService) Config(ctx context.Context) (AttributionConfig, error) {
	return s.repo.Config(ctx)
}
func (s *ModelAttributionService) SaveConfig(ctx context.Context, c AttributionConfig) (AttributionConfig, error) {
	return s.repo.SaveConfig(ctx, c)
}
func (s *ModelAttributionService) Models(ctx context.Context, base string) ([]string, error) {
	return s.analyzer.Models(ctx, base)
}
func (s *ModelAttributionService) Create(ctx context.Context, ids []int64) ([]*AttributionJob, error) {
	if len(ids) == 0 || len(ids) > 500 {
		return nil, ErrAttributionInvalid
	}
	for _, id := range ids {
		if id < 1 {
			return nil, ErrAttributionInvalid
		}
	}
	jobs, err := s.repo.Enqueue(ctx, ids, true)
	if err == nil {
		s.notify()
	}
	return jobs, err
}
func (s *ModelAttributionService) List(ctx context.Context, id int64, page, size int) (*AttributionPage, error) {
	if page < 1 || size < 1 || size > 100 || id < 0 {
		return nil, ErrAttributionInvalid
	}
	return s.repo.List(ctx, id, page, size)
}
func (s *ModelAttributionService) Get(ctx context.Context, id int64) (*AttributionJob, error) {
	return s.repo.Get(ctx, id)
}
func (s *ModelAttributionService) Summaries(ctx context.Context, ids []int64) (map[int64]*AttributionSummary, error) {
	out, err := s.repo.Summaries(ctx, ids)
	if err != nil {
		return nil, err
	}
	accounts, err := s.accounts.GetByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	c, err := s.repo.Config(ctx)
	if err != nil {
		return nil, err
	}
	for _, a := range accounts {
		if out[a.ID] == nil {
			out[a.ID] = &AttributionSummary{}
		}
		reason := AttributionSkipReason(a, time.Now())
		policy, _ := ResolveAttributionPolicy(c, a.AccountGroups)
		if reason == "" && AccountDiagnosticRateLimited(a, policy.Model, time.Now()) {
			reason = ErrDiagnosticRateLimited.Error()
		}
		if !c.Enabled {
			reason = "disabled"
		}
		out[a.ID].SkipReason = reason
	}
	return out, nil
}
