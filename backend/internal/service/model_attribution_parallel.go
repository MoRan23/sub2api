package service

import (
	"context"
	"errors"
	"time"
)

const attributionProbeConcurrency = 3

type attributionProbeRun struct {
	complete, deferred, lostClaim bool
}

type attributionProbeOutcome struct {
	index      int
	execution  *CandyTestExecution
	err        error
	attemptErr error
	elapsed    time.Duration
	finished   time.Time
	panicked   bool
}

// Workers own only a physical request. The coordinator alone owns the job,
// checkpoint, counters and repository writes, including after cancellation.
func (s *ModelAttributionService) attributionAttempt(ctx context.Context, accountID int64, model, prompt string, index int) (out attributionProbeOutcome) {
	out.index = index
	started := s.attributionNow()
	// A definite slow 503 remains exempt even when the ordinary budget is low.
	probeCtx, release := context.WithTimeout(ctx, 10*time.Minute)
	defer release()
	defer func() {
		if recover() != nil {
			out.panicked = true
		}
		out.attemptErr = probeCtx.Err()
		out.finished = s.attributionNow()
		out.elapsed = out.finished.Sub(started)
		if s.attemptElapsed != nil {
			out.elapsed = s.attemptElapsed(started, out.execution)
		}
	}()
	out.execution, out.err = s.probe.Probe(probeCtx, accountID, model, prompt)
	return out
}

func (s *ModelAttributionService) runAttributionProbes(ctx, probeBaseCtx context.Context, j *AttributionJob, started time.Time, previousDuration int64) (run attributionProbeRun) {
	cp := j.Checkpoint
	var activeResults <-chan attributionProbeOutcome
	var cancelActive context.CancelFunc
	activeRemaining := 0
	defer func() {
		// The outer executor also recovers repository/coordinator panics. It must
		// never finish and release the lease while this wave is still running.
		if cancelActive != nil {
			cancelActive()
		}
		for activeRemaining > 0 {
			out := <-activeResults
			activeRemaining--
			accumulateAttributionExecution(&j.Result, out.index, out.execution)
		}
	}()
	// A 503 always releases the lease through durable retry_wait, even if a
	// slower sibling finishes after its minimum wait has already elapsed.
	unavailable := make([]bool, len(cp.Slots))
	persist := func() bool {
		ok, err := s.repo.SaveCheckpoint(ctx, j)
		if err != nil {
			j.Status, j.Reason = "failed", "checkpoint_failed"
			return false
		}
		run.lostClaim = !ok
		return ok
	}
	validate := func() bool {
		if ctx.Err() != nil {
			j.Status, j.Reason = "failed", "interrupted"
			return false
		}
		valid, err := s.repo.Validate(ctx, j)
		if err != nil || !valid {
			j.Status, j.Reason = "failed", "configuration_or_authorization_changed"
			if diagnosticRateLimitError(err) {
				j.Status, j.Reason = "skipped", ErrDiagnosticRateLimited.Error()
			}
			return false
		}
		return true
	}
	for cp.CompletedCount() < len(cp.Challenges) {
		if cp.RemainingBudgetMS <= 0 {
			j.Status, j.Reason = "failed", "timeout"
			return run
		}
		if !validate() {
			return run
		}
		ready := make([]int, 0, attributionProbeConcurrency)
		now := s.attributionNow()
		var next *time.Time
		for i := range cp.Slots {
			slot := &cp.Slots[i]
			if slot.Output != nil {
				continue
			}
			if slot.NextAttemptAt != nil && (next == nil || slot.NextAttemptAt.Before(*next)) {
				value := *slot.NextAttemptAt
				next = &value
			}
			if !unavailable[i] && (slot.NextAttemptAt == nil || !now.Before(*slot.NextAttemptAt)) && len(ready) < attributionProbeConcurrency {
				ready = append(ready, i)
			}
		}
		if len(ready) == 0 {
			if next == nil {
				j.Status, j.Reason = "failed", "checkpoint_invalid"
				return run
			}
			j.Status, j.Reason = "retry_wait", "upstream_http_503"
			j.Result.DurationMS = previousDuration + s.attributionNow().Sub(started).Milliseconds()
			ok, err := s.repo.DeferRetry(ctx, j, *next)
			if err != nil {
				j.Status, j.Reason = "failed", "checkpoint_failed"
				return run
			}
			run.deferred, run.lostClaim = ok, !ok
			return run
		}

		// Every request in this wave receives one immutable authorization fence.
		attemptCtx := context.WithValue(probeBaseCtx, attributionProbeAuthorizationKey{}, cp.ProbeAuthorization)
		waveCtx, cancelWave := context.WithCancel(attemptCtx)
		results := make(chan attributionProbeOutcome, len(ready))
		activeResults, cancelActive, activeRemaining = results, cancelWave, len(ready)
		for _, index := range ready {
			slot := &cp.Slots[index]
			if slot.OrdinaryRetries > 0 || slot.UnavailableRetries > 0 {
				j.Result.Retries++
			}
			slot.NextAttemptAt = nil
			accountID, model, prompt := j.AccountID, j.Snapshot.Policy.Model, cp.Challenges[index].Prompt
			go func() {
				out := attributionProbeOutcome{index: index, panicked: true}
				defer func() { results <- out }()
				out = s.attributionAttempt(waveCtx, accountID, model, prompt, index)
			}()
		}
		terminal := false
		ordinaryWait := make([]int, 0, len(ready))
		for range ready {
			out := <-results
			activeRemaining--
			// Drain every worker before returning or releasing the lease. Even a
			// cancelled sibling may have consumed upstream tokens.
			accumulateAttributionExecution(&j.Result, out.index, out.execution)
			if terminal {
				continue
			}
			stop := func(status, reason string) {
				j.Status, j.Reason = status, reason
				terminal = true
				cancelWave()
			}
			if out.panicked {
				stop("failed", "internal_error")
				continue
			}
			if !validate() {
				terminal = true
				cancelWave()
				continue
			}
			execution := out.execution
			if execution != nil && execution.AttributionAuthorization != "" {
				if cp.ProbeAuthorization != "" && cp.ProbeAuthorization != execution.AttributionAuthorization {
					stop("failed", "authorization_changed")
					continue
				}
				cp.ProbeAuthorization = execution.AttributionAuthorization
			}
			slot := &cp.Slots[out.index]
			status, retryAfter := AttributionProbeRetryStatus(out.err, execution)
			if status == 503 {
				slot.UnavailableRetries++
				next := out.finished.Add(attributionRetryDelay(slot.UnavailableRetries, retryAfter))
				slot.NextAttemptAt = &next
				unavailable[out.index] = true
				if !persist() {
					terminal = true
					cancelWave()
				}
				continue
			}
			if elapsed := out.elapsed.Milliseconds(); elapsed > 0 {
				cp.RemainingBudgetMS -= elapsed
			}
			if cp.RemainingBudgetMS <= 0 || errors.Is(out.attemptErr, context.DeadlineExceeded) {
				stop("failed", "timeout")
				continue
			}
			if out.err != nil || execution == nil || !execution.Completed {
				err := out.err
				if err == nil {
					err = candyTestError("missing_terminal")
				}
				if status != 429 && !diagnosticRateLimitError(err) && retryableDiagnosticError(err) && slot.OrdinaryRetries < 2 {
					slot.OrdinaryRetries++
					ordinaryWait = append(ordinaryWait, out.index)
					continue
				}
				reason := "probe_failed"
				var failure CandyTestFailure
				if errors.As(err, &failure) {
					reason = failure.CandyTestFailureCode()
				}
				if status == 429 || diagnosticRateLimitError(err) {
					stop("skipped", ErrDiagnosticRateLimited.Error())
				} else {
					stop("failed", reason)
				}
				continue
			}
			if execution.ActualModel != j.Snapshot.Policy.Model {
				stop("failed", "probe_model_changed")
				continue
			}
			if j.Snapshot.Version >= 3 {
				for len(j.Result.Daybreak) <= out.index {
					j.Result.Daybreak = append(j.Result.Daybreak, nil)
				}
				if execution.Daybreak != nil {
					evidence := *execution.Daybreak
					j.Result.Daybreak[out.index] = &evidence
				}
			}
			slot.Output = &AttributionOutput{Text: execution.ResponseText, ExpectedCount: cp.Challenges[out.index].ExpectedCount}
			slot.OrdinaryRetries, slot.UnavailableRetries = 0, 0
			j.Result.CompletedProbes = cp.CompletedCount()
			if !persist() {
				terminal = true
				cancelWave()
			}
		}
		cancelWave()
		activeResults, cancelActive = nil, nil
		if terminal {
			return run
		}
		// These short ordinary waits retain their original aggregate budget cost.
		// No physical requests remain active while the coordinator waits.
		for _, index := range ordinaryWait {
			waitStart := s.attributionNow()
			delay := min(time.Duration(cp.Slots[index].OrdinaryRetries)*time.Second, time.Duration(cp.RemainingBudgetMS)*time.Millisecond)
			err := s.attributionWait(ctx, delay)
			if elapsed := s.attributionNow().Sub(waitStart).Milliseconds(); elapsed > 0 {
				cp.RemainingBudgetMS -= elapsed
			}
			if err != nil || cp.RemainingBudgetMS <= 0 {
				j.Status, j.Reason = "failed", "timeout"
				if ctx.Err() != nil && !errors.Is(ctx.Err(), context.DeadlineExceeded) {
					j.Reason = "interrupted"
				}
				return run
			}
		}
	}
	run.complete = true
	return run
}

// ValidAttributionCheckpoint also supports the persisted serial representation.
func ValidAttributionCheckpoint(cp *AttributionCheckpoint) bool {
	if cp == nil || (cp.Version != 1 && cp.Version != 2) || len(cp.Challenges) != 3 || cp.RemainingBudgetMS > (10*time.Minute).Milliseconds() {
		return false
	}
	seen := make(map[string]bool, 3)
	for _, challenge := range cp.Challenges {
		if challenge.ID == "" || seen[challenge.ID] || challenge.Prompt == "" || challenge.ExpectedCount <= 0 {
			return false
		}
		seen[challenge.ID] = true
	}
	validOutput := func(index int, output AttributionOutput) bool {
		return len(output.Text) <= CandyTestMaxResponseBytes && output.ExpectedCount == cp.Challenges[index].ExpectedCount
	}
	if cp.Version == 1 {
		if len(cp.Outputs) > 3 || len(cp.Slots) != 0 || cp.OrdinaryRetries < 0 || cp.OrdinaryRetries > 2 || cp.UnavailableRetries < 0 {
			return false
		}
		for i, output := range cp.Outputs {
			if !validOutput(i, output) {
				return false
			}
		}
		return true
	}
	if len(cp.Slots) != 3 || len(cp.Outputs) != 0 || cp.OrdinaryRetries != 0 || cp.UnavailableRetries != 0 {
		return false
	}
	for i, slot := range cp.Slots {
		if slot.OrdinaryRetries < 0 || slot.OrdinaryRetries > 2 || slot.UnavailableRetries < 0 {
			return false
		}
		if slot.Output != nil && (!validOutput(i, *slot.Output) || slot.OrdinaryRetries != 0 || slot.UnavailableRetries != 0 || slot.NextAttemptAt != nil) {
			return false
		}
		if slot.NextAttemptAt != nil && (slot.NextAttemptAt.IsZero() || slot.UnavailableRetries == 0) {
			return false
		}
	}
	return true
}

func validAttributionCheckpoint(cp *AttributionCheckpoint) bool {
	return ValidAttributionCheckpoint(cp)
}

func upgradeAttributionCheckpoint(cp *AttributionCheckpoint) {
	if cp.Version != 1 {
		return
	}
	cp.Slots = make([]AttributionProbeCheckpoint, len(cp.Challenges))
	for i, output := range cp.Outputs {
		value := output
		cp.Slots[i].Output = &value
	}
	if index := len(cp.Outputs); index < len(cp.Slots) {
		cp.Slots[index].OrdinaryRetries = cp.OrdinaryRetries
		cp.Slots[index].UnavailableRetries = cp.UnavailableRetries
	}
	cp.Version, cp.Outputs, cp.OrdinaryRetries, cp.UnavailableRetries = 2, nil, 0, 0
}

func (cp *AttributionCheckpoint) CompletedCount() int {
	if cp.Version == 1 {
		return len(cp.Outputs)
	}
	count := 0
	for _, slot := range cp.Slots {
		if slot.Output != nil {
			count++
		}
	}
	return count
}

func (cp *AttributionCheckpoint) orderedOutputs() []AttributionOutput {
	outputs := make([]AttributionOutput, len(cp.Slots))
	for i, slot := range cp.Slots {
		outputs[i] = *slot.Output
	}
	return outputs
}
