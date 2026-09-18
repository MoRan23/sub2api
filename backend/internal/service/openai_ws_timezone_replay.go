package service

import (
	"bytes"
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
)

// A replay source is identified by the immutable RawMessage supplied by the
// replay builders, never by its text. Equal text in a later reference does not
// inherit an earlier environment's frozen eligibility.
type openAIWSTimezoneReplayItemID struct {
	first *byte
	size  int
}

type openAIWSTimezoneReplayRecord struct {
	state      *RequestTimezoneState // Paths are relative to a one-item input array.
	projected  *RequestTimezoneState
	historical bool
}

type openAIWSTimezoneReplayLedger struct {
	items          map[openAIWSTimezoneReplayItemID]*openAIWSTimezoneReplayRecord
	projectedItems map[openAIWSTimezoneReplayItemID]*openAIWSTimezoneReplayRecord
}

func newOpenAIWSTimezoneReplayLedger() *openAIWSTimezoneReplayLedger {
	return &openAIWSTimezoneReplayLedger{items: make(map[openAIWSTimezoneReplayItemID]*openAIWSTimezoneReplayRecord)}
}

func openAIWSTimezoneReplayIdentity(item json.RawMessage) openAIWSTimezoneReplayItemID {
	if len(item) == 0 {
		return openAIWSTimezoneReplayItemID{}
	}
	return openAIWSTimezoneReplayItemID{first: &item[0], size: len(item)}
}

// Record restores only fields owned by state and binds the resulting neutral
// input items to their frozen source state. It never scans for new eligibility.
func (ledger *openAIWSTimezoneReplayLedger) Record(body []byte, state *RequestTimezoneState) ([]json.RawMessage, bool, error) {
	if ledger == nil || state == nil {
		return openAIWSExtractNormalizedInputSequence(body)
	}
	inputPaths := make(map[string]string)
	for _, path := range openAIWSTimezoneReplayStatePaths(state) {
		if path == "input" || strings.HasPrefix(path, "input.") {
			inputPaths[path] = path
		}
	}
	// Replay owns input only. A tools adapter may already have removed a search
	// source, and that unrelated change cannot reject otherwise valid input.
	inputState := RemapRequestTimezoneState(state, inputPaths)
	neutral, ok := inputState.UndoToBody(body)
	if !ok {
		return nil, false, errors.New("restore frozen websocket replay timezone sources")
	}
	items, exists, err := openAIWSExtractNormalizedInputSequence(neutral)
	if err != nil || !exists {
		return items, exists, err
	}
	if ledger.items == nil {
		ledger.items = make(map[openAIWSTimezoneReplayItemID]*openAIWSTimezoneReplayRecord)
	}
	inputIsArray := gjson.GetBytes(neutral, "input").IsArray()
	paths := openAIWSTimezoneReplayStatePaths(inputState)
	for index, item := range items {
		mapped := make(map[string]string)
		for _, path := range paths {
			if relative, belongs := openAIWSTimezoneReplayItemPath(path, index, inputIsArray); belongs {
				mapped[path] = relative
			}
		}
		if len(mapped) == 0 || len(item) == 0 {
			continue
		}
		itemState := RemapRequestTimezoneState(inputState, mapped)
		ledger.items[openAIWSTimezoneReplayIdentity(item)] = &openAIWSTimezoneReplayRecord{
			state: itemState, projected: itemState,
		}
	}
	return items, true, nil
}

// ProjectReplay projects a copy of a neutral replay sequence for this account.
// The supplied payload still describes the current turn: its non-input sources
// are retained, while input provenance comes exclusively from recorded items.
func (ledger *openAIWSTimezoneReplayLedger) ProjectReplay(payload []byte, items []json.RawMessage, target RequestLocationObservation, currentState *RequestTimezoneState) ([]json.RawMessage, *RequestTimezoneState, bool) {
	if ledger == nil || currentState == nil {
		return items, nil, true
	}
	states := make([]*RequestTimezoneState, 0, len(items)+1)
	for index, item := range items {
		record := ledger.items[openAIWSTimezoneReplayIdentity(item)]
		if record == nil {
			continue
		}
		states = append(states, RemapRequestTimezoneState(record.state, openAIWSTimezoneReplayRemapItem(record.state, index)))
	}
	currentPaths := make(map[string]string)
	for _, path := range openAIWSTimezoneReplayStatePaths(currentState) {
		if path != "input" && !strings.HasPrefix(path, "input.") {
			currentPaths[path] = path
		}
	}
	// The final state contributes this accepted turn's policy, clock and route;
	// merged historical sources retain their own frozen dates and eligibility.
	states = append(states, RemapRequestTimezoneState(currentState, currentPaths))
	combined := MergeRequestTimezoneStates(states...)
	replayBody, err := setOpenAIWSPayloadInputSequence(payload, items, true)
	if err != nil {
		return items, combined, false
	}
	projectedBody, projectedState, ok := combined.ProjectToTarget(replayBody, target)
	if !ok {
		return items, projectedState, false
	}
	projectedItems, _, err := openAIWSExtractNormalizedInputSequence(projectedBody)
	if err != nil || len(projectedItems) != len(items) {
		return items, projectedState, false
	}
	// Remapping intentionally discards old whole-body snapshots. This aggregate
	// now describes the expanded replay, so observations and a failover retry
	// must use the corresponding projected body rather than the unexpanded turn.
	projectedState.preparedBody = bytes.Clone(projectedBody)
	// Only the latest projection needs commit aliases. Retaining aliases for
	// every expanded body would keep quadratic amounts of replay storage alive.
	ledger.projectedItems = make(map[openAIWSTimezoneReplayItemID]*openAIWSTimezoneReplayRecord)
	for index, item := range items {
		record := ledger.items[openAIWSTimezoneReplayIdentity(item)]
		if record == nil {
			continue
		}
		// The item's own frozen clock/eligibility gives the same projection as
		// the aggregate, without repeatedly traversing all historical sources.
		record.projected = record.state.WithTarget(target)
		// This alias comes from an explicit preserving projection, not a content
		// comparison. It also lets a caller commit the returned sequence safely.
		if len(projectedItems[index]) > 0 {
			ledger.projectedItems[openAIWSTimezoneReplayIdentity(projectedItems[index])] = record
		}
	}
	return projectedItems, projectedState, true
}

// Commit runs only after a successful turn. A source's last projected date is
// frozen once; repeated commits through the two bridge histories are harmless.
func (ledger *openAIWSTimezoneReplayLedger) Commit(items []json.RawMessage) {
	if ledger == nil {
		return
	}
	for _, item := range items {
		identity := openAIWSTimezoneReplayIdentity(item)
		record := ledger.items[identity]
		if record == nil {
			record = ledger.projectedItems[identity]
		}
		if record == nil || record.historical {
			continue
		}
		record.state = HistoricalRequestTimezoneState(record.projected)
		record.projected = record.state
		record.historical = true
	}
}

// Trim retains only identities still owned by the active replay histories.
// A client may resend its complete history on every turn, creating fresh input
// buffers even for equal text. Rebuilding the maps releases superseded buffers
// and their frozen source states without transferring authority by text equality.
func (ledger *openAIWSTimezoneReplayLedger) Trim(retained ...[]json.RawMessage) {
	if ledger == nil {
		return
	}
	items := make(map[openAIWSTimezoneReplayItemID]*openAIWSTimezoneReplayRecord)
	var projectedItems map[openAIWSTimezoneReplayItemID]*openAIWSTimezoneReplayRecord
	for _, history := range retained {
		for _, item := range history {
			identity := openAIWSTimezoneReplayIdentity(item)
			if record := ledger.items[identity]; record != nil {
				items[identity] = record
			}
			if record := ledger.projectedItems[identity]; record != nil {
				if projectedItems == nil {
					projectedItems = make(map[openAIWSTimezoneReplayItemID]*openAIWSTimezoneReplayRecord)
				}
				projectedItems[identity] = record
			}
		}
	}
	ledger.items = items
	ledger.projectedItems = projectedItems
}

func openAIWSTimezoneReplayStatePaths(state *RequestTimezoneState) []string {
	if state == nil {
		return nil
	}
	seen := make(map[string]struct{})
	paths := make([]string, 0, len(state.Conversions))
	add := func(path string) {
		if path == "" {
			return
		}
		if _, exists := seen[path]; !exists {
			seen[path] = struct{}{}
			paths = append(paths, path)
		}
	}
	for _, conversion := range state.Conversions {
		add(conversion.Path)
	}
	if state.Inbound != nil {
		for _, item := range state.Inbound.Items {
			add(item.Path)
		}
	}
	for _, patch := range state.patches {
		add(patch.path)
	}
	return paths
}

func openAIWSTimezoneReplayItemPath(path string, index int, inputIsArray bool) (string, bool) {
	if !inputIsArray {
		if index != 0 {
			return "", false
		}
		if path == "input" || strings.HasPrefix(path, "input.") {
			return "input.0" + strings.TrimPrefix(path, "input"), true
		}
		return "", false
	}
	prefix := "input." + strconv.Itoa(index)
	if path == prefix || strings.HasPrefix(path, prefix+".") {
		return "input.0" + strings.TrimPrefix(path, prefix), true
	}
	return "", false
}

func openAIWSTimezoneReplayRemapItem(state *RequestTimezoneState, index int) map[string]string {
	paths := make(map[string]string)
	for _, path := range openAIWSTimezoneReplayStatePaths(state) {
		if path == "input.0" || strings.HasPrefix(path, "input.0.") {
			paths[path] = "input." + strconv.Itoa(index) + strings.TrimPrefix(path, "input.0")
		}
	}
	return paths
}
