package service

import (
	"encoding/json"
	"time"
)

type CodexTelemetryObservationQuery struct {
	AccountID int64
	Status    string
	Type      string
	Page      int
	PageSize  int
}

type CodexTelemetryCounters struct {
	Attempts  uint64 `json:"attempts"`
	Queued    uint64 `json:"queued"`
	Sent      uint64 `json:"sent"`
	Failed    uint64 `json:"failed"`
	Dropped   uint64 `json:"dropped"`
	Cancelled uint64 `json:"cancelled"`
	Skipped   uint64 `json:"skipped"`
}

type CodexTelemetryObservationSnapshot struct {
	ConfiguredEnabled bool                        `json:"configured_enabled"`
	EffectiveEnabled  bool                        `json:"effective_enabled"`
	ForcedOffReason   string                      `json:"forced_off_reason"`
	QueueDepth        int                         `json:"queue_depth"`
	Counters          CodexTelemetryCounters      `json:"counters"`
	Items             []CodexTelemetryObservation `json:"items"`
	Total             int                         `json:"total"`
	Page              int                         `json:"page"`
	PageSize          int                         `json:"page_size"`
}

// CodexTelemetryObservation contains only allowlisted diagnostics. Never add
// serialized input, transport errors or headers here: they may carry secrets.
type CodexTelemetryObservation struct {
	ID                uint64    `json:"id"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
	AccountID         int64     `json:"account_id"`
	AccountName       string    `json:"account_name"`
	Type              string    `json:"type"`
	Status            string    `json:"status"`
	EventNames        []string  `json:"event_names"`
	ContainsSimulated bool      `json:"contains_simulated"`
	AttemptID         uint64    `json:"attempt_id"`
	AttemptCount      int       `json:"attempt_count"`
	TurnCount         int       `json:"turn_count"`
	SessionID         string    `json:"session_id"`
	ThreadID          string    `json:"thread_id"`
	TurnID            string    `json:"turn_id"`
	ParentThreadID    string    `json:"parent_thread_id"`
	ParentTurnID      string    `json:"parent_turn_id"`
	RootTurnID        string    `json:"root_turn_id"`
	Model             string    `json:"model"`
	UserAgent         string    `json:"user_agent"`
	Originator        string    `json:"originator"`
	Version           string    `json:"version"`
	HTTPStatus        int       `json:"http_status"`
	Error             string    `json:"error"`
	// A missing field means no thread-initialization state was collected. An
	// explicit JSON null records unknown client worktree state, never host state.
	IsWorktree json.RawMessage `json:"is_worktree,omitempty"`
}

func (s *CodexTelemetryService) Observations(query CodexTelemetryObservationQuery) CodexTelemetryObservationSnapshot {
	if query.Page < 1 {
		query.Page = 1
	}
	if query.PageSize < 1 {
		query.PageSize = 20
	}
	if query.PageSize > 100 {
		query.PageSize = 100
	}
	result := CodexTelemetryObservationSnapshot{Items: []CodexTelemetryObservation{}, Page: query.Page, PageSize: query.PageSize}
	if s == nil {
		return result
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	result.ConfiguredEnabled = s.configured
	result.EffectiveEnabled, result.ForcedOffReason = CodexTelemetryEffectiveState(s.configured)
	result.EffectiveEnabled = result.EffectiveEnabled && !s.stopped
	result.QueueDepth, result.Counters = s.queueDepth, s.counters
	offset := (query.Page - 1) * query.PageSize
	for i := len(s.observations) - 1; i >= 0; i-- {
		entry := s.observations[i]
		if query.AccountID != 0 && entry.AccountID != query.AccountID {
			continue
		}
		if query.Status != "" && entry.Status != query.Status {
			continue
		}
		if query.Type != "" && entry.Type != query.Type {
			continue
		}
		if result.Total >= offset && len(result.Items) < query.PageSize {
			copyEntry := *entry
			copyEntry.EventNames = append([]string{}, entry.EventNames...)
			copyEntry.IsWorktree = append(json.RawMessage(nil), entry.IsWorktree...)
			result.Items = append(result.Items, copyEntry)
		}
		result.Total++
	}
	return result
}

func (s *CodexTelemetryService) newObservationLocked(profile codexTelemetryProfile, attemptID uint64, kind string, names []string, turns int) *CodexTelemetryObservation {
	s.nextID++
	now := time.Now()
	entry := &CodexTelemetryObservation{
		ID: s.nextID, CreatedAt: now, UpdatedAt: now, AccountID: profile.client.localID, AccountName: profile.client.name,
		Type: kind, Status: "queued", EventNames: append([]string{}, names...), ContainsSimulated: codexSimulatesClientBehavior(profile),
		AttemptID: attemptID, TurnCount: turns, Model: profile.model,
		UserAgent: profile.client.userAgent, Originator: profile.client.originator, Version: profile.client.version,
	}
	if kind == "analytics" {
		entry.AttemptCount = profile.attemptCount
		entry.SessionID, entry.ThreadID, entry.TurnID = profile.sessionID, profile.threadID, profile.turnID
		entry.ParentThreadID, entry.ParentTurnID, entry.RootTurnID = profile.input.ParentThreadID, profile.input.ParentTurnID, profile.rootTurnID
	}
	s.observations = append(s.observations, entry)
	if len(s.observations) > codexTelemetryHistorySize {
		copy(s.observations, s.observations[1:])
		s.observations[len(s.observations)-1] = nil
		s.observations = s.observations[:codexTelemetryHistorySize]
	}
	return entry
}

func (s *CodexTelemetryService) finishObservationLocked(entry *CodexTelemetryObservation, status string, code int, reason string) {
	if entry.Status != "queued" {
		return
	}
	entry.Status, entry.HTTPStatus, entry.Error, entry.UpdatedAt = status, code, reason, time.Now()
	switch status {
	case "sent":
		s.counters.Sent++
	case "failed":
		s.counters.Failed++
	case "dropped":
		s.counters.Dropped++
	case "cancelled":
		s.counters.Cancelled++
	case "skipped":
		s.counters.Skipped++
	}
}

func (s *CodexTelemetryService) skipLocked(profile codexTelemetryProfile, attemptID uint64, reason string) {
	entry := s.newObservationLocked(profile, attemptID, "analytics", nil, 1)
	s.finishObservationLocked(entry, "skipped", 0, reason)
}
