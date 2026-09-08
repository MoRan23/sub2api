package service

import "context"

// SupportsOpenAIWSModelLatest rechecks long-lived connections against the current
// account allowlist. A connection's frozen credentials must not grant access to
// models removed by an administrator after its first turn.
func (s *OpenAIGatewayService) SupportsOpenAIWSModelLatest(ctx context.Context, selected *Account, model string) bool {
	if selected == nil {
		return false
	}
	latest := selected
	if s != nil && s.accountRepo != nil {
		var err error
		latest, err = s.accountRepo.GetByID(ctx, selected.ID)
		if err != nil || latest == nil {
			return false
		}
	} else if s != nil && s.schedulerSnapshot != nil {
		var err error
		latest, err = s.schedulerSnapshot.GetAccount(ctx, selected.ID)
		if err != nil || latest == nil {
			return false
		}
	}
	return latest.IsModelSupported(model)
}
