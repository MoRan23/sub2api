package service

import (
	"container/list"
	"sort"
	"strings"
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

// All fields are protected by the containing fingerprintObserver.mu. This
// separate bounded index outlives ring eviction, but never a disabled observer
// or process restart. Only safe scalar response facts are retained.
type codexTurnStateObservationIndex struct {
	entries        map[codexTurnStateObservationKey]*list.Element
	lru            list.List
	discardThrough uint64
	window         uint64
}

func (index *codexTurnStateObservationIndex) clear(sequence uint64) {
	for key, element := range index.entries {
		entry := element.Value.(*codexTurnStateObservationIndexEntry)
		*entry = codexTurnStateObservationIndexEntry{}
		delete(index.entries, key)
	}
	index.entries = nil
	index.lru.Init()
	// Sequences are monotonic across disable/enable. A delayed completion from
	// an earlier observation window must not recreate cleared history.
	index.discardThrough = sequence
	index.window++
}

func (observer *fingerprintObserver) codexStateObservationWindow() uint64 {
	if observer == nil {
		return 0
	}
	observer.mu.Lock()
	defer observer.mu.Unlock()
	if !observer.enabled.Load() {
		return 0
	}
	return observer.codexStateIndex.window + 1
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

// codexStateObservations takes one read-only, coherent snapshot for all owners
// requested by a page. Returned values do not share mutable backing storage.
func (observer *fingerprintObserver) codexStateObservations(ownerAccountIDs []int64) (bool, map[int64][]CodexTurnStateModelObservation) {
	result := make(map[int64][]CodexTurnStateModelObservation)
	if observer == nil {
		return false, result
	}
	observer.mu.Lock()
	defer observer.mu.Unlock()
	if !observer.enabled.Load() {
		return false, result
	}
	owners := make(map[int64]bool, len(ownerAccountIDs))
	for _, id := range ownerAccountIDs {
		owners[id] = true
	}
	for key, element := range observer.codexStateIndex.entries {
		if !owners[key.ownerAccountID] {
			continue
		}
		entry := element.Value.(*codexTurnStateObservationIndexEntry)
		result[key.ownerAccountID] = append(result[key.ownerAccountID], entry.summary)
		observer.codexStateIndex.lru.MoveToFront(element)
	}
	for _, observations := range result {
		sort.Slice(observations, func(i, j int) bool { return observations[i].Model < observations[j].Model })
	}
	return true, result
}
