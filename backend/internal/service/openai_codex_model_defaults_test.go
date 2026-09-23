package service

import (
	"encoding/json"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/stretchr/testify/require"
)

func TestBundledCodexModelDefaultsCurrentGPT6Contract(t *testing.T) {
	for _, tc := range []struct {
		model, effort string
		ultra, review bool
	}{
		{"gpt-6-astra", "low", true, true},
		{"gpt-6-sol", "medium", true, true},
		{"gpt-6-luna", "medium", false, false},
	} {
		t.Run(tc.model, func(t *testing.T) {
			body, err := BuildCodexModelsManifest([]string{tc.model})
			require.NoError(t, err)
			model := decodeCodexManifestModels(t, body)[0]
			require.Equal(t, tc.effort, model["default_reasoning_level"])
			require.EqualValues(t, 272000, model["context_window"])
			require.EqualValues(t, 872000, model["max_context_window"])
			require.Equal(t, "shell_command", model["shell_type"])
			require.Equal(t, "v2", model["multi_agent_version"])
			require.Equal(t, true, model["supports_reasoning_summaries"])
			if tc.model != "gpt-6-astra" {
				require.Equal(t, "priority", model["default_service_tier"])
			}
			require.Equal(t, tc.review, model["node_repl_auto_review_required"])
			require.Equal(t, tc.ultra, stringSliceContains(effortsFromManifestModel(t, model), "ultra"))
			require.Equal(t, false, model["use_responses_lite"], "no account means no transport capability evidence")
			require.Equal(t, []any{"text"}, model["input_modalities"])
			messages := model["model_messages"].(map[string]any)
			require.Equal(t, openai.CodexBaseInstructionsForModel(tc.model), messages["instructions_template"])
			require.NotNil(t, messages["approvals"])
			require.NotEmpty(t, messages["persistent_instructions"])
			require.NotNil(t, messages["multi_agent"])
		})
	}
}

func TestBundledCodexModelDefaultsPreserveAccountRouteConstraints(t *testing.T) {
	for _, modelID := range []string{"gpt-6-sol", "gpt-6-luna"} {
		for _, tc := range []struct {
			name, kind, baseURL string
			lite, search, image bool
		}{
			{"oauth", AccountTypeOAuth, "https://chatgpt.com", true, true, true},
			{"official_api", AccountTypeAPIKey, "https://api.openai.com/v1", false, true, true},
			{"relay", AccountTypeAPIKey, "https://relay.example/v1", false, false, false},
		} {
			t.Run(modelID+"/"+tc.name, func(t *testing.T) {
				account := Account{Platform: PlatformOpenAI, Type: tc.kind, Credentials: map[string]any{
					"base_url": tc.baseURL, "model_mapping": map[string]any{"my-model": modelID},
				}}
				if tc.name == "relay" {
					account.SetUpstreamModelMetadataSnapshot(UpstreamModelMetadataSnapshot{Models: map[string]UpstreamModelMetadata{
						modelID: {InputModalities: []string{"text"}},
					}})
				}
				body, err := buildCodexModelsManifestForAccounts(PlatformOpenAI, []string{"my-model"}, []Account{account}, nil, nil, true)
				require.NoError(t, err)
				model := decodeCodexManifestModels(t, body)[0]
				require.Equal(t, "my-model", model["slug"])
				require.Equal(t, "my-model", model["display_name"])
				require.Equal(t, tc.lite, model["use_responses_lite"])
				require.Equal(t, tc.search, model["supports_search_tool"])
				if tc.image {
					require.Contains(t, model["input_modalities"], "image")
				} else {
					require.Equal(t, []any{"text"}, model["input_modalities"])
				}
			})
		}
	}
}

func TestBundledCodexModelDefaultsDoNotOverrideLiveMetadata(t *testing.T) {
	account := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{
		"base_url": "https://relay.example/v1", "model_mapping": map[string]any{"my-sol": "gpt-6-sol"},
	}}
	body := []byte(`{"models":[{"slug":"gpt-6-sol","description":"Provider description","context_window":1000000,"shell_type":"unified_exec","service_tiers":[{"id":"ultrafast","name":"Provider Fast"}],"use_responses_lite":true,"model_messages":{"instructions_template":"provider instructions","permissions":{"custom":"keep"}}},{"slug":"my-sol","use_responses_lite":true},{"slug":"gpt-6-luna","use_responses_lite":true}]}`)
	completed, err := completeAPIKeyCodexModelsManifestMetadata(body, true, account)
	require.NoError(t, err)
	completed, err = adjustAPIKeyCodexModelsManifest(completed, account)
	require.NoError(t, err)
	models := decodeCodexManifestModels(t, completed)
	model := models[0]
	require.Equal(t, "Provider description", model["description"])
	require.EqualValues(t, 1000000, model["context_window"])
	require.EqualValues(t, 1000000, model["max_context_window"])
	require.Equal(t, "unified_exec", model["shell_type"])
	require.Equal(t, "ultrafast", model["service_tiers"].([]any)[0].(map[string]any)["id"])
	messages := model["model_messages"].(map[string]any)
	require.Equal(t, "provider instructions", messages["instructions_template"])
	require.Equal(t, "keep", messages["permissions"].(map[string]any)["custom"])
	for _, entry := range models {
		require.Equal(t, false, entry["use_responses_lite"])
	}
}

func TestBundledCodexModelDefaultsDescriptorsAreIndependent(t *testing.T) {
	first := newConfiguredCodexModelDescriptor("gpt-6-sol")
	first.SupportedReasoningLevels[0].Effort = "changed"
	first.ModelMessages.Approvals.(map[string]any)["private-test"] = true
	next := newConfiguredCodexModelDescriptor("gpt-6-sol")
	require.Equal(t, "low", next.SupportedReasoningLevels[0].Effort)
	require.NotContains(t, next.ModelMessages.Approvals, "private-test")
	unknown := newConfiguredCodexModelDescriptor("my-unrecognized-model")
	require.Equal(t, []string{"none"}, effortsFromConfiguredCodexLevels(unknown.SupportedReasoningLevels))
	require.Nil(t, unknown.ModelMessages.Permissions)
	// Every data entry must deserialize into the client-facing contract.
	for _, raw := range bundledCodexModelDefaults {
		var descriptor configuredCodexModelDescriptor
		require.NoError(t, json.Unmarshal(raw, &descriptor))
		require.NotEmpty(t, descriptor.Slug)
	}
}

func TestBundledCodexModelDefaultsKnownAliasesOnly(t *testing.T) {
	for alias, base := range map[string]string{
		"gpt-6": "gpt-6-astra", "gpt-5.6": "gpt-5.6-sol",
		"openai/GPT-6-Sol-high": "gpt-6-sol", "gpt-6-luna-max": "gpt-6-luna",
		"gpt-6-astra-2026-09-01": "gpt-6-astra", "gpt-6-sol-2026-09-23": "gpt-6-sol",
	} {
		model := newConfiguredCodexModelDescriptor(alias)
		canonical := newConfiguredCodexModelDescriptor(base)
		require.Equal(t, alias, model.Slug)
		require.Equal(t, canonical.ModelMessages.InstructionsTemplate, model.ModelMessages.InstructionsTemplate)
		require.Equal(t, canonical.SupportedReasoningLevels, model.SupportedReasoningLevels)
		require.Equal(t, canonical.DefaultReasoningLevel, model.DefaultReasoningLevel)
		require.Equal(t, canonical.ShellType, model.ShellType)
	}
	for _, unknown := range []string{"gpt-6-other", "gpt-6-sol-custom", "company-gpt-5.5-private", "gpt-5-unknown"} {
		require.Nil(t, bundledCodexModelDefault(unknown), unknown)
	}
}
