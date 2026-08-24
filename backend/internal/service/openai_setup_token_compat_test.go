package service

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestIsOpenAIOAuthLike(t *testing.T) {
	tests := []struct {
		name    string
		account *Account
		want    bool
		codex   bool
	}{
		{name: "openai_oauth", account: &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth}, want: true, codex: true},
		{name: "openai_setup_token", account: &Account{Platform: PlatformOpenAI, Type: AccountTypeSetupToken}, want: true, codex: true},
		{name: "openai_api_key", account: &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}, want: false, codex: false},
		{name: "implicit_openai_oauth", account: &Account{Type: AccountTypeOAuth}, want: false, codex: true},
		{name: "anthropic_oauth", account: &Account{Platform: PlatformAnthropic, Type: AccountTypeOAuth}, want: false, codex: false},
		{name: "grok_oauth", account: &Account{Platform: PlatformGrok, Type: AccountTypeOAuth}, want: false, codex: false},
		{name: "anthropic_setup_token", account: &Account{Platform: PlatformAnthropic, Type: AccountTypeSetupToken}, want: false, codex: false},
		{name: "grok_setup_token", account: &Account{Platform: PlatformGrok, Type: AccountTypeSetupToken}, want: false, codex: false},
		{name: "nil", account: nil, want: false, codex: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, tt.account.IsOpenAIOAuthLike())
			require.Equal(t, tt.codex, tt.account.UsesOpenAICodexProtocol())
		})
	}
}

func TestOpenAIGatewayServiceGetAccessTokenSetupToken(t *testing.T) {
	svc := &OpenAIGatewayService{openAITokenProvider: &OpenAITokenProvider{}}
	account := &Account{
		Platform:    PlatformOpenAI,
		Type:        AccountTypeSetupToken,
		Credentials: map[string]any{"access_token": "setup-token-value"},
	}

	token, tokenType, err := svc.GetAccessToken(context.Background(), account)
	require.NoError(t, err)
	require.Equal(t, "setup-token-value", token)
	require.Equal(t, "oauth", tokenType)

	delete(account.Credentials, "access_token")
	_, _, err = svc.GetAccessToken(context.Background(), account)
	require.EqualError(t, err, "access_token not found in credentials")

	for _, platform := range []string{PlatformAnthropic, PlatformGrok} {
		foreign := &Account{
			Platform:    platform,
			Type:        AccountTypeSetupToken,
			Credentials: map[string]any{"access_token": "foreign-token"},
		}
		_, _, err = svc.GetAccessToken(context.Background(), foreign)
		require.EqualError(t, err, "unsupported account type: setup-token")
	}
}

func TestOpenAISetupTokenImagesUsesOAuthResponsesPath(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)

	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusTooManyRequests,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"rate limited"}}`)),
	}}
	svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
	account := &Account{
		ID:          73,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeSetupToken,
		Credentials: map[string]any{"access_token": "setup-token"},
	}
	parsed := &OpenAIImagesRequest{
		Endpoint:       openAIImagesGenerationsEndpoint,
		Model:          "gpt-image-2",
		Prompt:         "draw a square",
		N:              1,
		ResponseFormat: "b64_json",
	}

	result, err := svc.ForwardImages(context.Background(), c, account, nil, parsed, "")

	require.Nil(t, result)
	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Equal(t, http.StatusTooManyRequests, failoverErr.StatusCode)
	require.True(t, failoverErr.RetryableOnSameAccount)
	require.False(t, failoverErr.SameAccountRetryDeadline.IsZero())
	require.Contains(t, upstream.lastReq.URL.String(), "/backend-api/codex/responses")
}

func TestOpenAISetupTokenWSCompatibility(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{}`))
	c.Request.Header.Set("session_id", "session-one")
	c.Set("api_key", &APIKey{ID: 17})

	account := &Account{
		Platform: PlatformOpenAI,
		Type:     AccountTypeSetupToken,
		Credentials: map[string]any{
			"access_token":       "setup-token-value",
			"chatgpt_account_id": "chatgpt-setup",
		},
	}
	svc := &OpenAIGatewayService{cfg: &config.Config{}}

	wsURL, err := svc.buildOpenAIResponsesWSURL(account)
	require.NoError(t, err)
	require.Equal(t, "wss://chatgpt.com/backend-api/codex/responses", wsURL)
	foreignURL, err := svc.buildOpenAIResponsesWSURL(&Account{Platform: PlatformGrok, Type: AccountTypeSetupToken})
	require.NoError(t, err)
	require.Equal(t, "wss://api.openai.com/v1/responses", foreignURL)

	headers, session, err := svc.buildOpenAIWSHeaders(
		context.Background(), c, account, "setup-token-value",
		OpenAIWSProtocolDecision{Transport: OpenAIUpstreamTransportResponsesWebsocketV2},
		true, "", "", "", "gpt-5.1-codex", "",
	)
	require.NoError(t, err)
	require.Equal(t, "Bearer setup-token-value", headers.Get("authorization"))
	require.Equal(t, "chatgpt-setup", headers.Get("chatgpt-account-id"))
	require.NotEmpty(t, headers.Get("originator"))
	require.Equal(t, "session-one", session.SessionID)
	require.NotEqual(t, session.SessionID, headers.Get("session_id"))

	payload := svc.buildOpenAIWSCreatePayload(map[string]any{"store": true}, account)
	require.Equal(t, false, payload["store"])
}

func TestOpenAISetupTokenChatCompletionsUsesCodexTransform(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"model":"gpt-5.4","messages":[{"role":"system","content":"setup instructions"},{"role":"user","content":"hello"}],"stream":false}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusBadRequest,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"error":{"type":"invalid_request_error","message":"stop after request capture"}}`)),
	}}
	svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
	account := openAISetupTokenCompatAccount(71)

	result, err := svc.ForwardAsChatCompletions(context.Background(), c, account, body, "", "gpt-5.4")

	require.Error(t, err)
	require.Nil(t, result)
	require.NotNil(t, upstream.lastReq)
	require.Equal(t, chatgptCodexURL, upstream.lastReq.URL.String())
	require.Equal(t, "Bearer setup-token-value", upstream.lastReq.Header.Get("Authorization"))
	require.Equal(t, "chatgpt-setup", upstream.lastReq.Header.Get("chatgpt-account-id"))
	require.NotEmpty(t, upstream.lastReq.Header.Get("originator"))
	require.Equal(t, "setup instructions", gjson.GetBytes(upstream.lastBody, "instructions").String())
	require.Equal(t, int64(1), gjson.GetBytes(upstream.lastBody, "input.#").Int())
	require.Equal(t, "user", gjson.GetBytes(upstream.lastBody, "input.0.role").String())
	require.NotEmpty(t, gjson.GetBytes(upstream.lastBody, "prompt_cache_key").String())
}

func TestOpenAISetupTokenUsesUnifiedIdentityPlan(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const pinnedInstallationID = "77777777-7777-4777-8777-777777777777"
	body := []byte(`{"model":"gpt-5.4","stream":true,"store":false,"prompt_cache_key":"logical-session","client_metadata":{"session_id":"logical-session","thread_id":"logical-session"}}`)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	c.Set("api_key", &APIKey{ID: 91})
	account := openAISetupTokenCompatAccount(801)
	account.Extra = map[string]any{openAIPinnedInstallationIDKey: pinnedInstallationID}
	svc := &OpenAIGatewayService{cfg: &config.Config{JWT: config.JWTConfig{Secret: "setup-token-identity-secret"}}}
	capture := CaptureOpenAIOAuthIdentity(c, body, "")

	plan, err := svc.GetOrResolveOpenAIOAuthOutboundIdentity(
		context.Background(), c, account, capture,
		OpenAIOAuthIdentityPlanOptions{
			TurnIdentityEnabled: true,
			ProjectionMode:      OpenAIOAuthIdentityProjectionRegular,
			InstallationPolicy:  OpenAIOAuthInstallationAccountPin,
		},
		nil,
	)
	require.NoError(t, err)
	require.True(t, plan.TurnIdentityRequested)
	require.True(t, plan.TurnIdentityEnabled)
	require.NoError(t, ValidateOpenAICodexTurnIdentity(plan.TurnIdentity))
	require.Equal(t, "account:801", plan.CredentialOwnerNamespace)
	require.True(t, plan.InstallationEnabled)
	require.Equal(t, pinnedInstallationID, plan.InstallationID)
	require.True(t, plan.PromptCacheKey.Enabled)
	require.Equal(t, plan.TurnIdentity.SessionID, plan.PromptCacheKey.Value)

	upstreamReq := httptest.NewRequest(http.MethodPost, chatgptCodexURL, bytes.NewReader(body))
	finalBody, err := svc.FinalizeOpenAIOAuthResponsesRequest(c, account, upstreamReq, body, OpenAIOAuthResponsesFinalizeOptions{
		Plan:        plan,
		FinalModel:  "gpt-5.4",
		RequestKind: string(CodexWireRequestTurn),
		Transport:   "setup-token-test",
	})
	require.NoError(t, err)
	require.Equal(t, plan.TurnIdentity.SessionID, upstreamReq.Header.Get("session-id"))
	require.Equal(t, plan.TurnIdentity.ThreadID, upstreamReq.Header.Get("thread-id"))
	require.Equal(t, pinnedInstallationID, upstreamReq.Header.Get(codexInstallationIDKey))
	require.Equal(t, plan.TurnIdentity.SessionID, gjson.GetBytes(finalBody, "prompt_cache_key").String())
	require.Equal(t, plan.TurnIdentity.SessionID, gjson.GetBytes(finalBody, "client_metadata.session_id").String())
	require.Equal(t, plan.TurnIdentity.ThreadID, gjson.GetBytes(finalBody, "client_metadata.thread_id").String())

	passthroughPlan, err := svc.GetOrResolveOpenAIOAuthOutboundIdentity(
		context.Background(), c, account, capture,
		OpenAIOAuthIdentityPlanOptions{
			TurnIdentityEnabled: true,
			ProjectionMode:      OpenAIOAuthIdentityProjectionPassthrough,
			InstallationPolicy:  OpenAIOAuthInstallationAccountPin,
		},
		nil,
	)
	require.NoError(t, err)
	require.Equal(t, OpenAIOAuthInstallationAccountPin, passthroughPlan.InstallationPolicy)
	passthroughHeaders := http.Header{codexInstallationIDKey: {"client-installation"}}
	passthroughBody, err := ApplyOpenAIOAuthIdentityPlan(passthroughHeaders, body, passthroughPlan)
	require.NoError(t, err)
	require.Equal(t, pinnedInstallationID, passthroughHeaders.Get(codexInstallationIDKey))
	require.Equal(t, pinnedInstallationID, gjson.GetBytes(passthroughBody, "client_metadata.x-codex-installation-id").String())
	require.Equal(t, passthroughPlan.TurnIdentity.SessionID, gjson.GetBytes(passthroughBody, "prompt_cache_key").String())
}

func TestOpenAISetupTokenIdentityPlanIsCredentialOwnerScoped(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"model":"gpt-5.4","client_metadata":{"session_id":"shared-logical-session"}}`)
	options := OpenAIOAuthIdentityPlanOptions{
		TurnIdentityEnabled: true,
		ProjectionMode:      OpenAIOAuthIdentityProjectionRegular,
		InstallationPolicy:  OpenAIOAuthInstallationPreserve,
	}
	svc := &OpenAIGatewayService{cfg: &config.Config{JWT: config.JWTConfig{Secret: "setup-token-owner-secret"}}}
	resolve := func(accountID int64) OpenAIOAuthIdentityPlan {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
		c.Set("api_key", &APIKey{ID: 92})
		capture := CaptureOpenAIOAuthIdentity(c, body, "")
		plan, err := svc.GetOrResolveOpenAIOAuthOutboundIdentity(
			context.Background(), c, openAISetupTokenCompatAccount(accountID), capture, options, nil,
		)
		require.NoError(t, err)
		return plan
	}

	first := resolve(802)
	retry := resolve(802)
	otherOwner := resolve(803)
	require.Equal(t, first.TurnIdentity.SessionID, retry.TurnIdentity.SessionID)
	require.NotEqual(t, first.TurnIdentity.SessionID, otherOwner.TurnIdentity.SessionID)
	require.Equal(t, "account:802", first.CredentialOwnerNamespace)
	require.Equal(t, "account:803", otherOwner.CredentialOwnerNamespace)
}

func TestOpenAISetupTokenIdentityFlagOffAndAPIKeyStayNoOp(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(" { \"model\" : \"gpt-5.4\", \"client_metadata\" : {\"session_id\":\"raw-session\"} } \n")
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	c.Set(openAICodexFingerprintPolicyContextKey, CodexFingerprintPolicySnapshot{})
	svc := &OpenAIGatewayService{cfg: &config.Config{}}
	capture := CaptureOpenAIOAuthIdentity(c, body, "")
	plan, err := svc.GetOrResolveOpenAIOAuthOutboundIdentity(
		context.Background(), c, openAISetupTokenCompatAccount(804), capture,
		OpenAIOAuthIdentityPlanOptions{
			TurnIdentityEnabled: true,
			ProjectionMode:      OpenAIOAuthIdentityProjectionRegular,
			InstallationPolicy:  OpenAIOAuthInstallationAccountPin,
		},
		nil,
	)
	require.NoError(t, err)
	require.False(t, plan.TurnIdentityRequested)
	require.False(t, plan.TurnIdentityEnabled)
	require.False(t, plan.InstallationEnabled)
	require.False(t, plan.PromptCacheKey.Enabled)

	apiKeyAccount := &Account{ID: 805, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	headers := http.Header{"X-Keep": {"unchanged"}}
	req := httptest.NewRequest(http.MethodPost, "https://api.openai.com/v1/responses", bytes.NewReader(body))
	req.Header = headers
	out, err := svc.FinalizeOpenAIOAuthResponsesRequest(c, apiKeyAccount, req, body, OpenAIOAuthResponsesFinalizeOptions{Plan: plan})
	require.NoError(t, err)
	require.Equal(t, body, out)
	require.Equal(t, http.Header{"X-Keep": {"unchanged"}}, req.Header)
}

func TestOpenAISetupTokenTurnStateUsesUnifiedPlanProvenance(t *testing.T) {
	resetTurnStateLocalStore(t, 16)
	gin.SetMode(gin.TestMode)
	svc := &OpenAIGatewayService{cfg: &config.Config{JWT: config.JWTConfig{Secret: "setup-token-turn-state-secret"}}}
	account := openAISetupTokenCompatAccount(806)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	plan := OpenAIOAuthIdentityPlan{
		APIKeyID:                 93,
		CredentialOwnerNamespace: "account:806",
		TurnIdentityRequested:    true,
		TurnIdentityEnabled:      true,
		RequestTurn: OpenAICodexRequestTurnSnapshot{
			ID:              "018f5c3c-6e3a-7abf-8def-1234567890ae",
			StartedAtUnixMS: 1723000000000,
			Generated:       true,
		},
		TurnIdentity: OpenAICodexTurnIdentity{
			SessionID: "018f5c3c-6e3a-7abc-8def-1234567890ab",
			ThreadID:  "018f5c3c-6e3a-7abc-8def-1234567890ab",
			Relation:  OpenAICodexTurnRelationRoot,
		},
		ProjectionMode:     OpenAIOAuthIdentityProjectionRegular,
		InstallationPolicy: OpenAIOAuthInstallationPreserve,
	}
	SetOpenAIOAuthIdentityPlan(c, plan)

	upstream := http.Header{}
	upstream.Set(openAICodexTurnStateHeader, "setup-token-state")
	state := svc.relayOpenAICodexTurnState(c, account, upstream)
	require.Equal(t, "setup-token-state", state)
	svc.noteOpenAICodexTurnStateProvenance(c, account, state)

	sameOwnerHeaders := http.Header{openAICodexTurnStateHeader: {state}}
	sameOwnerBody := []byte(`{"client_metadata":{"x-codex-turn-state":"setup-token-state"}}`)
	sameOwnerBody = svc.guardOpenAICodexTurnStateEchoForPlan(c, account, plan, sameOwnerHeaders, sameOwnerBody)
	require.Equal(t, state, sameOwnerHeaders.Get(openAICodexTurnStateHeader))
	require.Equal(t, state, gjson.GetBytes(sameOwnerBody, "client_metadata.x-codex-turn-state").String())

	otherOwner := openAISetupTokenCompatAccount(807)
	otherPlan := plan
	otherPlan.CredentialOwnerNamespace = "account:807"
	otherOwnerHeaders := http.Header{openAICodexTurnStateHeader: {state}}
	otherOwnerBody := []byte(`{"client_metadata":{"x-codex-turn-state":"setup-token-state"}}`)
	otherOwnerBody = svc.guardOpenAICodexTurnStateEchoForPlan(c, otherOwner, otherPlan, otherOwnerHeaders, otherOwnerBody)
	require.Empty(t, otherOwnerHeaders.Get(openAICodexTurnStateHeader))
	require.False(t, gjson.GetBytes(otherOwnerBody, "client_metadata.x-codex-turn-state").Exists())
}

func TestOpenAISetupTokenMessagesUsesCodexBridgeAndTurnState(t *testing.T) {
	gin.SetMode(gin.TestMode)

	firstResp := openAICompatSSECompletedResponse("resp_setup_first", "gpt-5.4")
	firstResp.Header.Set("x-codex-turn-state", "turn_state_setup")
	upstream := &httpUpstreamRecorder{responses: []*http.Response{
		firstResp,
		openAICompatSSECompletedResponse("resp_setup_second", "gpt-5.4"),
	}}
	svc := &OpenAIGatewayService{
		cfg:          &config.Config{Security: config.SecurityConfig{URLAllowlist: config.URLAllowlistConfig{Enabled: false}}},
		httpUpstream: upstream,
	}
	account := openAISetupTokenCompatAccount(72)

	messages := make([]string, 0, openAICompatAnthropicReplayMaxTailMessages+3)
	for i := 0; i < openAICompatAnthropicReplayMaxTailMessages+3; i++ {
		messages = append(messages, `{"role":"user","content":"message-`+fmt.Sprintf("%02d", i)+`"}`)
	}
	firstBody := []byte(`{"model":"claude-sonnet-4-5","max_tokens":16,"messages":[` + strings.Join(messages, ",") + `],"stream":false}`)
	firstRec := httptest.NewRecorder()
	firstCtx, _ := gin.CreateTestContext(firstRec)
	firstCtx.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(firstBody))
	firstCtx.Request.Header.Set("Content-Type", "application/json")

	firstResult, err := svc.ForwardAsAnthropic(context.Background(), firstCtx, account, firstBody, "stable-cache-key", "gpt-5.4")

	require.NoError(t, err)
	require.NotNil(t, firstResult)
	require.True(t, isOpenAICompatMessagesBridgeContext(firstCtx))
	require.Equal(t, int64(openAICompatAnthropicReplayMaxTailMessages+4), gjson.GetBytes(upstream.bodies[0], "input.#").Int())
	require.Equal(t, "developer", gjson.GetBytes(upstream.bodies[0], "input.0.role").String())
	require.Contains(t, gjson.GetBytes(upstream.bodies[0], "input.0.content.0.text").String(), openAICompatClaudeCodeTodoGuardMarker)
	require.Equal(t, "message-00", gjson.GetBytes(upstream.bodies[0], "input.1.content.0.text").String())
	require.Equal(t, chatgptCodexURL, upstream.requests[0].URL.String())
	require.Equal(t, "Bearer setup-token-value", upstream.requests[0].Header.Get("Authorization"))
	require.Equal(t, "chatgpt-setup", upstream.requests[0].Header.Get("chatgpt-account-id"))
	requireOpenAIMessagesCodexIdentity(t, upstream.requests[0], codexCLIUserAgent, "codex-tui")
	require.Empty(t, upstream.requests[0].Header.Get("x-codex-turn-state"))
	firstSessionID := upstream.requests[0].Header.Get("session-id")
	require.True(t, ValidateFingerprintObservationUUIDv7(firstSessionID))
	require.Equal(t, firstSessionID, upstream.requests[0].Header.Get("thread-id"))
	require.Empty(t, upstream.requests[0].Header.Get("session_id"))
	require.Equal(t, firstSessionID, gjson.GetBytes(upstream.bodies[0], "prompt_cache_key").String())

	secondBody := []byte(`{"model":"claude-sonnet-4-5","max_tokens":16,"messages":[{"role":"user","content":"next"}],"stream":false}`)
	secondRec := httptest.NewRecorder()
	secondCtx, _ := gin.CreateTestContext(secondRec)
	secondCtx.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(secondBody))
	secondCtx.Request.Header.Set("Content-Type", "application/json")

	secondResult, err := svc.ForwardAsAnthropic(context.Background(), secondCtx, account, secondBody, "stable-cache-key", "gpt-5.4")

	require.NoError(t, err)
	require.NotNil(t, secondResult)
	require.True(t, isOpenAICompatMessagesBridgeContext(secondCtx))
	require.Empty(t, upstream.requests[1].Header.Get("x-codex-turn-state"), "a new request turn must not replay prior turn-state")
	require.Equal(t, firstSessionID, upstream.requests[1].Header.Get("session-id"))
	require.Equal(t, firstSessionID, upstream.requests[1].Header.Get("thread-id"))
	require.Empty(t, upstream.requests[1].Header.Get("session_id"))
	require.Equal(t, firstSessionID, gjson.GetBytes(upstream.bodies[1], "prompt_cache_key").String())
	require.Empty(t, upstream.requests[1].Header.Get("conversation_id"))
	requireOpenAIMessagesCodexIdentity(t, upstream.requests[1], codexCLIUserAgent, "codex-tui")
}

func openAISetupTokenCompatAccount(id int64) *Account {
	return &Account{
		ID:          id,
		Name:        "openai-setup-token",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeSetupToken,
		Concurrency: 1,
		Credentials: map[string]any{
			"access_token":       "setup-token-value",
			"chatgpt_account_id": "chatgpt-setup",
		},
	}
}
