package service

import (
	"container/list"
	"sort"
	"strings"
	"sync"
	"time"
)

const codexTurnStateObservationCapacity = 4096

type codexTurnStateObservationKey struct {
	ownerAccountID  int64
	model           string
	credentialEpoch string
}

type codexTurnStateObservationIndexEntry struct {
	key      codexTurnStateObservationKey
	sequence uint64
	summary  CodexTurnStateModelObservation
	envelope codexTurnStateObservationEnvelope
}

// All fields are protected by the containing summary store. Its lifetime is
// independent of full fingerprint diagnostics and ends at process restart.
type codexTurnStateObservationIndex struct {
	entries map[codexTurnStateObservationKey]*list.Element
	lru     list.List
}

type codexTurnStateSummaryStore struct {
	mu    sync.Mutex
	seq   uint64
	index codexTurnStateObservationIndex
}

var globalCodexTurnStateSummaryStore = &codexTurnStateSummaryStore{}

// Called at the physical-send boundary, independently of full diagnostics.
func (store *codexTurnStateSummaryStore) nextSequence() uint64 {
	if store == nil {
		return 0
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	store.seq++
	return store.seq
}

func (store *codexTurnStateSummaryStore) update(sequence uint64, ownerAccountID int64, value CodexTurnStateObservation, finished bool, observedAt time.Time) {
	if store == nil || sequence == 0 || !finished {
		return
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if sequence > store.seq {
		return
	}
	store.index.record(sequence, ownerAccountID, value, observedAt)
}

func (index *codexTurnStateObservationIndex) record(sequence uint64, ownerAccountID int64, value CodexTurnStateObservation, observedAt time.Time) {
	if ownerAccountID <= 0 || strings.TrimSpace(value.Model) == "" || observedAt.IsZero() {
		return
	}
	key := codexTurnStateObservationKey{ownerAccountID: ownerAccountID, model: value.Model, credentialEpoch: value.credentialEpoch}
	if index.entries == nil {
		index.entries = make(map[codexTurnStateObservationKey]*list.Element)
	}
	element := index.entries[key]
	if element != nil {
		previous := element.Value.(*codexTurnStateObservationIndexEntry)
		if observedAt.Before(previous.summary.ObservedAt) || (observedAt.Equal(previous.summary.ObservedAt) && sequence <= previous.sequence) {
			return
		}
	} else {
		if index.lru.Len() == codexTurnStateObservationCapacity {
			oldest := index.lru.Back()
			entry := oldest.Value.(*codexTurnStateObservationIndexEntry)
			delete(index.entries, entry.key)
			*entry = codexTurnStateObservationIndexEntry{}
			index.lru.Remove(oldest)
		}
		element = index.lru.PushFront(&codexTurnStateObservationIndexEntry{key: key})
		index.entries[key] = element
	}
	shape := value.ResponseShape
	if shape == "" {
		shape = "missing"
	}
	entry := element.Value.(*codexTurnStateObservationIndexEntry)
	entry.sequence = sequence
	entry.envelope = value.envelopeEvidence
	entry.summary = CodexTurnStateModelObservation{
		Model:                    value.Model,
		ObservedAt:               observedAt,
		ResponseLength:           value.ResponseLength,
		ResponseShape:            shape,
		ResponseObservedShape:    value.ResponseObservedShape,
		ResponseCipherBlocks:     value.ResponseCipherBlocks,
		ResponseValidationReason: value.ResponseValidationReason,
		ResponseSource:           value.ResponseSource,
		RequestSource:            value.RequestSource,
		OutboundLength:           value.OutboundLength,
		ObservationID:            value.ObservationID,
		RequestSentAt:            value.RequestSentAt,
		OutboundAction:           value.Action,
		OutboundSource:           value.Source,
		MaintenanceReason:        value.MaintenanceReason,
		BusinessDelivered:        value.BusinessDelivered,
	}
	if value.Action == "injected" {
		entry.summary.SnapshotVersion = value.SnapshotVersion
		entry.summary.SnapshotExpiresAt = value.ExpiresAt
	}
	entry.summary = cloneCodexTurnStateModelObservation(entry.summary)
	index.lru.MoveToFront(element)
}

// Management reads select only the currently bound credential identity. A late
// response can update its old epoch's entry but cannot overwrite or impersonate
// a new credential's state. Empty epochs match only other empty epochs.
func (store *codexTurnStateSummaryStore) snapshotForOwners(owners []*Account) (bool, map[int64][]CodexTurnStateModelObservation) {
	result := make(map[int64][]CodexTurnStateModelObservation)
	if store == nil {
		return false, result
	}
	byID := make(map[int64]*Account, len(owners))
	for _, owner := range owners {
		if codexTurnStateEligible(owner) {
			byID[owner.ID] = owner
		}
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	for key, element := range store.index.entries {
		owner := byID[key.ownerAccountID]
		if owner == nil || key.credentialEpoch != CodexTurnStateCredentialEpochForAccount(owner) {
			continue
		}
		entry := element.Value.(*codexTurnStateObservationIndexEntry)
		value := projectCodexTurnStateObservation(entry.summary, entry.envelope, CodexTurnStateAccountTypeForAccount(owner))
		result[key.ownerAccountID] = append(result[key.ownerAccountID], value)
		store.index.lru.MoveToFront(element)
	}
	for _, observations := range result {
		sort.Slice(observations, func(i, j int) bool { return observations[i].Model < observations[j].Model })
	}
	return true, result
}

func projectCodexTurnStateObservation(summary CodexTurnStateModelObservation, evidence codexTurnStateObservationEnvelope, accountType string) CodexTurnStateModelObservation {
	value := cloneCodexTurnStateModelObservation(summary)
	// Keep the envelope validation at observation time. An old observation is not
	// a usable cache, and its original validity does not change as the UI is read.
	if !evidence.checked || !evidence.valid || evidence.issuedAt.IsZero() || !evidence.expiresAt.Equal(evidence.issuedAt.Add(CodexTurnStateLifetime)) {
		return value
	}
	value.ResponseShape, value.ResponseValidationReason = "unknown", "unexpected_shape"
	if accountType != "personal" && accountType != "team_business" {
		value.ResponseValidationReason = "account_type_unknown"
		return value
	}
	switch {
	case accountType == "personal" && value.ResponseObservedShape == CodexTurnStateObservedPersonalTarget,
		accountType == "team_business" && value.ResponseObservedShape == CodexTurnStateObservedTeamBusinessTarget:
		value.ResponseShape, value.ResponseValidationReason = "target", ""
	case accountType == "personal" && value.ResponseObservedShape == CodexTurnStateObservedPersonalExtended,
		accountType == "team_business" && value.ResponseObservedShape == CodexTurnStateObservedTeamBusinessExtended:
		value.ResponseShape, value.ResponseValidationReason = "suspect", ""
	}
	return value
}

// snapshot takes one read-only, coherent snapshot for all owners
// requested by a page. Returned values do not share mutable backing storage.
func (store *codexTurnStateSummaryStore) snapshot(ownerAccountIDs []int64) (bool, map[int64][]CodexTurnStateModelObservation) {
	result := make(map[int64][]CodexTurnStateModelObservation)
	if store == nil {
		return false, result
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	owners := make(map[int64]bool, len(ownerAccountIDs))
	for _, id := range ownerAccountIDs {
		owners[id] = true
	}
	for key, element := range store.index.entries {
		if !owners[key.ownerAccountID] {
			continue
		}
		entry := element.Value.(*codexTurnStateObservationIndexEntry)
		result[key.ownerAccountID] = append(result[key.ownerAccountID], cloneCodexTurnStateModelObservation(entry.summary))
		store.index.lru.MoveToFront(element)
	}
	for _, observations := range result {
		sort.Slice(observations, func(i, j int) bool { return observations[i].Model < observations[j].Model })
	}
	return true, result
}

func cloneCodexTurnStateModelObservation(value CodexTurnStateModelObservation) CodexTurnStateModelObservation {
	if value.RequestSentAt != nil {
		copied := *value.RequestSentAt
		value.RequestSentAt = &copied
	}
	if value.SnapshotExpiresAt != nil {
		copied := *value.SnapshotExpiresAt
		value.SnapshotExpiresAt = &copied
	}
	if value.BusinessDelivered != nil {
		copied := *value.BusinessDelivered
		value.BusinessDelivered = &copied
	}
	return value
}
