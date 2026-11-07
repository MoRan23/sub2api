package service

import (
	"encoding/json"
	"math"
)

// CodexEngineCapacitySyncState is the persisted observation and pause ownership.
// API responses should project this into a summary without internal identities.
type CodexEngineCapacitySyncState struct {
	Identity      string `json:"identity"`
	Limit         *int   `json:"limit"`
	PauseRevision *int64 `json:"pause_revision"`
	ObservationID string `json:"observation_id"`
}

// OwnsSchedulingPause applies to a successfully decoded state. Ownership follows
// the scheduling revision across Engine identity edits until a fresh observation.
func (s CodexEngineCapacitySyncState) OwnsSchedulingPause(account *Account) bool {
	return account != nil && !account.Schedulable && s.Identity != "" && s.Limit != nil && *s.Limit == 0 &&
		s.PauseRevision != nil && *s.PauseRevision == account.SchedulingRevision
}

// CodexEngineCapacityStateFromAccount reads only valid Engine observations. Older
// observations without an observation_id are still valid, including revision 0.
func CodexEngineCapacityStateFromAccount(account *Account) *CodexEngineCapacitySyncState {
	if account == nil || !account.IsCodexEngine() || account.IsShadow() {
		return nil
	}
	raw, err := json.Marshal(account.Extra[CodexEngineCapacitySyncExtraKey])
	if err != nil {
		return nil
	}
	var state CodexEngineCapacitySyncState
	if err := json.Unmarshal(raw, &state); err != nil || state.Identity == "" || state.Limit == nil ||
		*state.Limit < 0 || *state.Limit > math.MaxInt32 {
		return nil
	}
	return &state
}
