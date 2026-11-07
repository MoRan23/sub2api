package dto

import (
	"encoding/json"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestAccountCodexEngineCapacitySummary(t *testing.T) {
	owned := &CodexEngineCapacity{Limit: 0, SchedulingPausedByCapacity: true}
	unowned := &CodexEngineCapacity{Limit: 0, SchedulingPausedByCapacity: false}
	const zeroCapacity = `{"identity":"previous-engine-identity","limit":0,"pause_revision":0}`
	tests := []struct {
		name   string
		raw    string
		change func(*service.Account)
		want   *CodexEngineCapacity
	}{
		{name: "legacy observation owns revision zero", raw: zeroCapacity, want: owned},
		{name: "observation generation", raw: `{"identity":"engine","limit":0,"pause_revision":0,"observation_id":"generation"}`, want: owned},
		{name: "identity edit preserves ownership", raw: zeroCapacity, change: func(a *service.Account) {
			a.Credentials["base_url"] = "https://new-engine.example/v1"
			a.Credentials["api_key"] = "replacement-secret-key"
		}, want: owned},
		{name: "manual pause revokes ownership", raw: zeroCapacity, change: func(a *service.Account) {
			a.SchedulingRevision++
		}, want: unowned},
		{name: "already schedulable", raw: zeroCapacity, change: func(a *service.Account) {
			a.Schedulable = true
		}, want: unowned},
		{name: "missing pause revision", raw: `{"identity":"engine","limit":0}`, want: unowned},
		{name: "null pause revision", raw: `{"identity":"engine","limit":0,"pause_revision":null}`, want: unowned},
		{name: "positive capacity cannot own stale pause", raw: `{"identity":"engine","limit":20,"pause_revision":0}`, want: &CodexEngineCapacity{Limit: 20}},
		{name: "largest accepted limit", raw: `{"identity":"engine","limit":2147483647}`, want: &CodexEngineCapacity{Limit: 2147483647}},
		{name: "missing observation"},
		{name: "null observation", raw: `null`},
		{name: "non-object observation", raw: `[]`},
		{name: "missing limit", raw: `{"identity":"engine"}`},
		{name: "null limit", raw: `{"identity":"engine","limit":null}`},
		{name: "string limit", raw: `{"identity":"engine","limit":"0"}`},
		{name: "fractional limit", raw: `{"identity":"engine","limit":0.5}`},
		{name: "negative limit", raw: `{"identity":"engine","limit":-1}`},
		{name: "limit exceeds protocol range", raw: `{"identity":"engine","limit":2147483648}`},
		{name: "missing identity", raw: `{"limit":0,"pause_revision":0}`},
		{name: "empty identity", raw: `{"identity":"","limit":0,"pause_revision":0}`},
		{name: "malformed pause revision", raw: `{"identity":"engine","limit":0,"pause_revision":"0"}`},
		{name: "fractional pause revision", raw: `{"identity":"engine","limit":0,"pause_revision":0.5}`},
		{name: "generic API key", raw: zeroCapacity, change: func(a *service.Account) {
			a.Extra[service.OpenAIAPIKeyModeExtraKey] = "generic"
		}},
		{name: "non-OpenAI account", raw: zeroCapacity, change: func(a *service.Account) {
			a.Platform = service.PlatformAnthropic
		}},
		{name: "OAuth account", raw: zeroCapacity, change: func(a *service.Account) {
			a.Type = service.AccountTypeOAuth
		}},
		{name: "shadow account", raw: zeroCapacity, change: func(a *service.Account) {
			parent := int64(1)
			a.ParentAccountID = &parent
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			account := &service.Account{
				ID: 27, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey,
				Credentials: map[string]any{"api_key": "secret-key", "base_url": "https://engine.example/v1"},
				Extra:       map[string]any{service.OpenAIAPIKeyModeExtraKey: "codex_engine"},
				Status:      service.StatusActive, Concurrency: 30, Schedulable: false, SchedulingRevision: 0,
			}
			if tt.raw != "" {
				var metadata any
				require.NoError(t, json.Unmarshal([]byte(tt.raw), &metadata))
				account.Extra[service.CodexEngineCapacitySyncExtraKey] = metadata
			}
			if tt.change != nil {
				tt.change(account)
			}

			full := AccountFromService(account)
			lite := AccountListItemFromAccount(full)
			require.Equal(t, tt.want, full.CodexEngineCapacity)
			require.Equal(t, tt.want, lite.CodexEngineCapacity)
			for _, response := range []any{full, lite} {
				raw, err := json.Marshal(response)
				require.NoError(t, err)
				require.NotContains(t, string(raw), "secret-key")
				var body map[string]json.RawMessage
				require.NoError(t, json.Unmarshal(raw, &body))
				if tt.want == nil {
					require.NotContains(t, body, "codex_engine_capacity")
					continue
				}
				var summary map[string]any
				require.NoError(t, json.Unmarshal(body["codex_engine_capacity"], &summary))
				require.Equal(t, map[string]any{
					"limit": float64(tt.want.Limit), "scheduling_paused_by_capacity": tt.want.SchedulingPausedByCapacity,
				}, summary, "the public summary must contain only capacity and pause ownership")
			}
		})
	}
}
