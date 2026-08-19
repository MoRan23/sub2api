package service

import (
	"context"
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

func TestAccountTestService_OpenAIImageOAuthHandlesOutputItemDoneFallback(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/1/test", nil)
	c.Request.Header.Set(codexInstallationIDKey, "client-installation")
	c.Request.Header.Set(openAIWSTurnMetadataHeader, `{"installation_id":"client-nested","turn":1}`)

	upstream := &httpUpstreamRecorder{
		resp: &http.Response{
			StatusCode: http.StatusOK,
			Header: http.Header{
				"Content-Type": []string{"text/event-stream"},
			},
			Body: io.NopCloser(strings.NewReader(
				"data: {\"type\":\"response.output_item.done\",\"item\":{\"id\":\"ig_123\",\"type\":\"image_generation_call\",\"result\":\"aGVsbG8=\",\"revised_prompt\":\"draw a cat\",\"output_format\":\"png\"}}\n\n" +
					"data: {\"type\":\"response.completed\",\"response\":{\"created_at\":1710000006,\"tool_usage\":{\"image_gen\":{\"images\":1}},\"output\":[]}}\n\n" +
					"data: [DONE]\n\n",
			)),
		},
	}
	svc := &AccountTestService{httpUpstream: upstream}
	account := &Account{
		ID:       53,
		Name:     "openai-oauth",
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"access_token": "token-123",
		},
		Extra: map[string]any{
			"openai_passthrough":          true,
			openAIPinnedInstallationIDKey: "22222222-3333-4333-8444-555555555555",
		},
	}

	err := svc.testOpenAIImageOAuth(c, context.Background(), account, "gpt-image-2", "draw a cat")
	require.NoError(t, err)
	require.NotNil(t, upstream.lastReq)
	require.Equal(t, HTTPUpstreamProfileOpenAI, HTTPUpstreamProfileFromContext(upstream.lastReq.Context()))
	require.Equal(t, "22222222-3333-4333-8444-555555555555", upstream.lastReq.Header.Get(codexInstallationIDKey))
	require.Equal(t, "22222222-3333-4333-8444-555555555555", gjson.GetBytes(upstream.lastBody, "client_metadata.x-codex-installation-id").String())
	require.Equal(t, "22222222-3333-4333-8444-555555555555", extractInstallationIDFromTurnMetadata(upstream.lastReq.Header.Get(openAIWSTurnMetadataHeader)))
	require.NotEmpty(t, upstream.lastReq.Header.Get("session-id"))
	require.Equal(t, upstream.lastReq.Header.Get("session-id"), gjson.GetBytes(upstream.lastBody, "client_metadata.session_id").String())
	require.NotEmpty(t, gjson.GetBytes(upstream.lastBody, "client_metadata.turn_id").String())
	require.False(t, gjson.GetBytes(upstream.lastBody, "client_metadata.turn_started_at_unix_ms").Exists())
	require.NotEmpty(t, upstream.lastReq.Header.Get("User-Agent"))
	require.NotEmpty(t, upstream.lastReq.Header.Get("Originator"))
	require.NotEmpty(t, upstream.lastReq.Header.Get("Version"))
	require.Contains(t, rec.Body.String(), "Calling Codex /responses image tool")
	require.Contains(t, rec.Body.String(), "data:image/png;base64,aGVsbG8=")
	require.Contains(t, rec.Body.String(), "\"success\":true")
}

func TestAccountTestService_OpenAIImageAPIKeyUsesConfiguredV1BaseURL(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/1/test", nil)

	upstream := &httpUpstreamRecorder{
		resp: &http.Response{
			StatusCode: http.StatusOK,
			Header: http.Header{
				"Content-Type": []string{"application/json"},
			},
			Body: io.NopCloser(strings.NewReader(`{"data":[{"b64_json":"aGVsbG8=","revised_prompt":"draw a cat"}]}`)),
		},
	}
	svc := &AccountTestService{
		httpUpstream: upstream,
		cfg:          &config.Config{},
	}
	account := &Account{
		ID:       54,
		Name:     "openai-apikey",
		Platform: PlatformOpenAI,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"api_key":  "test-api-key",
			"base_url": "https://image-upstream.example/v1",
		},
	}

	err := svc.testOpenAIImageAPIKey(c, context.Background(), account, "gpt-image-2", "draw a cat")
	require.NoError(t, err)
	require.NotNil(t, upstream.lastReq)
	require.Equal(t, HTTPUpstreamProfileOpenAI, HTTPUpstreamProfileFromContext(upstream.lastReq.Context()))
	require.Equal(t, "https://image-upstream.example/v1/images/generations", upstream.lastReq.URL.String())
	require.Equal(t, "Bearer test-api-key", upstream.lastReq.Header.Get("Authorization"))
	require.Contains(t, rec.Body.String(), "data:image/png;base64,aGVsbG8=")
	require.Contains(t, rec.Body.String(), "\"success\":true")
}

func TestCaptureOpenAIOAuthSyntheticRequestIgnoresAdminHeadersAndReusesRetrySnapshot(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/1/test", nil)
	const adminTurnID = "01989f44-7c00-7000-8000-000000000051"
	c.Request.Header.Set(openAIWSTurnMetadataHeader, `{"turn_id":"`+adminTurnID+`","turn_started_at_unix_ms":1}`)
	c.Request.Header.Set(codexInstallationIDKey, "admin-installation")

	first := captureOpenAIOAuthSyntheticRequest(c, []byte(`{"model":"gpt-5.6"}`), "account-test")
	retry := captureOpenAIOAuthSyntheticRequest(c, []byte(`{"model":"gpt-5.6","retry":true}`), "account-test")

	require.Equal(t, "account-test", first.Logical.SessionKey)
	require.Equal(t, first.RequestTurn, retry.RequestTurn)
	require.NotEqual(t, adminTurnID, first.RequestTurn.ID)
	require.Empty(t, first.ClientInstallationID)

	next := captureOpenAIOAuthSyntheticRequest(c, []byte(`{"model":"gpt-image-2"}`), "account-image-test")
	require.Equal(t, "account-image-test", next.Logical.SessionKey)
	require.NotEqual(t, first.RequestTurn.ID, next.RequestTurn.ID)
}
