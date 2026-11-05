package service

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestAttributionModelNamesWithSpaces(t *testing.T) {
	c := DefaultAttributionConfig()
	c.Enabled, c.BaseURL = true, "http://lm-detector:8080"
	c.Default = AttributionPolicy{Model: " 5.6 Cyber ", ExpectedModels: []string{"gpt-5.6-sol"}, HighModels: []string{" 5.6 Cyber ", "5.6 Cyber"}, LowModels: []string{"5.6  Cyber"}}
	c.Groups = []AttributionGroupPolicy{{GroupID: 2, Enabled: true, AttributionPolicy: c.Default}}
	c.NewAccountTests.AttributionModel = " 5.6  Cyber "
	got, err := NormalizeAttributionConfig(c)
	require.NoError(t, err)
	for _, policy := range []AttributionPolicy{got.Default, got.Groups[0].AttributionPolicy} {
		require.Equal(t, "5.6 Cyber", policy.Model)
		require.Equal(t, []string{"5.6 Cyber"}, policy.HighModels)
		require.Equal(t, []string{"5.6  Cyber"}, policy.LowModels)
		require.Equal(t, []string{"gpt-5.6-sol"}, ExpectedAttributionModels(policy))
	}
	require.Equal(t, "5.6  Cyber", got.NewAccountTests.AttributionModel)
	opts, err := NormalizeAttributionProbeOptions(AttributionProbeOptions{Model: " 5.6  Cyber "})
	require.NoError(t, err)
	require.Equal(t, "5.6  Cyber", opts.Model)
	require.Nil(t, opts.ExpectedModels)
	opts, err = NormalizeAttributionProbeOptions(AttributionProbeOptions{Model: "  "})
	require.NoError(t, err)
	require.Empty(t, opts.Model, "empty manual probe still inherits the configured model")

	// The detector reference IDs and independent Pelican settings keep their rules.
	_, err = NormalizeAttributionExpectedModels([]string{"5.6 Cyber"})
	require.ErrorIs(t, err, ErrAttributionInvalid)
	c.NewAccountTests.PelicanModel = "5.6 Cyber"
	_, err = NormalizeAttributionConfig(c)
	require.ErrorIs(t, err, ErrAttributionInvalid)
}

func TestAttributionModelNamesRejectNonConcreteIDs(t *testing.T) {
	for _, model := range []string{"5.6 *", "5.6\tCyber", "5.6\nCyber", "5.6\rCyber", "5.6\x00Cyber", "5.6\u00a0Cyber", strings.Repeat("x", 201)} {
		t.Run(model, func(t *testing.T) {
			_, err := NormalizeAttributionProbeOptions(AttributionProbeOptions{Model: model})
			require.ErrorIs(t, err, ErrAttributionInvalid)
			for _, field := range []string{"default", "group", "initial", "high", "low"} {
				c := DefaultAttributionConfig()
				switch field {
				case "default":
					c.Default.Model = model
				case "group":
					c.Groups = []AttributionGroupPolicy{{GroupID: 2, Enabled: true, AttributionPolicy: AttributionPolicy{Model: model}}}
				case "initial":
					c.NewAccountTests.AttributionModel = model
				case "high":
					c.Default.HighModels = []string{model}
				case "low":
					c.Default.LowModels = []string{model}
				}
				_, err = NormalizeAttributionConfig(c)
				require.ErrorIs(t, err, ErrAttributionInvalid, field)
			}
			require.Empty(t, attributionProbeModelIDs([]string{model}))
		})
	}
}

func TestAttributionProbeSendsModelWithSpacesUnchanged(t *testing.T) {
	for _, mode := range []string{"generic", "passthrough", "codex_engine", "oauth"} {
		t.Run(mode, func(t *testing.T) {
			account := newOpenAIRejectedFieldTestAccount()
			account.Extra[OpenAIAPIKeyModeExtraKey] = mode
			if mode == "passthrough" {
				account.Extra["openai_passthrough_enabled"] = true
				account.Extra["openai_passthrough"] = true
			}
			if mode == "oauth" {
				account = newOpenAIOAuthNamespaceTestAccount()
			}
			account.Credentials["model_mapping"] = map[string]any{"5.6  Cyber": "different-model"}
			var repo AccountRepository = &stubOpenAIAccountRepo{accounts: []Account{*account}}
			if mode == "oauth" {
				repo = newAuthorizedOpenAIOAuthTestRepo(account)
			}
			upstream := &httpUpstreamRecorder{responses: []*http.Response{{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(candyAuthorizationCompletedResponse))}}}
			gateway := newOpenAIRejectedFieldTestService(upstream)
			gateway.accountRepo = repo
			result, err := NewAccountCandyTestTransport(repo, gateway).Probe(attributionSingleAttemptContext(), account.ID, "5.6  Cyber", "synthetic challenge")
			require.NoError(t, err)
			require.True(t, result.Completed)
			require.Len(t, upstream.bodies, 1)
			require.Equal(t, "5.6  Cyber", gjson.GetBytes(upstream.bodies[0], "model").String())
		})
	}
}

func TestAttributionProbeCatalogPreservesSpaces(t *testing.T) {
	account := Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"model_mapping": map[string]any{"alias": " 5.6 Cyber "}}}
	s := NewModelAttributionService(nil, &stubOpenAIAccountRepo{accounts: []Account{account}}, nil)
	s.fetchProbeModels = func(context.Context, *Account) (*OpenAIModelsResponse, error) {
		return &OpenAIModelsResponse{Body: []byte(`{"data":[{"id":"5.6 Cyber"},{"id":"5.6  Cyber"},{"id":"bad*"},{"id":"bad\tmodel"}]}`)}, nil
	}
	got, err := s.ProbeModels(context.Background(), []int64{account.ID})
	require.NoError(t, err)
	require.Equal(t, []string{"5.6  Cyber", "5.6 Cyber"}, got.Models)
	require.Equal(t, got.Models, got.Accounts[0].Models)
	require.Equal(t, "success", got.Accounts[0].Status)
}
