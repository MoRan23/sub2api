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
	ownerAccountID int64
	model          string
}

type codexTurnStateObservationIndexEntry struct {
	key      codexTurnStateObservationKey
	sequence uint64
	summary  CodexTurnStateModelObservation
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
	key := codexTurnStateObservationKey{ownerAccountID: ownerAccountID, model: value.Model}
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
	entry.summary = CodexTurnStateModelObservation{
		Model:                    value.Model,
		ObservedAt:               observedAt,
		ResponseLength:           value.ResponseLength,
		ResponseShape:            shape,
		ResponseObservedShape:    value.ResponseObservedShape,
		ResponseCipherBlocks:     value.ResponseCipherBlocks,
		ResponseValidationReason: value.ResponseValidationReason,
		ResponseSource:           value.ResponseSource,
		OutboundLength:           value.OutboundLength,
	}
	index.lru.MoveToFront(element)
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
		result[key.ownerAccountID] = append(result[key.ownerAccountID], entry.summary)
		store.index.lru.MoveToFront(element)
	}
	for _, observations := range result {
		sort.Slice(observations, func(i, j int) bool { return observations[i].Model < observations[j].Model })
	}
	return true, result
}
