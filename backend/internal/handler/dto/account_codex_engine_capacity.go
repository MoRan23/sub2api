package dto

import "github.com/Wei-Shaw/sub2api/internal/service"

type CodexEngineCapacity struct {
	Limit                      int  `json:"limit"`
	SchedulingPausedByCapacity bool `json:"scheduling_paused_by_capacity"`
}

func codexEngineCapacityFromService(account *service.Account) *CodexEngineCapacity {
	state := service.CodexEngineCapacityStateFromAccount(account)
	if state == nil {
		return nil
	}
	return &CodexEngineCapacity{
		Limit:                      *state.Limit,
		SchedulingPausedByCapacity: state.OwnsSchedulingPause(account),
	}
}
