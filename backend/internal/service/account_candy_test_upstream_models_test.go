package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai_compat"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func newCandySyntheticCatalogTransport(repo AccountRepository, gateway *OpenAIGatewayService) *AccountCandyTestTransport {
	runner := NewAccountCandyTestTransport(repo, gateway)
	runner.fetchModels = func(context.Context, *Account) (*OpenAIModelsResponse, error) {
		return &OpenAIModelsResponse{Body: []byte(`{"data":[{"id":"gpt-5.5"},{"id":"gpt-6-astra","reasoning":true,"supported_reasoning_levels":["high","ultra"]}]}`)}, nil
	}
	return runner
}

func TestCandyLiveModelOptionsIgnoreBusinessWhitelistAndAliases(t *testing.T) {
	account := newOpenAIRejectedFieldTestAccount()
	account.Credentials["model_mapping"] = map[string]any{"business-only-alias": "mapped-only-target"}
	account.SetUpstreamModelMetadataSnapshot(UpstreamModelMetadataSnapshot{Models: map[string]UpstreamModelMetadata{
		"saved-only-model": {ID: "saved-only-model"},
	}})
	require.False(t, account.IsModelSupported("gpt-6-astra"))
	for _, body := range []string{
		`{"data":[{"id":"gpt-6-astra"},{"id":"provider-new-model"},{"id":"gpt-6-astra"},{"id":"text-embedding-3-large"}]}`,
		`{"models":[{"slug":"gpt-6-astra"},{"slug":"provider-new-model"},{"slug":"gpt-6-astra"},{"slug":"text-embedding-3-large"}]}`,
	} {
		models, err := candyTestUpstreamModelOptions(account, []byte(body))
		require.NoError(t, err)
		require.Len(t, models, 2)
		require.Equal(t, "gpt-6-astra", models[0].ID)
		require.Equal(t, "provider-new-model", models[1].ID)
		require.Empty(t, models[1].ReasoningEfforts, "unknown model only offers model default")
	}
}

func TestCandyLiveModelOptionsUseOriginalIDCapabilities(t *testing.T) {
	account := newOpenAIRejectedFieldTestAccount()
	account.Credentials["model_mapping"] = map[string]any{"gpt-6-astra": "gpt-6-luna"}
	account.SetUpstreamModelMetadataSnapshot(UpstreamModelMetadataSnapshot{Models: map[string]UpstreamModelMetadata{
		"gpt-6-astra":    {ID: "gpt-6-astra", SupportedReasoningLevels: []string{"medium"}},
		"gpt-6-luna":     {ID: "gpt-6-luna", SupportedReasoningLevels: []string{"low"}},
		"saved-reasoner": {ID: "saved-reasoner", SupportedReasoningLevels: []string{"max"}},
	}})
	models, err := candyTestUpstreamModelOptions(account, []byte(`{"models":[
		{"slug":"gpt-6-astra","display_name":"Actual Astra","reasoning":true,"supported_reasoning_levels":[{"effort":"high"},{"effort":"high"}]},
		{"slug":"gpt-6-luna","reasoning":false,"supported_reasoning_levels":[{"effort":"ultra"}]},
		{"slug":"saved-reasoner"}
	]}`))
	require.NoError(t, err)
	require.Len(t, models, 3)
	require.Equal(t, "gpt-6-astra", models[0].ID)
	require.Equal(t, "Actual Astra", models[0].DisplayName)
	require.Equal(t, []string{"high"}, models[0].ReasoningEfforts, "live capability must beat saved and mapped model capability")
	require.Empty(t, models[1].ReasoningEfforts, "explicit upstream reasoning=false must beat bundled defaults")
	require.Equal(t, []string{"max"}, models[2].ReasoningEfforts, "saved metadata can only fill capabilities for a returned model ID")
}

func TestCandyLiveModelOptionsFailureAndEmptyCatalogDoNotFallback(t *testing.T) {
	a := newOpenAIRejectedFieldTestAccount()
	b := snapshotOAuthRefreshAccount(a)
	b.ID++
	c := snapshotOAuthRefreshAccount(a)
	c.ID += 2
	repo := &stubOpenAIAccountRepo{accounts: []Account{*a, *b, *c}}
	runner := NewAccountCandyTestTransport(repo, nil)
	runner.fetchModels = func(_ context.Context, account *Account) (*OpenAIModelsResponse, error) {
		switch account.ID {
		case a.ID:
			return nil, errors.New("Bearer private-upstream-error")
		case b.ID:
			return &OpenAIModelsResponse{Body: []byte(`{"data":[]}`)}, nil
		default:
			return &OpenAIModelsResponse{Body: []byte(`{"data":[{"id":"provider-live-model"}]}`)}, nil
		}
	}
	options, err := runner.Options(context.Background(), []int64{a.ID, b.ID, c.ID})
	require.NoError(t, err)
	require.Equal(t, "model_catalog_failed", options.Accounts[0].SkipReason)
	require.Equal(t, "no_supported_models", options.Accounts[1].SkipReason)
	require.Empty(t, options.Accounts[0].Models)
	require.Empty(t, options.Accounts[1].Models)
	require.Len(t, options.Models, 1)
	require.Equal(t, "provider-live-model", options.Models[0].ID)
}

func TestCandyTransportCatalogFailureOrRemovedModelDoesNotSendInference(t *testing.T) {
	for _, tc := range []struct {
		name, body, want string
		catalogError     error
	}{
		{name: "upstream failure", want: "model_catalog_failed", catalogError: errors.New("upstream private body")},
		{name: "model no longer advertised", body: `{"data":[{"id":"another-model"}]}`, want: "configuration_changed"},
		{name: "empty catalog", body: `{"data":[]}`, want: "configuration_changed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			account := newOpenAIRejectedFieldTestAccount()
			repo := &stubOpenAIAccountRepo{accounts: []Account{*account}}
			upstream := &httpUpstreamRecorder{}
			gateway := newOpenAIRejectedFieldTestService(upstream)
			gateway.accountRepo = repo
			runner := NewAccountCandyTestTransport(repo, gateway)
			runner.fetchModels = func(context.Context, *Account) (*OpenAIModelsResponse, error) {
				return &OpenAIModelsResponse{Body: []byte(tc.body)}, tc.catalogError
			}
			_, err := runner.Execute(context.Background(), &CandyTestItem{AccountID: account.ID, Model: "gpt-6-astra", PromptVersion: CandyTestPromptVersion})
			require.EqualError(t, err, tc.want)
			require.Empty(t, upstream.bodies)
			require.True(t, repo.accounts[0].Schedulable)
			require.Equal(t, StatusActive, repo.accounts[0].Status)
		})
	}
}

func TestCandyLiveModelOptionsFetchConcurrencyAndCancellation(t *testing.T) {
	accounts := make([]Account, 7)
	ids := make([]int64, len(accounts))
	for i := range accounts {
		accounts[i] = *newOpenAIRejectedFieldTestAccount()
		accounts[i].ID = int64(i + 1)
		ids[i] = accounts[i].ID
	}
	runner := NewAccountCandyTestTransport(&stubOpenAIAccountRepo{accounts: accounts}, nil)
	var running, peak, count atomic.Int32
	started := make(chan struct{}, 3)
	release := make(chan struct{})
	runner.fetchModels = func(ctx context.Context, account *Account) (*OpenAIModelsResponse, error) {
		current := running.Add(1)
		defer running.Add(-1)
		for old := peak.Load(); current > old && !peak.CompareAndSwap(old, current); old = peak.Load() {
		}
		count.Add(1)
		started <- struct{}{}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-release:
			return &OpenAIModelsResponse{Body: fmt.Appendf(nil, `{"data":[{"id":"model-%d"}]}`, account.ID)}, nil
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := runner.Options(ctx, ids); done <- err }()
	for range 3 {
		select {
		case <-started:
		case <-time.After(5 * time.Second):
			t.Fatal("catalog workers did not start")
		}
	}
	require.EqualValues(t, 3, peak.Load())
	require.EqualValues(t, 3, count.Load())
	cancel()
	select {
	case err := <-done:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(5 * time.Second):
		t.Fatal("catalog cancellation did not finish")
	}
	require.Zero(t, running.Load())
}

func TestCandyTransportUsesUpstreamIDAcrossHTTPPaths(t *testing.T) {
	for _, path := range []string{"responses", "passthrough", "chat"} {
		for _, mapping := range []string{"gpt-6-astra", "*", "business-only-alias"} {
			t.Run(path+"/"+mapping, func(t *testing.T) {
				account := newOpenAIRejectedFieldTestAccount()
				account.Credentials["model_mapping"] = map[string]any{mapping: "gpt-6-luna"}
				if mapping == "business-only-alias" {
					require.False(t, account.IsModelSupported("gpt-6-astra"))
				}
				if path == "passthrough" {
					account.Extra["openai_passthrough"] = true
					account.Extra["openai_passthrough_enabled"] = true
				}
				if path == "chat" {
					account.Extra[openai_compat.ExtraKeyResponsesSupported] = false
					account.Extra[openai_compat.ExtraKeyResponsesMode] = string(openai_compat.ResponsesSupportModeAuto)
				}
				repo := &stubOpenAIAccountRepo{accounts: []Account{*account}}
				upstream := &httpUpstreamRecorder{responses: []*http.Response{newOpenAIRejectedFieldTestResponse(http.StatusUnauthorized, `{"error":{"message":"synthetic denied"}}`)}}
				gateway := newOpenAIRejectedFieldTestService(upstream)
				gateway.accountRepo = repo
				result, err := newCandySyntheticCatalogTransport(repo, gateway).Execute(context.Background(), &CandyTestItem{
					AccountID: account.ID, Model: "gpt-6-astra", ReasoningEffort: "high", PromptVersion: CandyTestPromptVersion,
				})
				require.EqualError(t, err, "upstream_http_401")
				require.Len(t, upstream.bodies, 1)
				require.Equal(t, "gpt-6-astra", gjson.GetBytes(upstream.bodies[0], "model").String())
				require.Equal(t, "gpt-6-astra", result.ActualModel)
				require.Equal(t, "high", result.ReasoningEffort)
				if path == "chat" {
					require.Contains(t, upstream.lastReq.URL.Path, "/chat/completions")
					require.Equal(t, "high", gjson.GetBytes(upstream.bodies[0], "reasoning_effort").String())
				} else {
					require.Contains(t, upstream.lastReq.URL.Path, "/responses")
					require.Equal(t, "high", gjson.GetBytes(upstream.bodies[0], "reasoning.effort").String())
				}
				require.False(t, repo.accounts[0].openAICandyTest)
				businessModel := "gpt-6-astra"
				if mapping == "business-only-alias" {
					businessModel = mapping
				}
				mapped, matched := repo.accounts[0].ResolveMappedModel(businessModel)
				require.True(t, matched, "normal business mapping must remain active")
				require.Equal(t, "gpt-6-luna", mapped)
			})
		}
	}
}

func TestCandyModelMappingBypassDoesNotAffectBusiness(t *testing.T) {
	for _, mapping := range []string{"gpt-6-astra", "*"} {
		t.Run(mapping, func(t *testing.T) {
			account := newOpenAIRejectedFieldTestAccount()
			account.Credentials["model_mapping"] = map[string]any{mapping: "gpt-6-luna"}
			mapped, matched := account.ResolveMappedModel("gpt-6-astra")
			require.Equal(t, "gpt-6-luna", mapped)
			require.True(t, matched)
			testAccount := snapshotOAuthRefreshAccount(account)
			testAccount.openAICandyTest = true
			mapped, matched = testAccount.ResolveMappedModel("gpt-6-astra")
			require.Equal(t, "gpt-6-astra", mapped)
			require.False(t, matched)
			mapped, matched = account.ResolveMappedModel("gpt-6-astra")
			require.Equal(t, "gpt-6-luna", mapped)
			require.True(t, matched)
		})
	}
}

func TestCandyOAuthAndSparkKeepRawCatalogIDWithoutLegacyNormalization(t *testing.T) {
	const selectedModel = "gpt-5.1-codex"
	for _, useSpark := range []bool{false, true} {
		name := "oauth"
		if useSpark {
			name = "spark"
		}
		t.Run(name, func(t *testing.T) {
			parent := newOpenAIOAuthNamespaceTestAccount()
			profiles, err := BuildOpenAIOAuthOSProfiles(parent, &OpenAIOAuthOSProfiles{DefaultOS: OpenAIOSMacOS})
			require.NoError(t, err)
			parent.OpenAIOAuthOSProfiles = profiles
			repo := newAuthorizedOpenAIOAuthTestRepo(parent)
			account := parent
			if useSpark {
				account = snapshotOAuthRefreshAccount(parent)
				account.ID = parent.ID + 1
				account.ParentAccountID = &parent.ID
				account.Credentials = map[string]any{"model_mapping": map[string]any{"business-only-alias": "gpt-6-astra"}}
				repo.accounts[account.ID] = account
			}
			require.Equal(t, "gpt-5.3-codex", normalizeOpenAIModelForUpstream(account, selectedModel), "ordinary OAuth and Spark requests retain legacy normalization")
			upstream := &httpUpstreamRecorder{responses: []*http.Response{newOpenAIRejectedFieldTestResponse(http.StatusUnauthorized, `{"error":{"message":"synthetic denied"}}`)}}
			gateway := newOpenAIRejectedFieldTestService(upstream)
			gateway.accountRepo = repo
			runner := NewAccountCandyTestTransport(repo, gateway)
			runner.fetchModels = func(context.Context, *Account) (*OpenAIModelsResponse, error) {
				return &OpenAIModelsResponse{Body: []byte(`{"models":[{"slug":"gpt-5.1-codex"}]}`)}, nil
			}
			result, err := runner.Execute(context.Background(), &CandyTestItem{AccountID: account.ID, Model: selectedModel, PromptVersion: CandyTestPromptVersion})
			require.EqualError(t, err, "upstream_http_401")
			require.Len(t, upstream.bodies, 1)
			require.Equal(t, selectedModel, gjson.GetBytes(upstream.bodies[0], "model").String())
			require.Equal(t, selectedModel, result.ActualModel)
			require.Equal(t, selectedModel, result.RequestedModel)
			require.Equal(t, "gpt-5.3-codex", normalizeOpenAIModelForUpstream(account, selectedModel), "the benchmark must not mutate the normal account")
			require.True(t, parent.Schedulable)
			require.True(t, account.Schedulable)
		})
	}
}
