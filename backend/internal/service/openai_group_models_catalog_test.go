package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func groupCatalogAccount(id int64, mapping map[string]any) Account {
	return Account{ID: id, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true,
		Credentials: map[string]any{"model_mapping": mapping}}
}

func catalogFixture(body string) *OpenAIModelsResponse {
	return &OpenAIModelsResponse{Body: []byte(body)}
}

func TestOpenAIGroupCatalogOfficialDescriptorsRemainComplete(t *testing.T) {
	group := &Group{ID: 1, Platform: PlatformOpenAI}
	account := groupCatalogAccount(1, map[string]any{"gpt-5.6-sol": "gpt-5.6-sol", "alias": "gpt-5.6-sol"})
	live := catalogFixture(`{"models":[{"slug":"gpt-5.6-sol","display_name":"Official Sol","context_window":272000,"shell_type":"shell_command","use_responses_lite":true,"supports_search_tool":true,"code_mode_only":true,"unknown":{"enabled":true},"explicit_null":null}],"catalog_version":"live"}`)
	public := catalogFixture(`{"models":[{"slug":"gpt-5.6-sol","context_window":500000}]}`)
	response, _, err := buildOpenAIGroupModelsCatalog(group, []Account{account}, []Account{account}, nil, live, public, "", true, nil)
	require.NoError(t, err)
	models := decodeCodexManifestModels(t, response.Body)
	require.Len(t, models, 2)
	for _, model := range models {
		require.EqualValues(t, 272000, model["context_window"])
		require.Equal(t, "unified_exec", model["shell_type"])
		require.Equal(t, true, model["use_responses_lite"])
		require.Equal(t, true, model["supports_search_tool"])
		require.Equal(t, true, model["code_mode_only"])
		require.Contains(t, model, "explicit_null")
		require.Equal(t, map[string]any{"enabled": true}, model["unknown"])
	}
	require.Equal(t, "Official Sol", models[0]["display_name"])
	require.Equal(t, "alias", models[1]["display_name"])
	require.Contains(t, string(live.Body), `"shell_type":"shell_command"`, "source bytes stay immutable")
	conditional, _, err := buildOpenAIGroupModelsCatalog(group, []Account{account}, []Account{account}, nil, live, public, response.ETag, true, nil)
	require.NoError(t, err)
	require.True(t, conditional.NotModified)
}

func TestOpenAIGroupCatalogCyberOverrideAndUnknownSynthesis(t *testing.T) {
	group := &Group{ID: 1, Platform: PlatformOpenAI}
	account := groupCatalogAccount(1, map[string]any{"6 Cyber": "gpt-6-cyber", "gpt-5.6-cyber": "gpt-5.6-cyber", "custom-provider-model": "custom-provider-model"})
	empty := catalogFixture(`{"models":[]}`)
	response, _, err := buildOpenAIGroupModelsCatalog(group, []Account{account}, []Account{account}, nil, empty, empty, "", true, nil)
	require.NoError(t, err)
	models := decodeCodexManifestModels(t, response.Body)
	require.Len(t, models, 3)
	byID := make(map[string]map[string]any)
	for _, model := range models {
		byID[model["slug"].(string)] = model
	}
	for alias, canonical := range map[string]string{"6 Cyber": "gpt-6-cyber", "gpt-5.6-cyber": "gpt-5.6-cyber"} {
		raw, ok := codexUserModelDescriptorOverride(canonical)
		require.True(t, ok)
		var expected map[string]any
		require.NoError(t, json.Unmarshal(raw, &expected))
		expected["slug"] = alias
		expected["shell_type"] = "unified_exec"
		if alias != canonical {
			expected["display_name"] = alias
		}
		require.Equal(t, expected, byID[alias])
	}
	require.Equal(t, "unified_exec", byID["custom-provider-model"]["shell_type"])
	require.NotEmpty(t, byID["custom-provider-model"]["supported_reasoning_levels"])
}

func TestOpenAIGroupCatalogFixedSourceOnlyControlsVisibleIDs(t *testing.T) {
	group := &Group{ID: 1, Platform: PlatformOpenAI}
	account := groupCatalogAccount(1, nil)
	fixed := []pinnedOpenAIModelsResult{{account: &account, response: catalogFixture(`{"data":[{"id":"gpt-6-sol","object":"model","created":4,"owned_by":"vendor","context_window":111,"shell_type":"shell_command","provider_extension":true},{"id":"gpt-image-2.6-flare"}]}`)}}
	official := catalogFixture(`{"models":[{"slug":"gpt-6-sol","id":"official-internal-id","object":"internal-model-info","created":999,"owned_by":"internal","context_window":123456,"shell_type":"shell_command","supports_search_tool":true},{"slug":"gpt-6-astra","context_window":999999}]}`)
	manifest, _, err := buildOpenAIGroupModelsCatalog(group, []Account{account}, []Account{account}, fixed, official, official, "", true, nil)
	require.NoError(t, err)
	models := decodeCodexManifestModels(t, manifest.Body)
	require.Len(t, models, 1)
	require.Equal(t, "gpt-6-sol", models[0]["slug"])
	require.EqualValues(t, 123456, models[0]["context_window"])
	require.True(t, models[0]["supports_search_tool"].(bool))
	standard, _, err := buildOpenAIGroupModelsCatalog(group, []Account{account}, []Account{account}, fixed, official, official, "", false, nil)
	require.NoError(t, err)
	var list struct {
		Data []map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(standard.Body, &list))
	require.Len(t, list.Data, 2)
	require.Equal(t, true, list.Data[0]["provider_extension"])
	require.Equal(t, "gpt-6-sol", list.Data[0]["id"])
	require.Equal(t, "model", list.Data[0]["object"])
	require.Equal(t, "vendor", list.Data[0]["owned_by"])
	require.EqualValues(t, 4, list.Data[0]["created"])
	require.EqualValues(t, 123456, list.Data[0]["context_window"])
	require.Equal(t, "unified_exec", list.Data[0]["shell_type"])
	require.Equal(t, "gpt-image-2.6-flare", list.Data[1]["id"])
	require.NotContains(t, string(standard.Body), "gpt-6-astra")
	fixed[0].response = catalogFixture(`{"data":[]}`)
	empty, _, err := buildOpenAIGroupModelsCatalog(group, []Account{account}, []Account{account}, fixed, official, official, "", true, nil)
	require.NoError(t, err)
	require.Empty(t, decodeCodexManifestModels(t, empty.Body))
}

func TestOpenAIGroupCatalogExactWhitelistRequiresRouteAndUsesPublicMetadata(t *testing.T) {
	group := &Group{ID: 1, Platform: PlatformOpenAI, ModelAllowlist: GroupModelAllowlist{Enabled: true, Models: []string{"gpt-6-sol", "alias", "unroutable"}}}
	account := groupCatalogAccount(1, map[string]any{"gpt-6-sol": "gpt-6-sol", "alias": "gpt-6-sol"})
	live := catalogFixture(`{"models":[]}`)
	public := catalogFixture(`{"models":[{"slug":"gpt-6-sol","context_window":777777,"shell_type":"shell_command"}]}`)
	for _, codex := range []bool{false, true} {
		response, _, err := buildOpenAIGroupModelsCatalog(group, []Account{account}, []Account{account}, nil, live, public, "", codex, nil)
		require.NoError(t, err)
		require.NotContains(t, string(response.Body), "unroutable")
		if codex {
			models := decodeCodexManifestModels(t, response.Body)
			require.Len(t, models, 2)
			require.EqualValues(t, 777777, models[0]["context_window"])
		} else {
			var list struct {
				Data []map[string]any `json:"data"`
			}
			require.NoError(t, json.Unmarshal(response.Body, &list))
			require.Len(t, list.Data, 2)
		}
	}
}

func TestOpenAIGroupCatalogPassthroughUnknownModelIgnoresStaleMapping(t *testing.T) {
	account := groupCatalogAccount(1, map[string]any{"custom-llm": "gpt-image-2.6-flare"})
	account.Type = AccountTypeOAuth
	account.Extra = map[string]any{"openai_passthrough": true}
	group := &Group{ID: 1, Platform: PlatformOpenAI, ModelAllowlist: GroupModelAllowlist{Enabled: true, Models: []string{"custom-llm"}}}
	empty := catalogFixture(`{"models":[]}`)
	for _, fixed := range [][]pinnedOpenAIModelsResult{nil, {{account: &account, response: catalogFixture(`{"data":[{"id":"custom-llm"}]}`)}}} {
		for _, codex := range []bool{false, true} {
			response, _, err := buildOpenAIGroupModelsCatalog(group, []Account{account}, []Account{account}, fixed, empty, empty, "", codex, nil)
			require.NoError(t, err)
			require.Contains(t, string(response.Body), "custom-llm")
			require.NotContains(t, string(response.Body), "gpt-image-2.6-flare")
		}
	}
	require.Equal(t, "gpt-image-2.6-flare", account.GetModelMapping()["custom-llm"], "source account remains immutable")
}

func TestOpenAIGroupCatalogRawChatPassthroughKeepsEffectiveMapping(t *testing.T) {
	account := groupCatalogAccount(1, map[string]any{"custom-alias": "gpt-6-sol"})
	account.Extra = map[string]any{"openai_passthrough": true, "openai_responses_supported": false}
	require.True(t, shouldForwardOpenAIResponsesViaRawChatCompletions(&account))
	group := &Group{ID: 1, Platform: PlatformOpenAI, ModelAllowlist: GroupModelAllowlist{Enabled: true, Models: []string{"custom-alias"}}}
	official := catalogFixture(`{"models":[{"slug":"gpt-6-sol","context_window":123456}]}`)
	response, _, err := buildOpenAIGroupModelsCatalog(group, []Account{account}, []Account{account}, nil, official, official, "", true, nil)
	require.NoError(t, err)
	models := decodeCodexManifestModels(t, response.Body)
	require.Len(t, models, 1)
	require.Equal(t, "custom-alias", models[0]["slug"])
	require.EqualValues(t, 123456, models[0]["context_window"])
}

func TestOpenAIGroupCatalogAPIKeyOnlyDoesNotProbeAccounts(t *testing.T) {
	account := groupCatalogAccount(1, nil)
	var calls int
	svc := newCodexModelsAPIKeyTestService(&codexModelsHTTPUpstreamStub{do: func(*http.Request, string, int64, int) (*http.Response, error) { calls++; return nil, context.Canceled }})
	svc.accountRepo = codexModelsVisibilityAccountRepo{byGroup: map[int64][]Account{1: {account}}}
	response, selected, err := svc.BuildOpenAIGroupModelsCatalog(context.Background(), &Group{ID: 1, Platform: PlatformOpenAI}, "", "", true)
	require.NoError(t, err)
	require.Nil(t, selected)
	require.NotEmpty(t, decodeCodexManifestModels(t, response.Body))
	require.Zero(t, calls)
	svc.codexModelCapabilities.mu.Lock()
	require.Empty(t, svc.codexModelCapabilities.entries, "public metadata must never become account authorization evidence")
	require.Empty(t, svc.codexModelCapabilities.observations)
	svc.codexModelCapabilities.mu.Unlock()
}

func TestOpenAIGroupCatalogOAuthSourcesAreBounded(t *testing.T) {
	var mu sync.Mutex
	var calls []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		mu.Lock()
		calls = append(calls, req.Header.Get("Authorization"))
		mu.Unlock()
		http.Error(w, `{"error":{"message":"offline"}}`, http.StatusServiceUnavailable)
	}))
	defer server.Close()
	originalURL := chatgptCodexModelsURL
	chatgptCodexModelsURL = server.URL
	defer func() { chatgptCodexModelsURL = originalURL }()
	svc := &OpenAIGatewayService{}
	accounts := make([]Account, 0, 6)
	for i := int64(1); i <= 6; i++ {
		account := newCodexModelsTestAccount()
		account.ID = i
		account.Status = StatusActive
		account.Schedulable = true
		account.Priority = int(i)
		account.Credentials["access_token"] = string(rune('0' + i))
		accounts = append(accounts, *account)
	}
	svc.accountRepo = groupCatalogOAuthAccountRepo{codexModelsVisibilityAccountRepo{byGroup: map[int64][]Account{1: accounts}}}
	manifest, selected := svc.fetchGroupOfficialCodexModels(context.Background(), &Group{ID: 1, Platform: PlatformOpenAI}, accounts, "")
	require.Nil(t, manifest)
	require.Nil(t, selected)
	require.Equal(t, []string{"Bearer 1", "Bearer 2", "Bearer 3", "Bearer 4"}, calls)
}

type groupCatalogOAuthAccountRepo struct {
	codexModelsVisibilityAccountRepo
}

func (r groupCatalogOAuthAccountRepo) GetByID(_ context.Context, id int64) (*Account, error) {
	for _, accounts := range r.byGroup {
		for _, account := range accounts {
			if account.ID == id {
				return &account, nil
			}
		}
	}
	return nil, ErrNoAvailableAccounts
}

func TestOpenAIGroupCatalogSourceRefreshUsesOverallDeadline(t *testing.T) {
	svc := newCodexModelsAPIKeyTestService(nil)
	deadline := time.Now().Add(time.Second)
	ctx := context.WithValue(context.Background(), officialCatalogDeadlineContextKey{}, deadline)
	for _, key := range []string{"source-one", "source-two"} {
		result := <-svc.refreshCachedOpenAIModels(key, openAIModelsRequest{refreshDeadline: officialCatalogRefreshDeadline(ctx)}, func(fetchCtx context.Context, _ string) (*OpenAIModelsResponse, error) {
			got, ok := fetchCtx.Deadline()
			require.True(t, ok)
			require.Equal(t, deadline, got)
			return catalogFixture(`{"models":[]}`), nil
		})
		require.NoError(t, result.Err)
	}
}
