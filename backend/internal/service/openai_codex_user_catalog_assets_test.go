package service

import (
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
