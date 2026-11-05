package service

import (
	"encoding/json"
	"io"
	"strconv"
	"strings"

	"github.com/tidwall/sjson"
)

// projectTimezone applies only the captured patch group to an immutable parsed
// view. The raw snapshot supplies provenance and encoded lengths; it is never
// rewritten. Counts include discarded fields, so selective materialization does
// not relax the limits on the complete adjusted document.
func (s *OpenAIRequestIntegrityState) projectTimezone(timezone *RequestTimezoneState) (map[string]any, bool) {
	if timezone == nil || len(timezone.patches) == 0 || !timezone.matchesBody(s.baseline) {
		return s.parsed, false
	}
	root := parseRawJSONView(s.baseline)
	projected := s.parsed
	bytes, nodes := len(s.baseline), s.nodes
	changed, trimmedRoot := false, false
	paths := make(map[string]string, len(timezone.patches))
	for _, patch := range timezone.patches {
		if prepared, exists := paths[patch.path]; exists {
			if prepared != patch.prepared {
				return s.parsed, false
			}
			continue // Capture emits one patch per source; an identical repeat is idempotent.
		}
		// Captured sources are disjoint text values or location objects. Reject
		// an unsupported overlapping group before granting any equivalence.
		for path := range paths {
			if strings.HasPrefix(path, patch.path+".") || strings.HasPrefix(patch.path, path+".") {
				return s.parsed, false
			}
		}
		paths[patch.path] = patch.prepared
		original := root.Get(patch.path)
		if original.Raw == patch.prepared {
			continue
		}
		parts := strings.Split(patch.path, ".")
		value, addedNodes, reason := readRequestIntegrityFragment(patch.prepared, len(parts), true)
		if reason != "" {
			return s.parsed, false
		}
		if original.Exists() {
			_, oldNodes, reason := readRequestIntegrityFragment(original.Raw, 0, false)
			if reason != "" {
				return s.parsed, false
			}
			bytes += len(patch.prepared) - len(original.Raw)
			nodes += addedNodes - oldNodes
		} else {
			// Locate the existing object into which sjson would insert the first
			// absent key. Known sources may add user_location or settings itself.
			container, prefix := root, 0
			for prefix < len(parts)-1 {
				next := root.Get(strings.Join(parts[:prefix+1], "."))
				if !next.Exists() {
					break
				}
				container = next
				prefix++
			}
			if !container.IsObject() {
				return s.parsed, false
			}
			// Run the established encoder only on an empty skeleton. Its inner
			// bytes are precisely the insertion; existing body bytes stay shared.
			addition, err := sjson.SetRaw("{}", strings.Join(parts[prefix:], "."), patch.prepared)
			if err != nil {
				return s.parsed, false
			}
			bytes += len(addition) - 2
			// A nonempty object also receives a comma, after its original spaces.
			containerRaw := strings.TrimSpace(container.Raw)
			if strings.TrimSpace(containerRaw[1:len(containerRaw)-1]) != "" {
				bytes++
			}
			if prefix == 0 && !trimmedRoot {
				bytes -= len(s.baseline) - len(strings.TrimSpace(root.Raw))
				trimmedRoot = true
			}
			nodes += addedNodes + 1 + 2*(len(parts)-prefix-1)
		}
		if requestIntegrityContentField(parts[0]) {
			updated, ok := setRequestIntegrityPath(projected, parts, value)
			if !ok {
				return s.parsed, false
			}
			projected = updated.(map[string]any)
		}
		changed = true
	}
	if bytes > requestIntegrityMaxBytes || nodes > requestIntegrityMaxNodes {
		return s.parsed, false
	}
	return projected, changed
}

func readRequestIntegrityFragment(raw string, depth int, retain bool) (any, int, string) {
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	nodes := 0
	value, reason := readRequestIntegrityJSON(decoder, depth, &nodes, retain, false)
	if reason == "" {
		if _, err := decoder.Token(); err != io.EOF {
			reason = "invalid_json"
		}
	}
	return value, nodes, reason
}

// Every changed ancestor is private; untouched children remain immutable views.
func setRequestIntegrityPath(current any, path []string, replacement any) (any, bool) {
	if len(path) == 0 {
		return replacement, true
	}
	switch value := current.(type) {
	case map[string]any:
		child := value[path[0]]
		if child == nil && len(path) > 1 {
			child = map[string]any{}
		}
		updated, ok := setRequestIntegrityPath(child, path[1:], replacement)
		if !ok {
			return nil, false
		}
		copy := cloneRequestIntegrityObject(value)
		copy[path[0]] = updated
		return copy, true
	case []any:
		index, err := strconv.Atoi(path[0])
		if err != nil || index < 0 || index >= len(value) {
			return nil, false
		}
		updated, ok := setRequestIntegrityPath(value[index], path[1:], replacement)
		if !ok {
			return nil, false
		}
		copy := append([]any{}, value...)
		copy[index] = updated
		return copy, true
	default:
		return nil, false
	}
}
