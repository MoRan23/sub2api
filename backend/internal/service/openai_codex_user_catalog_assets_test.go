package service

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUserCatalogBundledOfficialSnapshotIsComplete(t *testing.T) {
	var catalog struct {
		Models []map[string]json.RawMessage `json:"models"`
	}
	require.NoError(t, json.Unmarshal(bundledOfficialCodexCatalogJSON, &catalog))
	require.NotEmpty(t, catalog.Models)
	seen := make(map[string]bool, len(catalog.Models))
	for _, model := range catalog.Models {
		var slug string
		require.NoError(t, json.Unmarshal(model["slug"], &slug))
		require.NotEmpty(t, slug)
		require.False(t, seen[slug], "duplicate model %s", slug)
		seen[slug] = true
		var messages map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(model["model_messages"], &messages))
		var instructions string
		require.NoError(t, json.Unmarshal(messages["instructions_template"], &instructions))
		require.NotEmpty(t, instructions, "complete instructions for %s", slug)
	}
	require.True(t, seen["gpt-6.1-terra"])
	require.True(t, seen["gpt-6.2-sol"])
	require.True(t, seen["gpt-6.2-astra"])
}

func TestGPT62GroupCatalogUsesCompleteFallbackWithoutReplacingLiveFields(t *testing.T) {
	for _, model := range []string{"gpt-6.2-sol", "gpt-6.2-astra"} {
		t.Run(model, func(t *testing.T) {
			raw, sourceID := selectOpenAIGroupCodexDescriptor(model, model, nil, nil, nil)
			require.Equal(t, model, sourceID)
			require.NotEmpty(t, raw)
			projected, err := projectOfficialCodexDescriptor(raw, model, sourceID, nil)
			require.NoError(t, err)
			var got map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(projected, &got))
			var before, after map[string]any
			require.NoError(t, json.Unmarshal(raw, &before))
			require.NoError(t, json.Unmarshal(projected, &after))
			before["shell_type"] = "unified_exec"
			require.Equal(t, before, after, "preserve complete official JSON values")
			require.Equal(t, model, string(bytes.Trim(got["slug"], `"`)))
			require.Equal(t, `"unified_exec"`, string(got["shell_type"]))
			var gotMessages map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(got["model_messages"], &gotMessages))
			require.Contains(t, gotMessages, "permissions")
			require.NotNil(t, got["model_messages"], "full official descriptor preserves nested fields")
			var messages struct {
				Instructions string `json:"instructions_template"`
			}
			require.NoError(t, json.Unmarshal(got["model_messages"], &messages))
			require.NotEmpty(t, messages.Instructions)

			live := json.RawMessage(`{"slug":"` + model + `","context_window":123456,"unknown":null,"service_tiers":[]}`)
			for _, source := range []string{"fixed", "oauth", "public"} {
				catalog := map[string]json.RawMessage{model: live}
				var fixed, oauth, public map[string]json.RawMessage
				switch source {
				case "fixed":
					fixed = catalog
				case "oauth":
					oauth = catalog
				case "public":
					public = catalog
				}
				selected, _ := selectOpenAIGroupCodexDescriptor(model, "gpt-6.2-astra", fixed, oauth, public)
				require.Equal(t, live, selected, "live %s fields win over bundled metadata", source)
			}
			alias, aliasSource := selectOpenAIGroupCodexDescriptor("private-model", model, nil, nil, nil)
			require.Equal(t, model, aliasSource)
			require.Equal(t, raw, alias)
		})
	}
}

func TestUserCatalogCyberDescriptorsPreserveDeclaredCapabilities(t *testing.T) {
	for _, slug := range []string{"gpt-5.6-cyber", "gpt-6-cyber"} {
		t.Run(slug, func(t *testing.T) {
			raw, ok := codexUserModelDescriptorOverride(slug)
			require.True(t, ok)
			var descriptor struct {
				Slug       string `json:"slug"`
				Shell      string `json:"shell_type"`
				Lite       bool   `json:"use_responses_lite"`
				Search     bool   `json:"supports_search_tool"`
				ToolMode   string `json:"tool_mode"`
				Context    int    `json:"context_window"`
				MaxContext int    `json:"max_context_window"`
				Reasoning  []struct {
					Effort string `json:"effort"`
				} `json:"supported_reasoning_levels"`
				Messages map[string]json.RawMessage `json:"model_messages"`
			}
			require.NoError(t, json.Unmarshal(raw, &descriptor))
			require.Equal(t, slug, descriptor.Slug)
			require.Equal(t, "unified_exec", descriptor.Shell)
			require.True(t, descriptor.Lite)
			require.True(t, descriptor.Search)
			require.Equal(t, "code_mode_only", descriptor.ToolMode)
			require.Equal(t, 272000, descriptor.Context)
			require.Equal(t, 872000, descriptor.MaxContext)
			efforts := make([]string, len(descriptor.Reasoning))
			for i, level := range descriptor.Reasoning {
				efforts[i] = level.Effort
			}
			require.Equal(t, []string{"low", "medium", "high", "xhigh", "max", "ultra"}, efforts)
			require.NotEmpty(t, descriptor.Messages["instructions_template"])
			require.JSONEq(t, "null", string(descriptor.Messages["permissions"]))
		})
	}
	for _, unknown := range []string{"gpt-6", "gpt-6-cyber-custom", "gpt-5.6-cyber-preview"} {
		_, ok := codexUserModelDescriptorOverride(unknown)
		require.False(t, ok)
	}
}
