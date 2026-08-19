package service

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestFinalizeOpenAIOAuthResponsesRequestUsesFinalRoutingAndCanonicalIdentity(t *testing.T) {
	account := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	body := []byte(`{"model":"pre-map-model","service_tier":"flex","input":[]}`)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, chatgptCodexURL, strings.NewReader("stale"))
	require.NoError(t, err)
	req.Header["openai-beta"] = []string{
		"responses=experimental, future_feature=v1",
		"another_feature=v2",
	}
	req.Header.Set(openAICodexRoutingHintHeader, "model=stale")

	clientIdentity := resolveCodexClientIdentityPlan(CodexClientIdentityNormalize, "")
	plan := OpenAIOAuthIdentityPlan{
		ClientIdentity:        clientIdentity,
		ClientIdentityEnabled: true,
		ProjectionMode:        OpenAIOAuthIdentityProjectionRegular,
		InstallationPolicy:    OpenAIOAuthInstallationAccountPin,
	}
	service := &OpenAIGatewayService{}
	out, err := service.FinalizeOpenAIOAuthResponsesRequest(nil, account, req, body, OpenAIOAuthResponsesFinalizeOptions{
		Plan:             plan,
		FinalModel:       "gpt-final",
		FinalServiceTier: "priority",
		RequestKind:      "turn",
		Transport:        "test",
	})
	require.NoError(t, err)
	require.Equal(t, body, out)
	require.Equal(t, "model=gpt-final;tier=priority", req.Header.Get(openAICodexRoutingHintHeader))
	require.Equal(t, clientIdentity.UserAgent, req.Header.Get("User-Agent"))
	require.Equal(t, clientIdentity.Originator, req.Header.Get("Originator"))
	require.Equal(t, clientIdentity.Version, req.Header.Get("Version"))
	beta := strings.Join(req.Header.Values("OpenAI-Beta"), ",")
	require.NotContains(t, beta, "responses=experimental")
	require.Contains(t, beta, "future_feature=v1")
	require.Contains(t, beta, "another_feature=v2")

	replayed, err := req.GetBody()
	require.NoError(t, err)
	defer func() { _ = replayed.Close() }()
	replayedBody, err := io.ReadAll(replayed)
	require.NoError(t, err)
	require.Equal(t, out, replayedBody)
	require.Equal(t, int64(len(out)), req.ContentLength)
}

func TestFinalizeOpenAIOAuthResponsesRequestIsNoOpForAPIKey(t *testing.T) {
	account := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, openaiPlatformAPIURL, strings.NewReader("original request body"))
	require.NoError(t, err)
	req.Header["openai-beta"] = []string{"responses=experimental, api-key-feature=v1"}
	req.Header["x-codex-routing-hint"] = []string{"caller-owned"}
	beforeHeaders := req.Header.Clone()
	body := []byte(`{"model":"gpt-api-key","input":[]}`)

	out, err := (&OpenAIGatewayService{}).FinalizeOpenAIOAuthResponsesRequest(nil, account, req, body, OpenAIOAuthResponsesFinalizeOptions{
		FinalModel:       "must-not-apply",
		FinalServiceTier: "priority",
	})
	require.NoError(t, err)
	require.Equal(t, body, out)
	require.Equal(t, beforeHeaders, req.Header)

	requestBody, err := io.ReadAll(req.Body)
	require.NoError(t, err)
	require.Equal(t, "original request body", string(requestBody))
}

func TestFinalizeOpenAIOAuthResponsesRequestRejectsNilRequest(t *testing.T) {
	account := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	_, err := (&OpenAIGatewayService{}).FinalizeOpenAIOAuthResponsesRequest(nil, account, nil, nil, OpenAIOAuthResponsesFinalizeOptions{})
	require.ErrorContains(t, err, "request is nil")
}

func TestFinalizeOpenAIOAuthResponsesRequestUsesEffectiveResponsesLiteCapability(t *testing.T) {
	account := &Account{ID: 42, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	const model = "gpt-5.6-sol"
	newPlan := func(t *testing.T) OpenAIOAuthIdentityPlan {
		plan := codexWireProjectionTestPlan(t)
		plan.CredentialOwnerNamespace = "account:42"
		return plan
	}
	newBody := func() []byte {
		return []byte(`{"model":"gpt-5.6-sol","reasoning":{"context":"current_turn"},"tools":[{"type":"namespace","name":"collaboration"}],"input":"hi"}`)
	}

	t.Run("unknown manifest honors explicit HTTP marker", func(t *testing.T) {
		svc := &OpenAIGatewayService{cfg: &config.Config{Gateway: config.GatewayConfig{
			CodexMetadata: config.GatewayCodexMetadataConfig{TurnMetadataIncludesToolInfo: true},
		}}}
		req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, chatgptCodexURL, nil)
		require.NoError(t, err)
		req.Header["x-openai-internal-codex-responses-lite"] = []string{"true"}

		out, err := svc.FinalizeOpenAIOAuthResponsesRequest(nil, account, req, newBody(), OpenAIOAuthResponsesFinalizeOptions{
			Plan: newPlan(t), FinalModel: model, RequestKind: "turn",
		})
		require.NoError(t, err)
		require.Equal(t, "true", req.Header.Get(responsesLiteHeader))
		require.Equal(t, "all_turns", gjson.GetBytes(out, "reasoning.context").String())
		require.False(t, gjson.GetBytes(out, `tools.#(type=="namespace")`).Exists())
		require.Equal(t, "collaboration", gjson.GetBytes(out, `input.#(type=="additional_tools").tools.0.name`).String())
		nested := gjson.GetBytes(out, "client_metadata.x-codex-turn-metadata").String()
		require.True(t, gjson.Get(nested, "tool_namespaces_info").IsObject())
	})

	t.Run("default metadata profile keeps Lite but drops tool inventory", func(t *testing.T) {
		svc := &OpenAIGatewayService{}
		req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, chatgptCodexURL, nil)
		require.NoError(t, err)
		req.Header.Set(responsesLiteHeader, "true")

		out, err := svc.FinalizeOpenAIOAuthResponsesRequest(nil, account, req, newBody(), OpenAIOAuthResponsesFinalizeOptions{
			Plan: newPlan(t), FinalModel: model, RequestKind: "turn",
		})
		require.NoError(t, err)
		require.Equal(t, "true", req.Header.Get(responsesLiteHeader))
		nested := gjson.GetBytes(out, "client_metadata.x-codex-turn-metadata").String()
		require.False(t, gjson.Get(nested, "tool_namespaces_info").Exists())
	})

	t.Run("known false overrides explicit marker", func(t *testing.T) {
		svc := &OpenAIGatewayService{}
		svc.codexModelCapabilities.observeManifest("account:42", []byte(`{"models":[{"slug":"gpt-5.6-sol","use_responses_lite":false}]}`), time.Now())
		req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, chatgptCodexURL, nil)
		require.NoError(t, err)
		req.Header.Set(responsesLiteHeader, "true")

		out, err := svc.FinalizeOpenAIOAuthResponsesRequest(nil, account, req, newBody(), OpenAIOAuthResponsesFinalizeOptions{
			Plan: newPlan(t), FinalModel: model, RequestKind: "turn",
		})
		require.NoError(t, err)
		require.Empty(t, req.Header.Get(responsesLiteHeader))
		require.Equal(t, "current_turn", gjson.GetBytes(out, "reasoning.context").String())
		require.True(t, gjson.GetBytes(out, `tools.#(type=="namespace")`).Exists())
		nested := gjson.GetBytes(out, "client_metadata.x-codex-turn-metadata").String()
		require.False(t, gjson.Get(nested, "tool_namespaces_info").Exists())
	})

	t.Run("known true enables Lite without inbound marker", func(t *testing.T) {
		svc := &OpenAIGatewayService{}
		svc.codexModelCapabilities.observeManifest("account:42", []byte(`{"models":[{"slug":"gpt-5.6-sol","use_responses_lite":true}]}`), time.Now())
		req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, chatgptCodexURL, nil)
		require.NoError(t, err)

		out, err := svc.FinalizeOpenAIOAuthResponsesRequest(nil, account, req, newBody(), OpenAIOAuthResponsesFinalizeOptions{
			Plan: newPlan(t), FinalModel: model, RequestKind: "turn",
		})
		require.NoError(t, err)
		require.Equal(t, "true", req.Header.Get(responsesLiteHeader))
		require.Equal(t, "all_turns", gjson.GetBytes(out, "reasoning.context").String())
	})
}
