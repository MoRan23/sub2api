package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestForwardResponsesInputTokensCustomRelayUsesLocalEstimate(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses/input_tokens", nil)

	upstream := &httpUpstreamRecorder{}
	svc := &OpenAIGatewayService{
		cfg:          &config.Config{Security: config.SecurityConfig{URLAllowlist: config.URLAllowlistConfig{Enabled: false}}},
		httpUpstream: upstream,
	}
	account := &Account{
		ID:          159,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key":  "relay-key",
			"base_url": "https://relay.example/v1",
		},
	}
	body := []byte(`{"model":"gpt-5.4","instructions":"Be concise.","input":"hello world","tools":[{"type":"function","name":"lookup","description":"Look up a value","parameters":{"type":"object"}}]}`)

	err := svc.ForwardResponsesInputTokens(context.Background(), c, account, body)

	require.NoError(t, err)
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, "response.input_tokens", gjson.Get(recorder.Body.String(), "object").String())
	require.Positive(t, gjson.Get(recorder.Body.String(), "input_tokens").Int())
	require.Nil(t, upstream.lastReq, "custom relay must not receive /v1/responses/input_tokens")
}

func TestForwardResponsesInputTokensGrokOAuthUsesLocalEstimate(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses/input_tokens", nil)

	upstream := &httpUpstreamRecorder{}
	svc := &OpenAIGatewayService{httpUpstream: upstream}
	account := &Account{ID: 160, Platform: PlatformGrok, Type: AccountTypeOAuth}
	body := []byte(`{"model":"grok-4.1","input":"hello world"}`)

	err := svc.ForwardResponsesInputTokens(context.Background(), c, account, body)

	require.NoError(t, err)
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, "response.input_tokens", gjson.Get(recorder.Body.String(), "object").String())
	require.Positive(t, gjson.Get(recorder.Body.String(), "input_tokens").Int())
	require.Nil(t, upstream.lastReq)
}

func TestForwardResponsesInputTokensUpstream404FallsBackLocally(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses/input_tokens", nil)

	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusNotFound,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(`{"error":{"type":"invalid_request_error","message":"Invalid URL (POST /v1/responses/input_tokens)"}}`)),
	}}
	svc := &OpenAIGatewayService{
		cfg:          &config.Config{Security: config.SecurityConfig{URLAllowlist: config.URLAllowlistConfig{Enabled: false}}},
		httpUpstream: upstream,
	}
	account := &Account{
		ID:          171,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key":  "official-key",
			"base_url": "https://api.openai.com/v1",
		},
	}
	body := []byte(`{"model":"gpt-5.4","instructions":"Be concise.","input":"hello world"}`)

	err := svc.ForwardResponsesInputTokens(context.Background(), c, account, body)

	require.NoError(t, err)
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, "response.input_tokens", gjson.Get(recorder.Body.String(), "object").String())
	require.Positive(t, gjson.Get(recorder.Body.String(), "input_tokens").Int())
	require.NotNil(t, upstream.lastReq)
}

func TestBuildInputTokensUpstreamRequestOAuthUsesStrictProfileIdentityWithoutTurnIdentity(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses/input_tokens", nil)
	c.Request.Header.Set("User-Agent", "codex_vscode/0.120.0 (Linux; x86_64) vscode")
	c.Request.Header.Set("Session-Id", "caller-session")
	c.Request.Header.Set("Thread-Id", "caller-thread")
	c.Request.Header.Set("X-Codex-Installation-Id", "caller-installation")
	c.Request.Header.Set("X-Codex-Turn-Metadata", `{"turn_id":"0198d70a-bc00-7000-8000-000000000001"}`)

	account := &Account{
		ID:       172,
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"user_agent": "codex_vscode/0.120.0 (Windows 11.0.26100; x86_64) WindowsTerminal",
		},
	}
	req, err := (&OpenAIGatewayService{}).buildInputTokensUpstreamRequest(
		context.Background(), c, account, []byte(`{"model":"gpt-5.4","input":"hello"}`), "oauth-token",
	)

	require.NoError(t, err)
	require.Equal(t, openai.CodexDefaultOriginator, req.Header.Get("Originator"))
	require.Equal(t, codexCLIVersion, req.Header.Get("Version"))
	require.Equal(t,
		"codex-tui/"+codexCLIVersion+" (Windows 11.0.26100; x86_64) WindowsTerminal (codex-tui; "+codexCLIVersion+")",
		req.Header.Get("User-Agent"),
	)
	for _, key := range []string{
		"X-Codex-Installation-Id", "Session-Id", "Thread-Id", "X-Codex-Turn-Metadata",
		"X-Codex-Parent-Thread-Id", "X-Codex-Window-Id",
	} {
		require.Empty(t, req.Header.Get(key), "%s must not be projected on input_tokens", key)
	}
}

func TestBuildInputTokensUpstreamRequestAPIKeyPreservesExistingHeaderBehavior(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses/input_tokens", nil)
	c.Request.Header.Set("User-Agent", "caller-api-key-agent/1.0")
	c.Request.Header.Set("Accept-Language", "zh-CN")
	c.Request.Header.Set("Originator", "caller-originator")
	c.Request.Header.Set("Version", "9.9.9")

	account := &Account{
		ID:       173,
		Platform: PlatformOpenAI,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"api_key":  "sk-test",
			"base_url": "https://api.openai.com/v1",
		},
	}
	req, err := (&OpenAIGatewayService{cfg: &config.Config{Security: config.SecurityConfig{
		URLAllowlist: config.URLAllowlistConfig{Enabled: false},
	}}}).buildInputTokensUpstreamRequest(
		context.Background(), c, account, []byte(`{"model":"gpt-5.4","input":"hello"}`), "sk-test",
	)

	require.NoError(t, err)
	require.Equal(t, "caller-api-key-agent/1.0", req.Header.Get("User-Agent"))
	require.Equal(t, "zh-CN", req.Header.Get("Accept-Language"))
	require.Empty(t, req.Header.Get("Originator"))
	require.Empty(t, req.Header.Get("Version"))
}
