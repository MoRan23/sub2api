package service

import (
	"context"
	"errors"
)

// The settings service owns defaults, validation and cross-instance refresh.
// A missing/unavailable policy never grants cache maintenance to any model.
type CodexTurnStateModelPolicy interface {
	CodexTurnStateModelPolicy(context.Context) ([]string, string, error)
	CodexTurnStateModelPolicyAuthoritative(context.Context) ([]string, string, error)
}

func (s *CodexTurnStateService) modelAllowed(ctx context.Context, model string) (bool, string, error) {
	return s.checkModelPolicy(ctx, model, false)
}

func (s *CodexTurnStateService) checkModelPolicy(ctx context.Context, model string, authoritative bool) (bool, string, error) {
	if s == nil || s.modelPolicy == nil {
		return false, "", errors.New("turn_state_model_policy_unavailable")
	}
	var models []string
	var revision string
	var err error
	if authoritative {
		models, revision, err = s.modelPolicy.CodexTurnStateModelPolicyAuthoritative(ctx)
	} else {
		models, revision, err = s.modelPolicy.CodexTurnStateModelPolicy(ctx)
	}
	if err != nil || revision == "" {
		if err == nil {
			err = errors.New("turn_state_model_policy_unavailable")
		}
		return false, "", err
	}
	for _, allowed := range models {
		if allowed == model {
			return true, revision, nil
		}
	}
	return false, revision, nil
}

func (s *CodexTurnStateService) modelPolicyMatches(ctx context.Context, model, revision string) bool {
	allowed, current, err := s.modelAllowed(ctx, model)
	return err == nil && allowed && current == revision
}

func (s *CodexTurnStateService) authoritativeModelPolicyMatches(ctx context.Context, model, revision string) bool {
	allowed, current, err := s.checkModelPolicy(ctx, model, true)
	return err == nil && allowed && current == revision
}

// A rejected frozen cache attempt must not erase diagnostics for the ordinary
// guarded request that will still be sent. Never carry a cached token, lease or
// response candidate into its replacement observation.
func passiveCodexStateAfterValidationFailure(attempt *CodexTurnStateAttempt) *CodexTurnStateAttempt {
	if attempt == nil {
		return nil
	}
	attempt.mu.Lock()
	reason := attempt.validationReason
	attempt.mu.Unlock()
	if reason == "" {
		reason = "snapshot_unavailable"
	}
	return &CodexTurnStateAttempt{OwnerAccountID: attempt.OwnerAccountID, Model: attempt.Model,
		AccountEnabled: attempt.AccountEnabled, MaintenanceReason: reason, accountType: attempt.accountType,
		credentialEpoch: attempt.credentialEpoch, preparedAt: attempt.preparedAt, historyService: attempt.historyService}
}

// CancelExcludedModels only retires background work. Already sent business
// requests continue; their response publication separately checks the revision.
func (s *CodexTurnStateService) CancelExcludedModels(ctx context.Context) {
	if s == nil {
		return
	}
	s.mu.Lock()
	keys := make(map[CodexTurnStateKey]string, len(s.queued)+len(s.running))
	for key := range s.queued {
		keys[key] = ""
	}
	for key := range s.running {
		keys[key] = s.runningPolicy[key]
	}
	s.mu.Unlock()
	for key, revision := range keys {
		allowed, current, err := s.modelAllowed(ctx, key.Model)
		if err != nil || !allowed || (revision != "" && current != revision) {
			s.cancelCollection(key)
		}
	}
}
