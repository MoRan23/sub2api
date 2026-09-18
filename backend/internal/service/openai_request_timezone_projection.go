package service

import (
	"bytes"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// UndoToBody removes only this state's exact frozen projection. A changed
// source rejects the entire operation, preventing accidental rollback of tools
// or content modified by an unrelated adapter.
func (s *RequestTimezoneState) UndoToBody(body []byte) ([]byte, bool) {
	if s == nil || len(s.patches) == 0 {
		return bytes.Clone(body), true
	}
	if _, ok := s.ApplyToBody(body); !ok {
		return bytes.Clone(body), false
	}
	out := bytes.Clone(body)
	for _, patch := range s.patches {
		var err error
		if patch.originalExists {
			out, err = sjson.SetRawBytes(out, patch.path, []byte(patch.original))
		} else {
			out, err = sjson.DeleteBytes(out, patch.path)
		}
		if err != nil {
			return bytes.Clone(body), false
		}
		if !patch.containerExists && patch.containerPath != "" {
			container := gjson.GetBytes(out, patch.containerPath)
			if container.IsObject() && len(container.Map()) == 0 {
				out, err = sjson.DeleteBytes(out, patch.containerPath)
				if err != nil {
					return bytes.Clone(body), false
				}
			}
		}
	}
	return out, true
}

func (s *RequestTimezoneState) WithTarget(target RequestLocationObservation) *RequestTimezoneState {
	if s == nil {
		return nil
	}
	result := CloneRequestTimezoneState(s)
	result.Target = target
	result.buildTargetProjection()
	if len(s.preparedBody) != 0 {
		if neutral, ok := s.UndoToBody(s.preparedBody); ok {
			if prepared, valid := result.ApplyToBody(neutral); valid {
				result.preparedBody = prepared
			}
		}
	}
	return result
}

func (s *RequestTimezoneState) ProjectToTarget(body []byte, target RequestLocationObservation) ([]byte, *RequestTimezoneState, bool) {
	if s == nil {
		return bytes.Clone(body), nil, true
	}
	neutral, ok := s.UndoToBody(body)
	if !ok {
		return bytes.Clone(body), CloneRequestTimezoneState(s), false
	}
	state := s.WithTarget(target)
	prepared, ok := state.ApplyToBody(neutral)
	return prepared, state, ok
}

// RemapRequestTimezoneState accepts an explicit adapter ledger. Unmapped and
// removed paths lose their conversion authority; equal text is never authority.
// The result does not retain the original full request body.
func RemapRequestTimezoneState(s *RequestTimezoneState, paths map[string]string) *RequestTimezoneState {
	if s == nil {
		return nil
	}
	result := *s
	result.preparedBody = nil
	result.projectionSources = nil
	result.Inbound = cloneFingerprintTimezoneScan(s.Inbound)
	if result.Inbound != nil {
		result.Inbound.Items = nil
	}
	for _, source := range s.projectionSources {
		path, exists := paths[source.occurrence.item.Path]
		if !exists || path == "" {
			continue
		}
		source.occurrence.item.Path = path
		source.occurrence.item.Location = cloneRequestLocation(source.occurrence.item.Location)
		if !source.occurrence.environment {
			source.occurrence.locationPath = strings.TrimSuffix(path, ".timezone")
		}
		result.projectionSources = append(result.projectionSources, source)
	}
	if result.Inbound != nil {
		for _, item := range s.Inbound.Items {
			if path, exists := paths[item.Path]; exists && path != "" {
				item.Path = path
				result.Inbound.Items = append(result.Inbound.Items, item)
			}
		}
	}
	result.buildTargetProjection()
	return &result
}

// HistoricalRequestTimezoneState keeps the date already sent for a completed
// turn. Only its timezone may change when replaying through another route.
func HistoricalRequestTimezoneState(s *RequestTimezoneState) *RequestTimezoneState {
	if s == nil {
		return nil
	}
	result := CloneRequestTimezoneState(s)
	dates := make(map[string]string, len(s.Conversions))
	for _, report := range s.Conversions {
		dates[report.Path] = report.DateAfter
	}
	for i := range result.projectionSources {
		source := &result.projectionSources[i]
		if source.occurrence.environment && source.occurrence.item.Current {
			source.fixedDate = dates[source.occurrence.item.Path]
			source.occurrence.item.Current = false
			source.occurrence.currentBoundary = false
		}
	}
	if result.Inbound != nil {
		for i := range result.Inbound.Items {
			result.Inbound.Items[i].Current = false
		}
	}
	result.buildTargetProjection()
	return result
}

// MergeRequestTimezoneStates combines only explicit source ledgers. The last
// state provides the active turn's policy/target; each source retains its clock.
func MergeRequestTimezoneStates(states ...*RequestTimezoneState) *RequestTimezoneState {
	var result *RequestTimezoneState
	for _, state := range states {
		if state != nil {
			value := *state
			result = &value
		}
	}
	if result == nil {
		return nil
	}
	result.preparedBody = nil
	result.projectionSources = nil
	result.Inbound = nil
	sources := make(map[string]int)
	for _, state := range states {
		if state == nil {
			continue
		}
		for _, source := range state.projectionSources {
			source.occurrence.item.Location = cloneRequestLocation(source.occurrence.item.Location)
			path := source.occurrence.item.Path
			if index, exists := sources[path]; exists {
				result.projectionSources[index] = source
			} else {
				sources[path] = len(result.projectionSources)
				result.projectionSources = append(result.projectionSources, source)
			}
		}
		if state.Inbound != nil {
			if result.Inbound == nil {
				result.Inbound = &TimezoneScanResult{ScanStatus: state.Inbound.ScanStatus}
			}
			result.Inbound.Items = append(result.Inbound.Items, cloneFingerprintTimezoneScan(state.Inbound).Items...)
		}
	}
	result.buildTargetProjection()
	return result
}
