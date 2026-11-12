package dto

import "github.com/Wei-Shaw/sub2api/internal/service"

// CodexEngineModels contains public synchronization facts, not source credentials.
type CodexEngineModels struct {
	Synced     bool `json:"synced"`
	ModelCount int  `json:"model_count"`
}

func codexEngineModelsFromService(account *service.Account) *CodexEngineModels {
	state := service.CodexEngineModelsStateFromAccount(account)
	if state == nil {
		return nil
	}
	return &CodexEngineModels{Synced: true, ModelCount: state.ModelCount}
}
