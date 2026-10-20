package service

import (
	"context"
	"errors"
	"log/slog"
	"math/rand/v2"
	"strings"
	"sync"
	"time"
)

type AttributionProbe interface {
	Probe(context.Context, int64, string, string) (*CandyTestExecution, error)
}

// Set only for an explicit administrator attribution job, never from a public
// request header. Automatic attribution and pelican probes keep their gates.
type manualAttributionContextKey struct{}

func isManualAttribution(ctx context.Context) bool {
	manual, _ := ctx.Value(manualAttributionContextKey{}).(bool)
	return manual
}

type ModelAttributionService struct {
	repo             AttributionRepository
	accounts         AccountRepository
	probe            AttributionProbe
	analyzer         AttributionAnalyzer
	start            sync.Once
	stop             sync.Once
	cancel           context.CancelFunc
	wg               sync.WaitGroup
	wake             chan struct{}
	now              func() time.Time
	wait             func(context.Context, time.Duration) error
	fetchProbeModels func(context.Context, *Account) (*OpenAIModelsResponse, error)
}

func NewModelAttributionService(repo AttributionRepository, accounts AccountRepository, probe *AccountCandyTestTransport) *ModelAttributionService {
	s := &ModelAttributionService{repo: repo, accounts: accounts, probe: probe, analyzer: NewLMDetectorClient(), wake: make(chan struct{}, 1)}
	if publisher, ok := repo.(interface {
		SetAccountChangedCallback(func(context.Context, int64))
	}); ok {
		if refresher, ok := accounts.(AccountSchedulingSnapshotRefresher); ok {
			publisher.SetAccountChangedCallback(refresher.RefreshAccountSchedulingSnapshot)
		}
	}
	if probe != nil && probe.gateway != nil {
		s.fetchProbeModels = probe.gateway.FetchOpenAIProbeModels
	}
	return s
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
	parent = context.WithValue(parent, attributionExecutionContextKey{}, true)
	if j.Source == "manual" {
		parent = context.WithValue(parent, manualAttributionContextKey{}, true)
	}
	started := s.attributionNow()
	previousDuration := j.Result.DurationMS
	ctx, cancel := context.WithCancel(parent)
	done := make(chan struct{})
	var validationFailure error // Read only after the monitor has closed done.
	claim := *j                 // The monitor only needs the immutable identity and claim.
	deferred, lostClaim := false, false
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
				ok, err := s.repo.Heartbeat(check, &claim)
				if err == nil && ok {
					ok, err = s.repo.Validate(check, &claim)
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
		if deferred || lostClaim {
			return
		}
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
		j.Result.DurationMS = previousDuration + s.attributionNow().Sub(started).Milliseconds()
		saveCtx, saveCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer saveCancel()
		if err := s.repo.Finish(saveCtx, j); err != nil {
			slog.Error("model_attribution_finish_failed", "job_id", j.ID)
		}
	}()
	j.Status = "abnormal"
	j.Reason = ""
	valid, err := s.repo.Validate(ctx, j)
	if err != nil || !valid {
		j.Status = "skipped"
		j.Reason = "configuration_or_authorization_changed"
		if diagnosticRateLimitError(err) {
			j.Reason = ErrDiagnosticRateLimited.Error()
		}
		return
	}
	if j.Snapshot.Version >= 3 && j.Snapshot.Daybreak == nil {
		j.Status, j.Reason = "failed", "daybreak_evidence_missing"
		return
	}
	c, err := s.repo.Config(ctx)
	if err != nil {
		j.Reason = "configuration_unavailable"
		return
	}
	detector, err := s.analyzer.Info(ctx, c.BaseURL)
	if err != nil {
		j.Reason = safeAttributionAnalyzerError(err)
		return
	}
	if !SameAttributionDetector(detector, j.Snapshot.Detector) || !SameAttributionDetector(detector, c.Detector) {
		j.Reason = "detector_version_changed"
		return
	}
	models, err := s.analyzer.Models(ctx, c.BaseURL, detector)
	if err != nil {
		j.Reason = safeAttributionAnalyzerError(err)
		return
	}
	expected := ExpectedAttributionModels(j.Snapshot.Policy)
	known := make(map[string]bool, len(models))
	for _, model := range models {
		known[model] = true
	}
	for _, model := range expected {
		if !known[model] {
			j.Reason = "expected_model_not_enrolled"
			return
		}
	}
	if j.Checkpoint == nil {
		challenges, err := s.analyzer.Challenges(ctx, c.BaseURL, detector)
		if err != nil {
			j.Reason = safeAttributionAnalyzerError(err)
			return
		}
		j.Checkpoint = &AttributionCheckpoint{Version: 1, Challenges: challenges, Outputs: []AttributionOutput{}, RemainingBudgetMS: (10 * time.Minute).Milliseconds()}
	}
	cp := j.Checkpoint
	if !validAttributionCheckpoint(cp) {
		j.Status, j.Reason = "failed", "checkpoint_invalid"
		return
	}
	checkpoint := func() bool {
		ok, err := s.repo.SaveCheckpoint(ctx, j)
		if err != nil {
			j.Status, j.Reason = "failed", "checkpoint_failed"
			return false
		}
		lostClaim = !ok
		return ok
	}
	if !checkpoint() {
		return
	}
	probeBaseCtx := context.WithValue(ctx, diagnosticRetryValidationKey{}, func(check context.Context) error {
		valid, err := s.repo.Validate(check, &claim)
		if diagnosticRateLimitError(err) {
			return err
		}
		if err != nil || !valid {
			return candyTestError("configuration_or_authorization_changed")
		}
		return nil
	})
	if j.Snapshot.Version >= 3 {
		probeBaseCtx = withDiagnosticDaybreakPreferences(probeBaseCtx, *j.Snapshot.Daybreak)
	}
	for len(cp.Outputs) < len(cp.Challenges) {
		if cp.RemainingBudgetMS <= 0 {
			j.Status, j.Reason = "failed", "timeout"
			return
		}
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
		index := len(cp.Outputs)
		challenge := cp.Challenges[index]
		if cp.OrdinaryRetries > 0 || cp.UnavailableRetries > 0 {
			j.Result.Retries++
		}
		attemptStart := s.attributionNow()
		attemptCtx := context.WithValue(probeBaseCtx, attributionProbeAuthorizationKey{}, cp.ProbeAuthorization)
		// Classification is only known when the attempt ends. A nearly spent
		// ordinary budget must not cancel an otherwise definite slow 503.
		probeCtx, release := context.WithTimeout(attemptCtx, 10*time.Minute)
		execution, e := s.probe.Probe(probeCtx, j.AccountID, j.Snapshot.Policy.Model, challenge.Prompt)
		attemptErr := probeCtx.Err()
		release()
		accumulateAttributionExecution(&j.Result, index, execution)
		if ctx.Err() == nil {
			valid, err := s.repo.Validate(ctx, j)
			if err != nil || !valid {
				j.Status, j.Reason = "failed", "configuration_or_authorization_changed"
				if diagnosticRateLimitError(err) {
					j.Status, j.Reason = "skipped", ErrDiagnosticRateLimited.Error()
				}
				return
			}
		}
		if execution != nil && execution.AttributionAuthorization != "" {
			if cp.ProbeAuthorization != "" && cp.ProbeAuthorization != execution.AttributionAuthorization {
				j.Status, j.Reason = "failed", "authorization_changed"
				return
			}
			cp.ProbeAuthorization = execution.AttributionAuthorization
		}
		status, retryAfter := AttributionProbeRetryStatus(e, execution)
		if status == 503 && ctx.Err() == nil {
			// A definite rejection is safe to retry. Neither its request duration
			// nor its durable wait consumes the ordinary execution budget.
			cp.UnavailableRetries++
			next := s.attributionNow().Add(attributionRetryDelay(cp.UnavailableRetries, retryAfter))
			j.Status, j.Reason = "retry_wait", "upstream_http_503"
			j.Result.DurationMS = previousDuration + s.attributionNow().Sub(started).Milliseconds()
			ok, err := s.repo.DeferRetry(ctx, j, next)
			if err != nil {
				j.Status, j.Reason = "failed", "checkpoint_failed"
				return
			}
			deferred, lostClaim = ok, !ok
			return
		}
		if elapsed := s.attributionNow().Sub(attemptStart).Milliseconds(); elapsed > 0 {
			cp.RemainingBudgetMS -= elapsed
		}
		if cp.RemainingBudgetMS <= 0 || errors.Is(attemptErr, context.DeadlineExceeded) {
			j.Status, j.Reason = "failed", "timeout"
			return
		}
		if e != nil || execution == nil || !execution.Completed {
			if e == nil {
				e = candyTestError("missing_terminal")
			}
			if ctx.Err() == nil && status != 429 && !diagnosticRateLimitError(e) && retryableDiagnosticError(e) && cp.OrdinaryRetries < 2 {
				cp.OrdinaryRetries++
				waitStart := s.attributionNow()
				delay := min(time.Duration(cp.OrdinaryRetries)*time.Second, time.Duration(cp.RemainingBudgetMS)*time.Millisecond)
				waitErr := s.attributionWait(ctx, delay)
				if elapsed := s.attributionNow().Sub(waitStart).Milliseconds(); elapsed > 0 {
					cp.RemainingBudgetMS -= elapsed
				}
				if waitErr == nil {
					continue
				}
			}
			j.Status = "failed"
			j.Reason = "probe_failed"
			var failure CandyTestFailure
			if errors.As(e, &failure) {
				j.Reason = failure.CandyTestFailureCode()
			}
			if status == 429 || diagnosticRateLimitError(e) {
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
		if j.Snapshot.Version >= 3 {
			for len(j.Result.Daybreak) <= index {
				j.Result.Daybreak = append(j.Result.Daybreak, nil)
			}
			if execution.Daybreak != nil {
				evidence := *execution.Daybreak
				j.Result.Daybreak[index] = &evidence
			}
		}
		cp.Outputs = append(cp.Outputs, AttributionOutput{Text: execution.ResponseText, ExpectedCount: challenge.ExpectedCount})
		cp.OrdinaryRetries, cp.UnavailableRetries = 0, 0
		j.Result.CompletedProbes = len(cp.Outputs)
		if !checkpoint() {
			return
		}
	}
	analysis, err := s.analyzer.Analyze(ctx, c.BaseURL, detector, cp.Outputs)
	if err != nil {
		j.Reason = safeAttributionAnalyzerError(err)
		return
	}
	// Retain bounded numerical evidence for abnormal results such as tied winners
	// or a rejected answer. Unknown model labels and invalid numbers are omitted.
	j.Result.Analysis = attributionSafeAnalysis(analysis, models)
	if err = ValidateAttributionAnalysis(analysis, expected, models); err != nil {
		j.Reason = err.Error()
		return
	}
	j.Result.Analysis = analysis
	j.Status = "mismatch"
	for _, model := range expected {
		if analysis.Prediction == model {
			j.Status = "passed"
			break
		}
	}
}

func (s *ModelAttributionService) attributionNow() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

func (s *ModelAttributionService) attributionWait(ctx context.Context, delay time.Duration) error {
	if s.wait != nil {
		return s.wait(ctx, delay)
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func attributionRetryDelay(unavailableRetries int, retryAfter time.Duration) time.Duration {
	// Start at two seconds, add up to 20% jitter, and cap exponential backoff at
	// a minute. Retry-After is an upstream minimum, bounded to five minutes.
	ceiling := min(2*time.Second*time.Duration(1<<min(max(unavailableRetries-1, 0), 5)), 60*time.Second)
	delay := min(ceiling+time.Duration(rand.Int64N(int64(ceiling/5)+1)), 60*time.Second)
	if retryAfter = min(retryAfter, 300*time.Second); retryAfter > delay {
		return retryAfter
	}
	return delay
}

func validAttributionCheckpoint(cp *AttributionCheckpoint) bool {
	if cp == nil || cp.Version != 1 || len(cp.Challenges) != 3 || len(cp.Outputs) > 3 || cp.OrdinaryRetries < 0 || cp.OrdinaryRetries > 2 || cp.UnavailableRetries < 0 || cp.RemainingBudgetMS > (10*time.Minute).Milliseconds() {
		return false
	}
	seen := make(map[string]bool, 3)
	for _, challenge := range cp.Challenges {
		if challenge.ID == "" || seen[challenge.ID] || challenge.Prompt == "" || challenge.ExpectedCount <= 0 {
			return false
		}
		seen[challenge.ID] = true
	}
	for i, output := range cp.Outputs {
		if len(output.Text) > CandyTestMaxResponseBytes || output.ExpectedCount != cp.Challenges[i].ExpectedCount {
			return false
		}
	}
	return true
}

func accumulateAttributionExecution(result *AttributionResult, index int, execution *CandyTestExecution) {
	if execution == nil {
		return
	}
	for len(result.Usage) <= index {
		result.Usage = append(result.Usage, nil)
	}
	for len(result.ActualModels) <= index {
		result.ActualModels = append(result.ActualModels, "")
	}
	for len(result.UpstreamModels) <= index {
		result.UpstreamModels = append(result.UpstreamModels, "")
	}
	if usage := execution.Usage; usage != nil {
		if result.Usage[index] == nil {
			result.Usage[index] = &OpenAIUsage{}
		}
		total := result.Usage[index]
		total.InputTokens += usage.InputTokens
		total.OutputTokens += usage.OutputTokens
		total.CacheCreationInputTokens += usage.CacheCreationInputTokens
		total.CacheReadInputTokens += usage.CacheReadInputTokens
		total.ImageInputTokens += usage.ImageInputTokens
		total.ImageOutputTokens += usage.ImageOutputTokens
		total.ImageCacheReadTokens += usage.ImageCacheReadTokens
	}
	if execution.ActualModel != "" {
		result.ActualModels[index] = execution.ActualModel
	}
	if execution.UpstreamModel != "" {
		result.UpstreamModels[index] = execution.UpstreamModel
	}
}

func safeAttributionAnalyzerError(err error) string {
	// The concrete HTTP client only returns fixed codes, but alternate adapters
	// must not leak arbitrary response bodies/URLs into persistent diagnostics.
	code := err.Error()
	switch code {
	case "detector_unavailable", "detector_response_invalid", "detector_bank_invalid", "detector_challenges_invalid", "detector_identity_invalid", "detector_version_changed", "detector_calibration_unavailable":
		return code
	}
	return "detector_failed"
}
func (s *ModelAttributionService) Config(ctx context.Context) (AttributionConfig, error) {
	return s.repo.Config(ctx)
}
func (s *ModelAttributionService) SaveConfig(ctx context.Context, c AttributionConfig) (AttributionConfig, error) {
	var err error
	c, err = NormalizeAttributionConfig(c)
	if err != nil {
		return c, err
	}
	previous, err := s.repo.Config(ctx)
	if err != nil {
		return c, err
	}
	if c.Version != previous.Version {
		return c, ErrAttributionConflict
	}
	if !c.Enabled && c.BaseURL == previous.BaseURL && (c.Detector == nil || SameAttributionDetector(c.Detector, previous.Detector)) {
		// Disabling must remain possible during an analyzer outage.
		c.Detector = previous.Detector
	} else if c.BaseURL != "" {
		connection, err := s.Connection(ctx, c.BaseURL)
		if err != nil {
			return c, err
		}
		// The administrator must first inspect the connection's advertised version.
		if !SameAttributionDetector(c.Detector, connection.Detector) {
			return c, ErrAttributionInvalid
		}
		c.Detector = connection.Detector
	} else {
		c.Detector = nil
	}
	return s.repo.SaveConfig(ctx, c)
}
func (s *ModelAttributionService) Models(ctx context.Context, base string) ([]string, error) {
	connection, err := s.Connection(ctx, base)
	if err != nil {
		return nil, err
	}
	return connection.Models, nil
}
func (s *ModelAttributionService) Connection(ctx context.Context, base string) (*AttributionConnection, error) {
	detector, err := s.analyzer.Info(ctx, base)
	if err != nil {
		return nil, err
	}
	models, err := s.analyzer.Models(ctx, base, detector)
	if err != nil {
		return nil, err
	}
	return &AttributionConnection{Models: models, Detector: detector}, nil
}
func (s *ModelAttributionService) Create(ctx context.Context, ids []int64, model string, expectedModels ...*[]string) ([]*AttributionJob, error) {
	if len(ids) == 0 || len(ids) > 500 {
		return nil, ErrAttributionInvalid
	}
	for _, id := range ids {
		if id < 1 {
			return nil, ErrAttributionInvalid
		}
	}
	options := AttributionProbeOptions{Model: model}
	if len(expectedModels) > 1 {
		return nil, ErrAttributionInvalid
	}
	if len(expectedModels) == 1 {
		options.ExpectedModels = expectedModels[0]
	}
	options, err := NormalizeAttributionProbeOptions(options)
	if err != nil {
		return nil, err
	}
	jobs, err := s.repo.Enqueue(ctx, ids, true, options)
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

func (s *ModelAttributionService) Cancel(ctx context.Context, id int64) (*AttributionJob, error) {
	if id < 1 {
		return nil, ErrAttributionInvalid
	}
	return s.repo.Cancel(ctx, id)
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
		reason := AttributionSkipReason(a, time.Now(), true)
		if reason == "" && strings.TrimSpace(c.BaseURL) == "" {
			reason = "service_unconfigured"
		}
		out[a.ID].SkipReason = reason
	}
	return out, nil
}
