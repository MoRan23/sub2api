package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGroupOfficialCatalogAudioFollowsDeclaredRoutes(t *testing.T) {
	const model = "gpt-6.2-sol"
	source := catalogFixture(`{"models":[{"slug":"gpt-6.2-sol","input_modalities":["text","image","audio"],"unknown":null}]}`)
	group := &Group{ID: 1, Platform: PlatformOpenAI, ModelAllowlist: GroupModelAllowlist{Enabled: true, Models: []string{model}}}
	for _, tc := range []struct {
		name      string
		oauth     bool
		protocol  string
		metadata  bool
		fixed     string
		wantAudio bool
	}{
		{name: "public snapshot is not API key evidence"},
		{name: "OAuth official declaration", oauth: true, wantAudio: true},
		{name: "declared Responses", protocol: APIProtocolResponses, metadata: true, wantAudio: true},
		{name: "declared Chat bridge", protocol: APIProtocolChatCompletions, metadata: true, wantAudio: true},
		{name: "fixed manifest declaration", protocol: APIProtocolChatCompletions, fixed: `{"models":[{"slug":"gpt-6.2-sol","input_modalities":["text","image","audio"]}]}`, wantAudio: true},
		{name: "fixed partial declaration", protocol: APIProtocolChatCompletions, fixed: `{"data":[{"id":"gpt-6.2-sol","input_modalities":["text","image","audio"]}]}`, wantAudio: true},
		{name: "fixed IDs only", fixed: `{"data":[{"id":"gpt-6.2-sol"}]}`},
		{name: "fixed explicit revocation", metadata: true, fixed: `{"models":[{"slug":"gpt-6.2-sol","input_modalities":["text","image"]}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			account := groupCatalogAccount(1, map[string]any{model: model})
			if tc.protocol == APIProtocolChatCompletions {
				account.Extra = map[string]any{"openai_responses_mode": "force_chat_completions"}
			}
			if tc.oauth {
				account.Type = AccountTypeOAuth
			}
			if tc.metadata {
				account.SetUpstreamModelMetadataSnapshot(UpstreamModelMetadataSnapshot{Models: map[string]UpstreamModelMetadata{
					model: {InputModalities: []string{"text", "image", "audio"}},
				}})
			}
			var fixed []pinnedOpenAIModelsResult
			if tc.fixed != "" {
				fixed = []pinnedOpenAIModelsResult{{account: &account, response: catalogFixture(`{"data":[{"id":"gpt-6.2-sol"}]}`)}}
				fixed[0].response.upstreamSourceBody = []byte(tc.fixed)
			}
			response, _, err := buildOpenAIGroupModelsCatalog(group, []Account{account}, []Account{account}, fixed, source, source, "", true, nil)
			require.NoError(t, err)
			models := decodeCodexManifestModels(t, response.Body)
			require.Len(t, models, 1)
			want := []any{"text", "image"}
			if tc.wantAudio {
				want = append(want, "audio")
			}
			require.Equal(t, want, models[0]["input_modalities"])
			require.Contains(t, string(source.Body), `"audio"`, "shared source must stay immutable")
		})
	}
	anthropic := &Account{Platform: PlatformDeepseek, Type: AccountTypeAPIKey,
		Credentials: map[string]any{"api_protocol": APIProtocolAnthropic}}
	require.False(t, accountCodexModelSupportsAudioInput(anthropic, model, []string{"text", "audio"}))
}

func TestGroupCatalogAudioRespectsMappingAndRouting(t *testing.T) {
	group := &Group{ID: 1, Platform: PlatformOpenAI, ModelRoutingEnabled: true, ModelRouting: map[string][]int64{"alias": {1}}}
	good := groupCatalogAccount(1, map[string]any{"alias": "custom-audio"})
	good.SetUpstreamModelMetadataSnapshot(UpstreamModelMetadataSnapshot{Models: map[string]UpstreamModelMetadata{
		"custom-audio": {InputModalities: []string{"text", "audio"}},
	}})
	unknown := groupCatalogAccount(2, map[string]any{"alias": "custom-audio"})
	accounts := []Account{good, unknown}
	for _, source := range []*OpenAIModelsResponse{
		catalogFixture(`{"models":[{"slug":"custom-audio","input_modalities":["text","audio"]}]}`),
		catalogFixture(`{"models":[]}`),
	} {
		for _, routed := range []bool{true, false} {
			group.ModelRoutingEnabled = routed
			response, _, err := buildOpenAIGroupModelsCatalog(group, accounts, accounts, nil, source, source, "", true, nil)
			require.NoError(t, err)
			models := decodeCodexManifestModels(t, response.Body)
			require.Len(t, models, 1)
			modalities := models[0]["input_modalities"].([]any)
			if routed {
				require.Contains(t, modalities, "audio")
				continue
			}
			require.NotContains(t, modalities, "audio")
		}
	}
}
