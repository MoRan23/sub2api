package service

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAttributionManualResultWritebackPermission(t *testing.T) {
	for _, mode := range []string{"generic", "codex_engine"} {
		for _, tc := range []struct {
			name, source, accountType, reason string
			frozen                            *bool
			current                           any
		}{
			{name: "missing preference", source: "manual", frozen: new(false), reason: "manual_auto_attribution_disabled"},
			{name: "disabled", source: "manual", frozen: new(false), current: false, reason: "manual_auto_attribution_disabled"},
			{name: "enabled later", source: "manual", frozen: new(false), current: true, reason: "manual_auto_attribution_disabled"},
			{name: "disabled later", source: "manual", frozen: new(true), current: false, reason: "manual_auto_attribution_disabled"},
			{name: "invalid preference", source: "manual", frozen: new(true), current: "true", reason: "manual_auto_attribution_disabled"},
			{name: "legacy enabled", source: "manual", current: true, reason: "manual_auto_attribution_unrecorded"},
			{name: "enabled", source: "manual", frozen: new(true), current: true},
			{name: "scheduled", source: "scheduled", current: false},
			{name: "initial", source: "initial", current: false},
			{name: "oauth", source: "manual", accountType: AccountTypeOAuth},
		} {
			t.Run(mode+"/"+tc.name, func(t *testing.T) {
				a := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Extra: map[string]any{OpenAIAPIKeyModeExtraKey: mode}}
				if tc.accountType != "" {
					a.Type = tc.accountType
				}
				if tc.current != nil {
					a.Extra[OpenAIAPIKeyAutoAttributionEnabledExtraKey] = tc.current
				}
				j := &AttributionJob{Source: tc.source, Snapshot: AttributionSnapshot{Version: 4, ManualResultUpdatesAllowed: tc.frozen}}
				encoded, err := json.Marshal(j.Snapshot)
				require.NoError(t, err)
				require.NoError(t, json.Unmarshal(encoded, &j.Snapshot))
				require.Equal(t, tc.reason, j.ManualResultWritebackReason(a))
			})
		}
	}
}
