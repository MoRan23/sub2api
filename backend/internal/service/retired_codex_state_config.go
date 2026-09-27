package service

import (
	"maps"
	"strings"
)

// StripRetiredCodexStateExtra removes obsolete cache configuration and runtime
// data from old clients, imported backups and delayed account snapshots.
func StripRetiredCodexStateExtra(extra map[string]any) map[string]any {
	out := maps.Clone(extra)
	for key := range out {
		if key == "codex_turn_state" || strings.HasPrefix(key, "codex_turn_state_") {
			delete(out, key)
		}
	}
	return out
}
