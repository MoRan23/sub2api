package service

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCodexWireProfileParsesWindowMetadata(t *testing.T) {
	for _, test := range []struct {
		name, number string
		want         *uint64
	}{
		{"zero", "0", uint64Pointer(0)},
		{"nonzero", "7", uint64Pointer(7)},
		{"maximum", "18446744073709551615", uint64Pointer(^uint64(0))},
		{"null", "null", nil},
		{"negative", "-1", nil},
		{"fraction", "1.5", nil},
		{"string", `"7"`, nil},
		{"overflow", "18446744073709551616", nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw := `{"window_number":` + test.number + `,"context_window_id":"` + codexWireTestContextWindow + `"}`
			nested := ParseCodexWireProfile(raw)
			require.Equal(t, test.want, nested.WindowNumber)
			require.Equal(t, codexWireTestContextWindow, nested.ContextWindowID)
			var flat map[string]json.RawMessage
			require.NoError(t, json.Unmarshal([]byte(raw), &flat))
			profile := parseCodexWireFlatMetadata(flat)
			require.Equal(t, test.want, profile.WindowNumber)
			require.Equal(t, codexWireTestContextWindow, profile.ContextWindowID)
		})
	}
	for _, alias := range []string{"context_window_id", "context-window-id", "x-codex-context-window-id", "x-codex-context_window_id"} {
		profile := captureCodexWireProfile(nil, []byte(`{"client_metadata":{"window_number":0,"`+alias+`":"`+codexWireTestContextWindow+`"}}`), "")
		require.Equal(t, uint64Pointer(0), profile.WindowNumber, alias)
		require.Equal(t, codexWireTestContextWindow, profile.ContextWindowID, alias)
	}
	profile := ParseCodexWireProfile(`{}`)
	require.Nil(t, profile.WindowNumber, "absence is distinct from window zero")
	require.Empty(t, profile.ContextWindowID)
}

func TestCodexWireWindowMetadataMergePreservesZeroAndOwnsPointers(t *testing.T) {
	canonical := ParseCodexWireProfile(`{"window_number":0,"context_window_id":"` + codexWireTestContextWindow + `"}`)
	compatibility := ParseCodexWireProfile(`{"window_number":9,"context_window_id":"` + codexWireTestThread + `"}`)
	mergeCodexWireProfileMissing(&canonical, compatibility)
	require.Equal(t, uint64Pointer(0), canonical.WindowNumber)
	require.Equal(t, codexWireTestContextWindow, canonical.ContextWindowID)
	missing := newCodexWireProfile()
	mergeCodexWireProfileMissing(&missing, compatibility)
	*compatibility.WindowNumber = 10
	require.Equal(t, uint64Pointer(9), missing.WindowNumber)
	require.Equal(t, codexWireTestThread, missing.ContextWindowID)
}

func TestCodexWireProfileTimestampRejectsNull(t *testing.T) {
	for _, raw := range []string{"null", `"0"`, "1.5", "9223372036854775808"} {
		profile := ParseCodexWireProfile(`{"turn_started_at_unix_ms":` + raw + `}`)
		require.False(t, profile.TurnStartedAtSet, raw)
		require.Zero(t, profile.TurnStartedAtUnixMS, raw)
		var metadata map[string]json.RawMessage
		require.NoError(t, json.Unmarshal([]byte(`{"turn_started_at_unix_ms":`+raw+`}`), &metadata))
		flat := parseCodexWireFlatMetadata(metadata)
		require.False(t, flat.TurnStartedAtSet, raw)
	}
	profile := ParseCodexWireProfile(`{"turn_started_at_unix_ms":0}`)
	require.True(t, profile.TurnStartedAtSet, "an explicitly supplied integer zero remains distinct from null")
}
